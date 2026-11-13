package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func newIdPRefreshHandlerForTest(t *testing.T) *IdPHandler {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	set := settings.NewManager(st, nil, settings.CoreDescriptors())
	require.NoError(t, set.Load(context.Background()))
	ks, err := keystore.New(map[uint32][32]byte{1: {}})
	require.NoError(t, err)
	return NewIdPHandler(st, hubtestutil.TestSecretHasher(), &config.Config{}, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
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
	deletes   atomic.Int32
	deleteErr error
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
	if t.spy.deleteErr != nil {
		return t.spy.deleteErr
	}
	return t.OAuthTokenStore.DeleteByUserAndProvider(ctx, p)
}

func TestRefreshProtectsAdmittedRotationFromShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var survivedCancel, hasDeadline bool
	var calls int
	refreshedUser := ""
	h, spy := refreshFixture(t, func(workCtx context.Context, refreshToken string) (*huboauth.TokenSet, error) {
		calls++
		if calls == 1 {
			refreshedUser = "user-1"
			if refreshToken == "second-refresh-token" {
				refreshedUser = "user-2"
			}
		}
		_, hasDeadline = workCtx.Deadline()
		cancel()
		survivedCancel = workCtx.Err() == nil
		if err := workCtx.Err(); err != nil {
			return nil, err
		}
		return &huboauth.TokenSet{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "bearer"}, nil
	})
	seedSecondExpiringToken(t, h)
	h.refreshExpiringTokens(ctx)
	require.Equal(t, 1, calls, "shutdown must not admit another refresh after the current operation")
	assert.True(t, survivedCancel, "shutdown must not cancel a refresh that may already have rotated its token")
	assert.True(t, hasDeadline, "an admitted refresh must retain its own deadline")
	row, err := spy.Store.OAuthTokens().Get(t.Context(), store.GetOAuthTokensParams{UserID: userid.MustNew(refreshedUser), ProviderID: "gh"})
	require.NoError(t, err)
	decrypted, err := h.keystore.Decrypt(row.RefreshToken, keystore.RefreshTokenAAD(refreshedUser, "gh"))
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", string(decrypted))
}

func TestRefreshLeavesAnAlreadyCancelledSweepQuiet(t *testing.T) {
	var calls int
	h, _ := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		calls++
		return nil, errors.New("an already cancelled sweep must not reach the provider")
	})
	logs := testutil.CaptureDefaultLogger(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.refreshExpiringTokens(ctx)
	assert.Zero(t, calls)
	assert.NotContains(t, logs.String(), "oauth refresh: list expiring tokens")
}

func TestRefreshRetainsTokensAfterTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "transport", err: errors.New("connection reset")},
		{name: "throttled", err: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusTooManyRequests}, ErrorCode: "temporarily_unavailable"}},
		{name: "provider failure", err: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}, ErrorCode: "server_error"}},
		{name: "invalid client", err: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusUnauthorized}, ErrorCode: "invalid_client"}},
		{name: "unclassified response", err: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusBadRequest}}},
		{name: "server failure with invalid grant", err: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}, ErrorCode: "invalid_grant"}},
		{name: "missing response", err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			h, spy := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
				if !recovered.Load() {
					return nil, fmt.Errorf("refresh failed: %w", tc.err)
				}
				return &huboauth.TokenSet{AccessToken: "retry-access", RefreshToken: "retry-refresh", TokenType: "bearer"}, nil
			})
			key := store.GetOAuthTokensParams{UserID: userid.MustNew("user-1"), ProviderID: "gh"}
			before, err := spy.Store.OAuthTokens().Get(t.Context(), key)
			require.NoError(t, err)
			h.refreshExpiringTokens(t.Context())
			assert.Zero(t, spy.deletes.Load(), "a temporary failure must not destroy a reusable token")
			after, err := spy.Store.OAuthTokens().Get(t.Context(), key)
			require.NoError(t, err)
			assert.Equal(t, before.RefreshToken, after.RefreshToken)
			assert.Equal(t, before.AccessToken, after.AccessToken)
			recovered.Store(true)
			h.refreshExpiringTokens(t.Context())
			after, err = spy.Store.OAuthTokens().Get(t.Context(), key)
			require.NoError(t, err)
			plain, err := h.keystore.Decrypt(after.RefreshToken, keystore.RefreshTokenAAD("user-1", "gh"))
			require.NoError(t, err)
			assert.Equal(t, "retry-refresh", string(plain), "the retained credential must remain eligible for retry")
		})
	}
}

func TestRefreshReportsFailureToDeleteARefusedToken(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	h, spy := refreshFixture(t, func(context.Context, string) (*huboauth.TokenSet, error) {
		return nil, huboauth.ErrRefreshTokenRejected
	})
	spy.deleteErr = errors.New("the token delete failed")
	h.refreshExpiringTokens(t.Context())
	assert.Equal(t, int32(1), spy.deletes.Load())
	assert.Contains(t, logs.String(), spy.deleteErr.Error(), "a failed deletion must remain diagnosable")
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
	h := NewIdPHandler(spy, hubtestutil.TestSecretHasher(), &config.Config{}, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)

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
// is already dead, and dropping the write would lose the usable credentials.
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
		return nil, fmt.Errorf("the provider refused the grant: %w", huboauth.ErrRefreshTokenRejected)
	})

	h.refreshExpiringTokens(context.Background())

	assert.Equal(t, int32(1), spy.deletes.Load(), "a refused token must be deleted")
	_, err := spy.Store.OAuthTokens().Get(t.Context(), store.GetOAuthTokensParams{UserID: userid.MustNew("user-1"), ProviderID: "gh"})
	assert.ErrorIs(t, err, store.ErrNotFound)
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

// refreshResponseTransport supplies a provider response without external I/O.
type refreshResponseTransport func(*http.Request) (*http.Response, error)

func (f refreshResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshDeletesGitHubRejectedTokens(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		for _, contentType := range []string{"application/json", "application/x-www-form-urlencoded"} {
			t.Run(fmt.Sprintf("%d/%s", status, contentType), func(t *testing.T) {
				provider := huboauth.NewGitHubProvider("cid", "secret", "http://localhost/cb", nil)
				h, spy := refreshFixture(t, provider.Refresh)
				requests := 0
				client := &http.Client{Transport: refreshResponseTransport(func(r *http.Request) (*http.Response, error) {
					requests++
					assert.Equal(t, "github.com", r.URL.Host)
					assert.Equal(t, "/login/oauth/access_token", r.URL.Path)
					require.NoError(t, r.ParseForm())
					assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
					assert.Equal(t, "old-refresh-token", r.Form.Get("refresh_token"))
					body := `{"error":"bad_refresh_token"}`
					if contentType != "application/json" {
						body = "error=bad_refresh_token"
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}
				ctx := context.WithValue(t.Context(), oauth2.HTTPClient, client)
				h.refreshExpiringTokens(ctx)
				require.Positive(t, requests)
				assert.Equal(t, int32(1), spy.deletes.Load())
				_, err := spy.Store.OAuthTokens().Get(t.Context(), store.GetOAuthTokensParams{UserID: userid.MustNew("user-1"), ProviderID: "gh"})
				assert.ErrorIs(t, err, store.ErrNotFound)
				before := requests
				h.refreshExpiringTokens(ctx)
				assert.Equal(t, before, requests, "a rejected credential must not be retried")
			})
		}
	}
}
