package service

import (
	"context"
	"log/slog"
	"time"

	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
)

const tokenRefreshInterval = 1 * time.Minute

// StartTokenRefresh starts a background goroutine that periodically refreshes
// OAuth tokens that are about to expire. It stops when ctx is cancelled.
func (h *OAuthHandler) StartTokenRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(tokenRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.refreshExpiringTokens(ctx)
			}
		}
	}()
}

func (h *OAuthHandler) refreshExpiringTokens(ctx context.Context) {
	tokens, err := h.queries.ListExpiringOAuthTokens(ctx)
	if err != nil {
		slog.Error("oauth refresh: list expiring tokens", "error", err)
		return
	}

	// Cache providers to avoid repeated DB lookups and OIDC discovery per token.
	type cachedProvider struct {
		provider huboauth.Provider
		err      error
	}
	providerCache := make(map[string]*cachedProvider)

	for _, tok := range tokens {
		cached, ok := providerCache[tok.ProviderID]
		if !ok {
			cached = &cachedProvider{}
			dbProvider, getErr := h.queries.GetOAuthProviderByID(ctx, tok.ProviderID)
			if getErr != nil {
				cached.err = getErr
			} else {
				cached.provider, cached.err = h.buildProvider(ctx, &dbProvider)
			}
			providerCache[tok.ProviderID] = cached
		}
		if cached.err != nil {
			slog.Error("oauth refresh: build provider", "provider_id", tok.ProviderID, "error", cached.err)
			continue
		}

		// Decrypt the refresh token.
		refreshTokenPlain, err := h.keystore.Decrypt(tok.RefreshToken, keystore.RefreshTokenAAD(tok.UserID, tok.ProviderID))
		if err != nil {
			slog.Error("oauth refresh: decrypt refresh token", "user_id", tok.UserID, "error", err)
			continue
		}

		newTokens, err := cached.provider.Refresh(ctx, string(refreshTokenPlain))
		if err != nil {
			slog.Warn("oauth refresh: refresh failed, deleting tokens", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", err)
			_ = h.queries.DeleteOAuthTokensByUserAndProvider(ctx, gendb.DeleteOAuthTokensByUserAndProviderParams{
				UserID:     tok.UserID,
				ProviderID: tok.ProviderID,
			})
			continue
		}

		if err := h.storeTokens(ctx, tok.UserID, tok.ProviderID, newTokens); err != nil {
			slog.Error("oauth refresh: store tokens", "user_id", tok.UserID, "error", err)
		}
	}
}
