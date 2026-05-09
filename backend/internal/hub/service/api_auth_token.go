package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
)

// OAuth 2.0 grant types accepted by /api/auth/token. Values are
// RFC-defined wire identifiers:
//   - GrantTypeAuthorizationCode: RFC 6749 §4.1.3
//   - GrantTypeDeviceCode: RFC 8628 §3.4
const (
	GrantTypeAuthorizationCode = "authorization_code"
	GrantTypeDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
)

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
	row, err := h.store.CLIAuthorizationCodes().Consume(r.Context(), code)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired or already consumed")
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
	resp, err := h.issueAPIToken(r.Context(), row.UserID, "cli", deviceName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown device_code")
		return
	}
	// Throttle / expiry / already-consumed run before TouchPoll so a
	// fast-polling client gets `slow_down` rather than burning the
	// interval window. Approval-state guards run AFTER TouchPoll so a
	// pending poll still advances `last_polled_at` and the next poll
	// can detect the new interval.
	if code, desc, ok := h.preTouchPollOAuthError(row, time.Now()); ok {
		writeOAuthError(w, http.StatusBadRequest, code, desc)
		return
	}
	_ = h.store.DeviceAuthorizations().TouchPoll(r.Context(), deviceCode)
	if code, desc, ok := h.postTouchPollOAuthError(row); ok {
		writeOAuthError(w, http.StatusBadRequest, code, desc)
		return
	}
	if _, err := h.store.DeviceAuthorizations().Consume(r.Context(), deviceCode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := h.issueAPIToken(r.Context(), row.UserID, "cli", row.DeviceName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// preTouchPollOAuthError returns the OAuth-error code + description
// for the state guards that must short-circuit BEFORE TouchPoll runs:
// already-consumed, expired, or rapid-poll throttle.
func (h *APIAuthHandler) preTouchPollOAuthError(row *store.DeviceAuthorization, now time.Time) (string, string, bool) {
	if row.ConsumedAt != nil {
		return "invalid_grant", "device_code already used", true
	}
	if now.After(row.ExpiresAt) {
		return "expired_token", "", true
	}
	if h.shouldThrottle(row) {
		return "slow_down", "", true
	}
	return "", "", false
}

// postTouchPollOAuthError returns the OAuth-error code + description
// for the approval-state guards that must run AFTER TouchPoll, so a
// pending poll still updates `last_polled_at` and the next poll's
// throttle check has fresh state to compare against.
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

// evictAPITokenArtifacts drops an api-token bearer from both the
// validation cache and the refresh-grace cache. The grace cache holds
// the previous (access, refresh) pair for benign retry; both must
// die in lock-step or a revoked api token leaks a one-shot retry.
// Shared between the refresh-reuse / refresh-revoked branches of
// handleRefresh and the api-kind branch of handleRevoke.
func (h *APIAuthHandler) evictAPITokenArtifacts(tokenID string) {
	h.cache.EvictBearer(tokenID)
	if h.graceCache != nil {
		h.graceCache.Evict(tokenID)
	}
}

// evictRefreshArtifacts is the convenience overload that resolves the
// tokenID from a full refresh bearer (the form handleRefresh has on
// hand). Both reuse-detected and revoked / expired branches of
// handleRefresh need the same eviction sequence.
func (h *APIAuthHandler) evictRefreshArtifacts(refresh string) {
	h.evictAPITokenArtifacts(auth.BearerID(refresh))
}

func (h *APIAuthHandler) shouldThrottle(row *store.DeviceAuthorization) bool {
	if row.LastPolledAt == nil {
		return false
	}
	min := time.Duration(row.IntervalSeconds) * time.Second
	if min <= 0 {
		min = DeviceCodePollInterval
	}
	return time.Since(*row.LastPolledAt) < (min - 250*time.Millisecond)
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

	row, retry, err := h.validator.ValidateAPIRefresh(r.Context(), refresh)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRefreshReused):
			// Refuse to hand out the cached pair after a confirmed
			// reuse — the validator has already revoked the row.
			h.evictRefreshArtifacts(refresh)
			writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "refresh reuse detected; token revoked")
		case errors.Is(err, auth.ErrTokenRevoked), errors.Is(err, auth.ErrTokenExpired):
			h.evictRefreshArtifacts(refresh)
			writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "token revoked")
		case errors.Is(err, auth.ErrInvalidToken):
			writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "")
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	now := time.Now()

	if retry {
		// The client is replaying a refresh whose response was lost in
		// transit. Re-emit the same (access, refresh) pair we issued on
		// the original rotation so the client can resume cleanly. We
		// look the pair up in the encrypted in-process grace cache.
		// If the cache has no entry (process restart, ttl drift), we
		// must reject — issuing a fresh pair would invalidate the one
		// the legitimate client may have received and is using.
		if h.graceCache == nil {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "retry not recoverable: grace cache unavailable")
			return
		}
		access, refreshTok, err := h.graceCache.Get(row.ID)
		if err != nil {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "retry not recoverable: previous pair not cached")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  access,
			"refresh_token": refreshTok,
			"expires_in":    int(auth.AccessTokenTTL / time.Second),
			"token_id":      row.ID,
		})
		return
	}

	// First use of the current refresh: rotate both secrets in place
	// on the existing row. The access secret_hash + expires_at must
	// also advance, otherwise the bearer we hand back (`row.ID` +
	// newAccess) won't validate against `row.SecretHash`, which still
	// hashes the rotated-out access secret. The previous refresh
	// hash and its grace window get preserved so a racing retry can
	// still re-emit the cached pair from h.graceCache.
	pair := h.validator.MintBearerPair(auth.BearerKindAPI, row.ID, now, auth.AccessTokenTTL, auth.RefreshTokenTTL)
	prevHash := row.RefreshHash
	prevExp := now.Add(auth.RefreshReuseGrace)
	if err := h.store.APITokens().RotateRefresh(r.Context(), store.RotateAPITokenRefreshParams{
		ID:                       row.ID,
		NewSecretHash:            pair.AccessHash,
		NewExpiresAt:             &pair.AccessExpiresAt,
		NewRefreshHash:           pair.RefreshHash,
		NewRefreshExpiresAt:      &pair.RefreshExpiresAt,
		PreviousRefreshHash:      prevHash,
		PreviousRefreshExpiresAt: &prevExp,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.cache.EvictBearer(row.ID)

	if h.graceCache != nil {
		_ = h.graceCache.Put(row.ID, pair.AccessBearer, pair.RefreshBearer)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  pair.AccessBearer,
		"refresh_token": pair.RefreshBearer,
		"expires_in":    int(auth.AccessTokenTTL / time.Second),
		"token_id":      row.ID,
	})
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
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	switch kind {
	case auth.BearerKindAPI:
		if _, err := h.store.APITokens().Revoke(r.Context(), tokenID); err == nil {
			h.evictAPITokenArtifacts(tokenID)
		}
	case auth.BearerKindDelegation:
		if _, err := h.store.DelegationTokens().Revoke(r.Context(), tokenID); err == nil {
			h.cache.EvictBearer(tokenID)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *APIAuthHandler) issueAPIToken(ctx context.Context, userID, clientType, clientName string) (map[string]any, error) {
	tokenID := id.Generate()
	pair := h.validator.MintBearerPair(auth.BearerKindAPI, tokenID, time.Now(), auth.AccessTokenTTL, auth.RefreshTokenTTL)
	if err := h.store.APITokens().Create(ctx, store.CreateAPITokenParams{
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
		return nil, fmt.Errorf("create api token: %w", err)
	}
	user, err := h.store.Users().GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"access_token":  pair.AccessBearer,
		"refresh_token": pair.RefreshBearer,
		"expires_in":    int(auth.AccessTokenTTL / time.Second),
		"token_id":      tokenID,
		"user_id":       userID,
		"username":      user.Username,
	}, nil
}
