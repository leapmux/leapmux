package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Step-up for a command-line credential.
//
// A CLI token is minted once and lives for months, so the elevation gate used
// to admit it with no check: it had no row to stamp and nobody at a keyboard.
// That made possession of the credential file the whole of the check for the
// hub's settings, the user surface and the mint. It has a row now, and this
// is how a person proves the factor that stamps it.
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
	// device_name is what the REQUESTER calls itself, and it identifies
	// nothing on the activation page for a step-up: the hub reads the
	// credential's own row there instead (see grantCredential). It is stored
	// so the grant carries the label the caller sent, and normalized at
	// intake like every other copy of it.
	grant, err := h.createDeviceGrant(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceName:      normalizeDeviceName(r.FormValue("device_name")),
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       h.now().Add(DeviceCodeTTL),
		ElevateTokenID:  tokenID,
	})
	if err != nil {
		writeInternalError(w, "elevation authorization creation failed", err)
		return
	}
	// The SAME writer the device-code leg uses, because the CLI polls both
	// with one code path. It asks for no admin scope: a step-up widens
	// nothing, it only proves that somebody is still there.
	h.writeDeviceGrantResponse(w, grant, false)
}

// errElevationGrantUnavailable reports an approved step-up whose credential is
// gone: revoked, expired, deleted with its user, or owned by somebody else.
// Every one of those makes the approval a no-op rather than a failure, and the
// CLI reports it as a refusal it can act on.
var errElevationGrantUnavailable = errors.New("that command-line credential is no longer available")

// elevateGrantedToken stamps the window the approval just granted.
//
// The owner is the APPROVING user, taken from their session and never from the
// grant row. A grant that identifies somebody else's credential therefore
// elevates nothing: the store statement carries the owner equality, so the
// update matches no row and this reports the same refusal an expired one gets.
//
// It writes through the TRANSACTION its caller opened, so the approval and the
// window commit together or not at all. It emits no cache effect of its own:
// approveGrant's callback may run more than once, and the caller invalidates
// the cached UserInfo after the commit instead.
func (h *APIAuthHandler) elevateGrantedToken(ctx context.Context, tx store.Store, tokenID string, owner userid.UserID) error {
	now := h.now().UTC()
	n, err := tx.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
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
	return nil
}

// credentialElevationIsCurrent reports whether the credential a step-up grant
// identifies carries an elevation window that admits a sensitive action NOW.
//
// The token leg calls it before it answers "elevated", because the approval
// FLAG on the grant row is not enough to answer from. A row can be approved
// with no window -- a hub that died between the two writes left exactly that
// shape before the approval became one transaction -- and reporting success
// there tells the CLI to retry a command the hub refuses again, with no error
// the user can act on. The credential holds the truth, so the reader reads the
// credential.
//
// A credential that is gone reports false rather than an error: the caller
// refuses the exchange the same way for each, and a missing row is not a hub
// failure.
//
// This function deliberately does NOT test revocation, although the elevation
// write refuses a revoked credential. A credential revoked between the
// approval and the poll fails its very next request with "token revoked",
// which is an error the user can act on -- so restating the store statement's
// liveness rule in Go would add a second copy of it and gain nothing.
func (h *APIAuthHandler) credentialElevationIsCurrent(ctx context.Context, tokenID string) (bool, error) {
	row, err := h.store.APITokens().GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return auth.NewElevation(row.ElevationProvenAt, row.ElevationExpiresAt).IsCurrent(h.now()), nil
}

// grantCredential identifies, for the activation page, the credential a
// step-up grant elevates.
//
// It reads the api_tokens row -- the hub's OWN record: the name the account's
// credential list shows, and the date the account added it. The grant's
// device_name is what the REQUESTER sent, so a stolen credential could label
// its own step-up "the owner's laptop" and phish the owner into re-arming it
// under a name the attacker chose. A page that asks a person to verify a
// credential must show what the hub knows about that credential, and nothing
// the asker wrote.
//
// A credential that is already gone returns nil, and the page then identifies
// nothing rather than falling back to the requester's text. The approval
// refuses that grant anyway; see errElevationGrantUnavailable.
func (h *APIAuthHandler) grantCredential(ctx context.Context, tokenID string) (*activateCredential, error) {
	row, err := h.store.APITokens().GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	name := row.ClientName
	if name == "" {
		name = "an unnamed credential"
	}
	return &activateCredential{
		Name:  name,
		Added: row.CreatedAt.UTC().Format(credentialAddedLayout),
		// A revoked credential cannot be elevated, so the page says so
		// rather than asking a person to verify something the approval
		// refuses.
		Revoked: row.RevokedAt != nil,
	}, nil
}

// credentialAddedLayout prints the date a credential was added. UTC and
// numeric: the Go mux serves the page outside the SPA, so it has no locale
// and no time zone of the reader to print in, and an ISO date cannot be read
// two ways.
const credentialAddedLayout = "2006-01-02"
