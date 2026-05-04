package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
)

const tokenRefreshInterval = 1 * time.Minute

// StartTokenRefresh starts a background goroutine that periodically refreshes
// OAuth tokens that are about to expire. It stops when ctx is cancelled.
// The first refresh waits for the first interval tick — there is nothing to
// refresh at startup until tokens have aged.
func (h *OAuthHandler) StartTokenRefresh(ctx context.Context) {
	periodic.Start(ctx, periodic.Schedule{Interval: tokenRefreshInterval, SkipFirstRun: true}, func(ctx context.Context) {
		h.refreshExpiringTokens(ctx)
	})
}

func (h *OAuthHandler) refreshExpiringTokens(ctx context.Context) {
	tokens, err := h.store.OAuthTokens().ListExpiring(ctx)
	if err != nil {
		slog.Error("oauth refresh: list expiring tokens", "error", err)
		return
	}

	// Cache DB lookups within this tick to avoid repeated GetOAuthProviderByID
	// calls. The built Provider itself is cached on OAuthHandler.
	type dbLookup struct {
		dbProvider *store.OAuthProvider
		err        error
	}
	dbCache := make(map[string]*dbLookup)

	for _, tok := range tokens {
		lookup, ok := dbCache[tok.ProviderID]
		if !ok {
			lookup = &dbLookup{}
			dbProvider, getErr := h.store.OAuthProviders().GetByID(ctx, tok.ProviderID)
			if getErr != nil {
				lookup.err = getErr
			} else {
				lookup.dbProvider = dbProvider
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
			_ = h.store.OAuthTokens().DeleteByUserAndProvider(ctx, store.DeleteOAuthTokensByUserAndProviderParams{
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
