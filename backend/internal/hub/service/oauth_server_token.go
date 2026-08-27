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
// answers apart, and the token leg did not -- the discipline this restores.
//
// The CONTEXT is the caller's to choose while the REQUEST stays the request:
// the refresh leg reads the form from a request whose context may already be
// canceled (its flight detaches for exactly that reason), so it passes the
// detached flight context here and the form values still come from `r`.
func (h *OAuthServerHandler) authenticateClient(ctx context.Context, r *http.Request, presentedID string) (*store.OAuthClient, *oauthErrorResponse, error) {
	return h.authenticateClientOpts(ctx, r, presentedID, false)
}

// authenticateClientAllowRevoked runs the same authentication for the RFC 7009
// revocation leg, where a RETIRED app is not a refusal: its credentials were
// revoked by the retirement cascade, so the idempotent branch in
// bindRevocationToClient answers the retrying client with the 200 the endpoint
// promises. Every other leg keeps refusing a retired app at the door.
func (h *OAuthServerHandler) authenticateClientAllowRevoked(ctx context.Context, r *http.Request) (*store.OAuthClient, *oauthErrorResponse, error) {
	return h.authenticateClientOpts(ctx, r, "", true)
}

func (h *OAuthServerHandler) authenticateClientOpts(ctx context.Context, r *http.Request, presentedID string, allowRevoked bool) (*store.OAuthClient, *oauthErrorResponse, error) {
	clientID, clientSecret, hasBasic := r.BasicAuth()
	if !hasBasic {
		clientID = r.FormValue("client_id")
		clientSecret = r.FormValue("client_secret")
	}
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
	// nil viewer: the token leg carries no session. A PRIVATE app therefore
	// cannot be resolved here -- which is correct for the device flow, and
	// harmless for the code flow, where the grant row already proved the
	// account authorized this app. See resolveGrantApp.
	app, err := h.store.OAuthClients().Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			body := oauthErrorBody("invalid_client", "unknown or unavailable client_id")
			return nil, &body, nil
		}
		return nil, nil, err
	}
	if app.RevokedAt != nil && !allowRevoked {
		body := oauthErrorBody("invalid_client", "unknown or unavailable client_id")
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

func (h *OAuthServerHandler) handleTokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	if code == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}
	row, err := h.store.OAuthAuthorizationCodes().GetActive(r.Context(), code, h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A code that is unknown OR already consumed. The REPLAY case is
			// the dangerous one, and RFC 6749 section 4.1.2 says what to do
			// about it: revoke the credential the first exchange minted, since
			// a second presentation means the code leaked.
			h.revokeReplayedCode(r.Context(), code)
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
		} else {
			writeInternalError(w, "authorization code lookup failed", err)
		}
		return
	}
	app, clientErr, internalErr := h.authenticateClient(r.Context(), r, row.ClientID)
	if internalErr != nil {
		writeInternalError(w, "client lookup failed", internalErr)
		return
	}
	if clientErr != nil {
		writeOAuthErrorBody(w, statusForOAuthError(*clientErr), *clientErr)
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
			// Stamped AFTER the mint, so a replay can name what to revoke.
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
// effort: a code that was merely unknown names no credential, and a store
// failure here must not change the answer the caller gives.
func (h *OAuthServerHandler) revokeReplayedCode(ctx context.Context, code string) {
	row, err := h.store.OAuthAuthorizationCodes().Get(ctx, code)
	if err != nil || row.MintedTokenID == "" {
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
	resp, user, err := h.issueAPIToken(r.Context(), grant)
	if err != nil {
		if errors.Is(err, errAuthorizationGrantUnavailable) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", grant.invalidGrantMsg)
		} else {
			writeInternalError(w, grant.internalMsg, err)
		}
		return
	}
	if grant.afterCommit != nil {
		// WithoutCancel, for the same reason the refresh leg detaches its work:
		// a client that disconnects in the window between the commit and this
		// update must not lose the replay-revocation link -- the credential is
		// live, and a later replay of the code relies on MarkMinted to name
		// what to revoke.
		grant.afterCommit(context.WithoutCancel(r.Context()), resp.TokenID)
	}
	// After the commit and before the response, so the notice cannot be the
	// reason a minted token is never delivered. See notifyCredentialIssued.
	h.notifyTokenIssued(r.Context(), user, grant)
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
	if internalErr != nil {
		writeInternalError(w, "client lookup failed", internalErr)
		return
	}
	if clientErr != nil {
		writeOAuthErrorBody(w, statusForOAuthError(*clientErr), *clientErr)
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
		if err := h.store.DeviceAuthorizations().TouchPoll(r.Context(), deviceCode); err != nil {
			writeInternalError(w, "device authorization poll update failed", err)
			return
		}
		writeOAuthErrorBody(w, http.StatusBadRequest, body)
		return
	}
	// Advance last_polled_at outside the issuance transaction so the throttle
	// anchor moves forward even if issuance later fails and rolls back --
	// otherwise a client that polls an approved-but-transiently-failing grant
	// rapidly would never get slow_down. Touch errors are internal, matching
	// the pending/denied path above.
	if err := h.store.DeviceAuthorizations().TouchPoll(r.Context(), deviceCode); err != nil {
		writeInternalError(w, "device authorization poll update failed", err)
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
	affected, err := h.store.DeviceAuthorizations().Consume(r.Context(), deviceCode, h.now().UTC())
	if err != nil {
		writeInternalError(w, "elevation grant consumption failed", err)
		return
	}
	if affected != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code expired or already consumed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"elevated": true})
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
	return now.Sub(*row.LastPolledAt) < (minInterval - 250*time.Millisecond)
}

type parsedRefreshBearer struct {
	bearer     string
	tokenID    string
	secretHash []byte
}

func (h *OAuthServerHandler) parseAPIRefreshBearer(refresh string) (parsedRefreshBearer, error) {
	kind, tokenID, secret, err := auth.ParseBearer(refresh)
	if err != nil {
		return parsedRefreshBearer{}, err
	}
	if kind != auth.BearerKindAPI {
		return parsedRefreshBearer{}, auth.ErrInvalidToken
	}
	return parsedRefreshBearer{
		bearer:     refresh,
		tokenID:    tokenID,
		secretHash: h.validator.HashSecret(secret),
	}, nil
}

func (b parsedRefreshBearer) flightKey() string {
	return fmt.Sprintf("%d:%s:%x", len(b.tokenID), b.tokenID, b.secretHash)
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

// refreshRetryResponse re-emits the pair a racing rotation already wrote.
//
// Both deadlines come from the ROW, not from the freshly derived pair: the
// winning rotation is what the row records, and this leg only reproduces its
// answer. Reading the pair here would report a window the store never stored.
//
// The SCOPE keeps this caller's own narrowing. The racing winner wrote its own
// narrowing to the column, and re-emitting the winner's reachable grant would
// hand a caller a response that states permissions it explicitly asked to drop
// -- the exact outcome the flight-key comment promises cannot happen, and
// singleflight cannot dedupe two processes, two hubs, or two keys. The
// reported value is therefore the row's reachable grant intersected with what
// THIS request asked for, which is the widest set this caller may believe it
// holds; the stored column stays the winner's, because only the winner rotates.
func (h *OAuthServerHandler) refreshRetryResponse(row *store.APIToken, pair auth.MintedBearerPair, requestedScope string) refreshResponse {
	if row.ExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no access expiration", row.ID))
	}
	if row.RefreshExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no refresh expiration", row.ID))
	}
	scope := reachableScopeOf(row)
	if requestedScope != "" {
		decision, err := h.narrowedRefreshScope(row, requestedScope)
		if err != nil {
			return refreshOAuthError(http.StatusBadRequest, "invalid_scope", err.Error())
		}
		scope = decision.reported
	}
	now := h.now()
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(*row.ExpiresAt, now),
		remainingExpiresIn(*row.RefreshExpiresAt, now),
		scope,
	)
}

func writeRefreshResponse(w http.ResponseWriter, resp refreshResponse) {
	if resp.err != nil {
		writeInternalError(w, "refresh token request failed", resp.err)
		return
	}
	writeJSON(w, resp.status, resp.body)
}

// handleTokenRefresh runs the RFC 6749 section 6 refresh grant.
//
// It accepts a NARROWING `scope` parameter, which section 6 permits and which
// is the only direction a refresh may move: the value is intersected with the
// stored grant, so a request for something the account never granted yields the
// granted set rather than the requested one, and a request for LESS persists.
func (h *OAuthServerHandler) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.FormValue("refresh_token")
	if refresh == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	// RFC 6749 section 5.2 makes 400 the status for every token-endpoint
	// error EXCEPT invalid_client, and the distinction is one a client library
	// acts on: 401 says "your client credentials are wrong, authenticate
	// again", 400 says "this grant is finished". A 401 here sent a conformant
	// library back to re-authenticate the CLIENT for a refresh token that had
	// been revoked, which can never succeed.
	parsed, err := h.parseAPIRefreshBearer(refresh)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	requestedScope := r.FormValue("scope")
	// Blocking Do (not DoChan + ctx select) is deliberate: a refresh rotates the
	// token single-use, so once the flight starts, every caller -- including one
	// whose client disconnected -- must run to completion and receive the same
	// rotated pair, or it is left with a rotated-away refresh token and no
	// replacement. flightCtx (WithoutCancel) already decouples the work from the
	// leader's request cancellation. This is why it differs from the read-only
	// bearer-validation singleflight, which is safe to abandon on disconnect.
	//
	// The context and its timer are built INSIDE the closure, because only the
	// leader's closure runs. Built outside, every follower allocated a context
	// and armed a timer that nothing ever read.
	//
	// The flight key carries the requested scope, so two concurrent refreshes
	// asking for DIFFERENT narrowings are not collapsed onto one answer -- a
	// follower would otherwise receive a pair whose grant it never asked for.
	result, _, _ := h.refreshFlight.Do(parsed.flightKey()+"|"+requestedScope, func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), RefreshWorkTimeout)
		defer cancel()
		return h.refresh(flightCtx, r, parsed, requestedScope), nil
	})
	writeRefreshResponse(w, result.(refreshResponse))
}

func (h *OAuthServerHandler) refresh(ctx context.Context, r *http.Request, parsed parsedRefreshBearer, requestedScope string) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}

	// RFC 6749 sections 6 and 3.2.1: a client that was issued credentials
	// authenticates on the refresh grant too. The code, device and revocation
	// legs already demand it; without it here, a leaked confidential refresh
	// bearer rotated freely -- the app secret, the half of the proof the
	// registration exists to add, protected nothing on exactly the leg that
	// mints the long-lived pair. A public client satisfies this by naming
	// itself with client_id, which the control CLI sends on every refresh.
	if _, clientErr, internalErr := h.authenticateClientOpts(ctx, r, row.ClientID, false); internalErr != nil {
		return refreshInternalError(internalErr)
	} else if clientErr != nil {
		return refreshResponse{status: statusForOAuthError(*clientErr), body: *clientErr}
	}

	now := h.now()
	// This leg clips the refresh window to the credential's absolute lifetime,
	// measured from created_at. Without the clip every rotation would push the
	// window a full RefreshTokenTTL forward, so a client that refreshes weekly
	// would keep ONE browser consent alive for ever.
	refreshTTL := auth.RefreshWindowFor(row.CreatedAt, now)
	if refreshTTL <= 0 {
		// Revoke the ROW, not only the cache. Every other caller of
		// BearerRevoked reaches it on a row the store already revoked -- the
		// validator revokes on a confirmed reuse, and a revoked or expired row
		// is refused before it gets here. This leg is the one that decides a
		// credential is dead by itself, so it must also record it: without the
		// write the access token keeps authenticating until its own expiry, up
		// to an hour after the hub told the client the credential was finished,
		// and the row keeps listing as live in Preferences.
		if _, err := h.store.APITokens().Revoke(ctx, row.ID); err != nil {
			slog.ErrorContext(ctx, "could not revoke the credential that reached its maximum lifetime",
				"token_id", row.ID, "err", err)
		}
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant",
			"this credential reached its maximum lifetime; authorize the app again")
	}

	decision, scopeErr := h.narrowedRefreshScope(row, requestedScope)
	if scopeErr != nil {
		return refreshOAuthError(http.StatusBadRequest, "invalid_scope", scopeErr.Error())
	}

	// This leg clips both windows to the SAME ceiling. Bearer validation reads
	// expires_at alone, so an unclipped access token outlives the absolute
	// lifetime this leg just enforced: the last rotation before the ceiling
	// wrote a full hour past it, and the credential kept authenticating for
	// that hour after the hub already answered "this credential reached its
	// maximum lifetime".
	pair := h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		now,
		auth.AccessWindowFor(row.CreatedAt, now),
		refreshTTL,
	)

	if retry {
		return h.refreshRetryResponse(row, pair, requestedScope)
	}

	// First use of the current refresh: rotate both secrets in place on the
	// existing row. The access secret_hash + expires_at must also advance,
	// otherwise the bearer we hand back (`row.ID` + newAccess) won't validate
	// against `row.SecretHash`, which still hashes the rotated-out access
	// secret. The rotation preserves the previous refresh hash and its grace
	// window so a racing retry can deterministically derive and re-emit this
	// same pair on any Hub.
	prevHash := row.RefreshHash
	prevExp := now.Add(auth.RefreshReuseGrace)
	rotated, err := h.store.APITokens().RotateRefresh(ctx, store.RotateAPITokenRefreshParams{
		ID:                       row.ID,
		NewSecretHash:            pair.AccessHash,
		NewExpiresAt:             &pair.AccessExpiresAt,
		NewRefreshHash:           pair.RefreshHash,
		NewRefreshExpiresAt:      &pair.RefreshExpiresAt,
		PreviousRefreshHash:      prevHash,
		PreviousRefreshExpiresAt: &prevExp,
		NewGrantedScopes:         decision.stored,
	})
	if err != nil {
		return refreshInternalError(err)
	}
	if rotated != 1 {
		return h.recoverRefreshCASMiss(ctx, r, parsed, requestedScope)
	}
	// A NARROWING is a withdrawal of authority, so it runs the full teardown
	// rather than only invalidating the cached secret: an open Noise channel
	// carries the scope set announced at its handshake, and the hub cannot
	// renegotiate a session it cannot read, so closing it is the only way to
	// take the authority back. A widening or an unchanged grant is
	// cache-and-extend, as a plain rotation always was.
	//
	// ONE call takes both effects, so a narrowing refresh cannot leave every
	// channel running at the withdrawn authority because the caller made two
	// calls and only the first matched.
	h.lifecycle.BearerRotated(auth.BearerKindAPI, row.ID, pair.AccessExpiresAt, decision.narrowed)

	// Both deadlines come from the pair, because RotateRefresh just wrote it:
	// here the pair IS the row. now is the instant the pair was derived from,
	// so the two reported windows and the stored ones agree exactly.
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(pair.AccessExpiresAt, now),
		remainingExpiresIn(pair.RefreshExpiresAt, now),
		// What the credential REACHES, not what the column keeps. See
		// narrowedRefreshScope.
		decision.reported,
	)
}

// refreshScopeDecision is what narrowedRefreshScope answers. A struct rather
// than positional values, because stored and reported are adjacent
// same-typed strings a call site can transpose in silence -- the same smell
// consentedGrant and postTouchPollOAuthError exist to remove.
type refreshScopeDecision struct {
	// stored is what the column keeps: the account's CONSENT, narrowed only by
	// what this request asked to give up.
	stored string
	// reported is what the response names in `scope`: the consent intersected
	// with the app's registered ceiling.
	reported string
	// narrowed says whether the REACHABLE grant shrank, which decides whether
	// the rotation is a teardown or an extension.
	narrowed bool
}

// narrowedRefreshScope applies RFC 6749 section 6's scope rule.
//
// A refresh may ask for LESS and never for more, so the requested set is
// intersected with the grant. An empty request keeps the grant unchanged,
// which is what a client that does not care sends.
//
// It answers a refreshScopeDecision rather than positional values; the struct
// states why stored and reported are two different things:
//
//   - stored is what the column keeps: the account's CONSENT, narrowed only by
//     what this request asked to give up. The app's registered ceiling is
//     deliberately not written into it -- see the ceiling paragraph below.
//   - reported is what the response names in `scope`: the consent intersected
//     with that ceiling, which is what loadBearer computes at every validation
//     and therefore what the credential can actually do. Reporting the stored
//     value instead would name a permission the app's next call is refused.
//   - narrowed says whether the REACHABLE grant shrank, which is what decides
//     whether the rotation is a teardown or an extension.
//
// The ceiling is read here and never written. An owner who removes a
// permission from the registration takes it away at once, because validation
// re-reads the ceiling; folding it into the column instead would make the loss
// permanent, so putting the permission back would not restore what the account
// had already consented to.
func (h *OAuthServerHandler) narrowedRefreshScope(row *store.APIToken, requested string) (refreshScopeDecision, error) {
	current, err := authscope.Parse(row.GrantedScopes)
	if err != nil {
		return refreshScopeDecision{}, err
	}
	ceiling, err := authscope.Parse(row.ClientScopes)
	if err != nil {
		return refreshScopeDecision{}, err
	}
	// What this credential reaches TODAY, which is what an ask is measured
	// against. Measuring against the stored consent instead would let an app
	// whose registration just lost a permission ask for it and be told yes.
	reachable := current.NarrowTo(ceiling)

	next := current
	if requested != "" {
		asked, parseErr := authscope.Parse(requested)
		if parseErr != nil {
			return refreshScopeDecision{}, parseErr
		}
		// A request for something the credential cannot reach is REFUSED, not
		// quietly intersected away.
		//
		// RFC 6749 section 5.2 defines invalid_scope for exactly this -- a
		// request that "exceeds the scope granted by the resource owner" --
		// and refusing is the safer of the two readings. An app handed a
		// credential silently missing a permission it asked for discovers the
		// loss at its first call, far from the refresh, while its own state
		// says it holds the permission.
		//
		// The MESSAGE states which of the two causes applies, because they
		// ask the operator to do different things: a genuine widening is the
		// app's own bug, while a consent that covers the ask and a
		// registration that no longer does is the owner's edit, and the
		// "never widen" sentence would send them chasing the wrong one.
		if !reachable.Contains(asked) {
			if current.Contains(asked) {
				return refreshScopeDecision{}, errors.New(
					"this app is not registered for every permission it asked for")
			}
			return refreshScopeDecision{}, errors.New("a refresh may narrow a grant and never widen it")
		}
		next = asked.Close()
	}
	storable, err := next.Storable()
	if err != nil {
		return refreshScopeDecision{}, err
	}
	reachableNext := next.NarrowTo(ceiling)
	reportable, err := reachableNext.Storable()
	if err != nil {
		return refreshScopeDecision{}, err
	}
	return refreshScopeDecision{stored: storable, reported: reportable, narrowed: reachableNext != reachable}, nil
}

// reachableGrantOf is the ONE answer to "what can this credential actually do":
// its stored consent intersected with its app's REGISTERED ceiling and with the
// credential KIND's own ceiling -- every bound loadBearer applies at validation.
//
// The kind ceiling is the same value for every api_tokens row today
// (CeilingFor(BearerKindAPI) is the whole grantable vocabulary, and NarrowTo by
// it is the identity), so applying it here changes nothing now; it keeps this
// the same intersection loadBearer computes on the day that ceiling narrows,
// which is exactly the day a reporting surface that skipped it would start
// naming permissions the credential's next call is refused for.
// Three do -- the refresh response's `scope`, the account's own connected-app
// list, and the administrator's credential listing -- and each of them reading
// the stored column instead would name a permission the app's very next call
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

// reachableScopeOf renders reachableGrantOf as the canonical scope string, for
// the refresh RETRY path.
//
// That path re-emits the pair a racing caller already minted without rotating
// anything, so it reads the row rather than a freshly computed grant. It falls
// back to the stored string when the pair cannot be read, because a refresh
// response is the wrong place to discover a drifted vocabulary -- validation
// already refuses the credential for it.
func reachableScopeOf(row *store.APIToken) string {
	reachable, ok := reachableGrantOf(row.GrantedScopes, row.ClientScopes)
	if !ok {
		return row.GrantedScopes
	}
	value, err := reachable.Storable()
	if err != nil {
		return row.GrantedScopes
	}
	return value
}

func (h *OAuthServerHandler) recoverRefreshCASMiss(ctx context.Context, r *http.Request, parsed parsedRefreshBearer, requestedScope string) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}
	if !retry {
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "token revoked")
	}
	now := h.now()
	pair := h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		now,
		auth.AccessWindowFor(row.CreatedAt, now),
		auth.RefreshWindowFor(row.CreatedAt, now),
	)
	return h.refreshRetryResponse(row, pair, requestedScope)
}

func (h *OAuthServerHandler) refreshValidationError(tokenID string, err error) refreshResponse {
	switch {
	case errors.Is(err, auth.ErrRefreshReused):
		// Refuse to hand out the derived pair after a confirmed reuse — the
		// validator already revoked the row.
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "refresh reuse detected; token revoked")
	case errors.Is(err, auth.ErrTokenRevoked), errors.Is(err, auth.ErrTokenExpired):
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "token revoked")
	case errors.Is(err, auth.ErrInvalidToken):
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "")
	default:
		return refreshInternalError(err)
	}
}

// handleRevoke implements RFC 7009.
//
// It verifies the FULL bearer secret before revoking. RFC 7009 section 2.1
// requires the presented token to be valid; without this check, anyone who
// learns a token_id (which is non-secret -- it is returned in the token
// response and in the delegation mint) could revoke a victim's credential by
// posting `lmx_a<victim_id>_anything`.
//
// Already-revoked and already-expired rows still match the secret and proceed
// (an idempotent re-revoke is a 200), so a client retrying after a brief
// network failure does not need to handle 401. That is RFC 7009 section 2.2's
// requirement as well.
//
// It needs NO SCOPE, because the caller presents the very credential it is
// ending: an app disconnecting itself is the case this endpoint exists for,
// and demanding a scope for it would be demanding a permission to give up
// permissions.
//
// Client authentication follows RFC 7009 section 2.1's own split, validated
// BEFORE the token so a caller that cannot authenticate learns that and
// nothing else. A CONFIDENTIAL app must authenticate (Basic or body secret);
// a PUBLIC app must name itself with its client_id; and in both cases only
// the app a credential was issued to may end it. The token secret remains the
// other half of the proof -- client authentication alone must not let one app
// tear down another's installations. A delegation bearer carries no app at
// all, so its secret is its whole proof, which is the "none" method the
// metadata document advertises for this endpoint.
func (h *OAuthServerHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	bearer := r.FormValue("token")
	if bearer == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	app, clientErr, internalErr := h.authenticatePresentedClient(r.Context(), r)
	if internalErr != nil {
		writeInternalError(w, "client lookup for revocation failed", internalErr)
		return
	}
	if clientErr != nil {
		writeOAuthErrorBody(w, statusForOAuthError(*clientErr), *clientErr)
		return
	}
	kind, tokenID, err := h.validator.VerifyBearerSecret(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			// RFC 7009 section 2.2 says an INVALID token is still a 200: the
			// client's goal ("this token must not work") already holds, and a
			// distinct answer would let a caller probe which token ids exist.
			w.WriteHeader(http.StatusOK)
		} else {
			writeInternalError(w, "token verification for revocation failed", err)
		}
		return
	}
	if kind == auth.BearerKindAPI {
		bindErr, internalErr := h.bindRevocationToClient(r.Context(), app, tokenID)
		if internalErr != nil {
			writeInternalError(w, "reading the credential's app for revocation failed", internalErr)
			return
		}
		if bindErr != nil {
			writeOAuthErrorBody(w, statusForOAuthError(*bindErr), *bindErr)
			return
		}
	}
	switch kind {
	case auth.BearerKindAPI:
		if _, err := h.store.APITokens().Revoke(r.Context(), tokenID); err != nil {
			writeInternalError(w, "API token revocation failed", err)
			return
		}
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
	case auth.BearerKindDelegation:
		if _, err := h.store.DelegationTokens().Revoke(r.Context(), tokenID); err != nil {
			writeInternalError(w, "delegation token revocation failed", err)
			return
		}
		h.lifecycle.BearerRevoked(auth.BearerKindDelegation, tokenID)
	}
	w.WriteHeader(http.StatusOK)
}

// authenticatePresentedClient runs the token leg's client authentication over
// whichever identity the request carries, and answers nil when it carries
// none. Whether an identity is REQUIRED cannot be known until the credential's
// own app has been read, so absence is a decision for bindRevocationToClient
// rather than an error here -- and an unknown bearer must still answer 200
// without turning this endpoint into a probe for which client_ids exist.
//
// A RETIRED app authenticates here rather than being refused at the door: the
// retirement cascade already revoked its credentials, and the idempotent branch
// in bindRevocationToClient answers the retrying client with the 200 RFC 7009
// section 2.2 promises.
func (h *OAuthServerHandler) authenticatePresentedClient(ctx context.Context, r *http.Request) (*store.OAuthClient, *oauthErrorResponse, error) {
	if _, _, hasBasic := r.BasicAuth(); !hasBasic && r.FormValue("client_id") == "" {
		return nil, nil, nil
	}
	return h.authenticateClientAllowRevoked(ctx, r)
}

// bindRevocationToClient enforces the second half of RFC 7009 section 2.1:
// the credential may be ended only by the app it was issued to. A
// confidential app must have AUTHENTICATED as that app; a public one must at
// least have NAMED itself. The second return value carries an internal
// failure, which the caller answers with a 500 rather than a refusal that
// would read as "already revoked".
func (h *OAuthServerHandler) bindRevocationToClient(ctx context.Context, presented *store.OAuthClient, tokenID string) (*oauthErrorResponse, error) {
	row, err := h.store.APITokens().GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The secret verified against this row moments ago; a row that
			// cannot be read back is one that left between the two reads,
			// which is a revoke that already happened.
			return nil, nil
		}
		return nil, err
	}
	owner, err := h.store.OAuthClients().Get(ctx, row.ClientID)
	if err != nil {
		return nil, err
	}
	if owner.RevokedAt != nil {
		// A retired app's credentials were revoked by the retirement cascade
		// in the same transaction; repeating that is the idempotent 200 the
		// retrying client expects, not a refusal.
		return nil, nil
	}
	if presented == nil {
		if owner.IsConfidential() {
			body := oauthErrorBody("invalid_client", "client authentication is required for this app")
			return &body, nil
		}
		body := oauthErrorBody("invalid_request", "client_id is required to revoke this app's credential")
		return &body, nil
	}
	if presented.ClientID != owner.ClientID {
		if owner.IsConfidential() {
			body := oauthErrorBody("invalid_client", "this credential was issued to a different app")
			return &body, nil
		}
		body := oauthErrorBody("invalid_grant", "this credential was issued to a different app")
		return &body, nil
	}
	return nil, nil
}

func (h *OAuthServerHandler) issueAPIToken(
	ctx context.Context,
	grant consentedGrant,
) (*apiTokenResponse, *store.User, error) {
	userID := grant.userID
	now := h.now()
	granted, err := grant.scopes.Storable()
	if err != nil {
		return nil, nil, err
	}
	// What the credential REACHES on its first call, not what the consent
	// alone states: the grant intersected with the app's registration as it
	// stands at THIS exchange. An owner can shrink a registration inside a
	// code's ten-minute TTL or between a device grant's approval rows, and the
	// response's `scope` -- which the CLI persists and prints -- must not name
	// a permission loadBearer refuses on the very next call. This is the same
	// rule narrowedRefreshScope states for the rotation, applied at the mint.
	reachable := grant.scopes.NarrowTo(appScopeCeiling(grant.app))
	reachableValue, err := reachable.Storable()
	if err != nil {
		return nil, nil, err
	}
	// Rotating, always: a consent leg mints the credential an app will keep
	// using, and the short access token plus a refresh leg is what limits a
	// stolen credential file to one hour of use without the refresh secret.
	minted, err := mintAPIToken(h.validator, mintedByConsentGrant(grant.id), now, apiTokenMint{
		UserID:           userID,
		ClientID:         grant.app.ClientID,
		InstallationName: grant.installationName,
		GrantedScopes:    granted,
		Rotating:         true,
	})
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
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
	}, user, nil
}

// notifyTokenIssued supplies this handler's mailer to the shared notice. See
// notifyCredentialIssued, which both mint surfaces call.
//
// The notice lists the REACHABLE grant, matching the response's `scope`: the
// recipient reads it to learn what the app can do, and a shrunken registration
// already withdrew anything wider.
func (h *OAuthServerHandler) notifyTokenIssued(ctx context.Context, user *store.User, grant consentedGrant) {
	// The owner performed this consent in their own browser.
	notifyCredentialIssued(ctx, h.mail, h.renderer, user, credentialNotice{
		AppName:          grant.app.ClientName,
		InstallationName: grant.installationName,
		Scopes:           SortedScopeTokens(grant.scopes.NarrowTo(appScopeCeiling(grant.app))),
		IssuedByAdmin:    false,
	})
}
