package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// authTestSeed configures the settings manager before the server fields
// its first request; nil leaves every setting at its default.
type authTestSeed func(t *testing.T, set *settings.Manager)

func setupAuthTestServerBase(t *testing.T, cfg *config.Config, seed authTestSeed, closers ...auth.CredentialChannelCloser) (leapmuxv1connect.AuthServiceClient, store.Store, *settings.Manager) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	if seed != nil {
		seed(t, set)
	}

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	var closer auth.CredentialChannelCloser
	if len(closers) > 0 {
		closer = closers[0]
	}
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(sc, closer, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, st, set
}

func TestLifecycleAwareServicesRequireEffects(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		service.NewAuthService(service.AuthServiceDeps{Renderer: mail.Renderer{}})
	})
	assert.Panics(t, func() {
		service.NewUserService(nil, nil, nil, nil, nil, mail.Renderer{}, nil)
	})
	assert.Panics(t, func() {
		service.NewWorkerDelegationHandler(nil, nil, nil)
	})
}

// setupEmptyAuthTestServer creates a test auth server with an empty database
// (no users). Used for testing the initial setup flow.
func setupEmptyAuthTestServer(t *testing.T, cfg *config.Config, seed authTestSeed) (leapmuxv1connect.AuthServiceClient, store.Store, *settings.Manager) {
	return setupAuthTestServerBase(t, cfg, seed)
}

func setupAuthTestServer(t *testing.T, cfg *config.Config, seed authTestSeed) (leapmuxv1connect.AuthServiceClient, store.Store, *settings.Manager) {
	client, st, set := setupAuthTestServerBase(t, cfg, seed)
	hubtestutil.CreateTestAdmin(t, st)
	return client, st, set
}

type sessionCloseRecorder struct {
	sessionIDs []string
}

func (r *sessionCloseRecorder) CloseChannelsBySession(sessionID string) int {
	r.sessionIDs = append(r.sessionIDs, sessionID)
	return 0
}

func (*sessionCloseRecorder) CloseChannelsByBearer(auth.BearerRef) int        { return 0 }
func (*sessionCloseRecorder) CloseChannelsByUserRevocation(string, int64) int { return 0 }
func (*sessionCloseRecorder) RestampSessionGeneration(string, int64)          {}

func TestAuthService_LoginSuccess(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)

	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())

	// Verify Set-Cookie header is present with session cookie.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.NotEmpty(t, setCookie)
	assert.Contains(t, setCookie, auth.CookieName+"=")
	assert.Contains(t, setCookie, "HttpOnly")
}

func TestAuthService_LoginInvalidPassword(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "wrongpwd",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_GetCurrentUser(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	// Login first.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)

	// Get current user with token.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionFromCookie(t, loginResp.Header().Get("Set-Cookie")))

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
}

// TestAuthService_GetCurrentUser_ReportsTheProviderArmForAPasswordAccount is
// half of what lets the step-up screen offer exactly the arms the hub accepts.
//
// A provider may prove a step-up only for an account that holds no password
// and no passkey. This account holds a password, so BOTH links report false --
// including the OIDC one, which a client filtering on the provider's protocol
// capability would have offered. That capability is gone from the wire for
// exactly this reason: it never answered the question the form asks.
func TestAuthService_GetCurrentUser_ReportsTheProviderArmForAPasswordAccount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, st, _ := setupAuthTestServer(t, testConfig(), nil)

	loginResp, err := client.Login(ctx, connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin", Password: "admin123",
	}))
	require.NoError(t, err)
	userID := loginResp.Msg.GetUser().GetId()

	for _, p := range []struct{ id, providerType string }{
		{"gh", huboauth.ProviderTypeGitHub},
		{"okta", huboauth.ProviderTypeOIDC},
	} {
		require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: p.id, ProviderType: p.providerType, Name: p.id, ClientID: "cid",
			ClientSecret: []byte("secret"), Enabled: true,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(userID), ProviderID: p.id, ProviderSubject: "sub-" + p.id,
		}))
	}

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionFromCookie(t, loginResp.Header().Get("Set-Cookie")))
	resp, err := client.GetCurrentUser(ctx, req)
	require.NoError(t, err)

	assert.False(t, resp.Msg.GetUser().GetMayElevateThroughAProvider(),
		"a password account elevates with the password, never with a provider")
	ids := []string{}
	for _, p := range resp.Msg.GetUser().GetOauthProviders() {
		ids = append(ids, p.GetId())
		assert.Truef(t, p.GetEnabled(), "%s is an enabled provider", p.GetId())
	}
	assert.ElementsMatch(t, []string{"gh", "okta"}, ids,
		"both links are reported whatever the account rule answers")
}

// TestAuthService_GetCurrentUser_ReportsTheProviderArm is the other side of
// the same field: the hub DECIDES whether a provider may elevate, and the
// step-up form filters on that answer alone.
//
// The rule reads the ACCOUNT and is not derivable from
// requests_reauthentication. This account holds no password and no passkey,
// so the provider IS its sign-in credential and the arm is available --
// GitHub included, which proves no re-authentication at all. The form spelled
// the rule out in TypeScript before this field existed, which made it a
// second source of truth for an authorization decision the OAuth legs
// enforce.
func TestAuthService_GetCurrentUser_ReportsTheProviderArm(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, st, _ := setupAuthTestServer(t, testConfig(), nil)

	// A passwordless account, built directly: Login needs a password, and a
	// password is exactly the fact this case must not have.
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "oauthonly", PasswordHash: password.PlaceholderHash,
		DisplayName: "OAuth Only", PasswordSet: false,
	}))
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
		ID: sessionID, UserID: userid.MustNew(userID),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))

	for _, p := range []struct{ id, providerType string }{
		{"gh", huboauth.ProviderTypeGitHub},
		{"okta", huboauth.ProviderTypeOIDC},
	} {
		require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: p.id, ProviderType: p.providerType, Name: p.id, ClientID: "cid",
			ClientSecret: []byte("secret"), Enabled: true,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(userID), ProviderID: p.id, ProviderSubject: "sub-" + p.id,
		}))
	}

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionID)
	resp, err := client.GetCurrentUser(ctx, req)
	require.NoError(t, err)

	assert.True(t, resp.Msg.GetUser().GetMayElevateThroughAProvider(),
		"an account with no other factor elevates through any linked provider")
	ids := []string{}
	for _, p := range resp.Msg.GetUser().GetOauthProviders() {
		ids = append(ids, p.GetId())
	}
	assert.ElementsMatch(t, []string{"gh", "okta"}, ids)
}

// A DISABLED provider's link is still reported, with enabled=false.
//
// The link survives an administrator disabling the provider, and this list is
// the only feed for the Linked Accounts section -- so filtering the row out
// left the owner holding a login method they could neither use nor remove,
// while UnlinkOAuthProvider's own last-login-method guard still counted it.
// The verification screen filters on the flag instead.
func TestAuthService_GetCurrentUser_ReportsADisabledLinkAsDisabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, st, _ := setupAuthTestServer(t, testConfig(), nil)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "haslink", PasswordHash: password.PlaceholderHash,
		DisplayName: "Has Link", PasswordSet: true,
	}))
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
		ID: sessionID, UserID: userid.MustNew(userID),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID: "gh", ProviderType: huboauth.ProviderTypeGitHub, Name: "GitHub", ClientID: "cid",
		ClientSecret: []byte("secret"), Enabled: false,
	}))
	require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(userID), ProviderID: "gh", ProviderSubject: "sub-gh",
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionID)
	resp, err := client.GetCurrentUser(ctx, req)
	require.NoError(t, err)

	links := resp.Msg.GetUser().GetOauthProviders()
	require.Len(t, links, 1, "the link must stay visible so the owner can detach it")
	assert.Equal(t, "gh", links[0].GetId())
	assert.False(t, links[0].GetEnabled(), "the screen that offers step-up arms filters on this")
}

// GetCurrentUser is the only response the /verify-email page's own
// bootstrap reads, so it has to carry the resend cooldown: without it a
// hard reload restarted the countdown at zero, the button re-enabled, and
// the click got a ResourceExhausted with no timestamp to restart from.
//
// It must also REPORT and never SEND, because every page load calls it.
func TestAuthService_GetCurrentUser_CarriesTheVerificationCooldown(t *testing.T) {
	t.Parallel()

	// An admin already exists, so this is a PUBLIC sign-up rather than the
	// setup-mode first user (which is always an admin, and admins are
	// exempt from verification).
	client, st, _ := setupAuthTestServer(t, testConfig(), func(t *testing.T, set *settings.Manager) {
		t.Helper()
		seedSMTP(t, set)
		enableSignup(t, set)
	})

	signUp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "pending", Password: "strongpass1", DisplayName: "Pending",
		Email: "pending@example.test",
	}))
	require.NoError(t, err)
	require.True(t, signUp.Msg.GetEmailVerification().GetVerificationRequired())
	seeded := signUp.Msg.GetEmailVerification().GetNextResendAvailableAt()
	require.NotNil(t, seeded, "sign-up hands out the first cooldown")

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionFromCookie(t, signUp.Header().Get("Set-Cookie")))
	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	status := resp.Msg.GetEmailVerification()
	require.NotNil(t, status, "the bootstrap response must carry the status")
	assert.True(t, status.GetVerificationRequired())
	require.NotNil(t, status.GetNextResendAvailableAt(),
		"a reload must resume the countdown the sign-up handed out")
	assert.WithinDuration(t, seeded.AsTime(), status.GetNextResendAvailableAt().AsTime(), time.Second)

	// Reporting, not sending: the pending code is untouched by the call.
	before, err := st.Users().GetByUsername(context.Background(), "pending")
	require.NoError(t, err)
	_, err = client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)
	after, err := st.Users().GetByUsername(context.Background(), "pending")
	require.NoError(t, err)
	assert.Equal(t, before.PendingEmailToken, after.PendingEmailToken,
		"a page load must not mint or re-send a verification code")
}

func TestAuthService_GetCurrentUser_NoToken(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_Login_EmptyUsername(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "",
		Password: "admin123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_Login_EmptyPassword(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_SignUp_WhenEnabled(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "newpass123",
		DisplayName: "New User",
		Email:       "new@example.com",
	}))
	require.NoError(t, err)
	// Token assertion replaced by Set-Cookie check above
	assert.Equal(t, "newuser", resp.Msg.GetUser().GetUsername())
	assert.Equal(t, "New User", resp.Msg.GetUser().GetDisplayName())

	// Verify Set-Cookie header is present.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")
	assert.Contains(t, setCookie, "HttpOnly")
}

// Every path that mints a session must stamp the configured duration, not only
// login. Each one calls CreateSession separately, so one left on the default is
// a silent split: those users get the built-in lifetime and nothing reports it.
// The verification-required branch mints its own session (the user needs one to
// call VerifyEmail), which is why it is exercised apart from the plain sign-up.
func TestAuthService_SessionMintPathsUseConfiguredDuration(t *testing.T) {
	t.Parallel()

	const configured = 90 * time.Minute
	signUp := func(t *testing.T, seed authTestSeed, username string) (*connect.Response[leapmuxv1.SignUpResponse], store.Store, string) {
		t.Helper()
		client, st, _ := setupAuthTestServer(t, testConfig(), func(t *testing.T, s *settings.Manager) {
			if seed != nil {
				seed(t, s)
			}
			setSessionDuration(t, s, configured)
		})
		resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
			Username:    username,
			Password:    "newpass123",
			DisplayName: "New User",
			Email:       username + "@example.com",
		}))
		require.NoError(t, err)
		return resp, st, sessionFromCookie(t, resp.Header().Get("Set-Cookie"))
	}

	assertExpiry := func(t *testing.T, st store.Store, sessionID string, before time.Time) {
		t.Helper()
		sess, err := st.Sessions().GetByID(context.Background(), sessionID, time.Now().UTC())
		require.NoError(t, err)
		hubtestutil.AssertSessionLifetime(t, before, configured, sess.ExpiresAt)
	}

	t.Run("sign-up", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		resp, st, sessionID := signUp(t, enableSignup, "newuser")
		require.False(t, resp.Msg.GetEmailVerification().GetVerificationRequired(), "control: this is the plain branch")
		assertExpiry(t, st, sessionID, before)
	})

	t.Run("sign-up with verification required", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		resp, st, sessionID := signUp(t, func(t *testing.T, set *settings.Manager) {
			enableSignup(t, set)
			enableEmailVerification(t, set)
		}, "unverified")
		// Without this the subtest would silently repeat the branch above, and
		// the second CreateSession call site would go unexercised.
		require.True(t, resp.Msg.GetEmailVerification().GetVerificationRequired(), "this must take the verification branch")
		assertExpiry(t, st, sessionID, before)
	})

	t.Run("login", func(t *testing.T) {
		t.Parallel()
		client, st, set := setupAuthTestServer(t, testConfig(), nil)
		setSessionDuration(t, set, configured)

		before := time.Now()
		resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
			Username: hubtestutil.TestAdminUsername,
			Password: hubtestutil.TestAdminPassword,
		}))
		require.NoError(t, err)
		assertExpiry(t, st, sessionFromCookie(t, resp.Header().Get("Set-Cookie")), before)
	})
}

func TestAuthService_SignUp_WhenDisabled(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	// Signup is disabled by default.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "newuser",
		Password: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAuthService_SignUp_DuplicateUsername(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	// First signup should succeed.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "dupuser",
		Password: "pass1234",
	}))
	require.NoError(t, err)

	// Second signup with the same username should fail.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "dupuser",
		Password: "pass4567",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// TestAuthService_ChangePassword_NeedsElevatedSession pins the gate on the
// cookie path end to end: a fresh sign-in is NOT enough to change the
// password. The session must prove a factor first, and the refusal is
// FailedPrecondition -- the frontend reads Unauthenticated as "signed out"
// and would discard the session the user is about to elevate.
func TestAuthService_ChangePassword_NeedsElevatedSession(t *testing.T) {
	t.Parallel()

	client, st, set := setupAuthTestServer(t, testConfig(), nil)

	// Login to get a token.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)
	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	// Set up a UserService client using the same queries and auth interceptor.
	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	userClient := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	req := connect.NewRequest(&leapmuxv1.ChangePasswordRequest{NewPassword: "newpass123"})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = userClient.ChangePassword(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// The password did not change: the old one still authenticates.
	_, _, _, err = auth.Login(context.Background(), st, "admin", "admin123", auth.DefaultSessionDuration)
	require.NoError(t, err, "a refused change must leave the password alone")
}

func TestSignUp_DuplicateEmail_Rejected(t *testing.T) {
	t.Parallel()

	client, st, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	// Create a user with that email directly in the DB.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           id.Generate(),
		Username:     "emailuser",
		PasswordHash: hash,
		DisplayName:  "Email User",
		Email:        "taken@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	// Try to sign up with the same email.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "newpass123",
		DisplayName: "New User",
		Email:       "taken@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestPromotePendingEmail_ClearsCompetingPendingEmails(t *testing.T) {
	t.Parallel()

	_, st, _ := setupAuthTestServer(t, testConfig(), enableSignup)
	ctx := context.Background()

	// Create two users, both with pending_email = "shared@example.com".
	for _, username := range []string{"user-a", "user-b"} {
		hash, err := password.Hash("testpass")
		require.NoError(t, err)
		userID := id.Generate()
		err = st.Users().Create(ctx, store.CreateUserParams{
			ID:           userID,
			Username:     username,
			PasswordHash: hash,
			DisplayName:  username,
			Email:        "",
			PasswordSet:  true,
			IsAdmin:      false,
		})
		require.NoError(t, err)
		_, err = st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			PendingEmail:          "shared@example.com",
			PendingEmailToken:     verifycode.Generate(),
			PendingEmailExpiresAt: ptrTime(time.Now().Add(24 * time.Hour).UTC()),
			ID:                    userID,
			CooldownCutoff:        store.UnconditionalMintCutoff(),
		})
		require.NoError(t, err)
	}

	// Verify both have pending_email set.
	userA, err := st.Users().GetByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.PendingEmail)
	userB, err := st.Users().GetByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userB.PendingEmail)

	// User A promotes — this should also clear user B's pending_email.
	err = st.Users().PromotePendingEmail(ctx, userA.ID)
	require.NoError(t, err)
	err = st.Users().ClearCompetingPendingEmails(ctx, store.ClearCompetingPendingEmailsParams{
		PendingEmail: "shared@example.com",
		ExcludeID:    userA.ID,
	})
	require.NoError(t, err)

	// User A now has verified email.
	userA, err = st.Users().GetByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.Email)
	assert.True(t, userA.EmailVerified)
	assert.Empty(t, userA.PendingEmail)

	// User B's pending_email should be cleared.
	userB, err = st.Users().GetByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Empty(t, userB.PendingEmail)
	assert.Empty(t, userB.Email)
}

func TestSignUp_DirectEmail_ClearsCompetingPendingEmails(t *testing.T) {
	t.Parallel()

	client, st, _ := setupAuthTestServer(t, testConfig(), enableSignup)
	ctx := context.Background()

	// User A sets pending_email = "race@example.com" (unverified).
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	userAID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID:           userAID,
		Username:     "racer",
		PasswordHash: hash,
		DisplayName:  "Racer",
		Email:        "",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)
	_, err = st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		PendingEmail:          "race@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: ptrTime(time.Now().Add(24 * time.Hour).UTC()),
		ID:                    userAID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	// User B signs up with email = "race@example.com" directly (verification off).
	_, err = client.SignUp(ctx, connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "winner",
		Password:    "pass1234",
		DisplayName: "Winner",
		Email:       "race@example.com",
	}))
	require.NoError(t, err)

	// User A's pending_email should be cleared.
	userA, err := st.Users().GetByUsername(ctx, "racer")
	require.NoError(t, err)
	assert.Empty(t, userA.PendingEmail, "competing pending_email should be cleared when another user claims the email directly")
}

func TestSignUp_EmptyEmail_AllowedMultiple(t *testing.T) {
	t.Parallel()

	client, _, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	// First signup with empty email should succeed.
	resp1, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "emptyemail1",
		Password:    "pass1234",
		DisplayName: "User 1",
		Email:       "",
	}))
	require.NoError(t, err)
	assert.Equal(t, "emptyemail1", resp1.Msg.GetUser().GetUsername())

	// Second signup with empty email should also succeed.
	resp2, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "emptyemail2",
		Password:    "pass4567",
		DisplayName: "User 2",
		Email:       "",
	}))
	require.NoError(t, err)
	assert.Equal(t, "emptyemail2", resp2.Msg.GetUser().GetUsername())
}

// setupVerificationGatingTestServer creates a test server with both
// UserService and AuthService, with the verification gate armed as
// specified (both the services and the interceptor read it from the same
// settings).
func setupVerificationGatingTestServer(t *testing.T, emailVerificationRequired bool) (
	leapmuxv1connect.UserServiceClient,
	leapmuxv1connect.AuthServiceClient,
	store.Store,
) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	set := servicetest.NewSettingsManager(t, st, nil)
	if emailVerificationRequired {
		enableEmailVerification(t, set)
	}

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, Policy: servicetest.AuthPolicy(set)})
	opts := connect.WithInterceptors(interceptor)

	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	userClient := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)
	authClient := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return userClient, authClient, st
}

func TestVerificationGating_UnverifiedBlocked(t *testing.T) {
	t.Parallel()

	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// Create a user with email_verified=0 directly via DB.
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "unverified",
		PasswordHash: hash,
		DisplayName:  "Unverified User",
		Email:        "unverified@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0 in the DB.

	token, _, _, err := auth.Login(context.Background(), st, "unverified", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// Try UpdateProfile — should be blocked by verification gating.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "unverified",
		DisplayName: "Updated",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestVerificationGating_AdminExempt(t *testing.T) {
	t.Parallel()

	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// The bootstrap admin has email_verified=0 by default (no email set).
	// Verify the admin can still call protected RPCs.
	adminToken, _, _, err := auth.Login(context.Background(), st, "admin", "admin123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// Admin should be able to call UpdateProfile even with email_verified=0.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "admin",
		DisplayName: "Admin Updated",
	}, adminToken))
	require.NoError(t, err)
}

func TestVerificationGating_ConfigOff_NotBlocked(t *testing.T) {
	t.Parallel()

	userClient, _, st := setupVerificationGatingTestServer(t, false)

	// Create an unverified user.
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "nogate",
		PasswordHash: hash,
		DisplayName:  "No Gate User",
		Email:        "nogate@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0 — but gating is OFF.

	token, _, _, err := auth.Login(context.Background(), st, "nogate", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// Unverified user should be able to call UpdateProfile when gating is off.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "nogate",
		DisplayName: "Updated",
	}, token))
	require.NoError(t, err)
}

// --- SignUp with email verification (requires SMTP) ---

func TestSignUp_VerificationRequired_EmailInPendingColumn(t *testing.T) {
	t.Parallel()

	client, st, _ := setupAuthTestServer(t, testConfig(), func(t *testing.T, set *settings.Manager) {
		enableSignup(t, set)
		enableEmailVerification(t, set)
	})

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "verifyuser",
		Password:    "password123",
		DisplayName: "Verify User",
		Email:       "verify@example.com",
	}))
	require.NoError(t, err)

	// The response should indicate verification was required and that
	// the (stub) email send succeeded.
	assert.True(t, resp.Msg.GetEmailVerification().GetVerificationRequired())
	assert.True(t, resp.Msg.GetEmailVerification().GetVerificationEmailSent())
	assert.Equal(t, "verifyuser", resp.Msg.GetUser().GetUsername())

	// Email stays in the pending column until the user submits the code.
	// No more stub auto-verify: a real (stub-logged) email was dispatched
	// and verification waits on UserService.VerifyEmail.
	user, err := st.Users().GetByUsername(context.Background(), "verifyuser")
	require.NoError(t, err)
	assert.Empty(t, user.Email)
	assert.False(t, user.EmailVerified)
	assert.Equal(t, "verify@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)

	// Signup must issue a session even when verification is required —
	// otherwise the user can't authenticate to the verify endpoint.
	assert.Contains(t, resp.Header().Get("Set-Cookie"), auth.CookieName+"=")
}

type failingMailSender struct{ err error }

func (f failingMailSender) Send(context.Context, mail.Message) error { return f.err }

func TestSignUp_FailClosedWhenVerificationEmailFails(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	enableSignup(t, set)
	enableEmailVerification(t, set)

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store:     st,
		Config:    testConfig(),
		Settings:  set,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, nil, nil),
		Mail:      failingMailSender{err: errors.New("smtp down")},
		Renderer:  mail.Renderer{},
	})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	hubtestutil.CreateTestAdmin(t, st)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "failclosed",
		Password:    "password123",
		DisplayName: "Fail Closed",
		Email:       "failclosed@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	_, err = st.Users().GetByUsername(context.Background(), "failclosed")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestVerificationGating_LogoutAllowed(t *testing.T) {
	t.Parallel()

	_, authClient, st := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "logoutgating",
		PasswordHash: hash,
		DisplayName:  "Logout Gating",
		Email:        "logoutgating@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), st, "logoutgating", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// Logout should be allowed for unverified users.
	logoutResp, err := authClient.Logout(context.Background(), authedReq(&leapmuxv1.LogoutRequest{}, token))
	require.NoError(t, err)

	// Verify the cookie is cleared.
	logoutCookie := logoutResp.Header().Get("Set-Cookie")
	assert.Contains(t, logoutCookie, "Max-Age=0")
}

func TestVerificationGating_RequestEmailChangeAllowed(t *testing.T) {
	t.Parallel()

	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "emailchangegate",
		PasswordHash: hash,
		DisplayName:  "Email Change Gating",
		Email:        "emailchangegate@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), st, "emailchangegate", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// RequestEmailChange should be allowed for unverified users
	// (it should not return PermissionDenied from the gating interceptor).
	_, err = userClient.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "newemail@example.com",
	}, token))
	// The RPC may succeed or fail for business logic reasons, but should NOT
	// be blocked by the verification gating interceptor (no PermissionDenied).
	if err != nil {
		assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err),
			"RequestEmailChange should not be blocked by verification gating")
	}
}

func TestAuthService_Logout(t *testing.T) {
	t.Parallel()

	closer := &sessionCloseRecorder{}
	client, st, _ := setupAuthTestServerBase(t, testConfig(), nil, closer)
	hubtestutil.CreateTestAdmin(t, st)

	// Login.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)

	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	// Logout.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	logoutResp, err := client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// Verify logout response clears the cookie.
	logoutCookie := logoutResp.Header().Get("Set-Cookie")
	assert.Contains(t, logoutCookie, auth.CookieName+"=")
	assert.Contains(t, logoutCookie, "Max-Age=0")
	assert.Equal(t, []string{token}, closer.sessionIDs)

	// Token should be invalidated.
	getUserReq := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	getUserReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), getUserReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

type sessionOverrideStore struct {
	store.Store
	sessions store.SessionStore
}

func (s sessionOverrideStore) Sessions() store.SessionStore {
	return s.sessions
}

type sessionDeleteFailStore struct {
	store.SessionStore
}

func (s sessionDeleteFailStore) Delete(context.Context, string) (int64, error) {
	return 0, errors.New("forced session delete failure")
}

func TestAuthService_LogoutDeleteFailureReturnsInternal(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	wrapped := sessionOverrideStore{
		Store: st,
		sessions: sessionDeleteFailStore{
			SessionStore: st.Sessions(),
		},
	}

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: wrapped})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(wrapped, testConfig(), servicetest.NewSettingsManager(t, wrapped, nil), auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)
	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.Logout(context.Background(), logoutReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Contains(t, err.(*connect.Error).Meta().Get("Set-Cookie"), "Max-Age=0",
		"failed server-side revocation must still clear the browser's HttpOnly cookie")

	getUserReq := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	getUserReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), getUserReq)
	require.NoError(t, err, "failed logout must not report success while leaving deletion unapplied")
}

// --- Setup mode tests ---

func TestSetupSignUp_CreatesAdminWithVerifiedEmail(t *testing.T) {
	t.Parallel()

	// Signup disabled, but no users exist — setup mode should kick in.
	client, st, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "admin@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, "myadmin", user.GetUsername())
	assert.True(t, user.GetIsAdmin())
	assert.Equal(t, "admin@example.com", user.GetEmail())
	// NOT verified: nobody confirmed this address, and the operator typed it
	// into a form. The administrator can still sign in, because the login
	// gate takes its own exemption -- see auth.EmailVerificationSatisfied.
	assert.False(t, user.GetEmailVerified())

	// Session cookie should be set.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify in DB.
	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	assert.Equal(t, "admin@example.com", dbUser.Email)
	assert.False(t, dbUser.EmailVerified)
	assert.True(t, auth.EmailVerificationSatisfied(true, dbUser.IsAdmin, dbUser.EmailVerified),
		"an unconfirmed address must not lock the hub's first administrator out")
}

func TestSetupSignUp_EmptyEmail(t *testing.T) {
	t.Parallel()

	client, st, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.True(t, user.GetIsAdmin())
	assert.Empty(t, user.GetEmail())
	// With no address at all there is nothing anybody could have confirmed,
	// so the column says false. The setup-mode admin still signs in.
	assert.False(t, user.GetEmailVerified())

	// Verify in DB.
	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	assert.False(t, dbUser.EmailVerified)
	assert.True(t, auth.EmailVerificationSatisfied(true, dbUser.IsAdmin, dbUser.EmailVerified))
}

func TestSetupSignUp_GetSystemInfoReturnsSetupRequired(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	// Before setup: setup_required should be true.
	infoResp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, infoResp.Msg.GetSetupRequired())

	// Perform setup.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
	}))
	require.NoError(t, err)

	// After setup: setup_required should be false.
	infoResp, err = client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, infoResp.Msg.GetSetupRequired())
}

func TestSetupSignUp_RejectedWhenUsersExist(t *testing.T) {
	t.Parallel()

	// Signup disabled, admin user already exists.
	client, _, _ := setupAuthTestServer(t, testConfig(), nil)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "strongpass1",
		DisplayName: "New User",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_RejectedInSoloMode(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.SoloMode = true

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	// No solo user is seeded — the test asserts that AuthService rejects setup
	// signup in solo mode at the service layer, independent of the interceptor.
	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_NormalSignupStillCreatesNonAdmin(t *testing.T) {
	t.Parallel()

	// Signup enabled + users already exist = normal non-admin signup.
	client, st, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "regularuser",
		Password:    "strongpass1",
		DisplayName: "Regular User",
		Email:       "regular@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.False(t, user.GetIsAdmin())

	dbUser, err := st.Users().GetByUsername(context.Background(), "regularuser")
	require.NoError(t, err)
	assert.False(t, dbUser.IsAdmin)
}

func TestSetupSignUp_WithSignupEnabled(t *testing.T) {
	t.Parallel()

	// Signup enabled + no users = setup mode should still create admin.
	client, st, _ := setupEmptyAuthTestServer(t, testConfig(), enableSignup)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "admin@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.True(t, user.GetIsAdmin(), "first user should be admin even when signup is enabled")
	// Unconfirmed, like every other setup-mode address; the login gate is
	// what keeps the administrator in.
	assert.False(t, user.GetEmailVerified())

	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
}

func TestSetupSignUp_RaceCondition(t *testing.T) {
	t.Parallel()

	// Two setup signups — only the first should succeed.
	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	// First signup should succeed.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin1",
		Password:    "strongpass1",
		DisplayName: "Admin 1",
	}))
	require.NoError(t, err)

	// Second signup should fail (users now exist, signup disabled).
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin2",
		Password:    "strongpass2",
		DisplayName: "Admin 2",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_ValidatesInputs(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	tests := []struct {
		name     string
		username string
		password string
		email    string
	}{
		{"empty username", "", "strongpass1", ""},
		{"weak password", "myadmin", "short", ""},
		{"invalid email", "myadmin", "strongpass1", "not-an-email"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username: tt.username,
				Password: tt.password,
				Email:    tt.email,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestGetSystemInfo_DevModeReportsSetupRequired(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.DevMode = true

	client, _, _ := setupEmptyAuthTestServer(t, cfg, nil)

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetSetupRequired(), "dev mode with empty DB should require setup")
}

func TestGetSystemInfo_WorkerHubURL(t *testing.T) {
	t.Parallel()

	t.Run("PublicURL wins over Listen", func(t *testing.T) {
		cfg := testConfig()
		cfg.Listen = ":4327"

		client, _, _ := setupEmptyAuthTestServer(t, cfg, func(t *testing.T, set *settings.Manager) {
			require.NoError(t, settings.KeyPublicURL.Set(context.Background(), set, "https://hub.example.com"))
		})

		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example.com", resp.Msg.GetWorkerHubUrl())
	})

	t.Run("empty Listen and no PublicURL falls back to LocalListen", func(t *testing.T) {
		// NoTCP/desktop scenario: empty Listen, no PublicURL → local socket URL.
		cfg := testConfig()
		cfg.Listen = ""
		cfg.LocalListen = "unix:" + filepath.Join(t.TempDir(), "hub.sock")

		client, _, _ := setupEmptyAuthTestServer(t, cfg, nil)

		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.Equal(t, cfg.LocalListen, resp.Msg.GetWorkerHubUrl())
	})

	t.Run("TCP enabled and no PublicURL leaves WorkerHubUrl empty", func(t *testing.T) {
		// Frontend then falls back to window.location.origin.
		cfg := testConfig()
		cfg.Listen = ":4327"

		client, _, _ := setupEmptyAuthTestServer(t, cfg, nil)

		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.Empty(t, resp.Msg.GetWorkerHubUrl())
	})
}

// TestGetSystemInfo_EmailEnabled covers the email_enabled flag the
// frontend reads to decide whether to render the "Send email" button on
// the worker registration dialog. We mirror it directly off SMTPValue's
// own Enabled() so admins control it through the same SMTP block they
// configure for verification emails.
//
// Enabled() needs the host AND the from address, because the settings
// write path accepts a half-staged SMTP block on purpose: the dialog
// writes one field per row, so the host lands before the from address. A
// flag mirrored off the host alone would offer to send email through a
// relay with no envelope sender.
func TestGetSystemInfo_EmailEnabled(t *testing.T) {
	t.Parallel()

	t.Run("false when the SMTP block is empty", func(t *testing.T) {
		client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)
		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.GetEmailEnabled())
	})

	t.Run("false when only the host is staged", func(t *testing.T) {
		client, _, _ := setupEmptyAuthTestServer(t, testConfig(), func(t *testing.T, set *settings.Manager) {
			t.Helper()
			require.NoError(t, set.Update(context.Background(), settings.KeySMTP,
				json.RawMessage(`{"host":"smtp.example.test"}`)))
		})
		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.GetEmailEnabled(), "a relay with no from address cannot send")
	})

	t.Run("true when the host and the from address are both set", func(t *testing.T) {
		client, _, _ := setupEmptyAuthTestServer(t, testConfig(), seedSMTP)
		resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
		require.NoError(t, err)
		assert.True(t, resp.Msg.GetEmailEnabled())
	})
}

func TestSignUp_RejectsSoloAlways(t *testing.T) {
	t.Parallel()

	t.Run("setup mode", func(t *testing.T) {
		client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

		for _, input := range []string{"solo", "SOLO", "  solo  "} {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username:    input,
				Password:    "strongpass1",
				DisplayName: "First",
			}))
			require.Errorf(t, err, "setup mode must reject %q", input)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}
	})

	t.Run("public signup", func(t *testing.T) {
		client, _, _ := setupAuthTestServer(t, testConfig(), enableSignup)

		for _, input := range []string{"solo", "SOLO"} {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username:    input,
				Password:    "strongpass1",
				DisplayName: "Someone",
			}))
			require.Errorf(t, err, "public signup must reject %q", input)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}
	})
}

func TestSignUp_AllowsAdminInSetupMode(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin",
		Password:    "strongpass1",
		DisplayName: "First Admin",
	}))
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

// The other half of the setup exemption, and the half that matters for
// safety: `admin` becomes claimable in setup mode, `solo` never does. A user
// by that name in a non-solo database is auto-authenticated for every request
// the day the same data-dir is opened in solo mode.
func TestSignUp_RejectsSoloInSetupMode(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    usernames.Solo,
		Password:    "strongpass1",
		DisplayName: "Not The Solo User",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved username")
}

func TestSignUp_RejectsAdminInPublicSignup(t *testing.T) {
	t.Parallel()

	// A seeded user exists, so isSetupMode=false and the public reservation applies.
	client, _, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin",
		Password:    "strongpass1",
		DisplayName: "Squatter",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSignUp_RequiresEmailWhenSMTPConfigured(t *testing.T) {
	t.Parallel()

	client, _, set := setupAuthTestServer(t, testConfig(), enableSignup)
	enableEmailVerification(t, set)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "noemail",
		Password:    "password123",
		DisplayName: "No Email",
		Email:       "",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "email is required")
}

func TestSignUp_SMTPOff_StoresUnverifiedEmail(t *testing.T) {
	t.Parallel()

	client, st, _ := setupAuthTestServer(t, testConfig(), enableSignup)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "smtpoff",
		Password:    "password123",
		DisplayName: "SMTP Off",
		Email:       "smtpoff@example.com",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetEmailVerification().GetVerificationRequired())
	assert.NotEmpty(t, resp.Header().Get("Set-Cookie"))

	user, err := st.Users().GetByUsername(context.Background(), "smtpoff")
	require.NoError(t, err)
	assert.Equal(t, "smtpoff@example.com", user.Email)
	assert.False(t, user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
}

func TestLogin_ReturnsVerificationFlagsWhenVerificationRequired(t *testing.T) {
	t.Parallel()

	client, st, set := setupAuthTestServer(t, testConfig(), enableSignup)
	enableEmailVerification(t, set)

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "gateduser",
		Password:    "password123",
		DisplayName: "Gated",
		Email:       "gated@example.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByUsername(context.Background(), "gateduser")
	require.NoError(t, err)
	assert.False(t, user.EmailVerified)

	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "gateduser",
		Password: "password123",
	}))
	require.NoError(t, err)
	assert.True(t, loginResp.Msg.GetEmailVerification().GetVerificationRequired())
	// Signup already issued pending_email; login surfaces cooldown without resending.
	assert.False(t, loginResp.Msg.GetEmailVerification().GetVerificationEmailSent())
	require.NotNil(t, loginResp.Msg.GetEmailVerification().GetNextResendAvailableAt())
}

func TestFinishPasskeySignUp_FailClosedWhenVerificationEmailFails(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, func(t *testing.T, set *settings.Manager) {
		enableSignup(t, set)
		enableEmailVerification(t, set)
	}, failingMailSender{err: errors.New("smtp down")})

	begin := beginPasskeySignUp(t, env.client, "pkfailclosed", "pkfailclosed@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	_, err = finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	_, err = env.store.Users().GetByUsername(context.Background(), "pkfailclosed")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestFinishPasskeyLogin_ReturnsVerificationFlagsWhenVerificationRequired(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, func(t *testing.T, set *settings.Manager) {
		enableSignup(t, set)
		enableEmailVerification(t, set)
	}, nil)

	begin := beginPasskeySignUp(t, env.client, "pkgated", "pkgated@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)
	signUpResp, err := finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.NoError(t, err)
	assert.True(t, signUpResp.Msg.GetEmailVerification().GetVerificationRequired())

	user, err := env.store.Users().GetByUsername(context.Background(), "pkgated")
	require.NoError(t, err)
	assert.False(t, user.EmailVerified)

	loginBegin := beginPasskeyLogin(t, env.client, "pkgated")
	assertionJSON, err := ceremony.assertionResponse(loginBegin.GetOptionsJson())
	require.NoError(t, err)
	loginResp, err := finishPasskeyLogin(t, env.client, loginBegin.GetSessionId(), assertionJSON)
	require.NoError(t, err)
	assert.True(t, loginResp.Msg.GetEmailVerification().GetVerificationRequired())
	assert.False(t, loginResp.Msg.GetEmailVerification().GetVerificationEmailSent())
	require.NotNil(t, loginResp.Msg.GetEmailVerification().GetNextResendAvailableAt())
}

func TestBeginPasskeySignUp_RequiresEmailWhenSMTPConfigured(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	enableSignup(t, set)
	enableEmailVerification(t, set)
	hubtestutil.CreateTestAdmin(t, st)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store:     st,
		Config:    testConfig(),
		Settings:  set,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, nil, nil),
		Mail:      mail.NewStubSender(),
		Renderer:  mail.Renderer{},
		Keystore:  ks,
	})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	req := connect.NewRequest(&leapmuxv1.BeginPasskeySignUpRequest{
		Username:    "nopkemail",
		DisplayName: "No Email",
		Email:       "",
	})
	req.Header().Set("Origin", "https://localhost")
	_, err = client.BeginPasskeySignUp(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "email is required")
}

// The first administrator registers with a passkey, exactly as they can with
// a password. Signup stays DISABLED here (the default), which is the whole
// point of the setup exemption: the setting is an administrator's decision,
// and setup mode is the state in which no administrator exists to have made
// it.
func TestPasskeySignUp_SetupModeCreatesFirstAdmin(t *testing.T) {
	t.Parallel()

	env := setupEmptyPasskeyAuthTestServer(t, nil, nil)

	infoResp, err := env.client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	require.True(t, infoResp.Msg.GetSetupRequired())

	// `admin` is the conventional first-administrator name, and it is
	// squat-protected everywhere else. Claiming it here proves the setup
	// exemption reaches the passkey path's username rule too.
	begin := beginPasskeySignUp(t, env.client, usernames.Admin, "first@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	signUpResp, err := finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.NoError(t, err)
	assert.True(t, signUpResp.Msg.GetUser().GetIsAdmin())
	assert.Equal(t, int32(1), signUpResp.Msg.GetUser().GetPasskeyCount())
	assert.False(t, signUpResp.Msg.GetEmailVerification().GetVerificationRequired())

	dbUser, err := env.store.Users().GetByUsername(context.Background(), usernames.Admin)
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	// No password was chosen, so nothing may claim one was: the account signs
	// in with its passkey until the owner adds a password from Preferences.
	assert.False(t, dbUser.PasswordSet)

	// Setup is over, so /setup is withdrawn.
	infoResp, err = env.client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, infoResp.Msg.GetSetupRequired())

	// And the credential works: the account can sign back in with it.
	loginBegin := beginPasskeyLogin(t, env.client, usernames.Admin)
	assertionJSON, err := ceremony.assertionResponse(loginBegin.GetOptionsJson())
	require.NoError(t, err)
	loginResp, err := finishPasskeyLogin(t, env.client, loginBegin.GetSessionId(), assertionJSON)
	require.NoError(t, err)
	assert.Equal(t, usernames.Admin, loginResp.Msg.GetUser().GetUsername())
}

// An administrator never waits behind a pending verification row, so their
// address lands in the email column unverified -- the same outcome
// signUpSetupMode produces for a password sign-up, and the reason
// FinishPasskeySignUp reads the STORED code rather than its own pre-call
// intent.
func TestFinishPasskeySignUp_SetupModePromotesEmailPastVerification(t *testing.T) {
	t.Parallel()

	env := setupEmptyPasskeyAuthTestServer(t, enableEmailVerification, nil)

	begin := beginPasskeySignUp(t, env.client, "pksetupmail", "pksetupmail@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	signUpResp, err := finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.NoError(t, err)
	assert.False(t, signUpResp.Msg.GetEmailVerification().GetVerificationRequired())

	dbUser, err := env.store.Users().GetByUsername(context.Background(), "pksetupmail")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	assert.Equal(t, "pksetupmail@example.com", dbUser.Email)
	assert.Empty(t, dbUser.PendingEmail)
	// Nobody confirmed the address, and the column records only what somebody
	// confirmed.
	assert.False(t, dbUser.EmailVerified)
}

// Setup mode is re-read at Finish rather than carried over from Begin, and
// this is the window that makes the difference: a second operator wins the
// race to become the first administrator while the browser is still in the
// ceremony. The ceremony that started in setup mode must land under PUBLIC
// rules -- `admin` squat-protected, the signup setting binding -- or the race
// loser walks past both.
// Signup ENABLED, so the reserved-name rule is the one under test rather than
// the signup setting: `admin` was claimable when the ceremony began and is
// squat-protected by the time it ends.
func TestFinishPasskeySignUp_SetupModeEndingMidCeremonyReservesAdmin(t *testing.T) {
	t.Parallel()

	env := setupEmptyPasskeyAuthTestServer(t, enableSignup, nil)

	begin := beginPasskeySignUp(t, env.client, usernames.Admin, "race@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	// The race: somebody else finishes setup first, under a different name so
	// the refusal below is the reserved rule rather than a name collision.
	hubtestutil.CreateTestUser(t, env.store, "winner", "password123")

	_, err = finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved username")

	_, err = env.store.Users().GetByUsername(context.Background(), usernames.Admin)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// And the setting itself, which is the ordinary deployment: signup stays off,
// so the ceremony that Begin admitted under the setup exemption is refused at
// Finish the moment an administrator exists to have made that decision.
func TestFinishPasskeySignUp_SetupModeEndingMidCeremonyHonorsSignupDisabled(t *testing.T) {
	t.Parallel()

	env := setupEmptyPasskeyAuthTestServer(t, nil, nil)

	begin := beginPasskeySignUp(t, env.client, "racer", "racer@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	hubtestutil.CreateTestUser(t, env.store, "winner", "password123")

	_, err = finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "sign-up is disabled")

	_, err = env.store.Users().GetByUsername(context.Background(), "racer")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// `solo` carries a hazard that belongs to the DATABASE rather than to the
// flow that wrote the row: opening a non-solo data-dir in solo mode
// auto-authenticates every request as that user. Setup mode exempts `admin`
// and nothing else.
func TestBeginPasskeySignUp_SetupModeStillRejectsSoloUsername(t *testing.T) {
	t.Parallel()

	env := setupEmptyPasskeyAuthTestServer(t, nil, nil)

	_, err := env.client.BeginPasskeySignUp(context.Background(), beginPasskeySignUpRequest(usernames.Solo, ""))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved username")
}

// The polarity of the exemption, which a setup-mode-only test cannot pin: a
// hub that already has an account is public signup, and there `admin` stays
// squat-protected.
func TestBeginPasskeySignUp_PublicModeRejectsAdminUsername(t *testing.T) {
	t.Parallel()

	// A non-admin fixture, so the refusal below is the reserved-name rule
	// rather than the availability check on the seeded admin row.
	env := setupEmptyPasskeyAuthTestServer(t, enableSignup, nil)
	hubtestutil.CreateTestUser(t, env.store, "someone", "password123")

	_, err := env.client.BeginPasskeySignUp(context.Background(), beginPasskeySignUpRequest(usernames.Admin, ""))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved username")
}

// Signup disabled is an administrator's decision, so it binds once one
// exists. Without this the setup exemption would read as "passkey sign-up is
// always open".
func TestBeginPasskeySignUp_PublicModeRefusesWhenSignupDisabled(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, nil, nil)

	_, err := env.client.BeginPasskeySignUp(context.Background(), beginPasskeySignUpRequest("latecomer", ""))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "sign-up is disabled")
}

func TestBeginPasskeyLogin_UnknownAndNoPasskeysShareError(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, enableSignup, nil)

	unknownReq := connect.NewRequest(&leapmuxv1.BeginPasskeyLoginRequest{Username: "nobody"})
	unknownReq.Header().Set("Origin", passkeyTestOrigin)
	_, errUnknown := env.client.BeginPasskeyLogin(context.Background(), unknownReq)
	require.Error(t, errUnknown)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(errUnknown))

	_, err := env.client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "nopasskeys",
		Password:    "password123",
		DisplayName: "No Keys",
		Email:       "nopasskeys@example.com",
	}))
	require.NoError(t, err)

	noneReq := connect.NewRequest(&leapmuxv1.BeginPasskeyLoginRequest{Username: "nopasskeys"})
	noneReq.Header().Set("Origin", passkeyTestOrigin)
	_, errNone := env.client.BeginPasskeyLogin(context.Background(), noneReq)
	require.Error(t, errNone)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(errNone))
	assert.Equal(t, errUnknown.Error(), errNone.Error())
}

func TestFinishPasskeyLogin_FailedAssertionBurnsCeremony(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, enableSignup, nil)

	begin := beginPasskeySignUp(t, env.client, "pkburn", "pkburn@example.com")
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)
	_, err = finishPasskeySignUp(t, env.client, begin.GetSessionId(), credentialJSON)
	require.NoError(t, err)

	loginBegin := beginPasskeyLogin(t, env.client, "pkburn")
	_, err = finishPasskeyLogin(t, env.client, loginBegin.GetSessionId(), "not-a-credential")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	assertionJSON, err := ceremony.assertionResponse(loginBegin.GetOptionsJson())
	require.NoError(t, err)
	_, err = finishPasskeyLogin(t, env.client, loginBegin.GetSessionId(), assertionJSON)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "failed Finish must consume the ceremony")
}

// TestAuthService_GetCurrentUser_MarksADisabledLinkedProvider pins BOTH
// halves of the disabled-provider rule, which one flag cannot carry.
//
// The link survives an administrator disabling the provider, and nothing
// behind it works: both OAuth legs answer 403 "provider disabled" from
// loadEnabledProvider. So it must not be offered as a step-up arm -- for an
// OAuth-only account that is the one arm it has, and the dead end is total.
//
// It must still be REPORTED, though, and omitting it was the defect that
// hiding it introduced: this list is the only feed for the Linked Accounts
// section, so an omitted link left the owner unable to detach it while
// UnlinkOAuthProvider's own last-login-method guard still counted the row.
// The flag says which, and the verification screen filters on it.
func TestAuthService_GetCurrentUser_MarksADisabledLinkedProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, st, _ := setupAuthTestServer(t, testConfig(), nil)

	loginResp, err := client.Login(ctx, connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin", Password: "admin123",
	}))
	require.NoError(t, err)
	userID := loginResp.Msg.GetUser().GetId()

	for _, p := range []struct {
		id      string
		enabled bool
	}{
		{"live", true},
		{"dead", false},
	} {
		require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: p.id, ProviderType: huboauth.ProviderTypeOIDC, Name: p.id, ClientID: "cid",
			ClientSecret: []byte("secret"), Enabled: p.enabled,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(userID), ProviderID: p.id, ProviderSubject: "sub-" + p.id,
		}))
	}

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionFromCookie(t, loginResp.Header().Get("Set-Cookie")))
	resp, err := client.GetCurrentUser(ctx, req)
	require.NoError(t, err)

	enabledByID := map[string]bool{}
	for _, p := range resp.Msg.GetUser().GetOauthProviders() {
		enabledByID[p.GetId()] = p.GetEnabled()
	}
	assert.Equal(t, map[string]bool{"live": true, "dead": false}, enabledByID,
		"both links stay detachable, and the flag is what stops the disabled one being offered")
}
