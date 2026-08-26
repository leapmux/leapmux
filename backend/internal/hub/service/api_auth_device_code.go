package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/leapmux/leapmux/locallisten"
)

// --- Device-code flow ---

func (h *APIAuthHandler) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceName := normalizeDeviceName(r.FormValue("device_name"))
	deviceCode := id.Generate()
	userCode := generateUserCode()
	if err := h.store.DeviceAuthorizations().Create(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		DeviceName:      deviceName,
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       h.now().Add(DeviceCodeTTL),
	}); err != nil {
		writeInternalError(w, "device authorization creation failed", err)
		return
	}
	verifyURI := locallisten.JoinPath(h.hubURL(), "/auth/cli/activate")
	// The complete URI carries the admin ASK forward so the activation page
	// pre-selects the checkbox. It is only a hint: the row's admin_scope is
	// written from what the browser posts, so a user who types the code by
	// hand simply leaves the box clear and the CLI is told it did not get
	// the scope.
	completeQuery := url.Values{"user_code": {verifycode.Format(userCode)}}
	if requestsAdminScope(r) {
		completeQuery.Set("admin", "1")
	}
	complete := verifyURI + "?" + completeQuery.Encode()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 verifycode.Format(userCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": complete,
		"expires_in":                int(DeviceCodeTTL / time.Second),
		"interval":                  int(DeviceCodePollInterval / time.Second),
	})
}

// handleActivate is the user-facing page where the user enters the
// user_code displayed by the CLI. GET shows the form; POST processes it.
//
// Approving a device grant mints the same long-lived token pair the consent
// form does, so it carries the same elevation gate: the GET leg bounces
// through /elevate and comes back, the POST leg refuses (see
// requireElevatedSession on why the two legs differ).
//
// This is the flow used from SSH and containers, so the elevation happens on
// a DIFFERENT machine from the one being authorized. That is the point --
// the browser is where a human can prove a factor.
func (h *APIAuthHandler) handleActivate(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		userCode := r.URL.Query().Get("user_code")
		grant := h.liveGrantByUserCode(r.Context(), userCode)
		elevating := grant != nil && grant.ElevateTokenID != ""
		writePage(w, http.StatusOK, activatePageTmpl, activatePageData{
			DeviceName: grantDeviceName(grant),
			UserCode:   userCode,
			AdminScope: requestsAdminScope(r),
			// No scope to grant on a step-up: it proves that somebody is
			// still there, and widens nothing. Offering the checkbox would
			// promise a change this flow cannot make.
			ShowAdminCheckbox: user.IsAdmin && !elevating,
			Elevating:         elevating,
		})
	case http.MethodPost:
		// consentLeg runs the gate BEFORE ParseForm; see handleAuthorize.
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		raw := r.FormValue("user_code")
		normalized := verifycode.Normalize(raw)
		if normalized == "" {
			http.Error(w, "invalid user_code", http.StatusBadRequest)
			return
		}
		// The grant decides what approval MEANS, and it is read before the
		// scope is resolved: a step-up grants no scope, so asking for one
		// here would refuse a non-administrator for a widening this flow
		// never performs.
		grant := h.liveGrantByUserCode(r.Context(), normalized)
		elevating := grant != nil && grant.ElevateTokenID != ""
		adminScope := false
		if !elevating {
			var ok bool
			adminScope, ok = resolveAdminScope(w, requestsAdminScope(r), user)
			if !ok {
				return
			}
		}
		// h.now(), not time.Now(): the handler's seam is the one clock every
		// instant it mints or compares comes from, so a test that advances
		// the fake clock past the grant TTL sees the expiry it set up.
		rows, err := h.store.DeviceAuthorizations().ApproveByUserCode(r.Context(), store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode:   normalized,
			UserID:     user.ID,
			AdminScope: adminScope,
		}, h.now().UTC())
		if err != nil {
			writeInternalError(w, "device authorization approval failed", err)
			return
		}
		if rows == 0 {
			http.Error(w, "code not found or expired", http.StatusBadRequest)
			return
		}
		// A step-up is done HERE. There is nothing for the CLI to exchange --
		// the window is on its existing row -- so the approval writes it
		// rather than leaving an approved grant for the token leg to mint
		// from. The CLI's poll then sees the grant approved and retries the
		// request the hub refused.
		if elevating {
			if err := h.elevateGrantedToken(r, grant.ElevateTokenID, user.ID); err != nil {
				if errors.Is(err, errElevationGrantUnavailable) {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				writeInternalError(w, "credential elevation failed", err)
				return
			}
		}
		writePage(w, http.StatusOK, activatedPageTmpl, activatedPageData{Elevating: elevating})
	}
}

// liveGrantByUserCode returns the grant a user code names, or nil when the
// code names none that can still be approved.
//
// ONE lookup for the two questions the activation page asks -- which device
// asked, and what approving it would DO -- because they must answer from the
// same row. Read separately, a page could name the device from a live grant
// and decide its verb from a missing one.
//
// A miss is nil rather than an error, and it tells a caller nothing it could
// not already learn: reaching this leg needs a signed-in and elevated session,
// and the POST beside it already answers "is this code live" by approving or
// refusing. An expired or already-consumed grant is a miss for the same
// reason -- neither can be approved.
func (h *APIAuthHandler) liveGrantByUserCode(ctx context.Context, rawUserCode string) *store.DeviceAuthorization {
	normalized := verifycode.Normalize(rawUserCode)
	if normalized == "" {
		return nil
	}
	row, err := h.store.DeviceAuthorizations().GetByUserCode(ctx, normalized)
	if err != nil || row == nil || row.ConsumedAt != nil || !h.now().UTC().Before(row.ExpiresAt) {
		return nil
	}
	return row
}

// grantDeviceName is what the activation page shows for the device that
// asked. It returns the NAME and not markup, because the template owns how a
// name is rendered and escaped.
//
// The consent on this page is a decision about a device, and the page
// identified none: the user approved a credential for something they could
// not recognize. The name is attacker-chosen -- the device-authorization
// endpoint is anonymous -- which is why normalizeDeviceName cleans it at
// intake, and this is the surface that cleaning was for.
func grantDeviceName(row *store.DeviceAuthorization) string {
	if row == nil {
		return ""
	}
	if row.DeviceName == "" {
		return "an unnamed device"
	}
	return row.DeviceName
}
