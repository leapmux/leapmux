package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const tokenRefreshInterval = 1 * time.Minute

// StartTokenRefresh periodically refreshes OAuth tokens that are about to
// expire and returns a channel that closes after the loop and its current pass
// finish. The first pass waits for the first interval tick. Cancellation stops
// new refreshes. An admitted refresh finishes under its own request and write
// deadlines before shutdown joins the loop. See periodic.Start.
func (h *IdPHandler) StartTokenRefresh(ctx context.Context) <-chan struct{} {
	return periodic.Start(ctx, periodic.Schedule{Interval: tokenRefreshInterval, SkipFirstRun: true}, func(ctx context.Context) {
		h.refreshExpiringTokens(ctx)
	})
}

const (
	// oauthTokenRefreshTimeout limits an admitted provider request without
	// letting shutdown cancel a response that may carry a rotated token.
	oauthTokenRefreshTimeout = 15 * time.Second
	// oauthTokenStoreTimeout reserves a separate budget for the token-state
	// write after a refresh attempt, even when the periodic loop was cancelled.
	oauthTokenStoreTimeout = 5 * time.Second
	// OAuthTokenRefreshJoinTimeout allows one provider request, its token
	// write, and a scheduling margin during shutdown.
	OAuthTokenRefreshJoinTimeout = oauthTokenRefreshTimeout + oauthTokenStoreTimeout + time.Second
)

// oauthTokenRefreshLead is how far before its stored expiry a provider
// OAuth token becomes refresh-eligible. The SQL compared the expiry
// against the database clock plus this window; the clock and the window
// both come from the hub now.
const oauthTokenRefreshLead = 5 * time.Minute

func (h *IdPHandler) refreshExpiringTokens(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tokens, err := h.store.OAuthTokens().ListExpiring(ctx, h.now().UTC().Add(oauthTokenRefreshLead))
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("oauth refresh: list expiring tokens", "error", err)
		}
		return
	}

	// Cache DB lookups within this tick to avoid repeated GetOAuthProviderByID
	// calls. IdPHandler caches the built Provider itself.
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
		if ctx.Err() != nil {
			return
		}
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
			if ctx.Err() != nil {
				return
			}
			slog.Error("oauth refresh: get provider", "provider_id", tok.ProviderID, "error", lookup.err)
			continue
		}

		provider, buildErr := h.buildProvider(ctx, lookup.dbProvider, lookup.gen)
		if buildErr != nil {
			if ctx.Err() != nil {
				return
			}
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

		if ctx.Err() != nil {
			return
		}
		// A rotating provider may consume the old token before its response
		// reaches this process. Once admitted, the request and replacement
		// write must finish independently of the loop's cancellation.
		refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), oauthTokenRefreshTimeout)
		newTokens, err := provider.Refresh(refreshCtx, string(refreshTokenPlain))
		cancelRefresh()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Only the provider can establish that its refresh credential is
			// permanently invalid. Temporary failures leave the pair reusable.
			if !errors.Is(err, huboauth.ErrRefreshTokenRejected) {
				slog.Warn("oauth refresh failed; keeping the tokens for retry", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", err)
				continue
			}
			slog.Warn("oauth refresh: provider refused the token", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", err)
			deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), oauthTokenStoreTimeout)
			deleteErr := h.store.OAuthTokens().DeleteByUserAndProvider(deleteCtx, store.DeleteOAuthTokensByUserAndProviderParams{
				UserID:     refreshUID,
				ProviderID: tok.ProviderID,
			})
			cancelDelete()
			if deleteErr != nil {
				slog.Error("oauth refresh: delete refused tokens", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", deleteErr)
			}
			continue
		}

		// A rotating provider has consumed the old token. Reserve the full
		// write budget for its replacement. The Hub's join allows both
		// budgets and their scheduling margin before it closes the store.
		storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), oauthTokenStoreTimeout)
		storeErr := h.storeTokens(storeCtx, tok.UserID, tok.ProviderID, newTokens)
		cancelStore()
		if err := storeErr; err != nil {
			slog.Error("oauth refresh: store tokens", "user_id", tok.UserID, "error", err)
		}
	}
}
