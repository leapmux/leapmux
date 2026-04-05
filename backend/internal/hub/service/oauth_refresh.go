package service

import (
	"context"
	"log/slog"
	"time"

	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
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

	// Cache DB lookups within this tick to avoid repeated GetOAuthProviderByID
	// calls. The built Provider itself is cached on OAuthHandler.
	type dbLookup struct {
		dbProvider *gendb.OauthProvider
		err        error
	}
	dbCache := make(map[string]*dbLookup)

	for _, tok := range tokens {
		lookup, ok := dbCache[tok.ProviderID]
		if !ok {
			lookup = &dbLookup{}
			dbProvider, getErr := h.queries.GetOAuthProviderByID(ctx, tok.ProviderID)
			if getErr != nil {
				lookup.err = getErr
			} else {
				lookup.dbProvider = &dbProvider
			}
			dbCache[tok.ProviderID] = lookup
		}
		if lookup.err != nil {
			slog.Error("oauth refresh: get provider", "provider_id", tok.ProviderID, "error", lookup.err)
			continue
		}

		provider, buildErr := h.buildProvider(ctx, lookup.dbProvider)
		if buildErr != nil {
			slog.Error("oauth refresh: build provider", "provider_id", tok.ProviderID, "error", buildErr)
			continue
		}

		// Decrypt the refresh token.
		refreshTokenPlain, err := h.keystore.Decrypt(tok.RefreshToken, keystore.RefreshTokenAAD(tok.UserID, tok.ProviderID))
		if err != nil {
			slog.Error("oauth refresh: decrypt refresh token", "user_id", tok.UserID, "error", err)
			continue
		}

		newTokens, err := provider.Refresh(ctx, string(refreshTokenPlain))
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
