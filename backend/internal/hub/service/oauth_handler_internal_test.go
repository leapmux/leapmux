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

	h := NewOAuthHandler(st, &config.Config{SessionDuration: configured}, ks)

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

	sess, err := st.Sessions().GetByID(context.Background(), parsed.Value)
	require.NoError(t, err)
	assert.WithinDuration(t, sess.ExpiresAt, parsed.Expires, time.Second,
		"the cookie and the row must carry the same deadline")
}
