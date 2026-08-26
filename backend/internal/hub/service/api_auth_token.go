package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// OAuth 2.0 grant types accepted by /auth/cli/token. Values are
// RFC-defined wire identifiers:
//   - GrantTypeAuthorizationCode: RFC 6749 §4.1.3
//   - GrantTypeDeviceCode: RFC 8628 §3.4
const (
	GrantTypeAuthorizationCode = "authorization_code"
	GrantTypeDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
)

var errAuthorizationGrantUnavailable = errors.New("authorization grant unavailable")

// --- Token endpoints ---

func (h *APIAuthHandler) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	grantType := r.FormValue("grant_type")
	if grantType == "" {
		// Convenience: when grant_type is omitted, infer from the
		// presence of `code` (local-redirect) or `device_code` (device).
		switch {
		case r.FormValue("code") != "":
			grantType = GrantTypeAuthorizationCode
		case r.FormValue("device_code") != "":
			grantType = GrantTypeDeviceCode
		}
	}
	switch grantType {
	case GrantTypeAuthorizationCode:
		h.handleTokenAuthorizationCode(w, r)
	case GrantTypeDeviceCode:
		h.handleTokenDeviceCode(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (h *APIAuthHandler) handleTokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	if code == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}
	row, err := h.store.CLIAuthorizationCodes().GetActive(r.Context(), code, h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
		} else {
			writeInternalError(w, "authorization code lookup failed", err)
		}
		return
	}
	expected := pkce.S256(verifier)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(row.CodeChallenge)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	// The grant row's user_id is a column, so a blank one is corrupt data, not a
	// programmer error: it is refused as an unusable grant rather than panicked
	// on. The device-code path has always had this guard (postTouchPollOAuthError
	// answers authorization_pending); this path did not, so a blank
	// cli_authorization_codes.user_id reached the mint.
	grantUID, mintOK := userid.New(row.UserID)
	if !mintOK {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
		return
	}
	// The device name comes from the GRANT row, and so does the scope --
	// never from this request's form. Otherwise any holder of an
	// authorization code could upgrade what the user consented to, or label
	// the credential as the victim's own laptop in the list and in the
	// issuance notice while the consent page showed something else. The
	// device-code sibling below already reads the row alone.
	h.issueTokenResponse(w, r, consentedGrant{
		id:              code,
		userID:          grantUID,
		deviceName:      row.DeviceName,
		adminScope:      row.AdminScope,
		invalidGrantMsg: "code expired or already consumed",
		internalMsg:     "authorization code token issuance failed",
		consume: func(tx store.Store) error {
			if _, err := tx.CLIAuthorizationCodes().Consume(r.Context(), code, h.now().UTC()); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return errAuthorizationGrantUnavailable
				}
				return fmt.Errorf("consume authorization code: %w", err)
			}
			return nil
		},
	})
}

// consentedGrant is what a validated OAuth grant hands the mint: who
// consented, what they consented TO, and the two refusals this exchange can
// answer with.
//
// A struct rather than six positional arguments, because two of them are
// adjacent same-typed message strings that a call site can transpose in
// silence -- the caller would then answer "code expired" for an internal
// failure and log the grant message for a live one. Naming them at the call
// site makes that mistake visible, and a seventh fact about the consent is
// added in one place.
type consentedGrant struct {
	// id is the grant row this mint redeems. It is what the mint reads as its
	// AUTHORITY: /auth/cli/token carries no session at all, so the browser
	// consent that produced this row is where the elevated session was
	// required, and holding the row is the proof. See mintAuthority.
	id         string
	userID     userid.UserID
	deviceName string
	adminScope bool
	// invalidGrantMsg answers a single-use grant that is already gone
	// (errAuthorizationGrantUnavailable); internalMsg answers anything else.
	invalidGrantMsg string
	internalMsg     string
	// consume spends the grant inside the mint's transaction.
	consume func(tx store.Store) error
}

// issueTokenResponse mints a CLI API token for an already-validated OAuth grant
// and writes the RFC 6749/8628 token response: the token JSON on success,
// invalid_grant when the single-use consume closure reports the grant is gone,
// or an internal error otherwise. The authorization_code and device_code
// handlers share this issue-and-map tail so they cannot drift on the error
// codes they must both emit.
func (h *APIAuthHandler) issueTokenResponse(w http.ResponseWriter, r *http.Request, grant consentedGrant) {
	resp, user, err := h.issueAPIToken(r.Context(), grant)
	if err != nil {
		if errors.Is(err, errAuthorizationGrantUnavailable) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", grant.invalidGrantMsg)
		} else {
			writeInternalError(w, grant.internalMsg, err)
		}
		return
	}
	// After the commit and before the response, so the notice cannot be the
	// reason a minted token is never delivered. See notifyTokenIssued.
	h.notifyTokenIssued(r.Context(), user, grant.deviceName, grant.adminScope)
	writeJSON(w, http.StatusOK, resp)
}

func (h *APIAuthHandler) handleTokenDeviceCode(w http.ResponseWriter, r *http.Request) {
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
	// Throttle / expiry / already-consumed run before TouchPoll so a
	// fast-polling client gets `slow_down` rather than burning the
	// interval window. Pending and denied polls touch immediately. An
	// approved poll also touches immediately -- before, and outside, the
	// issuance transaction -- so last_polled_at advances (keeping the
	// slow_down throttle honest) even when a transient token-insert
	// failure rolls the transaction back. The grant stays retryable
	// because only Consume, which remains inside the transaction, is
	// single-use.
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
	// Advance last_polled_at outside the issuance transaction so the
	// throttle anchor moves forward even if issuance later fails and
	// rolls back -- otherwise a client hammering an approved-but-
	// transiently-failing grant would never get slow_down. Touch errors
	// are internal, matching the pending/denied path above.
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
	// credential into a second credential, which is precisely the widening
	// the step-up flow exists to avoid.
	if row.ElevateTokenID != "" {
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
		return
	}
	h.issueTokenResponse(w, r, consentedGrant{
		id:              deviceCode,
		userID:          pollUID,
		deviceName:      row.DeviceName,
		adminScope:      row.AdminScope,
		invalidGrantMsg: "device_code expired or already consumed",
		internalMsg:     "device authorization token issuance failed",
		consume: func(tx store.Store) error {
			// Consume is the single-use consumption that must stay atomic
			// with token creation, so it -- and only it -- remains in the
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

// preTouchPollOAuthError returns the OAuth-error code + description
// for the state guards that must short-circuit BEFORE TouchPoll runs:
// already-consumed, expired, or rapid-poll throttle.
func (h *APIAuthHandler) preTouchPollOAuthError(row *store.DeviceAuthorization, now time.Time) (oauthErrorResponse, bool) {
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
// guards whose responses must still update last_polled_at. The caller
// performs that update before writing the returned body.
//
// A BODY, not a (code, description) pair: the two are adjacent same-typed
// strings that a call site can transpose in silence, which is the exact smell
// consentedGrant above was introduced to remove. The type this returns is the
// one the file already writes.
func (h *APIAuthHandler) postTouchPollOAuthError(row *store.DeviceAuthorization) (oauthErrorResponse, bool) {
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

func (h *APIAuthHandler) shouldThrottle(row *store.DeviceAuthorization, now time.Time) bool {
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

func (h *APIAuthHandler) parseAPIRefreshBearer(refresh string) (parsedRefreshBearer, error) {
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

type apiTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenID      string `json:"token_id"`
	UserID       string `json:"user_id,omitempty"`
	Username     string `json:"username,omitempty"`
	// AdminScope reports what the browser actually granted, which is not
	// always what the CLI asked for: on the device-code flow the user ticks
	// a checkbox, and they may not. The CLI says so rather than letting the
	// first admin verb fail with nothing to point at.
	AdminScope bool `json:"admin_scope"`
	// RefreshExpiresIn is how long the credential can keep refreshing before
	// the device must sign in again -- the absolute lifetime, not the access
	// token's hour. The CLI stores it so `auth status` can report it.
	RefreshExpiresIn int `json:"refresh_expires_in,omitempty"`
}

func refreshOAuthError(status int, code, description string) refreshResponse {
	return refreshResponse{status: status, body: oauthErrorBody(code, description)}
}

func refreshInternalError(err error) refreshResponse {
	return refreshResponse{status: http.StatusInternalServerError, err: err}
}

// refreshTokenResponse builds the rotation payload.
//
// It reports refreshExpiresIn and adminScope, exactly as the minting
// response does. Omitting them made the rotation answer look like a
// credential with no absolute deadline and no scope: `refresh_expires_in`
// carries omitempty, so the CLI never advanced the deadline it stores and
// `auth status` printed a date further into the past on every rotation,
// while `admin_scope` has no omitempty and answered a flat false for a
// credential that really did carry the scope.
func refreshTokenResponse(tokenID, accessBearer, refreshBearer string, expiresIn, refreshExpiresIn int, adminScope bool) refreshResponse {
	return refreshResponse{
		status: http.StatusOK,
		body: apiTokenResponse{
			AccessToken:      accessBearer,
			RefreshToken:     refreshBearer,
			ExpiresIn:        expiresIn,
			TokenID:          tokenID,
			AdminScope:       adminScope,
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
// winning rotation is what the row records, and this leg only reproduces
// its answer. Reading the pair here would report a window the store never
// stored.
func (h *APIAuthHandler) refreshRetryResponse(row *store.APIToken, pair auth.MintedBearerPair) refreshResponse {
	if row.ExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no access expiration", row.ID))
	}
	if row.RefreshExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no refresh expiration", row.ID))
	}
	now := h.now()
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(*row.ExpiresAt, now),
		remainingExpiresIn(*row.RefreshExpiresAt, now),
		row.AdminScope,
	)
}

func writeRefreshResponse(w http.ResponseWriter, resp refreshResponse) {
	if resp.err != nil {
		writeInternalError(w, "refresh token request failed", resp.err)
		return
	}
	writeJSON(w, resp.status, resp.body)
}

func (h *APIAuthHandler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	refresh := r.FormValue("refresh_token")
	if refresh == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	parsed, err := h.parseAPIRefreshBearer(refresh)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "")
		return
	}
	flightCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), RefreshWorkTimeout)
	defer cancel()
	// Blocking Do (not DoChan + ctx select) is deliberate: a refresh rotates the
	// token single-use, so once the flight starts, every caller -- including one
	// whose client disconnected -- must run to completion and receive the same
	// rotated pair, or it is left with a rotated-away refresh token and no
	// replacement. flightCtx (WithoutCancel) already decouples the work from the
	// leader's request cancellation. This is why it differs from the read-only
	// bearer-validation singleflight, which is safe to abandon on disconnect.
	result, _, _ := h.refreshFlight.Do(parsed.flightKey(), func() (any, error) {
		return h.refresh(flightCtx, parsed), nil
	})
	writeRefreshResponse(w, result.(refreshResponse))
}

func (h *APIAuthHandler) refresh(ctx context.Context, parsed parsedRefreshBearer) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}

	now := h.now()
	// The refresh window is clipped to the credential's absolute lifetime,
	// measured from created_at. Without the clip every rotation would push
	// the window a full RefreshTokenTTL forward, so a CLI that refreshes
	// weekly would keep ONE browser consent alive for ever -- and the CLI
	// now does refresh, so the clip is what limits it.
	refreshTTL := auth.RefreshWindowFor(row.CreatedAt, now)
	if refreshTTL <= 0 {
		// Revoke the ROW, not only the cache. Every other caller of
		// BearerRevoked reaches it on a row the store already revoked --
		// the validator revokes on a confirmed reuse, and a revoked or
		// expired row is refused before it gets here. This leg is the one
		// that decides a credential is dead by itself, so it must also
		// record it: BearerRevoked evicts the cached credential and closes
		// its channels, and leaves revoked_at NULL. Without the write the
		// access token keeps authenticating until its own expiry, up to an
		// hour after the hub told the CLI the credential was finished, and
		// the row keeps listing as live in Preferences.
		if _, err := h.store.APITokens().Revoke(ctx, row.ID); err != nil {
			slog.ErrorContext(ctx, "could not revoke the credential that reached its maximum lifetime",
				"token_id", row.ID, "err", err)
		}
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusUnauthorized, "invalid_grant",
			"this credential reached its maximum lifetime; run `leapmux control auth login` again")
	}
	// Both windows are clipped to the SAME ceiling. Bearer validation reads
	// expires_at alone, so an unclipped access token outlives the absolute
	// lifetime this leg just enforced: the last rotation before the ceiling
	// wrote a full hour past it, and the credential kept authenticating for
	// that hour after the hub had already answered "this credential reached
	// its maximum lifetime".
	pair := h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		now,
		auth.AccessWindowFor(row.CreatedAt, now),
		refreshTTL,
	)

	if retry {
		return h.refreshRetryResponse(row, pair)
	}

	// First use of the current refresh: rotate both secrets in place
	// on the existing row. The access secret_hash + expires_at must
	// also advance, otherwise the bearer we hand back (`row.ID` +
	// newAccess) won't validate against `row.SecretHash`, which still
	// hashes the rotated-out access secret. The previous refresh
	// hash and its grace window get preserved so a racing retry can
	// deterministically derive and re-emit this same pair on any Hub.
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
	})
	if err != nil {
		return refreshInternalError(err)
	}
	if rotated != 1 {
		return h.recoverRefreshCASMiss(ctx, parsed)
	}
	// Pass the prolonged access expiry so the rotation not only invalidates the
	// cached secret but extends the bearer's leases and channel expiries, since
	// the row remains valid (with more lifetime) under the newly derived secret.
	h.lifecycle.BearerRotatedExtending(auth.BearerKindAPI, row.ID, pair.AccessExpiresAt)

	// Both deadlines come from the pair, because RotateRefresh just wrote it:
	// here the pair IS the row. now is the instant the pair was derived
	// from, so the two reported windows and the stored ones agree exactly.
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(pair.AccessExpiresAt, now),
		remainingExpiresIn(pair.RefreshExpiresAt, now),
		row.AdminScope,
	)
}

func (h *APIAuthHandler) recoverRefreshCASMiss(ctx context.Context, parsed parsedRefreshBearer) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}
	if !retry {
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusUnauthorized, "invalid_grant", "token revoked")
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
	return h.refreshRetryResponse(row, pair)
}

func (h *APIAuthHandler) refreshValidationError(tokenID string, err error) refreshResponse {
	switch {
	case errors.Is(err, auth.ErrRefreshReused):
		// Refuse to hand out the derived pair after a confirmed
		// reuse — the validator has already revoked the row.
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusUnauthorized, "invalid_grant", "refresh reuse detected; token revoked")
	case errors.Is(err, auth.ErrTokenRevoked), errors.Is(err, auth.ErrTokenExpired):
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusUnauthorized, "invalid_grant", "token revoked")
	case errors.Is(err, auth.ErrInvalidToken):
		return refreshOAuthError(http.StatusUnauthorized, "invalid_grant", "")
	default:
		return refreshInternalError(err)
	}
}

func (h *APIAuthHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	// Verify the FULL bearer secret before revoking. RFC 7009 §2.1
	// requires the presented token to be valid; without this check,
	// anyone who learns a token_id (which is non-secret — returned in
	// JSON responses to /auth/cli/token, /auth/cli/refresh, and the
	// worker delegation mint endpoint) could revoke a victim's
	// session by posting `lmx_a<victim_id>_anything`. Already-revoked
	// / already-expired rows still match the secret and proceed
	// (idempotent re-revoke is a 200 OK), so a client retrying after
	// a network blip doesn't need to handle 401.
	kind, tokenID, err := h.validator.VerifyBearerSecret(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
		} else {
			writeInternalError(w, "token verification for revocation failed", err)
		}
		return
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

func (h *APIAuthHandler) issueAPIToken(
	ctx context.Context,
	grant consentedGrant,
) (*apiTokenResponse, *store.User, error) {
	userID := grant.userID
	now := h.now()
	// Rotating, always: a consent leg mints the credential a person will keep
	// using, and the short access token plus a refresh leg is what limits a
	// stolen credential file to one hour of use without the refresh secret.
	minted, err := mintAPIToken(h.validator, mintedByConsentGrant(grant.id), now, apiTokenMint{
		UserID:     userID,
		ClientType: "cli",
		ClientName: grant.deviceName,
		AdminScope: grant.adminScope,
		Rotating:   true,
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
		RefreshToken:     minted.RefreshBearer(),
		ExpiresIn:        remainingExpiresIn(minted.Pair.AccessExpiresAt, h.now()),
		RefreshExpiresIn: minted.RefreshExpiresIn(h.now()),
		TokenID:          minted.TokenID,
		UserID:           userID.String(),
		Username:         user.Username,
		AdminScope:       grant.adminScope,
	}, user, nil
}

// notifyTokenIssued supplies this handler's mailer to the shared notice. See
// notifyCredentialIssued, which both mint surfaces call.
func (h *APIAuthHandler) notifyTokenIssued(ctx context.Context, user *store.User, deviceName string, adminScope bool) {
	// The owner performed this consent in their own browser.
	notifyCredentialIssued(ctx, h.mail, h.renderer, user, deviceName, adminScope, false)
}
