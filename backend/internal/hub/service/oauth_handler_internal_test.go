package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

func TestSanitizeRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"empty", "", ""},
		{"root", "/", "/"},
		{"relative path", "/workspace/123", "/workspace/123"},
		{"deep path", "/a/b/c?q=1", "/a/b/c?q=1"},
		{"absolute URL rejected", "https://evil.com", ""},
		{"http URL rejected", "http://evil.com", ""},
		{"protocol-relative rejected", "//evil.com", ""},
		{"protocol-relative with path rejected", "//evil.com/callback", ""},
		{"bare domain rejected", "evil.com", ""},
		{"javascript scheme rejected", "javascript:alert(1)", ""},
		{"data scheme rejected", "data:text/html,<h1>hi</h1>", ""},
		{"backslash rejected", "\\evil.com", ""},
		// A WHATWG parser reads a backslash as a slash for a special
		// scheme, so "/\host" leaves the origin although Go's own URL
		// parser calls it a path. This is why the guard reads raw bytes
		// instead of parsing and comparing origins.
		{"backslash twin rejected", "/\\evil.com", ""},
		{"backslash twin with path rejected", "/\\evil.com/callback", ""},
		// url.Parse refuses a control byte, so http.Redirect skips its
		// cleaning branch and writes the value verbatim; the browser then
		// strips the tab and reads "//evil.com".
		{"tab-injected authority rejected", "/\t/evil.com", ""},
		{"tab before a backslash twin rejected", "/\t\\evil.com", ""},
		{"carriage return rejected", "/\r/evil.com", ""},
		{"line feed rejected", "/\n/evil.com", ""},
		{"delete byte rejected", "/\x7f/evil.com", ""},
		{"null byte rejected", "/\x00/evil.com", ""},
		// Percent-encodings are NOT decoded before the browser picks the
		// authority, so these stay ordinary same-origin paths.
		{"percent-encoded tab kept", "/%09/evil.com", "/%09/evil.com"},
		{"percent-encoded backslash kept", "/%5Cevil.com", "/%5Cevil.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeRedirectURI(tt.uri))
		})
	}
}

// The OAuth callback logs an already-linked user in through its own
// CreateSession call, which no RPC reaches: the exported route needs a real
// token exchange with an identity provider. Calling the login step directly is
// what keeps that fifth mint path from being the one left on the built-in
// default while every other path follows the operator's setting.

// TestProviderCacheHoldsOneEntryPerProvider pins the cache's size, not only
// its hits.
//
// The key carries the redirect URL as well as the provider id, because a
// built Provider bakes both in and the redirect URL derives from public_url
// and secure_cookies -- settings an administrator changes live. Nothing
// evicted, so every such edit left the previous instance (with its own OIDC
// client and discovery state) reachable by no lookup and alive for the life
// of the process. Only the CURRENT redirect URL can be looked up again.
func TestProviderCacheHoldsOneEntryPerProvider(t *testing.T) {
	t.Parallel()

	h := &OAuthHandler{providers: make(map[providerCacheKey]huboauth.Provider)}
	stub := huboauth.NewGitHubProvider("id", "secret", "http://localhost/cb", nil)

	for _, redirect := range []string{
		"http://localhost:4327/auth/oauth/gh/callback",
		"https://hub.example.com/auth/oauth/gh/callback",
		"https://hub2.example.com/auth/oauth/gh/callback",
	} {
		key := providerCacheKey{providerID: "gh", redirectURL: redirect}
		h.cacheProvider(key, stub)
		require.Len(t, h.providers, 1, "an edit to public_url must replace the entry, not add one")
		_, ok := h.providers[key]
		assert.True(t, ok, "the current redirect URL is the one that stays reachable")
	}

	// A DIFFERENT provider keeps its own entry: the eviction is per
	// provider id, not a one-entry cache.
	other := providerCacheKey{providerID: "okta", redirectURL: "https://hub2.example.com/auth/oauth/okta/callback"}
	h.cacheProvider(other, stub)
	assert.Len(t, h.providers, 2)
}

func TestLoginOAuthUser_UsesConfiguredSessionDuration(t *testing.T) {
	t.Parallel()

	const configured = 90 * time.Minute
	st := hubtestutil.OpenTestStore(t)
	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "oauthuser",
		PasswordHash: "hash",
		DisplayName:  "OAuth User",
		Email:        "oauth@example.com",
	}))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	set := settings.NewManager(st, nil, settings.CoreDescriptors())
	require.NoError(t, set.Load(context.Background()))
	require.NoError(t, settings.KeySessionDurationSeconds.Set(context.Background(), set, int64(configured/time.Second)))

	h := NewOAuthHandler(st, &config.Config{}, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oauth/test/callback", nil)
	before := time.Now()
	h.loginOAuthUser(rec, req, userID, "", &huboauth.TokenSet{
		AccessToken: "access", RefreshToken: "refresh", TokenType: "bearer",
		ExpiresAt: time.Now().Add(time.Hour),
	}, "")

	require.Equal(t, http.StatusFound, rec.Code)
	parsed, err := http.ParseSetCookie(rec.Header().Get("Set-Cookie"))
	require.NoError(t, err, "the callback must set a session cookie")
	hubtestutil.AssertSessionLifetime(t, before, configured, parsed.Expires)

	sess, err := st.Sessions().GetByID(context.Background(), parsed.Value, time.Now().UTC())
	require.NoError(t, err)
	assert.WithinDuration(t, sess.ExpiresAt, parsed.Expires, time.Second,
		"the cookie and the row must carry the same deadline")
}

// TestInvalidateProviderDropsEveryEntry pins the eviction that a REMOVED or
// DISABLED provider needs.
//
// cacheProvider only sweeps when the same provider is rebuilt, and
// loadEnabledProvider refuses a deleted row with 404 and a disabled row with
// 403 BEFORE it would rebuild -- so nothing could ever reach those entries
// again, and each one holds the client secret the keystore decrypted, for
// the life of the process.
func TestInvalidateProviderDropsEveryEntry(t *testing.T) {
	t.Parallel()

	h := &OAuthHandler{providers: make(map[providerCacheKey]huboauth.Provider)}
	stub := huboauth.NewGitHubProvider("id", "secret", "http://localhost/cb", nil)

	// Two entries for one provider is the state a live public_url edit
	// leaves behind, so the sweep has to clear both.
	h.providers[providerCacheKey{providerID: "gh", redirectURL: "http://a/cb"}] = stub
	h.providers[providerCacheKey{providerID: "gh", redirectURL: "http://b/cb"}] = stub
	kept := providerCacheKey{providerID: "okta", redirectURL: "http://a/cb"}
	h.providers[kept] = stub

	h.InvalidateProvider("gh")

	require.Len(t, h.providers, 1)
	_, ok := h.providers[kept]
	assert.True(t, ok, "the eviction is per provider id, not a cache flush")

	// Idempotent: an id with no entries is the state a second delete finds.
	h.InvalidateProvider("gh")
	assert.Len(t, h.providers, 1)
}
