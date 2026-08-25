package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	h := NewOAuthHandler(st, &config.Config{}, set, ks)

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
