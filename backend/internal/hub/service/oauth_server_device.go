package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/leapmux/leapmux/locallisten"
)

// --- RFC 8628: device authorization ---

// handleDeviceAuthorization starts the device flow.
//
// It is no longer anonymous in the sense that mattered: the request must
// specify a registered client_id, because the activation page has to say WHICH
// app the person authorizes and the token stage has to refuse a redemption by
// a different one. It still carries no user credential -- the whole point of
// this flow is that the machine that asks has no browser.
func (h *OAuthServerHandler) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	// RFC 8628 section 3.1 incorporates RFC 6749 section 3.2.1: the client
	// AUTHENTICATES on this stage exactly as the token stages do. A CONFIDENTIAL
	// app must present its secret; without this check anyone who learned a
	// client_id could mint grants in that app's name and drive the hub's
	// activation pages with flows the app never started.
	app, clientErr, internalErr := h.authenticateClientOpts(r.Context(), r, "", false)
	if respondClientAuthFailure(w, clientErr, internalErr, "device authorization client lookup failed") {
		return
	}
	// The viewer is nil: this stage carries no session, so it reaches HUB-WIDE
	// apps only. A user's PRIVATE app cannot start a device flow, and that is
	// the honest consequence of a flow whose first stage has no identity -- the
	// authorization-code flow, which runs in the owner's browser, is where a
	// private app belongs.
	if !app.IsHubWide() {
		writeOAuthErrorBody(w, http.StatusBadRequest, appUnavailableBody())
		return
	}
	if !appAllowsGrantType(app, GrantTypeDeviceCode) {
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client",
			"this app is not registered for the device_code grant")
		return
	}
	// The user is UNKNOWN here, so the admin-scope refusal cannot run yet: it
	// needs an account. resolveRequestedScopes is called again at the
	// activation page, with the approving user, and that is where a
	// non-administrator's request for an admin scope is refused.
	requested, scopeErr := resolveRequestedScopes(r.FormValue("scope"), app, nil)
	if scopeErr != nil {
		writeOAuthErrorBody(w, http.StatusBadRequest, *scopeErr)
		return
	}
	stored, err := requested.Storable()
	if err != nil {
		writeInternalError(w, "device authorization produced an unstorable ask", err)
		return
	}
	// The hub's address is resolved BEFORE the grant row is inserted: the
	// response cannot state a verification URL without it, and a refusal after
	// the insert would leave a pending grant nobody can ever poll, lingering
	// until the sweep. Resolving first answers the same 503 with nothing
	// written.
	base, ok := h.metadataBase(w)
	if !ok {
		return
	}
	grant, err := h.createDeviceGrant(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceName:      normalizeInstallationName(r.FormValue("installation_name")),
		ClientID:        app.ClientID,
		RequestedScopes: stored,
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       h.now().Add(DeviceCodeTTL),
	})
	if err != nil {
		writeInternalError(w, "device authorization creation failed", err)
		return
	}
	h.writeDeviceGrantResponse(w, base, grant)
}

// deviceGrant is the code pair one created grant hands back: the secret the
// client polls with, and the short code a human reads out and types.
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
// redraw the collision fails the POST with an internal error on a healthy hub.
// Four draws reduce that outcome to a negligible probability.
const DeviceGrantDrawLimit = 4

// createDeviceGrant inserts one device-code grant and returns the codes it
// stored. It draws a new pair on a uniqueness conflict and inserts again, up
// to DeviceGrantDrawLimit times. It returns every other failure as it is.
//
// The helper OWNS both codes, so no caller draws one. That is what makes the
// redraw complete: the helper answers a conflict on either unique column
// with a fresh pair rather than with the same value again.
//
// ONE helper for the two insert sites -- an app login and a credential step-up
// -- because they share the row. A redraw that only one of them performed
// would leave the other answering 500 for the same collision.
func (h *OAuthServerHandler) createDeviceGrant(ctx context.Context, p store.CreateDeviceAuthorizationParams) (deviceGrant, error) {
	var err error
	for range DeviceGrantDrawLimit {
		p.DeviceCode = id.Generate()
		// verifycode.Generate produces a 6-char alphanumeric from an
		// unambiguous alphabet, which is the user-code shape this grant wants;
		// verifycode.Format adds the display form (XXX-XXX) when the response
		// builds verification_uri_complete.
		p.UserCode = verifycode.Generate()
		if err = h.store.DeviceAuthorizations().Create(ctx, p); err == nil {
			return deviceGrant{DeviceCode: p.DeviceCode, UserCode: p.UserCode}, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return deviceGrant{}, err
		}
	}
	return deviceGrant{}, fmt.Errorf("device authorization codes collided %d times: %w", DeviceGrantDrawLimit, err)
}

// writeDeviceGrantResponse writes the RFC 8628 section 3.2 authorization
// response.
//
// ONE writer for the two grant stages -- an app login and a credential step-up --
// because a client polls both with one code path, so the two bodies must carry
// the same six fields and the same verification URLs. A shared function states
// that rule; two copies would state it in prose and could drift silently.
//
// The callers resolve the hub's base URL -- through the SAME refusal the
// metadata documents give -- BEFORE inserting the grant row, and pass it in:
// a grant row whose response could not name a verification URL would sit
// pending until the sweep, and a code the client never held cannot be polled.
func (h *OAuthServerHandler) writeDeviceGrantResponse(w http.ResponseWriter, base string, grant deviceGrant) {
	verifyURI := locallisten.JoinPath(base, "/oauth/device")
	completeQuery := url.Values{"user_code": {verifycode.Format(grant.UserCode)}}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               grant.DeviceCode,
		"user_code":                 verifycode.Format(grant.UserCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": verifyURI + "?" + completeQuery.Encode(),
		"expires_in":                int(DeviceCodeTTL / time.Second),
		"interval":                  int(DeviceCodePollInterval / time.Second),
	})
}

// handleDevice is the user-facing page where the user enters the user_code the
// app displays. GET shows the form; POST processes it.
//
// Approving a device grant mints the same long-lived token pair the consent
// form does, so it carries the same elevation requirement: the GET stage bounces
// through /elevate and comes back, the POST stage refuses (see
// requireElevatedSession on why the two stages differ).
//
// This is the flow used from SSH and containers, so the elevation happens on a
// DIFFERENT machine from the one it authorizes. That is the point -- the
// browser is where a human can prove a factor.
func (h *OAuthServerHandler) handleDevice(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.renderDevicePage(w, r, user)
	case http.MethodPost:
		h.approveOrDenyDevice(w, r, user)
	}
}

func (h *OAuthServerHandler) renderDevicePage(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	userCode := r.URL.Query().Get("user_code")
	grant, err := h.liveGrantByUserCode(r.Context(), userCode)
	if err != nil {
		writeInternalError(w, "device authorization lookup failed", err)
		return
	}
	elevating := isElevationGrant(grant)
	data := devicePageData{UserCode: userCode, Elevating: elevating}
	// The two grant kinds identify their subject from DIFFERENT sources, and
	// only one of them may be requester-supplied. See grantCredential.
	switch {
	case elevating:
		credential, err := h.grantCredential(r.Context(), grant.ElevateTokenID)
		if err != nil {
			writeInternalError(w, "elevation credential lookup failed", err)
			return
		}
		data.Credential = credential
	case grant != nil:
		app, err := h.resolveApp(r.Context(), grant.ClientID, user)
		if err != nil && !errors.Is(err, errAppUnavailable) {
			writeInternalError(w, "device authorization client lookup failed", err)
			return
		}
		if app != nil {
			display := appDisplay(app)
			data.App = &display
			asked, parseErr := authscope.Parse(grant.RequestedScopes)
			if parseErr == nil {
				data.Permissions = describeScopes(asked)
			}
		}
	}
	writePage(w, http.StatusOK, devicePageTmpl, data, "")
}

func (h *OAuthServerHandler) approveOrDenyDevice(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	// consentLeg runs the gate BEFORE ParseForm; see handleConsent.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	normalized := verifycode.Normalize(r.PostFormValue("user_code"))
	if normalized == "" {
		http.Error(w, "invalid user_code", http.StatusBadRequest)
		return
	}
	// The grant decides what approval MEANS, and this handler reads it before
	// it resolves the scope: a step-up grants no scope, so asking for one here
	// would refuse a non-administrator for a widening this flow never performs.
	grant, err := h.liveGrantByUserCode(r.Context(), normalized)
	if err != nil {
		writeInternalError(w, "device authorization lookup failed", err)
		return
	}
	elevating := isElevationGrant(grant)

	// DENY actually returns, and it is final: the approve statements match a
	// pending row only, so a denied grant can never become approved. Without
	// it the poller waited out the whole ten-minute expiry to learn that a
	// person refused.
	//
	// The nil check mirrors the allow branch, and so does the ROWS check: a
	// code that is unknown, mistyped or already answered denies nothing, and
	// rendering the "Refused" success page for it told the person at the
	// keyboard they blocked a grant that stayed pending -- or, for a grant
	// already approved, that they stopped a credential the poller kept.
	if r.PostFormValue("decision") != consentDecisionAllow {
		if grant == nil {
			http.Error(w, errDeviceGrantNotApprovable.Error(), http.StatusBadRequest)
			return
		}
		rows, err := h.store.DeviceAuthorizations().DenyByUserCode(r.Context(), normalized)
		if err != nil {
			writeInternalError(w, "device authorization denial failed", err)
			return
		}
		if rows == 0 {
			http.Error(w, errDeviceGrantNotApprovable.Error(), http.StatusBadRequest)
			return
		}
		writePage(w, http.StatusOK, deviceDonePageTmpl, deviceDonePageData{Elevating: elevating, Denied: true}, "")
		return
	}

	granted := ""
	if !elevating {
		if grant == nil {
			http.Error(w, errDeviceGrantNotApprovable.Error(), http.StatusBadRequest)
			return
		}
		app, appErr := h.resolveApp(r.Context(), grant.ClientID, user)
		if appErr != nil {
			if errors.Is(appErr, errAppUnavailable) {
				http.Error(w, "that app is no longer available", http.StatusBadRequest)
				return
			}
			writeInternalError(w, "device authorization client lookup failed", appErr)
			return
		}
		// The ask is re-resolved against THIS user, which is where a
		// non-administrator's request for an admin scope is refused: the
		// anonymous first stage had no account to judge it against.
		scopes, scopeErr := resolveRequestedScopes(grant.RequestedScopes, app, user)
		if scopeErr != nil {
			http.Error(w, scopeErr.ErrorDescription, http.StatusForbidden)
			return
		}
		// An admin-reaching ask confirms ONCE before it binds. The device
		// flow authorizes a machine the person typing the code cannot see,
		// under an app name the phisher also chose how to run; the old flow's
		// checkbox made a hand-typed code default to NON-admin, and losing
		// that left one click on a trusted name between a phished
		// administrator and the admin credential. The re-rendered page states
		// the admin sentences beside the caution and carries
		// admin_confirmed, so the second Allow binds exactly what the person
		// confirmed -- a deliberate stop, never a silent narrowing.
		if _, admin := firstAdminScope(scopes); admin && r.PostFormValue("admin_confirmed") == "" {
			display := appDisplay(app)
			writePage(w, http.StatusOK, devicePageTmpl, devicePageData{
				UserCode:     normalized,
				App:          &display,
				Permissions:  describeScopes(scopes),
				ConfirmAdmin: true,
			}, "")
			return
		}
		stored, storeErr := scopes.Storable()
		if storeErr != nil {
			writeInternalError(w, "device approval produced an unstorable grant", storeErr)
			return
		}
		granted = stored
	}

	// A step-up happens HERE, inside the approval. There is nothing for the
	// client to exchange -- the window is on its existing row -- so the
	// approval writes it rather than leaving an approved grant for the token
	// stage to mint from. The poll then sees the grant approved and retries the
	// request the hub refused.
	//
	// This handler reads elevateTokenID from the GRANT, and only when the grant
	// is an elevation. A grant this read missed is one the approval below
	// refuses too: the read returns nil for a code that is unknown, consumed or
	// expired, and every one of those makes the UPDATE match no row.
	elevateTokenID := ""
	if elevating {
		elevateTokenID = grant.ElevateTokenID
	}
	err = h.approveGrant(r.Context(), store.ApproveDeviceAuthorizationByUserCodeParams{
		UserCode:      normalized,
		UserID:        user.ID,
		GrantedScopes: granted,
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
	// AFTER the commit, and outside it. The cached UserInfo for the elevated
	// credential still carries the OLD deadline, and this process may be the one
	// serving the client's retry -- so the cache must be dropped through the lane
	// whose contract is exactly "re-read the user without logging them out". The
	// durable event the store emitted covers the watcher's replay.
	//
	// It runs here rather than inside the transaction because RunInTransaction
	// may run its callback more than once, and a lifecycle effect is state that
	// ACCUMULATES: an attempt that lost its commit also invalidates the cache.
	if elevating {
		h.lifecycle.UserInfoInvalidated(user.ID.String())
	}
	writePage(w, http.StatusOK, deviceDonePageTmpl, deviceDonePageData{Elevating: elevating}, "")
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
// above, which discloses nothing: reaching this stage needs a signed-in session
// that already proved a factor.
var errDeviceGrantNotApprovable = errors.New(
	"that code cannot be authorized: it is unknown, expired, or already answered")

// approveGrant commits the approval and, for a step-up, the elevation that
// approval grants -- in ONE transaction.
//
// The two writes used to be independent, and nothing reverted the approval
// when the elevation failed. The row then stayed approved, with a live
// elevate_token_id and no window: the poll reported "verified", the client
// retried the restricted command, and the hub refused it again, with no error
// the user could act on. One transaction is what makes "approved" and
// "elevated" the same fact.
//
// The instant comes from h.now(), never from time.Now(): the handler's seam is
// the one clock every instant it mints or compares comes from, so a test that
// advances the fake clock past the grant TTL sees the expiry it set up.
func (h *OAuthServerHandler) approveGrant(
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

// isElevationGrant reports whether approving this grant VERIFIES an app
// credential the account already holds, rather than issuing a new one. A nil
// row is neither: the code identifies no live grant.
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
// ONE lookup for the three questions the activation page asks -- which app
// asked, what it asked for, and what approving it would DO -- because they must
// answer from the same row. Read separately, a page could identify the app from
// a live grant and decide its verb from a missing one.
//
// A MISS is nil with no error, and it tells a caller nothing it could not
// already learn: reaching this stage needs a signed-in and elevated session, and
// the POST beside it already answers "is this code live" by approving or
// refusing. An expired or already-consumed grant is a miss for the same reason
// -- neither can be approved.
//
// A store FAILURE is an error, and the two must stay apart. Folded together, a
// transient read failure rendered the issuance page for a step-up grant, and
// the POST then approved that grant while it skipped the elevation the approval
// exists to write.
func (h *OAuthServerHandler) liveGrantByUserCode(ctx context.Context, rawUserCode string) (*store.DeviceAuthorization, error) {
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
