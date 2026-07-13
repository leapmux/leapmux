package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
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
	deviceName := r.FormValue("device_name")
	if code == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}
	row, err := h.store.CLIAuthorizationCodes().GetActive(r.Context(), code)
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
	if deviceName == "" {
		deviceName = row.DeviceName
	}
	h.issueTokenResponse(w, r, row.UserID, deviceName,
		"code expired or already consumed",
		"authorization code token issuance failed",
		func(tx store.Store) error {
			if _, err := tx.CLIAuthorizationCodes().Consume(r.Context(), code); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return errAuthorizationGrantUnavailable
				}
				return fmt.Errorf("consume authorization code: %w", err)
			}
			return nil
		})
}

// issueTokenResponse mints a CLI API token for an already-validated OAuth grant
// and writes the RFC 6749/8628 token response: the token JSON on success,
// invalid_grant with invalidGrantMsg when the single-use consume closure reports
// the grant is gone (errAuthorizationGrantUnavailable), or an internal error
// (internalMsg) otherwise. The authorization_code and device_code handlers share
// this issue-and-map tail so they cannot drift on the error codes they must both
// emit.
func (h *APIAuthHandler) issueTokenResponse(
	w http.ResponseWriter,
	r *http.Request,
	userID, deviceName, invalidGrantMsg, internalMsg string,
	consume func(tx store.Store) error,
) {
	resp, err := h.issueAPIToken(r.Context(), userID, "cli", deviceName, consume)
	if err != nil {
		if errors.Is(err, errAuthorizationGrantUnavailable) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", invalidGrantMsg)
		} else {
			writeInternalError(w, internalMsg, err)
		}
		return
	}
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
	if code, desc, ok := h.preTouchPollOAuthError(row, time.Now()); ok {
		writeOAuthError(w, http.StatusBadRequest, code, desc)
		return
	}
	if code, desc, ok := h.postTouchPollOAuthError(row); ok {
		if err := h.store.DeviceAuthorizations().TouchPoll(r.Context(), deviceCode); err != nil {
			writeInternalError(w, "device authorization poll update failed", err)
			return
		}
		writeOAuthError(w, http.StatusBadRequest, code, desc)
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
	h.issueTokenResponse(w, r, row.UserID, row.DeviceName,
		"device_code expired or already consumed",
		"device authorization token issuance failed",
		func(tx store.Store) error {
			// Consume is the single-use consumption that must stay atomic
			// with token creation, so it -- and only it -- remains in the
			// transaction.
			affected, err := tx.DeviceAuthorizations().Consume(r.Context(), deviceCode)
			if err != nil {
				return fmt.Errorf("consume device authorization: %w", err)
			}
			if affected != 1 {
				return errAuthorizationGrantUnavailable
			}
			return nil
		})
}

// preTouchPollOAuthError returns the OAuth-error code + description
// for the state guards that must short-circuit BEFORE TouchPoll runs:
// already-consumed, expired, or rapid-poll throttle.
func (h *APIAuthHandler) preTouchPollOAuthError(row *store.DeviceAuthorization, now time.Time) (string, string, bool) {
	if row.ConsumedAt != nil {
		return "invalid_grant", "device_code already used", true
	}
	if auth.IsExpired(now, row.ExpiresAt) {
		return "expired_token", "", true
	}
	if h.shouldThrottle(row, now) {
		return "slow_down", "", true
	}
	return "", "", false
}

// postTouchPollOAuthError returns the OAuth-error code + description for
// approval-state guards whose responses must still update last_polled_at.
// The caller performs that update before writing the returned OAuth error.
func (h *APIAuthHandler) postTouchPollOAuthError(row *store.DeviceAuthorization) (string, string, bool) {
	switch row.Approved {
	case 0:
		return "authorization_pending", "", true
	case 2:
		return "access_denied", "", true
	}
	if row.UserID == "" {
		return "authorization_pending", "", true
	}
	return "", "", false
}

func (h *APIAuthHandler) shouldThrottle(row *store.DeviceAuthorization, now time.Time) bool {
	if row.LastPolledAt == nil {
		return false
	}
	min := time.Duration(row.IntervalSeconds) * time.Second
	if min <= 0 {
		min = DeviceCodePollInterval
	}
	return now.Sub(*row.LastPolledAt) < (min - 250*time.Millisecond)
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
}

func refreshOAuthError(status int, code, description string) refreshResponse {
	return refreshResponse{status: status, body: oauthErrorBody(code, description)}
}

func refreshInternalError(err error) refreshResponse {
	return refreshResponse{status: http.StatusInternalServerError, err: err}
}

func refreshTokenResponse(tokenID, accessBearer, refreshBearer string, expiresIn int) refreshResponse {
	return refreshResponse{
		status: http.StatusOK,
		body: apiTokenResponse{
			AccessToken:  accessBearer,
			RefreshToken: refreshBearer,
			ExpiresIn:    expiresIn,
			TokenID:      tokenID,
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

func (h *APIAuthHandler) refreshRetryResponse(row *store.APIToken, pair auth.MintedBearerPair) refreshResponse {
	if row.ExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no access expiration", row.ID))
	}
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(*row.ExpiresAt, time.Now()),
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

	now := time.Now()
	pair := h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		now,
		auth.AccessTokenTTL,
		auth.RefreshTokenTTL,
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

	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(pair.AccessExpiresAt, time.Now()),
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
	pair := h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		time.Now(),
		auth.AccessTokenTTL,
		auth.RefreshTokenTTL,
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
	userID, clientType, clientName string,
	consumeGrant func(tx store.Store) error,
) (*apiTokenResponse, error) {
	tokenID := id.Generate()
	pair := h.validator.MintBearerPair(auth.BearerKindAPI, tokenID, time.Now(), auth.AccessTokenTTL, auth.RefreshTokenTTL)
	var user *store.User
	err := h.store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		if err := consumeGrant(tx); err != nil {
			return err
		}
		var err error
		user, err = tx.Users().GetByID(ctx, userID)
		if err != nil {
			return fmt.Errorf("query token user: %w", err)
		}
		if err := tx.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           userID,
			ClientType:       clientType,
			ClientName:       clientName,
			SecretHash:       pair.AccessHash,
			RefreshHash:      pair.RefreshHash,
			Scope:            "remote:*",
			ExpiresAt:        &pair.AccessExpiresAt,
			RefreshExpiresAt: &pair.RefreshExpiresAt,
		}); err != nil {
			return fmt.Errorf("create api token: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &apiTokenResponse{
		AccessToken:  pair.AccessBearer,
		RefreshToken: pair.RefreshBearer,
		ExpiresIn:    remainingExpiresIn(pair.AccessExpiresAt, time.Now()),
		TokenID:      tokenID,
		UserID:       userID,
		Username:     user.Username,
	}, nil
}
