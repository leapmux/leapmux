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
// StartTokenRefresh runs the periodic provider-token refresh and returns the
// channel that closes when the loop has stopped, so a shutdown can join a
// pass in flight before the store closes. See periodic.Start.
func (h *IdPHandler) StartTokenRefresh(ctx context.Context) <-chan struct{} {
	return periodic.Start(ctx, periodic.Schedule{Interval: tokenRefreshInterval, SkipFirstRun: true}, func(ctx context.Context) {
		h.refreshExpiringTokens(ctx)
	})
}

// oauthTokenStoreTimeout limits the detached write of a refreshed token pair,
// so a shutdown that cancelled the loop's context cannot fail the one write
// that keeps the account linked.
const oauthTokenStoreTimeout = 5 * time.Second

// oauthTokenRefreshLead is how far before its stored expiry a provider
// OAuth token becomes refresh-eligible. The SQL compared the expiry
// against the database clock plus this window; the clock and the window
// both come from the hub now.
const oauthTokenRefreshLead = 5 * time.Minute

func (h *IdPHandler) refreshExpiringTokens(ctx context.Context) {
	tokens, err := h.store.OAuthTokens().ListExpiring(ctx, h.now().UTC().Add(oauthTokenRefreshLead))
	if err != nil {
		slog.Error("oauth refresh: list expiring tokens", "error", err)
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
			// A failure the shutdown itself caused must not take the delete
			// branch: the provider may have rotated the refresh token before
			// the cancel landed, so the stored refresh token is still the
			// live one, and deleting it would unlink the user's account
			// until they link it again. A genuine refusal keeps its delete.
			// The sweep ends here, because every later token would fail on
			// the same context and log at each shutdown.
			if ctx.Err() != nil {
				return
			}
			slog.Warn("oauth refresh: refresh failed, deleting tokens", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", err)
			_ = h.store.OAuthTokens().DeleteByUserAndProvider(ctx, store.DeleteOAuthTokensByUserAndProviderParams{
				UserID:     refreshUID,
				ProviderID: tok.ProviderID,
			})
			continue
		}

		// The refresh succeeded, so the provider has burned the OLD refresh
		// token on a rotating provider: the new pair must land, whatever the
		// loop's own context is doing. The detached budget is what makes the
		// write safe -- the shutdown joins this loop before the store closes,
		// so the write cannot race it.
		storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), oauthTokenStoreTimeout)
		storeErr := h.storeTokens(storeCtx, tok.UserID, tok.ProviderID, newTokens)
		cancelStore()
		if err := storeErr; err != nil {
			slog.Error("oauth refresh: store tokens", "user_id", tok.UserID, "error", err)
		}
	}
}
