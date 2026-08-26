package service

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/leapmux/leapmux/locallisten"
)

// Step-up for a command-line credential.
//
// A CLI token is minted once and lives for months, so the elevation gate used
// to wave it through: it had no row to stamp and nobody at a keyboard. That
// made possession of the credential file the whole of the check for the hub's
// settings, the user surface and the mint. It has a row now, and this is how
// a person proves the factor that stamps it.
//
// The ceremony is the DEVICE-CODE ceremony, unchanged: the CLI asks, the hub
// returns a user code and a URL, a human approves it in a browser, the CLI
// polls. The two flows share the row, the TTL, the poll throttle, the
// slow_down throttle, the expiry sweep and the activation page; they differ in
// what the approval does, which is what device_authorizations.elevate_token_id
// records. A second table would have been a second copy of every one of those
// rules.
//
// The browser is not an implementation detail. It is the only place a person
// can answer a password or a passkey prompt, and it is deliberately somewhere
// the credential file cannot reach -- a CLI that could elevate itself from the
// terminal would give a stolen file everything the window exists to withhold.

// handleElevateAuthorization starts a step-up for the CALLING command-line
// credential.
//
// Authenticated by the bearer itself, and by nothing else. The caller is
// asking to elevate the credential it already holds, so holding it is the
// right to ask; what it cannot do is APPROVE, which needs a browser session
// that proves a factor.
//
// A DELEGATION bearer is refused, because it can carry no elevation at all --
// a worker mints it for an agent that reads untrusted input. The refusal comes
// from the credential kind (see CredentialIdentity.ElevatableRow), not from a
// second list of kinds kept here.
func (h *APIAuthHandler) handleElevateAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, err := auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:     h.store,
		Validator: h.validator,
	})
	if err != nil || user == nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "a command-line credential is required")
		return
	}
	tokenID := user.Credential.APITokenID()
	if tokenID == "" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_credential",
			"this credential cannot be verified; sign in from a browser instead")
		return
	}
	deviceCode := id.Generate()
	userCode := generateUserCode()
	if err := h.store.DeviceAuthorizations().Create(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		DeviceName:      normalizeDeviceName(r.FormValue("device_name")),
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       h.now().Add(DeviceCodeTTL),
		ElevateTokenID:  tokenID,
	}); err != nil {
		writeInternalError(w, "elevation authorization creation failed", err)
		return
	}
	verifyURI := locallisten.JoinPath(h.hubURL(), "/auth/cli/activate")
	complete := verifyURI + "?" + url.Values{"user_code": {verifycode.Format(userCode)}}.Encode()
	// The SAME body shape the device-code leg returns, because the CLI polls
	// it with the same code path. There is no admin ask: a step-up widens
	// nothing, it only proves that somebody is still there.
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 verifycode.Format(userCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": complete,
		"expires_in":                int(DeviceCodeTTL / time.Second),
		"interval":                  int(DeviceCodePollInterval / time.Second),
	})
}

// errElevationGrantUnavailable reports an approved step-up whose credential is
// gone: revoked, expired, deleted with its user, or owned by somebody else.
// Every one of those makes the approval a no-op rather than a failure, and the
// CLI reports it as a refusal it can act on.
var errElevationGrantUnavailable = errors.New("that command-line credential is no longer available")

// elevateGrantedToken stamps the window the approval just granted.
//
// The owner is the APPROVING user, taken from their session and never from the
// grant row. A grant naming somebody else's credential therefore elevates
// nothing: the store statement carries the owner equality, so the update
// matches no row and this reports the same refusal an expired one gets.
func (h *APIAuthHandler) elevateGrantedToken(r *http.Request, tokenID string, owner userid.UserID) error {
	now := h.now().UTC()
	n, err := h.store.APITokens().Elevate(r.Context(), store.ElevateAPITokenParams{
		TokenID:            tokenID,
		UserID:             owner,
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	if err != nil {
		return err
	}
	if n == 0 {
		return errElevationGrantUnavailable
	}
	// The cached UserInfo for this bearer still carries the OLD deadline, and
	// this process may be the one serving the CLI's retry. Drop it through the
	// lane whose contract is exactly "re-read the user without logging them
	// out"; the durable event the store emitted covers every other hub.
	h.lifecycle.UserInfoInvalidated(owner.String())
	return nil
}
