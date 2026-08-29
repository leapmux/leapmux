package service

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/internal/util/userid"
)

var errAuthorizationGrantUnavailable = errors.New("authorization grant unavailable")

// --- /oauth/token ---

// handleToken is the ONE token endpoint. It serves all three grant types, and
// grant_type is REQUIRED.
//
// The previous surface had two endpoints and inferred the grant type from
// which field happened to be present. Both were wrong for a standards client:
// an off-the-shelf library posts grant_type and expects one address, and an
// inference makes a request with two fields mean whichever the server tested
// first.
func (h *OAuthServerHandler) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	switch r.FormValue("grant_type") {
	case GrantTypeAuthorizationCode:
		h.handleTokenAuthorizationCode(w, r)
	case GrantTypeDeviceCode:
		h.handleTokenDeviceCode(w, r)
	case GrantTypeRefreshToken:
		h.handleTokenRefresh(w, r)
	case "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

// authenticateClient identifies the app making a token request, and verifies
// its secret when it has one.
//
// It accepts the two forms RFC 6749 section 2.3.1 defines: HTTP Basic, which a
// conformant library prefers, and `client_id`/`client_secret` in the body.
//
// A PUBLIC client presenting a secret is REFUSED rather than ignored. The app's
// registration says it holds none, so a request that carries one is either a
// client misconfigured to leak a secret it should not have, or somebody probing
// -- and neither should be told "that worked".
//
// The third result carries an INTERNAL failure -- a store read that errored on
// a live hub. It must not fold into invalid_client: a one-second database blip
// answered "401, your credentials are wrong" sends a conformant library
// re-registering for a secret that was correct all along, so the caller answers
// it with a 500 the client retries. parseAuthorizeRequest keeps the same two
// answers apart, and the token stage did not -- the discipline this restores.
//
// The CONTEXT is the caller's to choose while the REQUEST stays the request:
// the refresh stage reads the form from a request whose context may already be
// canceled (its flight detaches for exactly that reason), so it passes the
// detached flight context here and the form values still come from `r`.
func (h *OAuthServerHandler) authenticateClient(ctx context.Context, r *http.Request, presentedID string) (*store.OAuthClient, *oauthErrorResponse, error) {
	return h.authenticateClientOpts(ctx, r, presentedID, false)
}

// authenticateClientAllowRevoked runs the same authentication for the RFC 7009
// revocation stage, where a RETIRED app is not a refusal: its credentials were
// revoked by the retirement cascade, so the idempotent branch in
// bindRevocationToClient answers the retrying client with the 200 the endpoint
// promises. Every other stage keeps refusing a retired app at the door.
func (h *OAuthServerHandler) authenticateClientAllowRevoked(ctx context.Context, r *http.Request) (*store.OAuthClient, *oauthErrorResponse, error) {
	return h.authenticateClientOpts(ctx, r, "", true)
}

// presentedClientCredentials returns the app identity the request presents,
// through the two forms RFC 6749 section 2.3.1 defines: HTTP Basic, which a
// conformant library prefers, and `client_id`/`client_secret` in the body.
// authenticateClientOpts reads them through this helper so the refresh
// singleflight can key on the SAME extraction rather than a second spelling
// that could drift from the one the authentication runs.
func presentedClientCredentials(r *http.Request) (clientID, clientSecret string) {
	clientID, clientSecret, hasBasic := r.BasicAuth()
	if !hasBasic {
		clientID = r.FormValue("client_id")
		clientSecret = r.FormValue("client_secret")
	}
	return clientID, clientSecret
}

func (h *OAuthServerHandler) authenticateClientOpts(ctx context.Context, r *http.Request, presentedID string, allowRevoked bool) (*store.OAuthClient, *oauthErrorResponse, error) {
	clientID, clientSecret := presentedClientCredentials(r)
	if clientID == "" {
		body := oauthErrorBody("invalid_client", "client_id is required")
		return nil, &body, nil
	}
	// The presented id must match the GRANT's, when the caller already loaded
	// one. RFC 6749 section 4.1.3 requires a code to be redeemable only by the
	// client it was issued to, and nothing else in this exchange enforces it.
	if presentedID != "" && clientID != presentedID {
		body := oauthErrorBody("invalid_grant", "this grant was issued to a different app")
		return nil, &body, nil
	}
	// nil viewer: the token stage carries no session. A PRIVATE app therefore
	// cannot be resolved here -- which is correct for the device flow, and
	// harmless for the code flow, where the grant row already proved the
	// account authorized this app. See resolveGrantApp.
	app, err := h.store.OAuthClients().Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			body := appUnavailableBody()
			return nil, &body, nil
		}
		return nil, nil, err
	}
	if app.RevokedAt != nil && !allowRevoked {
		body := appUnavailableBody()
		return nil, &body, nil
	}
	if !app.IsConfidential() {
		if clientSecret != "" {
			body := oauthErrorBody("invalid_client",
				"this app is registered as a public client and must not present a secret")
			return nil, &body, nil
		}
		return app, nil, nil
	}
	if clientSecret == "" {
		body := oauthErrorBody("invalid_client", "client_secret is required for this app")
		return nil, &body, nil
	}
	if !hmac.Equal(h.validator.HashSecret(clientSecret), app.SecretHash) {
		body := oauthErrorBody("invalid_client", "client authentication failed")
		return nil, &body, nil
	}
	return app, nil, nil
}

// statusForOAuthError picks the HTTP status from the error CODE, per RFC 6749
// section 5.2: 400 for everything, and 401 only for invalid_client.
//
// It reads the code rather than taking a status from the call site, and that is
// the point. authenticateClient answers with two different codes -- a client it
// cannot authenticate, and a grant issued to a different client -- and a caller
// that wrote one status for both sent `invalid_grant` as a 401. A conformant
// library reads 401 as "re-authenticate the client", which cannot help when the
// code simply belongs to somebody else.
func statusForOAuthError(body oauthErrorResponse) int {
	if body.Error == "invalid_client" {
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}

// respondClientAuthFailure writes the refusal a client-authentication failure
// maps to, and reports whether it wrote one. Every token stage answers the two
// failure shapes the same way -- an internal error as a 500 the client retries,
// a client refusal with the status its code derives -- and a stage that spelled
// the mapping by hand could answer one refusal with two different statuses
// after an edit to only one of them.
func respondClientAuthFailure(w http.ResponseWriter, clientErr *oauthErrorResponse, internalErr error, internalMsg string) bool {
	if internalErr != nil {
		writeInternalError(w, internalMsg, internalErr)
		return true
	}
	if clientErr != nil {
		writeOAuthErrorBody(w, statusForOAuthError(*clientErr), *clientErr)
		return true
	}
	return false
}

func (h *OAuthServerHandler) handleTokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	// RFC 7636 section 4.1: the verifier is 43-128 characters of the
	// unreserved set. A shorter one makes the stored challenge's preimage
	// guessable, so the limit is enforced on the exchange exactly as the
	// authorize stage enforces it on the challenge.
	if code == "" || !pkce.ValidVerifier(verifier) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"code is required and code_verifier must be 43-128 characters from the unreserved set")
		return
	}
	row, err := h.store.OAuthAuthorizationCodes().GetActive(r.Context(), code, h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A code that is unknown OR already consumed. The REPLAY case is
			// the dangerous one, and RFC 6749 section 4.1.2 says what to do
			// about it: revoke the credential the first exchange minted, since
			// a second presentation means the code leaked.
			//
			// The remedy runs only for a caller that AUTHENTICATES as the
			// code's app. RFC 6749 section 3.2.1 authenticates the client
			// before the grant is acted on, and in the wrong hands the remedy
			// is a denial of service: the code is a one-time value a referrer
			// or a log can leak, and a caller that holds nothing else must not
			// reach the credential it minted.
			app, clientErr, internalErr := h.authenticateClientAllowRevoked(r.Context(), r)
			switch {
			case internalErr != nil:
				slog.WarnContext(r.Context(), "could not authenticate the client presenting a replayed code",
					"err", internalErr)
			case clientErr != nil:
				// No revocation; the invalid_grant below is the whole answer.
			default:
				h.revokeReplayedCode(r.Context(), code, app)
			}
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
		} else {
			writeInternalError(w, "authorization code lookup failed", err)
		}
		return
	}
	app, clientErr, internalErr := h.authenticateClient(r.Context(), r, row.ClientID)
	if respondClientAuthFailure(w, clientErr, internalErr, "client lookup failed") {
		return
	}
	// RFC 6749 section 4.1.3: the redirect_uri MUST be present and identical to
	// the one the authorization used. Without it a code intercepted at one
	// registered address could be redeemed as though it came from another.
	if presented := r.FormValue("redirect_uri"); presented != row.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant",
			"redirect_uri does not match the one used to obtain this code")
		return
	}
	expected := pkce.S256(verifier)
	// hmac.Equal is the codebase's one spelling of a constant-time comparison
	// of secret-derived values, so an audit for them greps a single symbol.
	if !hmac.Equal([]byte(expected), []byte(row.CodeChallenge)) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	// The grant row's user_id is a column, so a blank one is corrupt data, not
	// a programmer error: the handler refuses it as an unusable grant rather
	// than panicking on it.
	grantUID, mintOK := userid.New(row.UserID)
	if !mintOK {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
		return
	}
	scopes, scopeErr := authscope.Parse(row.GrantedScopes)
	if scopeErr != nil {
		writeInternalError(w, "authorization code carries an unreadable grant", scopeErr)
		return
	}
	// Everything comes from the GRANT ROW -- the app, the scope, the label --
	// and nothing from this request's form. Otherwise any holder of an
	// authorization code could widen what the user consented to, or label the
	// credential as the victim's own laptop in the connected-app list and in the
	// issuance notice while the consent page showed something else.
	h.issueTokenResponse(w, r, consentedGrant{
		id:               code,
		userID:           grantUID,
		app:              app,
		installationName: row.InstallationName,
		scopes:           scopes,
		invalidGrantMsg:  "code expired or already consumed",
		internalMsg:      "authorization code token issuance failed",
		consume: func(tx store.Store) error {
			if _, err := tx.OAuthAuthorizationCodes().Consume(r.Context(), code, h.now().UTC()); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return errAuthorizationGrantUnavailable
				}
				return fmt.Errorf("consume authorization code: %w", err)
			}
			return nil
		},
		afterCommit: func(ctx context.Context, tokenID string) {
			// Stamped AFTER the mint, so a replay can identify what to revoke.
			// A failure here is logged and not fatal: the credential is
			// already live, and refusing to return it would be worse than
			// losing the ability to auto-revoke on a replay that may never
			// happen.
			if err := h.store.OAuthAuthorizationCodes().MarkMinted(ctx, code, tokenID); err != nil {
				slog.ErrorContext(ctx, "could not record which credential an authorization code minted",
					"code_id", code, "token_id", tokenID, "err", err)
			}
		},
	})
}

// revokeReplayedCode applies RFC 6749 section 4.1.2's remedy for a code
// presented twice: revoke the credential the FIRST exchange minted.
//
// A second presentation means the code reached somebody who should not hold
// it, so the credential it already produced is presumed compromised. Best
// effort: a code that was merely unknown identifies no credential, and a store
// failure here must not change the answer the caller gives.
//
// The caller must have AUTHENTICATED as app, and the revocation binds to it:
// a code is redeemable only by the app it was issued to (RFC 6749 section
// 4.1.3), so the same binding governs the remedy -- one app's replayed code
// must not let a different app's caller revoke it.
func (h *OAuthServerHandler) revokeReplayedCode(ctx context.Context, code string, app *store.OAuthClient) {
	row, err := h.store.OAuthAuthorizationCodes().Get(ctx, code)
	if err != nil || row.MintedTokenID == "" {
		return
	}
	if row.ClientID != app.ClientID {
		return
	}
	if _, err := h.store.APITokens().Revoke(ctx, row.MintedTokenID); err != nil {
		slog.ErrorContext(ctx, "could not revoke the credential a replayed authorization code minted",
			"token_id", row.MintedTokenID, "err", err)
		return
	}
	h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.MintedTokenID)
	slog.WarnContext(ctx, "an authorization code was presented twice; the credential it minted is revoked",
		"token_id", row.MintedTokenID)
}

// consentedGrant is what a validated OAuth grant hands the mint: who consented,
// WHICH APP they consented to, what they consented TO, and the two refusals
// this exchange can answer with.
//
// A struct rather than positional arguments, because two of them are adjacent
// same-typed message strings that a call site can transpose in silence -- the
// caller would then answer "code expired" for an internal failure and log the
// grant message for a live one. Writing their names at the call site makes that
// mistake visible, and a further fact about the consent is added in one place.
type consentedGrant struct {
	// id is the grant row this mint redeems. It is what the mint reads as its
	// AUTHORITY: /oauth/token carries no session at all, so the browser consent
	// that produced this row is where the elevated session was required, and
	// holding the row is the proof.
	id               string
	userID           userid.UserID
	app              *store.OAuthClient
	installationName string
	scopes           authscope.ScopeSet
	// invalidGrantMsg answers a single-use grant that is already gone
	// (errAuthorizationGrantUnavailable); internalMsg answers anything else.
	invalidGrantMsg string
	internalMsg     string
	// consume spends the grant inside the mint's transaction.
	consume func(tx store.Store) error
	// afterCommit runs once the credential exists, with its id. Optional.
	afterCommit func(ctx context.Context, tokenID string)
}

// issueTokenResponse mints an app credential for an already-validated OAuth
// grant and writes the RFC 6749 / RFC 8628 token response: the token JSON on
// success, invalid_grant when the single-use consume closure reports the grant
// is gone, or an internal error otherwise. The two grant handlers share this
// issue-and-map tail so they cannot drift on the error codes they must both
// emit.
func (h *OAuthServerHandler) issueTokenResponse(w http.ResponseWriter, r *http.Request, grant consentedGrant) {
	resp, user, reachable, err := h.issueAPIToken(r.Context(), grant)
	if err != nil {
		if errors.Is(err, errAuthorizationGrantUnavailable) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", grant.invalidGrantMsg)
		} else {
			writeInternalError(w, grant.internalMsg, err)
		}
		return
	}
	if grant.afterCommit != nil {
		// WithoutCancel, for the same reason the refresh stage detaches its work:
		// a client that disconnects in the window between the commit and this
		// update must not lose the replay-revocation link -- the credential is
		// live, and a later replay of the code relies on MarkMinted to identify
		// what to revoke.
		grant.afterCommit(context.WithoutCancel(r.Context()), resp.TokenID)
	}
	// After the commit and before the response, so the notice cannot be the
	// reason a minted token is never delivered. See notifyCredentialIssued.
	h.notifyTokenIssued(r.Context(), user, grant, reachable.SortedTokens())
	writeJSON(w, http.StatusOK, resp)
}

func (h *OAuthServerHandler) handleTokenDeviceCode(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.FormValue("device_code")
	if deviceCode == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	row, err := h.store.DeviceAuthorizations().Get(r.Context(), deviceCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown device_code")
		} else {
			writeInternalError(w, "device authorization lookup failed", err)
		}
		return
	}
	app, clientErr, internalErr := h.authenticateClient(r.Context(), r, row.ClientID)
	if respondClientAuthFailure(w, clientErr, internalErr, "client lookup failed") {
		return
	}
	// Throttle / expiry / already-consumed run before TouchPoll so a
	// fast-polling client gets `slow_down` rather than consuming the interval
	// window. Pending and denied polls touch immediately. An approved poll also
	// touches immediately -- before, and outside, the issuance transaction --
	// so last_polled_at advances (keeping the slow_down throttle honest) even
	// when a transient token-insert failure rolls the transaction back. The
	// grant stays retryable because only Consume, which remains inside the
	// transaction, is single-use.
	if body, ok := h.preTouchPollOAuthError(row, h.now()); ok {
		writeOAuthErrorBody(w, http.StatusBadRequest, body)
		return
	}
	if body, ok := h.postTouchPollOAuthError(row); ok {
		if !h.touchPoll(w, r, deviceCode) {
			return
		}
		writeOAuthErrorBody(w, http.StatusBadRequest, body)
		return
	}
	// Advance last_polled_at outside the issuance transaction so the throttle
	// anchor moves forward even if issuance later fails and rolls back --
	// otherwise a client that polls an approved-but-transiently-failing grant
	// rapidly would never get slow_down. Touch errors are internal, matching
	// the pending/denied path above. The instant is the HUB's clock, the same
	// clock shouldThrottle measures with: TouchPoll writes it, so one clock
	// domain answers the whole throttle.
	if !h.touchPoll(w, r, deviceCode) {
		return
	}
	// postTouchPollOAuthError above already answers authorization_pending for a
	// blank user_id, so this cannot fail -- minting rather than asserting keeps
	// that true if the guard above is ever moved.
	pollUID, mintOK := userid.New(row.UserID)
	if !mintOK {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device code expired or already consumed")
		return
	}
	// A STEP-UP grant mints nothing, and this branch is what makes that true.
	// The approval already stamped the window on the credential the caller
	// holds; issuing a token here would turn a request to verify an existing
	// credential into a second credential, which is precisely the widening the
	// step-up flow exists to avoid.
	if isElevationGrant(row) {
		h.answerElevationPoll(w, r, row, deviceCode)
		return
	}
	scopes, scopeErr := authscope.Parse(row.GrantedScopes)
	if scopeErr != nil {
		writeInternalError(w, "device grant carries an unreadable grant", scopeErr)
		return
	}
	h.issueTokenResponse(w, r, consentedGrant{
		id:               deviceCode,
		userID:           pollUID,
		app:              app,
		installationName: row.DeviceName,
		scopes:           scopes,
		invalidGrantMsg:  "device_code expired or already consumed",
		internalMsg:      "device authorization token issuance failed",
		consume: func(tx store.Store) error {
			// Consume is the single-use consumption that must stay atomic with
			// token creation, so it -- and only it -- remains in the
			// transaction.
			affected, err := tx.DeviceAuthorizations().Consume(r.Context(), deviceCode, h.now().UTC())
			if err != nil {
				return fmt.Errorf("consume device authorization: %w", err)
			}
			if affected != 1 {
				return errAuthorizationGrantUnavailable
			}
			return nil
		},
	})
}

// answerElevationPoll finishes a step-up poll: it reports the window the
// approval stamped, and spends the grant.
func (h *OAuthServerHandler) answerElevationPoll(w http.ResponseWriter, r *http.Request, row *store.DeviceAuthorization, deviceCode string) {
	// The approval FLAG is not the answer, so this reads the credential itself.
	// An approval whose elevation never committed leaves the row approved with
	// no window; answering "elevated" there tells the client to retry a command
	// the hub refuses again, with no error the user can act on. The check runs
	// before Consume, so a grant that cannot report success is not spent
	// reporting it.
	current, err := h.credentialElevationIsCurrent(r.Context(), row.ElevateTokenID)
	if err != nil {
		writeInternalError(w, "elevation credential lookup failed", err)
		return
	}
	if !current {
		// Not errElevationGrantUnavailable's own text. That sentence says the
		// credential is GONE, and the more common case here is a credential
		// that is present with no window on it.
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant",
			"that app credential is not verified; start the verification again")
		return
	}
	if err := h.spendDeviceGrant(r.Context(), deviceCode); err != nil {
		if errors.Is(err, errAuthorizationGrantUnavailable) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code expired or already consumed")
			return
		}
		writeInternalError(w, "elevation grant consumption failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"elevated": true})
}

// spendDeviceGrant consumes a device grant outside a transaction and maps the
// two ways it can fail. ONE mapping for the elevation poll and the consent
// mint's consume closure, so the "already consumed" answer cannot drift in
// code or message between the two stages that spend the same row.
func (h *OAuthServerHandler) spendDeviceGrant(ctx context.Context, deviceCode string) error {
	affected, err := h.store.DeviceAuthorizations().Consume(ctx, deviceCode, h.now().UTC())
	if err != nil {
		return err
	}
	if affected != 1 {
		return errAuthorizationGrantUnavailable
	}
	return nil
}

// preTouchPollOAuthError returns the OAuth-error code + description for the
// state guards that must short-circuit BEFORE TouchPoll runs: already-consumed,
// expired, or rapid-poll throttle.
func (h *OAuthServerHandler) preTouchPollOAuthError(row *store.DeviceAuthorization, now time.Time) (oauthErrorResponse, bool) {
	if row.ConsumedAt != nil {
		return oauthErrorBody("invalid_grant", "device_code already used"), true
	}
	if auth.IsExpired(now, row.ExpiresAt) {
		return oauthErrorBody("expired_token", ""), true
	}
	if h.shouldThrottle(row, now) {
		return oauthErrorBody("slow_down", ""), true
	}
	return oauthErrorResponse{}, false
}

// postTouchPollOAuthError returns the OAuth error body for approval-state
// guards whose responses must still update last_polled_at. The caller performs
// that update before writing the returned body.
//
// A BODY, not a (code, description) pair: the two are adjacent same-typed
// strings that a call site can transpose in silence, which is the exact smell
// consentedGrant above exists to remove.
func (h *OAuthServerHandler) postTouchPollOAuthError(row *store.DeviceAuthorization) (oauthErrorResponse, bool) {
	switch row.Approved {
	case 0:
		return oauthErrorBody("authorization_pending", ""), true
	case 2:
		return oauthErrorBody("access_denied", ""), true
	}
	if row.UserID == "" {
		return oauthErrorBody("authorization_pending", ""), true
	}
	return oauthErrorResponse{}, false
}

// touchPoll records a poll on the device grant and reports whether the
// handler may continue. Both poll answers -- the refusal a pending or denied
// grant writes, and the issuance an approved grant starts -- must advance
// last_polled_at first, so the two spellings share one helper rather than
// restating the statement and its error answer.
func (h *OAuthServerHandler) touchPoll(w http.ResponseWriter, r *http.Request, deviceCode string) bool {
	if err := h.store.DeviceAuthorizations().TouchPoll(r.Context(), deviceCode, h.now().UTC()); err != nil {
		writeInternalError(w, "device authorization poll update failed", err)
		return false
	}
	return true
}

func (h *OAuthServerHandler) shouldThrottle(row *store.DeviceAuthorization, now time.Time) bool {
	if row.LastPolledAt == nil {
		return false
	}
	// Named minInterval, not min: a local `min` shadows the builtin for the
	// rest of the function.
	minInterval := time.Duration(row.IntervalSeconds) * time.Second
	if minInterval <= 0 {
		minInterval = DeviceCodePollInterval
	}
	// ONE clock domain: TouchPoll writes last_polled_at with the same seam this
	// reads, so no skew tolerance is needed between the two instants. (The
	// statement once wrote the DATABASE's clock, and the fudge factor that
	// tolerated the skew answered every on-time poll with slow_down wherever
	// it exceeded 250ms.)
	return now.Sub(*row.LastPolledAt) < minInterval
}

type refreshResponse struct {
	status int
	body   any
	err    error
}

// apiTokenResponse is the RFC 6749 section 5.1 token response.
type apiTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	// Scope reports what the account actually GRANTED, which is not always what
	// the app asked for: on the device flow the person at the browser may have
	// held an account that could not grant part of it. RFC 6749 section 5.1
	// requires the field whenever the two differ, and reporting it always is
	// simpler than deciding whether they did.
	Scope   string `json:"scope"`
	TokenID string `json:"token_id"`
	// UserID and Username are a LeapMux extension, so a client can print who it
	// authenticated as without a second call.
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	// RefreshExpiresIn is how long the credential can keep refreshing before the
	// account must authorize the app again -- the absolute lifetime, not the
	// access token's hour.
	RefreshExpiresIn int `json:"refresh_expires_in,omitempty"`
}

// tokenTypeBearer is the only token_type this server issues. RFC 6749 section
// 5.1 makes the field REQUIRED, and a conformant client library refuses a
// response without it.
const tokenTypeBearer = "Bearer"

func refreshOAuthError(status int, code, description string) refreshResponse {
	return refreshResponse{status: status, body: oauthErrorBody(code, description)}
}

func refreshInternalError(err error) refreshResponse {
	return refreshResponse{status: http.StatusInternalServerError, err: err}
}

// refreshTokenResponse builds the rotation payload.
//
// It reports refresh_expires_in and scope, exactly as the minting response
// does. Omitting them made the rotation answer look like a credential with no
// absolute deadline and no grant: `refresh_expires_in` carries omitempty, so a
// client never advanced the deadline it stores and `auth status` printed a date
// further into the past on every rotation.
func refreshTokenResponse(tokenID, accessBearer, refreshBearer string, expiresIn, refreshExpiresIn int, scope string) refreshResponse {
	return refreshResponse{
		status: http.StatusOK,
		body: apiTokenResponse{
			AccessToken:      accessBearer,
			TokenType:        tokenTypeBearer,
			RefreshToken:     refreshBearer,
			ExpiresIn:        expiresIn,
			Scope:            scope,
			TokenID:          tokenID,
			RefreshExpiresIn: refreshExpiresIn,
		},
	}
}

func remainingExpiresIn(expiresAt, now time.Time) int {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int(math.Ceil(remaining.Seconds()))
}

func writeRefreshResponse(w http.ResponseWriter, resp refreshResponse) {
	if resp.err != nil {
		writeInternalError(w, "refresh token request failed", resp.err)
		return
	}
	writeJSON(w, resp.status, resp.body)
}

// reachableGrantOf is the ONE answer to "what can this credential actually do":
// its stored consent intersected with its app's REGISTERED ceiling and with the
// credential KIND's own ceiling -- every limit loadBearer applies at validation.
//
// The kind ceiling is the same value for every api_tokens row today
// (CeilingFor(BearerKindAPI) is the whole grantable vocabulary, and NarrowTo by
// it is the identity), so applying it here changes nothing now; it keeps this
// the same intersection loadBearer computes on the day that ceiling narrows,
// which is exactly the day a reporting surface that skipped it would start
// listing permissions the credential's next call is refused for.
// Three do -- the refresh response's `scope`, the account's own connected-app
// list, and the administrator's credential listing -- and each of them reading
// the stored column instead would list a permission the app's very next call
// is refused, on the exact screens a person consults to decide what an app can
// reach.
//
// An unreadable value on either side answers the EMPTY set with false, which
// every caller renders as no permissions. That matches what happens at
// validation, where the same unreadable value refuses the credential outright:
// a listing must not show a grant as legible when the hub cannot read it.
func reachableGrantOf(grantedScopes, clientScopes string) (authscope.ScopeSet, bool) {
	granted, err := authscope.Parse(grantedScopes)
	if err != nil {
		return authscope.ScopeSet{}, false
	}
	ceiling, err := authscope.Parse(clientScopes)
	if err != nil {
		return authscope.ScopeSet{}, false
	}
	return granted.NarrowTo(ceiling).NarrowTo(auth.CeilingFor(auth.BearerKindAPI)), true
}

func (h *OAuthServerHandler) issueAPIToken(
	ctx context.Context,
	grant consentedGrant,
) (*apiTokenResponse, *store.User, authscope.ScopeSet, error) {
	userID := grant.userID
	now := h.now()
	granted, err := grant.scopes.Storable()
	if err != nil {
		return nil, nil, authscope.ScopeSet{}, err
	}
	// What the credential REACHES on its first call, not what the consent
	// alone states: the grant intersected with the app's registration as it
	// stands at THIS exchange. An owner can shrink a registration inside a
	// code's ten-minute TTL or between a device grant's approval rows, and the
	// response's `scope` -- which the CLI persists and prints -- must not list
	// a permission loadBearer refuses on the very next call. This is the same
	// rule narrowedRefreshScope states for the rotation, applied at the mint.
	//
	// ONE derivation for both readers: the response's `scope` and the issuance
	// notice's permission list are computed from this set, so the two cannot
	// disagree after an edit to one of them.
	reachable := grant.scopes.NarrowTo(appScopeCeiling(grant.app))
	reachableValue, err := reachable.Storable()
	if err != nil {
		return nil, nil, authscope.ScopeSet{}, err
	}
	// Rotating, always: a consent stage mints the credential an app will keep
	// using, and the short access token plus a refresh stage is what limits a
	// stolen credential file to one hour of use without the refresh secret.
	minted, err := mintAPIToken(h.validator, mintedByConsentGrant(grant.id), now, apiTokenMint{
		UserID:           userID,
		ClientID:         grant.app.ClientID,
		InstallationName: grant.installationName,
		GrantedScopes:    granted,
		Rotating:         true,
	})
	if err != nil {
		return nil, nil, authscope.ScopeSet{}, err
	}
	var user *store.User
	err = h.store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		if err := grant.consume(tx); err != nil {
			return err
		}
		var err error
		user, err = tx.Users().GetByID(ctx, userID.String())
		if err != nil {
			return fmt.Errorf("query token user: %w", err)
		}
		if err := tx.APITokens().Create(ctx, minted.Params); err != nil {
			return fmt.Errorf("create api token: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, authscope.ScopeSet{}, err
	}
	return &apiTokenResponse{
		AccessToken:      minted.Pair.AccessBearer,
		TokenType:        tokenTypeBearer,
		RefreshToken:     minted.RefreshBearer(),
		ExpiresIn:        remainingExpiresIn(minted.Pair.AccessExpiresAt, h.now()),
		RefreshExpiresIn: minted.RefreshExpiresIn(h.now()),
		Scope:            reachableValue,
		TokenID:          minted.TokenID,
		UserID:           userID.String(),
		Username:         user.Username,
	}, user, reachable, nil
}

// notifyTokenIssued supplies this handler's mailer to the shared notice. See
// notifyCredentialIssued, which both mint surfaces call.
//
// The notice lists the REACHABLE grant the mint already computed, so the
// recipient reads the same permission list the response's `scope` states: the
// two are one value, not two derivations that can drift.
func (h *OAuthServerHandler) notifyTokenIssued(ctx context.Context, user *store.User, grant consentedGrant, scopes []string) {
	// The owner performed this consent in their own browser.
	notifyCredentialIssued(ctx, h.mail, h.renderer, user, credentialNotice{
		AppName:          grant.app.ClientName,
		InstallationName: grant.installationName,
		Scopes:           scopes,
		IssuedByAdmin:    false,
	})
}
