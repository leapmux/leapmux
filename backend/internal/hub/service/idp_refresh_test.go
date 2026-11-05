package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func newIdPRefreshHandlerForTest(t *testing.T) *IdPHandler {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	set := settings.NewManager(st, nil, settings.CoreDescriptors())
	require.NoError(t, set.Load(context.Background()))
	ks, err := keystore.New(map[uint32][32]byte{1: {}})
	require.NoError(t, err)
	return NewIdPHandler(st, &config.Config{}, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
}

// The refresh loop's stopped channel is what the shutdown joins, so it must
// close once the context ends.
func TestStartTokenRefreshChannelClosesAfterCancel(t *testing.T) {
	t.Parallel()

	h := newIdPRefreshHandlerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := h.StartTokenRefresh(ctx)

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh loop's channel never closed after the cancel")
	}
}

// refreshStubProvider is the huboauth.Provider whose Refresh the test scripts.
type refreshStubProvider struct {
	huboauth.Provider
	refresh func(ctx context.Context, refreshToken string) (*huboauth.TokenSet, error)
}

func (p *refreshStubProvider) Refresh(ctx context.Context, refreshToken string) (*huboauth.TokenSet, error) {
	return p.refresh(ctx, refreshToken)
}

// tokenSpyStore records token deletes, so a test can prove a branch never ran.
type tokenSpyStore struct {
	store.Store
	deletes atomic.Int32
}

func (s *tokenSpyStore) OAuthTokens() store.OAuthTokenStore {
	inner := s.Store.OAuthTokens()
	return &tokenSpyTokens{OAuthTokenStore: inner, spy: s}
}

type tokenSpyTokens struct {
	store.OAuthTokenStore
	spy *tokenSpyStore
}

func (t *tokenSpyTokens) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	t.spy.deletes.Add(1)
	return t.OAuthTokenStore.DeleteByUserAndProvider(ctx, p)
}

// refreshFixture seeds one expiring token for user-1 at provider gh, encrypted
// with the handler's keystore, and caches the stub provider under the redirect
// URL that buildProvider derives.
func refreshFixture(t *testing.T, refresh func(ctx context.Context, refreshToken string) (*huboauth.TokenSet, error)) (*IdPHandler, *tokenSpyStore) {
	t.Helper()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	set := settings.NewManager(st, nil, settings.CoreDescriptors())
	require.NoError(t, set.Load(ctx))
	ks, err := keystore.New(map[uint32][32]byte{1: {}})
	require.NoError(t, err)
	spy := &tokenSpyStore{Store: st}
	h := NewIdPHandler(spy, &config.Config{}, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)

	// The token row refers to its user, so the user exists first.
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: "user-1", Username: "linked-user",
		PasswordHash: hash, DisplayName: "Linked", FirstCredentialExempt: true,
	}))

	require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID: "gh", ProviderType: huboauth.ProviderTypeGitHub, Name: "GitHub",
		ClientID: "cid", ClientSecret: mustEncrypt(t, ks, "secret", "gh"), Enabled: true,
	}))
	plainRefresh := []byte("old-refresh-token")
	encRefresh, err := ks.Encrypt(plainRefresh, keystore.RefreshTokenAAD("user-1", "gh"))
	require.NoError(t, err)
	require.NoError(t, st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID: userid.MustNew("user-1"), ProviderID: "gh",
		AccessToken: encRefresh, RefreshToken: encRefresh,
		TokenType:  "bearer",
		ExpiresAt:  h.now().UTC().Add(time.Minute),
		KeyVersion: int64(ks.ActiveVersion()),
	}))

	stub := &refreshStubProvider{Provider: huboauth.NewGitHubProvider("cid", "unused", "http://localhost/cb", nil), refresh: refresh}
	redirect := fmt.Sprintf("%s/auth/idp/%s/callback", settings.BaseURL(set.Snapshot(ctx), h.cfg.PrimaryTCPListen()), "gh")
	h.cacheProvider(providerCacheKey{providerID: "gh", redirectURL: redirect}, stub, h.providerGeneration("gh"))
	return h, spy
}

func mustEncrypt(t *testing.T, ks *keystore.Keystore, plain, providerID string) []byte {
	t.Helper()
	enc, err := ks.Encrypt([]byte(plain), keystore.ProviderAAD(providerID))
	require.NoError(t, err)
	return enc
}

// A provider that rotated its tokens before the shutdown's cancel landed must
// still have its new pair stored: on a rotating provider the OLD refresh token
// is already dead, and dropping the write would unlink the account.
func TestRefreshStoresRotatedTokensAfterCancel(t *testing.T) {
	var (
		cancelLoop context.CancelFunc
		loopCtx    context.Context
	)
	h, spy := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		// The shutdown lands mid-pass: the loop's context ends, and the
		// provider completes the rotation anyway.
		cancelLoop()
		return &huboauth.TokenSet{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "bearer"}, nil
	})
	loopCtx, cancelLoop = context.WithCancel(context.Background())
	defer cancelLoop()
	h.refreshExpiringTokens(loopCtx)

	row, err := spy.Store.OAuthTokens().Get(context.Background(), store.GetOAuthTokensParams{UserID: userid.MustNew("user-1"), ProviderID: "gh"})
	require.NoError(t, err, "the rotated pair must be stored")
	decrypted, err := h.keystore.Decrypt(row.RefreshToken, keystore.RefreshTokenAAD("user-1", "gh"))
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", string(decrypted))
}

// A refresh failure the shutdown itself caused must not take the delete
// branch: the cancelled context makes the refresh fail either way, and the
// stored refresh token may still be the live one.
func TestRefreshDeletesNothingWhenShutdownCancelsTheRefresh(t *testing.T) {
	previousLogger := slog.Default()
	var warns int
	slog.SetDefault(slog.New(&errorCountingHandlerForWarns{count: &warns}))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	// Cancelled from inside the refresh. A context cancelled before the sweep
	// fails ListExpiring, so the refresh would never run.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	h, spy := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		calls.Add(1)
		cancel()
		return nil, context.Canceled
	})

	h.refreshExpiringTokens(ctx)

	require.Equal(t, int32(1), calls.Load(), "the refresh must have run")
	assert.Zero(t, spy.deletes.Load(), "a cancelled refresh must not delete the stored tokens")
	assert.Zero(t, warns, "the delete branch must not log, because it must not run")
}

// A cancel ends the sweep, not one token: every later token would fail on the
// same context, and each failure would log at every shutdown.
func TestRefreshStopsTheSweepWhenShutdownCancelsARefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	h, _ := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		calls.Add(1)
		cancel()
		return nil, context.Canceled
	})
	seedSecondExpiringToken(t, h)

	h.refreshExpiringTokens(ctx)

	assert.Equal(t, int32(1), calls.Load(), "the sweep must stop at the first cancelled refresh")
}

// seedSecondExpiringToken gives the fixture's provider a second token to sweep.
func seedSecondExpiringToken(t *testing.T, h *IdPHandler) {
	t.Helper()
	ctx := context.Background()
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	require.NoError(t, h.store.Users().Create(ctx, store.CreateUserParams{
		ID: "user-2", Username: "second-user",
		PasswordHash: hash, DisplayName: "Second", FirstCredentialExempt: true,
	}))
	encRefresh, err := h.keystore.Encrypt([]byte("second-refresh-token"), keystore.RefreshTokenAAD("user-2", "gh"))
	require.NoError(t, err)
	require.NoError(t, h.store.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID: userid.MustNew("user-2"), ProviderID: "gh",
		AccessToken: encRefresh, RefreshToken: encRefresh,
		TokenType:  "bearer",
		ExpiresAt:  h.now().UTC().Add(time.Minute),
		KeyVersion: int64(h.keystore.ActiveVersion()),
	}))
}

// A genuine refusal with a live context keeps its delete: the refresh token is
// dead on the provider's side, and the row would only invite a doomed retry.
func TestRefreshStillDeletesARefusedToken(t *testing.T) {
	h, spy := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		return nil, errors.New("the provider refused the grant")
	})

	h.refreshExpiringTokens(context.Background())

	assert.Equal(t, int32(1), spy.deletes.Load(), "a refused token must be deleted")
}

// errorCountingHandlerForWarns counts Warn records, so a test can assert a
// branch never logged.
type errorCountingHandlerForWarns struct {
	count *int
	mu    sync.Mutex
}

func (h *errorCountingHandlerForWarns) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *errorCountingHandlerForWarns) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level == slog.LevelWarn {
		*h.count++
	}
	return nil
}

func (h *errorCountingHandlerForWarns) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *errorCountingHandlerForWarns) WithGroup(name string) slog.Handler       { return h }
