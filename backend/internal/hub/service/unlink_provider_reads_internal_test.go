package service

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// providerListCounts records where the code reads the configured-provider
// list.
type providerListCounts struct {
	total  int
	locked int
}

// countingProviderStore counts OAuthProviders().ListAll, and separately
// counts the calls that happen while the user-auth transaction holds the
// writer lock.
type countingProviderStore struct {
	store.Store
	counts *providerListCounts
	locked bool
}

func (s countingProviderStore) OAuthProviders() store.OAuthProviderStore {
	return countingProviderList{OAuthProviderStore: s.Store.OAuthProviders(), counts: s.counts, locked: s.locked}
}

func (s countingProviderStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		return fn(countingProviderStore{Store: tx, counts: s.counts, locked: true})
	})
}

type countingProviderList struct {
	store.OAuthProviderStore
	counts *providerListCounts
	locked bool
}

func (l countingProviderList) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	l.counts.total++
	if l.locked {
		l.counts.locked++
	}
	return l.OAuthProviderStore.ListAll(ctx)
}

// TestUnlinkOAuthProvider_ReadsTheProviderListOnceOutsideTheLock pins where
// the hub-wide read happens.
//
// RunInUserAuthTransaction takes the SQLite writer lock at the start, so every
// query inside it makes every other writer on the hub wait. The rule runs
// TWICE -- once before the lock to answer an ordinary refusal early, once
// under it as the TOCTOU guard -- and each run read the configured-provider
// list again, which put a whole round trip inside the lock for an answer the
// first read already had. That lock does not protect the oauth_providers
// table (no administrator verb takes it), so one read before the lock is the
// same answer.
//
// The passkey COUNT must stay inside, and it does: the lock DOES protect the
// account's passkeys, so the locked run has to see them.
func TestUnlinkOAuthProvider_ReadsTheProviderListOnceOutsideTheLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, false)

	for _, providerID := range []string{"gh", "okta"} {
		require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: providerID, ProviderType: huboauth.ProviderTypeOIDC, Name: providerID,
			ClientID: "cid", ClientSecret: []byte("secret"), Enabled: true,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(user.ID), ProviderID: providerID, ProviderSubject: "sub-" + providerID,
		}))
	}

	sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(user.ID), auth.DefaultSessionDuration)
	require.NoError(t, err)
	now := time.Now().UTC()

	counts := &providerListCounts{}
	svc := &UserService{
		store:     countingProviderStore{Store: st, counts: counts},
		cfg:       &config.Config{},
		lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil),
	}
	acting := auth.WithUser(ctx, &auth.UserInfo{
		ID:                 userid.MustNew(user.ID),
		Credential:         auth.SessionCredential(sessionID),
		AuthenticatedAt:    now,
		Elevation:          auth.NewElevation(&now, ptrTime(now.Add(auth.ElevationWindow))),
		UserAuthGeneration: user.AuthGeneration,
	})

	_, err = svc.UnlinkOAuthProvider(acting, connect.NewRequest(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "okta",
	}))
	require.NoError(t, err, "a second enabled link remains, so the account keeps a login method")

	assert.Equal(t, 1, counts.total, "one read serves both runs of the rule")
	assert.Zero(t, counts.locked, "the writer lock must not hold a hub-wide read")

	links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "gh", links[0].ProviderID)
}
