package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const tokenRefreshInterval = 1 * time.Minute

// StartTokenRefresh starts a background goroutine that periodically refreshes
// OAuth tokens that are about to expire. It stops when ctx is cancelled.
// The first refresh waits for the first interval tick — there is nothing to
// refresh at startup until tokens age.
func (h *OAuthHandler) StartTokenRefresh(ctx context.Context) {
	periodic.Start(ctx, periodic.Schedule{Interval: tokenRefreshInterval, SkipFirstRun: true}, func(ctx context.Context) {
		h.refreshExpiringTokens(ctx)
	})
}

// oauthTokenRefreshLead is how far before its stored expiry a provider
// OAuth token becomes refresh-eligible. The SQL compared the expiry
// against the database clock plus this window; the clock and the window
// both come from the hub now.
const oauthTokenRefreshLead = 5 * time.Minute

func (h *OAuthHandler) refreshExpiringTokens(ctx context.Context) {
	tokens, err := h.store.OAuthTokens().ListExpiring(ctx, h.now().UTC().Add(oauthTokenRefreshLead))
	if err != nil {
		slog.Error("oauth refresh: list expiring tokens", "error", err)
		return
	}

	// Cache DB lookups within this tick to avoid repeated GetOAuthProviderByID
	// calls. OAuthHandler caches the built Provider itself.
	type dbLookup struct {
		dbProvider *store.OAuthProvider
		// gen is the provider cache's invalidation count, read BEFORE the row
		// beside it. buildProvider states why the order matters: the count
		// has to cover every instant from the last evidence that the row
		// exists to the cache insert. This tick reuses one row read for many
		// tokens, so it reuses that read's count with it.
		gen uint64
		err error
	}
	dbCache := make(map[string]*dbLookup)

	for _, tok := range tokens {
		lookup, ok := dbCache[tok.ProviderID]
		if !ok {
			lookup = &dbLookup{gen: h.providerGeneration(tok.ProviderID)}
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

		provider, buildErr := h.buildProvider(ctx, lookup.dbProvider, lookup.gen)
		if buildErr != nil {
			slog.Error("oauth refresh: build provider", "provider_id", tok.ProviderID, "error", buildErr)
			continue
		}

		// A blank owner on an oauth_tokens row is corrupt data; skip the row
		// rather than letting a zero id address a delete.
		refreshUID, mintOK := userid.New(tok.UserID)
		if !mintOK {
			slog.Error("oauth refresh: token row has a blank user id", "provider_id", tok.ProviderID)
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
				UserID:     refreshUID,
				ProviderID: tok.ProviderID,
			})
			continue
		}

		if err := h.storeTokens(ctx, tok.UserID, tok.ProviderID, newTokens); err != nil {
			slog.Error("oauth refresh: store tokens", "user_id", tok.UserID, "error", err)
		}
	}
}
