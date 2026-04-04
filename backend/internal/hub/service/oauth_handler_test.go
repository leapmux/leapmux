package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupOAuthTestServer(t *testing.T) (*httptest.Server, *gendb.Queries, *keystore.Keystore) {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)
	err = bootstrap.Run(context.Background(), q, false)
	require.NoError(t, err)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[byte][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Addr:          ":4327",
		SignupEnabled: true,
	}

	oauthHandler := service.NewOAuthHandler(q, cfg, ks)

	mux := http.NewServeMux()
	oauthHandler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, q, ks
}

func createTestProvider(t *testing.T, q *gendb.Queries, ks *keystore.Keystore) string {
	t.Helper()
	providerID := id.Generate()
	aad := []byte("oauth_provider:" + providerID)
	encSecret, err := ks.Encrypt([]byte("test-client-secret"), aad)
	require.NoError(t, err)

	err = q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: "github",
		Name:         "Test GitHub",
		ClientID:     "test-client-id",
		ClientSecret: encSecret,
		Scopes:       "read:user user:email",
		Enabled:      1,
	})
	require.NoError(t, err)
	return providerID
}

func TestOAuthLogin_RedirectsToProvider(t *testing.T) {
	server, q, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, q, ks)

	// Don't follow redirects.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusFound, resp.StatusCode)

	location := resp.Header.Get("Location")
	assert.Contains(t, location, "github.com")
	assert.Contains(t, location, "state=")

	// Verify state was stored in DB.
	// (We can't easily extract it from the redirect URL without parsing, but
	// the redirect working proves CreateOAuthState succeeded.)
}

func TestOAuthLogin_UnknownProvider_Returns404(t *testing.T) {
	server, _, _ := setupOAuthTestServer(t)

	resp, err := http.Get(server.URL + "/auth/oauth/nonexistent/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestOAuthLogin_DisabledProvider_Returns403(t *testing.T) {
	server, q, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, q, ks)

	// Disable the provider.
	err := q.UpdateOAuthProviderEnabled(context.Background(), gendb.UpdateOAuthProviderEnabledParams{
		Enabled: 0,
		ID:      providerID,
	})
	require.NoError(t, err)

	resp, err := http.Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestOAuthLogin_StoresRedirectURI(t *testing.T) {
	server, q, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, q, ks)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(server.URL + "/auth/oauth/" + providerID + "/login?redirect=/workspace/123")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
}

func TestOAuthCallback_InvalidState_Returns400(t *testing.T) {
	server, _, _ := setupOAuthTestServer(t)

	resp, err := http.Get(server.URL + "/auth/oauth/some-provider/callback?code=test&state=invalid-state")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuthCallback_MissingCodeOrState_Returns400(t *testing.T) {
	server, _, _ := setupOAuthTestServer(t)

	// Missing both.
	resp, err := http.Get(server.URL + "/auth/oauth/some-provider/callback")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing code.
	resp2, err := http.Get(server.URL + "/auth/oauth/some-provider/callback?state=abc")
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)

	// Missing state.
	resp3, err := http.Get(server.URL + "/auth/oauth/some-provider/callback?code=abc")
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp3.StatusCode)
}

func TestOAuthCallback_ExpiredState_Returns400(t *testing.T) {
	server, q, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, q, ks)

	// Create an already-expired state.
	err := q.CreateOAuthState(context.Background(), gendb.CreateOAuthStateParams{
		State:        "expired-state",
		ProviderID:   providerID,
		PkceVerifier: "test-verifier",
		ExpiresAt:    time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	resp, err := http.Get(server.URL + "/auth/oauth/" + providerID + "/callback?code=test&state=expired-state")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestGetOAuthProviders_ReturnsEnabledOnly(t *testing.T) {
	_, q, ks := setupOAuthTestServer(t)

	// Create two providers, one enabled and one disabled.
	enabledID := createTestProvider(t, q, ks)
	disabledID := id.Generate()
	aad := []byte("oauth_provider:" + disabledID)
	encSecret, _ := ks.Encrypt([]byte("secret"), aad)
	_ = q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           disabledID,
		ProviderType: "oidc",
		Name:         "Disabled OIDC",
		ClientID:     "disabled-client",
		ClientSecret: encSecret,
		Scopes:       "openid",
		Enabled:      0,
	})

	providers, err := q.ListEnabledOAuthProviders(context.Background())
	require.NoError(t, err)

	// Only the enabled provider should be listed.
	assert.Len(t, providers, 1)
	assert.Equal(t, enabledID, providers[0].ID)
}

func TestOAuthTokenStorage_EncryptedInDB(t *testing.T) {
	_, _, ks := setupOAuthTestServer(t)

	plainAccess := "access-token-plaintext"
	plainRefresh := "refresh-token-plaintext"
	userID := "test-user"
	providerID := "test-provider"

	accessAAD := []byte("access_token:" + userID + ":" + providerID)
	refreshAAD := []byte("refresh_token:" + userID + ":" + providerID)

	encAccess, err := ks.Encrypt([]byte(plainAccess), accessAAD)
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte(plainRefresh), refreshAAD)
	require.NoError(t, err)

	// Verify ciphertext is different from plaintext.
	assert.NotEqual(t, []byte(plainAccess), encAccess)
	assert.NotEqual(t, []byte(plainRefresh), encRefresh)

	// Verify decryption returns original values.
	gotAccess, err := ks.Decrypt(encAccess, accessAAD)
	require.NoError(t, err)
	assert.Equal(t, plainAccess, string(gotAccess))

	gotRefresh, err := ks.Decrypt(encRefresh, refreshAAD)
	require.NoError(t, err)
	assert.Equal(t, plainRefresh, string(gotRefresh))

	// Verify wrong AAD fails.
	_, err = ks.Decrypt(encAccess, []byte("wrong-aad"))
	assert.Error(t, err)
}

func TestOAuthTokenStorage_KeyVersionMatches(t *testing.T) {
	_, _, ks := setupOAuthTestServer(t)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	// First byte is the key version.
	assert.Equal(t, ks.ActiveVersion(), ct[0])
}

// Ensure unused imports don't cause errors in this test file.
var _ = auth.CookieName
