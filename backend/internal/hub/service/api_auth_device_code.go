package service

import (
	"context"
	"errors"
	"fmt"
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
	grant, err := h.createDeviceGrant(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceName:      normalizeDeviceName(r.FormValue("device_name")),
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       h.now().Add(DeviceCodeTTL),
	})
	if err != nil {
		writeInternalError(w, "device authorization creation failed", err)
		return
	}
	h.writeDeviceGrantResponse(w, grant, requestsAdminScope(r))
}

// deviceGrant is the code pair one created grant hands back: the secret the
// CLI polls with, and the short code a human reads out and types.
type deviceGrant struct {
	DeviceCode string
	UserCode   string
}

// DeviceGrantDrawLimit is how many code pairs one grant creation draws before
// it reports a failure.
//
// A user code is 6 characters from a 31-symbol alphabet, and the column is
// UNIQUE, so two live grants can collide. The sweep keeps an expired or
// consumed row for up to an hour after its TTL, which makes the population
// that must stay distinct larger than the set of grants in flight. Without a
// redraw the collision fails an anonymous POST with an internal error on a
// healthy hub. Four draws reduce that outcome to a negligible probability.
const DeviceGrantDrawLimit = 4

// createDeviceGrant inserts one device-code grant and returns the codes it
// stored. It draws a new pair on a uniqueness conflict and inserts again, up
// to DeviceGrantDrawLimit times. It returns every other failure as it is.
//
// The helper OWNS both codes, so no caller draws one. That is what makes the
// redraw complete: the helper answers a conflict on either unique column
// with a fresh pair rather than with the same value again.
//
// ONE helper for the two insert sites -- a CLI login and a credential step-up
// -- because they share the row. A redraw that only one of them performed
// would leave the other answering 500 for the same collision.
func (h *APIAuthHandler) createDeviceGrant(ctx context.Context, p store.CreateDeviceAuthorizationParams) (deviceGrant, error) {
	var err error
	for range DeviceGrantDrawLimit {
		p.DeviceCode = id.Generate()
		p.UserCode = generateUserCode()
		if err = h.store.DeviceAuthorizations().Create(ctx, p); err == nil {
			return deviceGrant{DeviceCode: p.DeviceCode, UserCode: p.UserCode}, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return deviceGrant{}, err
		}
	}
	return deviceGrant{}, fmt.Errorf("device authorization codes collided %d times: %w", DeviceGrantDrawLimit, err)
}

// writeDeviceGrantResponse writes the RFC 8628 authorization response.
//
// ONE writer for the two grant legs -- a CLI login and a credential step-up
// -- because the CLI polls both with one code path, so the two bodies must
// carry the same six fields and the same verification URLs. A shared function
// states that rule; the two copies it replaces stated it in prose and could
// drift silently.
//
// adminAsk is the ONLY difference between the legs. A login can carry the
// admin ask forward; a step-up widens nothing, so it never asks.
func (h *APIAuthHandler) writeDeviceGrantResponse(w http.ResponseWriter, grant deviceGrant, adminAsk bool) {
	verifyURI := locallisten.JoinPath(h.hubURL(), "/auth/cli/activate")
	// The complete URI carries the admin ASK forward so the activation page
	// pre-selects the checkbox. It is only a hint: the hub writes the row's
	// admin_scope from what the browser posts, so a user who types the code by
	// hand simply leaves the box clear and the hub tells the CLI that it did
	// not get the scope.
	completeQuery := url.Values{"user_code": {verifycode.Format(grant.UserCode)}}
	if adminAsk {
		completeQuery.Set("admin", "1")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               grant.DeviceCode,
		"user_code":                 verifycode.Format(grant.UserCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": verifyURI + "?" + completeQuery.Encode(),
		"expires_in":                int(DeviceCodeTTL / time.Second),
		"interval":                  int(DeviceCodePollInterval / time.Second),
	})
}

// handleActivate is the user-facing page where the user enters the
// user_code the CLI displays. GET shows the form; POST processes it.
//
// Approving a device grant mints the same long-lived token pair the consent
// form does, so it carries the same elevation gate: the GET leg bounces
// through /elevate and comes back, the POST leg refuses (see
// requireElevatedSession on why the two legs differ).
//
// This is the flow used from SSH and containers, so the elevation happens on
// a DIFFERENT machine from the one it authorizes. That is the point --
// the browser is where a human can prove a factor.
func (h *APIAuthHandler) handleActivate(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		userCode := r.URL.Query().Get("user_code")
		grant, err := h.liveGrantByUserCode(r.Context(), userCode)
		if err != nil {
			writeInternalError(w, "device authorization lookup failed", err)
			return
		}
		elevating := isElevationGrant(grant)
		data := activatePageData{
			UserCode:   userCode,
			AdminScope: requestsAdminScope(r),
			// No scope to grant on a step-up: it proves that somebody is
			// still there, and widens nothing. Offering the checkbox would
			// promise a change this flow cannot make.
			ShowAdminCheckbox: user.IsAdmin && !elevating,
			Elevating:         elevating,
		}
		// The two grant kinds identify their subject from DIFFERENT sources,
		// and only one of them may be requester-supplied. See
		// grantCredential and grantDeviceName.
		if elevating {
			credential, err := h.grantCredential(r.Context(), grant.ElevateTokenID)
			if err != nil {
				writeInternalError(w, "elevation credential lookup failed", err)
				return
			}
			data.Credential = credential
		} else {
			data.DeviceName = grantDeviceName(grant)
		}
		writePage(w, http.StatusOK, activatePageTmpl, data)
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
		// The grant decides what approval MEANS, and this handler reads it
		// before it resolves the scope: a step-up grants no scope, so asking
		// for one here would refuse a non-administrator for a widening this
		// flow never performs.
		grant, err := h.liveGrantByUserCode(r.Context(), normalized)
		if err != nil {
			writeInternalError(w, "device authorization lookup failed", err)
			return
		}
		elevating := isElevationGrant(grant)
		adminScope := false
		if !elevating {
			var ok bool
			adminScope, ok = resolveAdminScope(w, requestsAdminScope(r), user)
			if !ok {
				return
			}
		}
		// A step-up happens HERE, inside the approval. There is nothing for
		// the CLI to exchange -- the window is on its existing row -- so the
		// approval writes it rather than leaving an approved grant for the
		// token leg to mint from. The CLI's poll then sees the grant approved
		// and retries the request the hub refused.
		//
		// This handler reads elevateTokenID from the GRANT, and only when
		// the grant is an elevation. A grant this read missed is one the
		// approval below refuses too: the read returns nil for a code that is
		// unknown, consumed or expired, and every one of those makes the
		// UPDATE match no row.
		elevateTokenID := ""
		if elevating {
			elevateTokenID = grant.ElevateTokenID
		}
		err = h.approveGrant(r.Context(), store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode:   normalized,
			UserID:     user.ID,
			AdminScope: adminScope,
		}, elevateTokenID)
		switch {
		case err == nil:
		case errors.Is(err, errDeviceGrantNotApprovable), errors.Is(err, errElevationGrantUnavailable):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		default:
			writeInternalError(w, "device authorization approval failed", err)
			return
		}
		// AFTER the commit, and outside it. The cached UserInfo for the
		// elevated credential still carries the OLD deadline, and this
		// process may be the one serving the CLI's retry -- so the cache
		// must be dropped through the lane whose contract is exactly
		// "re-read the user without logging them out". The durable event the
		// store emitted covers every other hub.
		//
		// It runs here rather than inside the transaction because
		// RunInTransaction may run its callback more than once, and a
		// lifecycle effect is state that ACCUMULATES: an attempt that lost
		// its commit would still have invalidated the cache.
		if elevating {
			h.lifecycle.UserInfoInvalidated(user.ID.String())
		}
		writePage(w, http.StatusOK, activatedPageTmpl, activatedPageData{Elevating: elevating})
	}
}

// errDeviceGrantNotApprovable reports a user code that matched no grant this
// browser could still approve: unknown, expired, consumed, already approved,
// or denied. The activation page answers it as a bad request, because the
// person at the keyboard can correct the code they typed.
//
// A SECOND submit of the same form lands here, and that is deliberate. The
// approval is once, so a re-submit cannot be answered with the success page
// without giving the second submitter the grant. The message says "already
// answered" rather than pretending the code is unknown.
//
// It states which of the five conditions applies only as far as the list
// above, which discloses nothing: reaching this leg needs a signed-in session
// that already proved a factor.
var errDeviceGrantNotApprovable = errors.New(
	"that code cannot be authorized: it is unknown, expired, or already answered")

// approveGrant commits the approval and, for a step-up, the elevation that
// approval grants -- in ONE transaction.
//
// The two writes used to be independent, and nothing reverted the approval
// when the elevation failed. The row then stayed approved, with a live
// elevate_token_id and no window: the CLI's poll reported "verified", the CLI
// retried the restricted command, and the hub refused it again, with no error
// the user could act on. One transaction is what makes "approved" and
// "elevated" the same fact.
//
// The instant comes from h.now(), never from time.Now(): the handler's seam is
// the one clock every instant it mints or compares comes from, so a test that
// advances the fake clock past the grant TTL sees the expiry it set up.
func (h *APIAuthHandler) approveGrant(
	ctx context.Context,
	p store.ApproveDeviceAuthorizationByUserCodeParams,
	elevateTokenID string,
) error {
	return h.store.RunInTransaction(ctx, func(tx store.Store) error {
		rows, err := tx.DeviceAuthorizations().ApproveByUserCode(ctx, p, h.now().UTC())
		if err != nil {
			return fmt.Errorf("approve device authorization: %w", err)
		}
		if rows == 0 {
			return errDeviceGrantNotApprovable
		}
		if elevateTokenID == "" {
			return nil
		}
		return h.elevateGrantedToken(ctx, tx, elevateTokenID, p.UserID)
	})
}

// isElevationGrant reports whether approving this grant VERIFIES a
// command-line credential the account already holds, rather than issuing a new
// one. A nil row is neither: the code identifies no live grant.
//
// One predicate for the three places that ask -- the activation page, the
// approval, and the token exchange -- because a raw test of the column at each
// site is the same rule written three times.
func isElevationGrant(row *store.DeviceAuthorization) bool {
	return row != nil && row.ElevateTokenID != ""
}

// liveGrantByUserCode returns the grant a user code identifies, or nil when
// the code identifies none that can still be approved.
//
// ONE lookup for the two questions the activation page asks -- which device
// asked, and what approving it would DO -- because they must answer from the
// same row. Read separately, a page could identify the device from a live
// grant and decide its verb from a missing one.
//
// A MISS is nil with no error, and it tells a caller nothing it could not
// already learn: reaching this leg needs a signed-in and elevated session, and
// the POST beside it already answers "is this code live" by approving or
// refusing. An expired or already-consumed grant is a miss for the same reason
// -- neither can be approved.
//
// A store FAILURE is an error, and the two must stay apart. Folded together,
// a transient read failure rendered the issuance page with the hub
// administration checkbox for a step-up grant, and the POST then approved that
// grant while it skipped the elevation the approval exists to write.
func (h *APIAuthHandler) liveGrantByUserCode(ctx context.Context, rawUserCode string) (*store.DeviceAuthorization, error) {
	normalized := verifycode.Normalize(rawUserCode)
	if normalized == "" {
		return nil, nil
	}
	row, err := h.store.DeviceAuthorizations().GetByUserCode(ctx, normalized)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if row.ConsumedAt != nil || !h.now().UTC().Before(row.ExpiresAt) {
		return nil, nil
	}
	return row, nil
}

// grantDeviceName is what the activation page shows for the device that asked
// for a NEW credential. It returns the NAME and not markup, because the
// template owns how it renders and escapes a name.
//
// The consent on this page is a decision about a device, and the page
// identified none: the user approved a credential for something they could
// not recognize. The name is attacker-chosen -- the device-authorization
// endpoint is anonymous -- which is why normalizeDeviceName cleans it at
// intake, and this is the surface that cleaning was for.
//
// It is for the ISSUANCE flow alone. A step-up asks about a credential the
// hub already holds a record of, so it identifies that record instead; see
// grantCredential.
func grantDeviceName(row *store.DeviceAuthorization) string {
	if row == nil {
		return ""
	}
	if row.DeviceName == "" {
		return "an unnamed device"
	}
	return row.DeviceName
}
