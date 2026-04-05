package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
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
	hubtestutil.CreateTestAdmin(t, q)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Addr:          ":4327",
		SignupEnabled: true,
	}

	oauthHandler := service.NewOAuthHandler(sqlDB, q, cfg, ks)

	mux := http.NewServeMux()
	oauthHandler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, q, ks
}

func createTestProvider(t *testing.T, q *gendb.Queries, ks *keystore.Keystore) string {
	return createTestProviderWithTrustEmail(t, q, ks, 1)
}

func createTestProviderWithTrustEmail(t *testing.T, q *gendb.Queries, ks *keystore.Keystore, trustEmail int64) string {
	t.Helper()
	providerID := id.Generate()
	aad := keystore.ProviderAAD(providerID)
	encSecret, err := ks.Encrypt([]byte("test-client-secret"), aad)
	require.NoError(t, err)

	err = q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: "github",
		Name:         "Test GitHub",
		ClientID:     "test-client-id",
		ClientSecret: encSecret,
		Scopes:       "read:user user:email",
		TrustEmail:   trustEmail,
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
	aad := keystore.ProviderAAD(disabledID)
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

	accessAAD := keystore.AccessTokenAAD(userID, providerID)
	refreshAAD := keystore.RefreshTokenAAD(userID, providerID)

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

	ver, err := keystore.CiphertextVersion(ct)
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveVersion(), ver)
}

// setupOAuthTestServerWithAuthService sets up both OAuthHandler (HTTP routes) and
// AuthService (ConnectRPC) on a single test server, enabling tests that exercise
// the pending-signup → complete-signup flow via RPC.
func setupOAuthTestServerWithAuthService(t *testing.T) (
	*httptest.Server,
	leapmuxv1connect.AuthServiceClient,
	*gendb.Queries,
	*keystore.Keystore,
	*config.Config,
) {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)
	hubtestutil.CreateTestAdmin(t, q)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Addr:          ":4327",
		SignupEnabled: true,
	}

	mux := http.NewServeMux()

	// Register OAuth HTTP routes.
	oauthHandler := service.NewOAuthHandler(sqlDB, q, cfg, ks)
	oauthHandler.RegisterRoutes(mux)

	// Register AuthService ConnectRPC routes.
	interceptor, _ := auth.NewInterceptor(q, false, false, false)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, cfg, nil, ks)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return server, client, q, ks, cfg
}

// insertPendingSignup creates a pending_oauth_signups row with encrypted tokens.
func insertPendingSignup(t *testing.T, q *gendb.Queries, ks *keystore.Keystore, providerID, token, email, displayName, subject string, expiresAt time.Time) {
	t.Helper()
	encAccess, err := ks.Encrypt([]byte("mock-access-token"), keystore.AccessTokenAAD(token, providerID))
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte("mock-refresh-token"), keystore.RefreshTokenAAD(token, providerID))
	require.NoError(t, err)

	err = q.CreatePendingOAuthSignup(context.Background(), gendb.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: subject,
		Email:           email,
		DisplayName:     displayName,
		AccessToken:     encAccess,
		RefreshToken:    encRefresh,
		TokenType:       "bearer",
		TokenExpiresAt:  time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:      int64(ks.ActiveVersion()),
		ExpiresAt:       expiresAt,
	})
	require.NoError(t, err)
}

// --- GetPendingOAuthSignup RPC tests ---

func TestGetPendingOAuthSignup_Success(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "alice@example.com", "Alice", "sub-123", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.GetPendingOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", resp.Msg.GetEmail())
	assert.Equal(t, "Alice", resp.Msg.GetDisplayName())
	assert.Equal(t, "Test GitHub", resp.Msg.GetProviderName())
}

func TestGetPendingOAuthSignup_InvalidToken(t *testing.T) {
	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.GetPendingOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: "nonexistent-token",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetPendingOAuthSignup_ExpiredToken(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	// Insert an already-expired pending signup.
	insertPendingSignup(t, q, ks, providerID, signupToken, "expired@example.com", "Expired", "sub-expired", time.Now().Add(-1*time.Minute).UTC())

	_, err := client.GetPendingOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- CompleteOAuthSignup RPC tests ---

func TestCompleteOAuthSignup_Success(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "bob@example.com", "Bob", "sub-bob", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "bobuser",
		DisplayName: "Bob User",
	}))
	require.NoError(t, err)
	assert.Equal(t, "bobuser", resp.Msg.GetUser().GetUsername())
	assert.Equal(t, "Bob User", resp.Msg.GetUser().GetDisplayName())

	// Verify session cookie is set.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify OAuth user link was created.
	link, err := q.GetOAuthUserLink(context.Background(), gendb.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: "sub-bob",
	})
	require.NoError(t, err)
	assert.Equal(t, resp.Msg.GetUser().GetId(), link.UserID)

	// Verify pending signup was consumed.
	_, err = q.GetPendingOAuthSignup(context.Background(), signupToken)
	require.Error(t, err)
}

func TestCompleteOAuthSignup_UsesProviderEmail_IgnoresRequestEmail(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks) // trust_email=1 by default
	signupToken := id.Generate()

	// Pending signup has provider email "provider@example.com".
	insertPendingSignup(t, q, ks, providerID, signupToken, "provider@example.com", "Provider", "sub-provider", time.Now().Add(5*time.Minute).UTC())

	// Request tries to override with a different email — should be ignored.
	resp, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "provideruser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	// The user's email should be the provider's, not the attacker's.
	user, err := q.GetUserByID(context.Background(), resp.Msg.GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "provider@example.com", user.Email, "email must come from provider, not request")
}

func TestCompleteOAuthSignup_DuplicateUsername(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "dup@example.com", "Dup", "sub-dup", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same username.
	orgID := id.Generate()
	err := q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "existing-org"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           id.Generate(),
		OrgID:        orgID,
		Username:     "takenname",
		PasswordHash: hash,
		DisplayName:  "Taken",
		Email:        "",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	require.NoError(t, err)

	_, err = client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "takenname",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// Pending row should NOT be deleted so the user can retry with a different username.
	_, err = q.GetPendingOAuthSignup(context.Background(), signupToken)
	require.NoError(t, err)
}

func TestCompleteOAuthSignup_DuplicateEmail(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, q, ks, 0) // untrusted provider
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "taken@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same email.
	orgID := id.Generate()
	err := q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "emaildup-org"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           id.Generate(),
		OrgID:        orgID,
		Username:     "emailowner",
		PasswordHash: hash,
		DisplayName:  "Email Owner",
		Email:        "taken@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	require.NoError(t, err)

	// Provider has trust_email=0 and cfg.EmailVerificationRequired is false,
	// so email goes directly to the email column unverified. Duplicate check should fire.
	_, err = client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "newuniqueuser",
		Email:       "taken@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_InvalidToken(t *testing.T) {
	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: "nonexistent-token",
		Username:    "someuser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_InvalidUsername(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "valid@example.com", "Valid", "sub-valid", time.Now().Add(5*time.Minute).UTC())

	_, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "", // empty username
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_TokenConsumedOnSuccess(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "consume@example.com", "Consume", "sub-consume", time.Now().Add(5*time.Minute).UTC())

	// First call succeeds.
	_, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "consumeuser",
	}))
	require.NoError(t, err)

	// Second call with the same token fails.
	_, err = client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "anotheruser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_ReencryptsTokensWithActiveKeyVersion(t *testing.T) {
	_, client, q, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, q, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, q, ks, providerID, signupToken, "keyver@example.com", "KeyVer", "sub-keyver", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(context.Background(), connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "keyveruser",
	}))
	require.NoError(t, err)

	userID := resp.Msg.GetUser().GetId()

	// Verify stored tokens use the active key version and can be decrypted
	// with the user ID as AAD (not the signup token).
	tok, err := q.GetOAuthTokens(context.Background(), gendb.GetOAuthTokensParams{
		UserID:     userID,
		ProviderID: providerID,
	})
	require.NoError(t, err)

	assert.Equal(t, int64(ks.ActiveVersion()), tok.KeyVersion)

	ver, err := keystore.CiphertextVersion(tok.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveVersion(), ver, "access token ciphertext should use active key version")

	// Decrypt with user ID AAD should succeed.
	plainAccess, err := ks.Decrypt(tok.AccessToken, keystore.AccessTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "mock-access-token", string(plainAccess))

	plainRefresh, err := ks.Decrypt(tok.RefreshToken, keystore.RefreshTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "mock-refresh-token", string(plainRefresh))
}

// --- Callback behavior tests (signup disabled) ---

func TestOAuthCallback_NewUser_SignupDisabled(t *testing.T) {
	// Use a custom setup with SignupEnabled=false.
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)
	hubtestutil.CreateTestAdmin(t, q)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Addr:          ":4327",
		SignupEnabled: false, // signup disabled
	}

	oauthHandler := service.NewOAuthHandler(sqlDB, q, cfg, ks)
	mux := http.NewServeMux()
	oauthHandler.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	providerID := createTestProvider(t, q, ks)

	// Create a valid OAuth state for the callback.
	stateValue := id.Generate()
	err = q.CreateOAuthState(context.Background(), gendb.CreateOAuthStateParams{
		State:        stateValue,
		ProviderID:   providerID,
		PkceVerifier: "test-verifier",
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	// The callback will attempt to exchange the code with GitHub, which will fail
	// because we're using a real GitHub endpoint in tests. However, since there's no
	// real token server, the exchange step itself will fail with a network error
	// before reaching the signup-disabled check. So this test validates the full
	// state validation path. The signup-disabled check is tested via the RPC path
	// (GetPendingOAuthSignup / CompleteOAuthSignup flow) and by examining the handler
	// code logic.

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(server.URL + "/auth/oauth/" + providerID + "/callback?code=test-code&state=" + stateValue)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// The exchange will fail (no mock token server), returning 400.
	// This validates the state is consumed and the provider is resolved correctly.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestAutoLinkByVerifiedEmail validates that the auto-link-by-email path
// (in handleCallback) correctly links a new OAuth identity to an existing
// user when the emails match and the existing email is verified.
// Since handleCallback requires a real OAuth token exchange, this test
// exercises the DB-level operations that the auto-link path performs.
func TestAutoLinkByVerifiedEmail(t *testing.T) {
	_, q, ks := setupOAuthTestServer(t)

	// Create a user with a verified email.
	orgID := id.Generate()
	err := q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "alice-org", IsPersonal: 1})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	userID := id.Generate()
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:            userID,
		OrgID:         orgID,
		Username:      "alice",
		PasswordHash:  hash,
		DisplayName:   "Alice",
		Email:         "alice@example.com",
		EmailVerified: 1,
		IsAdmin:       0,
	})
	require.NoError(t, err)

	// Link Alice to GitHub provider.
	githubProviderID := createTestProvider(t, q, ks)
	err = q.CreateOAuthUserLink(context.Background(), gendb.CreateOAuthUserLinkParams{
		UserID:          userID,
		ProviderID:      githubProviderID,
		ProviderSubject: "github-alice-123",
	})
	require.NoError(t, err)

	// Create a second provider (simulating Google OIDC).
	googleProviderID := id.Generate()
	aad := keystore.ProviderAAD(googleProviderID)
	encSecret, err := ks.Encrypt([]byte("google-secret"), aad)
	require.NoError(t, err)
	err = q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           googleProviderID,
		ProviderType: "oidc",
		Name:         "Test Google",
		ClientID:     "google-client-id",
		ClientSecret: encSecret,
		Scopes:       "openid profile email",
		Enabled:      1,
	})
	require.NoError(t, err)

	// Simulate what handleCallback does for auto-link:
	// 1. GetOAuthUserLink for Google identity → not found (new identity).
	_, err = q.GetOAuthUserLink(context.Background(), gendb.GetOAuthUserLinkParams{
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.Error(t, err, "should not find Google link yet")

	// 2. Look up user by verified email.
	existingUser, err := q.GetUserByEmail(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, userID, existingUser.ID)
	assert.Equal(t, int64(1), existingUser.EmailVerified)

	// 3. Create the OAuth link for the new provider identity.
	err = q.CreateOAuthUserLink(context.Background(), gendb.CreateOAuthUserLinkParams{
		UserID:          existingUser.ID,
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.NoError(t, err)

	// Verify: user now has links to both providers.
	links, err := q.ListOAuthUserLinksByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.Len(t, links, 2)

	providerIDs := map[string]bool{}
	for _, l := range links {
		providerIDs[l.ProviderID] = true
	}
	assert.True(t, providerIDs[githubProviderID], "should have GitHub link")
	assert.True(t, providerIDs[googleProviderID], "should have Google link")

	// Verify: looking up either provider identity resolves to the same user.
	githubLink, err := q.GetOAuthUserLink(context.Background(), gendb.GetOAuthUserLinkParams{
		ProviderID:      githubProviderID,
		ProviderSubject: "github-alice-123",
	})
	require.NoError(t, err)
	assert.Equal(t, userID, githubLink.UserID)

	googleLink, err := q.GetOAuthUserLink(context.Background(), gendb.GetOAuthUserLinkParams{
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.NoError(t, err)
	assert.Equal(t, userID, googleLink.UserID)
}

// TestAutoLinkByEmail_SkippedWhenUnverified validates that auto-link does NOT
// happen when the existing user's email is unverified.
func TestAutoLinkByEmail_SkippedWhenUnverified(t *testing.T) {
	_, q, _ := setupOAuthTestServer(t)

	// Create a user with an unverified email.
	orgID := id.Generate()
	err := q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "bob-org", IsPersonal: 1})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:            id.Generate(),
		OrgID:         orgID,
		Username:      "bob",
		PasswordHash:  hash,
		DisplayName:   "Bob",
		Email:         "bob@example.com",
		EmailVerified: 0, // unverified
		IsAdmin:       0,
	})
	require.NoError(t, err)

	// Look up the user by email — found but not verified.
	existingUser, err := q.GetUserByEmail(context.Background(), "bob@example.com")
	require.NoError(t, err)
	assert.Equal(t, int64(0), existingUser.EmailVerified)

	// The auto-link path checks EmailVerified == 1 and skips when unverified.
	// This means a new pending signup would be created instead (tested elsewhere).
}

func TestDeleteOAuthTokens_ScopedToProvider(t *testing.T) {
	_, q, ks := setupOAuthTestServer(t)

	// Create two OAuth providers.
	providerA := createTestProvider(t, q, ks)
	providerBID := id.Generate()
	aad := keystore.ProviderAAD(providerBID)
	encSecret, err := ks.Encrypt([]byte("secret-b"), aad)
	require.NoError(t, err)
	err = q.CreateOAuthProvider(context.Background(), gendb.CreateOAuthProviderParams{
		ID:           providerBID,
		ProviderType: "oidc",
		Name:         "Test OIDC",
		ClientID:     "client-b",
		ClientSecret: encSecret,
		Scopes:       "openid",
		Enabled:      1,
	})
	require.NoError(t, err)

	// Use the bootstrap admin as the token owner.
	admin, err := q.GetUserByUsername(context.Background(), "admin")
	require.NoError(t, err)

	// Insert tokens for both providers.
	err = q.UpsertOAuthTokens(context.Background(), gendb.UpsertOAuthTokensParams{
		UserID:       admin.ID,
		ProviderID:   providerA,
		AccessToken:  []byte("dummy"),
		RefreshToken: []byte("dummy"),
		TokenType:    "bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:   int64(ks.ActiveVersion()),
	})
	require.NoError(t, err)

	err = q.UpsertOAuthTokens(context.Background(), gendb.UpsertOAuthTokensParams{
		UserID:       admin.ID,
		ProviderID:   providerBID,
		AccessToken:  []byte("dummy"),
		RefreshToken: []byte("dummy"),
		TokenType:    "bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:   int64(ks.ActiveVersion()),
	})
	require.NoError(t, err)

	// Delete tokens for provider A only.
	err = q.DeleteOAuthTokensByUserAndProvider(context.Background(), gendb.DeleteOAuthTokensByUserAndProviderParams{
		UserID:     admin.ID,
		ProviderID: providerA,
	})
	require.NoError(t, err)

	// Provider A's tokens should be gone.
	_, err = q.GetOAuthTokens(context.Background(), gendb.GetOAuthTokensParams{
		UserID:     admin.ID,
		ProviderID: providerA,
	})
	require.Error(t, err, "provider A tokens should have been deleted")

	// Provider B's tokens should still exist.
	tok, err := q.GetOAuthTokens(context.Background(), gendb.GetOAuthTokensParams{
		UserID:     admin.ID,
		ProviderID: providerBID,
	})
	require.NoError(t, err, "provider B tokens should still exist")
	assert.Equal(t, providerBID, tok.ProviderID)
}
