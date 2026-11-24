package auth_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
	"google.golang.org/protobuf/proto"
)

// adminGateFixture mounts the REAL AdminSettingsService (plus AuthService
// for logins and AdminUserService for UpdateUser) behind the real auth
// interceptor, with an admin and a non-admin user seeded.
type adminGateFixture struct {
	authClient      leapmuxv1connect.AuthServiceClient
	adminClient     leapmuxv1connect.AdminSettingsServiceClient
	adminUserClient leapmuxv1connect.AdminUserServiceClient
	st              store.Store
	tv              *auth.TokenValidator
	adminUserID     string
	plainUserID     string
}

func setupAdminGateServer(t *testing.T) *adminGateFixture {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	plainUserID := hubtestutil.CreateTestUser(t, st, "plainuser", "plainpass123")

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st, TokenValidator: tv})
	connectServer := connect.NewServer(interceptor)

	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)

	adminSvc := service.NewAdminSettingsService(servicetest.NewSettingsManager(t, st), &config.Config{}, st)
	leapmuxv1connect.RegisterAdminSettingsServiceHandler(connectServer, adminSvc)

	adminUserSvc := service.NewAdminUserService(service.AdminUserServiceDeps{
		Store:   st,
		Notices: service.NewCredentialNotices(nil, mail.Renderer{}),
	})
	leapmuxv1connect.RegisterAdminUserServiceHandler(connectServer, adminUserSvc)
	connecthttp.Mount(mux, connectServer)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &adminGateFixture{
		authClient:      leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		adminClient:     leapmuxv1connect.NewAdminSettingsServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		adminUserClient: leapmuxv1connect.NewAdminUserServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
		st:              st,
		tv:              tv,
		adminUserID: func() string {
			u, err := st.Users().GetByUsername(context.Background(), "admin")
			require.NoError(t, err)
			return u.ID
		}(),
		plainUserID: plainUserID,
	}
}

func (f *adminGateFixture) adminCookie(t *testing.T) string {
	ctx, info := connect.NewClientContext(context.Background())
	_, err := f.authClient.Login(ctx, &leapmuxv1.LoginRequest{
		Username: "admin", Password: "admin123",
	})
	require.NoError(t, err)
	cookie := hubtestutil.SessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
	// The RPC sign-in writes a FreshAuthenticationWindow. Drop it so the
	// fixture yields the unelevated session the gate tests need.
	owner, ok := userid.New(f.adminUserID)
	require.True(t, ok)
	_, dropErr := f.st.Sessions().DropElevation(context.Background(), store.DropSessionElevationParams{
		SessionID: hubtestutil.SessionIDFromCookie(t, cookie),
		UserID:    owner,
	})
	require.NoError(t, dropErr)
	return cookie
}

func (f *adminGateFixture) plainUserCookie(t *testing.T) string {
	ctx, info := connect.NewClientContext(context.Background())
	_, err := f.authClient.Login(ctx, &leapmuxv1.LoginRequest{
		Username: "plainuser", Password: "plainpass123",
	})
	require.NoError(t, err)
	cookie := hubtestutil.SessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
	owner, ok := userid.New(f.plainUserID)
	require.True(t, ok)
	_, dropErr := f.st.Sessions().DropElevation(context.Background(), store.DropSessionElevationParams{
		SessionID: hubtestutil.SessionIDFromCookie(t, cookie),
		UserID:    owner,
	})
	require.NoError(t, dropErr)
	return cookie
}

func TestAdminGate_AdminCookieAllowed(t *testing.T) {
	f := setupAdminGateServer(t)

	// Without an elevation window the interceptor refuses with the marker.
	refusedCookie := f.adminCookie(t)
	refusedCtx, refusedInfo := connect.NewClientContext(context.Background())
	refusedInfo.RequestHeader().Set("Cookie", auth.CookieName+"="+refusedCookie)
	_, err := f.adminClient.ListSettings(refusedCtx, &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "1", refusedInfo.ResponseHeader().Get(service.ElevationRequiredHeader),
		"the refusal must be the one a step-up prompt can clear")

	// With a current window the admin cookie passes. A FRESH login, because
	// the interceptor caches the UserInfo it validated: stamping the row
	// after a refused request would be invisible until that entry expired.
	cookie := f.adminCookie(t)
	hubtestutil.ElevateSession(t, f.st, hubtestutil.SessionIDFromCookie(t, cookie), f.adminUserID)
	_, err = f.adminClient.ListSettings(authedReq(context.Background(), &leapmuxv1.ListSettingsRequest{}, cookie))
	require.NoError(t, err)
}

// TestAdminGate_ExpiredWindowRefused pins that a lapsed pair is not a window:
// the columns hold a deadline that already passed, and the refusal must be
// the one an absent window gets.
func TestAdminGate_ExpiredWindowRefused(t *testing.T) {
	f := setupAdminGateServer(t)
	cookie := f.adminCookie(t)

	now := time.Now().UTC()
	n, err := f.st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          hubtestutil.SessionIDFromCookie(t, cookie),
		UserID:             userid.MustNew(f.adminUserID),
		ElevationProvenAt:  now.Add(-time.Minute),
		ElevationExpiresAt: now.Add(-time.Second),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	callCtx, info := connect.NewClientContext(context.Background())
	info.RequestHeader().Set("Cookie", auth.CookieName+"="+cookie)
	_, err = f.adminClient.ListSettings(callCtx, &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "1", info.ResponseHeader().Get(service.ElevationRequiredHeader),
		"an expired window can be recovered through verification")
}

// TestAdminGate_AdminReadLeavesTheWindowUnchanged pins that an admitted read
// does not slide the step-up window. The pair is stamped as it stood one
// minute ago, so a slide that extends the deadline by the elapsed time moves
// it a whole minute and cannot hide in the store's millisecond precision.
func TestAdminGate_AdminReadLeavesTheWindowUnchanged(t *testing.T) {
	f := setupAdminGateServer(t)
	cookie := f.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie)

	provenAt := time.Now().UTC().Add(-time.Minute)
	n, err := f.st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             userid.MustNew(f.adminUserID),
		ElevationProvenAt:  provenAt,
		ElevationExpiresAt: provenAt.Add(auth.ElevationWindow),
	}, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	ctx := context.Background()
	before, err := f.st.Sessions().GetByID(ctx, sessionID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, before.ElevationExpiresAt)

	_, err = f.adminClient.ListSettings(authedReq(ctx, &leapmuxv1.ListSettingsRequest{}, cookie))
	require.NoError(t, err)

	after, err := f.st.Sessions().GetByID(ctx, sessionID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, after.ElevationExpiresAt)
	assert.True(t, before.ElevationExpiresAt.Equal(*after.ElevationExpiresAt),
		"an admin read must not slide the window: before %s, after %s", before.ElevationExpiresAt, after.ElevationExpiresAt)
}

func TestAdminGate_NonAdminCookieDenied(t *testing.T) {
	f := setupAdminGateServer(t)
	callCtx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Cookie", auth.CookieName+"="+f.plainUserCookie(t))
	_, err := f.adminClient.ListSettings(callCtx, &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "administrator privileges are required")
	assert.False(t, info.ResponseHeader().Has(service.ElevationRequiredHeader))
}

func TestAdminGate_Unauthenticated(t *testing.T) {
	f := setupAdminGateServer(t)
	_, err := f.adminClient.ListSettings(context.Background(), &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestAdminGate_AdminBearerNeedsAnAdminScope is the point of the scope model:
// being an administrator is not enough for an app credential. A routine
// authorization mints a credential that can do everything its owner can do
// EXCEPT administer the hub, so the credential file it leaves on disk for
// months is not a hub-administration credential.
//
// It also pins the GRANULARITY the four admin scopes bought, which the single
// admin_scope boolean could not express: a credential granted admin:users is
// still refused the settings surface. That is the gap recorded in-tree at
// elevation_service.go -- "a credential minted for user administration can also
// rewrite the hub's security settings" -- and this is the test that says it is
// closed.
func TestAdminGate_AdminBearerNeedsAnAdminScope(t *testing.T) {
	f := setupAdminGateServer(t)
	owner, ownerOK := userid.New(f.adminUserID)
	require.True(t, ownerOK)

	// Every minted token is elevated, so the scope assertions below are not
	// masked by the interceptor's elevation check. The scope refusals happen
	// at the scope rung, which runs before the elevation rung.
	mint := func(grant string) string {
		tokenID := id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
			Issuance:         store.APITokenIssuanceAuthorizationCode,
			ID:               tokenID,
			UserID:           owner,
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "test",
			GrantedScopes:    grant,
			SecretHash:       f.tv.HashSecret(secret),
		}))
		hubtestutil.ElevateAPIToken(t, f.st, tokenID, f.adminUserID)
		return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	}
	listSettings := func(bearer string) error {
		_, err := f.adminClient.ListSettings(bearerReq(context.Background(), &leapmuxv1.ListSettingsRequest{}, "Bearer "+bearer))
		return err
	}

	// The DEFAULT grant: everything except administration.
	err := listSettings(mint(authscope.NonAdminGrant().String()))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "admin:read", "the refusal must name the permission it wanted")

	// admin:users is administration, and it still does not reach the settings
	// surface: that separation is what four scopes exist for.
	err = listSettings(mint("admin:users"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// admin:read is what ListSettings asks for.
	require.NoError(t, listSettings(mint("admin:read")))
}

// TestAdminGate_AppWithoutElevationAllowedRefused pins the loadBearer rule
// that zeroes a window when the app's elevation_allowed is off. The row still
// holds a live pair; the read must refuse it all the same.
func TestAdminGate_AppWithoutElevationAllowedRefused(t *testing.T) {
	f := setupAdminGateServer(t)
	owner, ownerOK := userid.New(f.adminUserID)
	require.True(t, ownerOK)

	clientID := id.Generate()
	_, err := f.st.OAuthClients().Create(context.Background(), store.CreateOAuthClientParams{
		ClientID:           clientID,
		ClientName:         "elevating-app",
		RedirectURIs:       "https://app.example.com/callback",
		Scopes:             "admin:read",
		GrantTypes:         "authorization_code",
		ElevationAllowed:   true,
		RegistrationSource: store.OAuthClientSourceAdmin,
	})
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		ID:               tokenID,
		UserID:           owner,
		ClientID:         clientID,
		InstallationName: "test",
		GrantedScopes:    "admin:read",
		SecretHash:       f.tv.HashSecret(secret),
	}))
	// The store admits an elevation write only while the app is allowed, so
	// the window is stamped first and the owner then withdraws the
	// permission. That is the row state the read must zero.
	hubtestutil.ElevateAPIToken(t, f.st, tokenID, f.adminUserID)
	n, err := f.st.OAuthClients().SetElevationAllowed(context.Background(), store.SetOAuthClientElevationAllowedParams{
		ClientID: clientID, ElevationAllowed: false,
		CallerUserID: owner, CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	callCtx, info := connect.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err = f.adminClient.ListSettings(callCtx, &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Empty(t, info.ResponseHeader().Get(service.ElevationRequiredHeader),
		"the app owner must enable elevation before a prompt can help")
	assert.Contains(t, err.Error(), "application owner disabled identity verification")

	n, err = f.st.OAuthClients().SetElevationAllowed(context.Background(), store.SetOAuthClientElevationAllowedParams{
		ClientID: clientID, ElevationAllowed: true,
		CallerUserID: owner, CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	retryTokenID := id.Generate()
	retrySecret := auth.MintAccessSecret()
	require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		ID:               retryTokenID,
		UserID:           owner,
		ClientID:         clientID,
		InstallationName: "retry",
		GrantedScopes:    "admin:read",
		SecretHash:       f.tv.HashSecret(retrySecret),
	}))

	retryCtx, retryInfo := connect.NewClientContext(context.Background())
	retryInfo.RequestHeader().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, retryTokenID, retrySecret))
	_, err = f.adminClient.ListSettings(retryCtx, &leapmuxv1.ListSettingsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "1", retryInfo.ResponseHeader().Get(service.ElevationRequiredHeader),
		"re-enabling the app restores the ordinary verification remedy")
}

// TestAdminGate_AppCredentialWithoutWindowUpdateUserRefused pins the marker on
// a NON-session-only admin procedure: an app credential without a window is
// refused WITH the prompt signal, unlike the session-only refusals that can
// offer no ceremony that would help.
func TestAdminGate_AppCredentialWithoutWindowUpdateUserRefused(t *testing.T) {
	f := setupAdminGateServer(t)
	owner, ownerOK := userid.New(f.adminUserID)
	require.True(t, ownerOK)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		ID:               tokenID,
		UserID:           owner,
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    "admin:users",
		SecretHash:       f.tv.HashSecret(secret),
	}))

	callCtx, info := connect.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err := f.adminUserClient.UpdateUser(callCtx, &leapmuxv1.UpdateUserRequest{
		User:        service.SelectUserByID(f.adminUserID),
		DisplayName: proto.String("Renamed"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "1", info.ResponseHeader().Get(service.ElevationRequiredHeader),
		"the refusal must be the one a step-up prompt can clear")

	emailCtx, emailInfo := connect.NewClientContext(context.Background())
	emailInfo.RequestHeader().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err = f.adminUserClient.UpdateUser(emailCtx, &leapmuxv1.UpdateUserRequest{
		User:  service.SelectUserByID(f.adminUserID),
		Email: proto.String("admin@example.com"),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Empty(t, emailInfo.ResponseHeader().Get(service.ElevationRequiredHeader),
		"a browser session is required, so a bearer step-up cannot clear this refusal")
}

// TestAdminGate_NonAdminAdminScopedBearerStillDenied pins the COMPOSITION
// rule: a scope subtracts from the account's own authority and never adds to
// it, so an admin scope on a credential whose owner is not an administrator
// grants nothing.
//
// A hand-edited row is the case that matters. The consent leg refuses to write
// such a grant, but the enforcement must not depend on the mint having refused
// it -- so this seeds the row directly.
func TestAdminGate_NonAdminAdminScopedBearerStillDenied(t *testing.T) {
	f := setupAdminGateServer(t)
	owner, ownerOK := userid.New(f.plainUserID)
	require.True(t, ownerOK)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		ID:               tokenID,
		UserID:           owner,
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		// Every admin scope, on an account that is not an administrator.
		GrantedScopes: "admin:read admin:users admin:settings admin:workers",
		SecretHash:    f.tv.HashSecret(secret),
	}))

	_, err := f.adminClient.ListSettings(bearerReq(context.Background(), &leapmuxv1.ListSettingsRequest{},
		"Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "administrator privileges are required",
		"the SCOPE rung passes, so the ACCOUNT rung must be the one that refuses")
}

func TestAdminGate_DelegationBearerOfAdminRefused(t *testing.T) {
	// The load-bearing negative case: a delegation bearer resolves the FULL
	// user row -- including IsAdmin -- so without a refusal that runs FIRST, a
	// worker-spawned agent holding an admin's delegation token would pass the
	// admin gate outright.
	//
	// The refusal used to be an allowlist of procedures. It is now the
	// delegation CEILING, applied when loadBearer reads the row: the grant
	// itself cannot carry an admin scope, so the row below states one and the
	// bearer still holds none.
	f := setupAdminGateServer(t)
	owner, ownerOK := userid.New(f.adminUserID)
	require.True(t, ownerOK)

	// The delegation row's worker reference is foreign-keyed; seed a real one.
	workerID := id.Generate()
	require.NoError(t, f.st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:            workerID,
		AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
		RegisteredBy:  owner,
		PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}))

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, f.st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "admin:read worker:read workspace:read workspace:write",
		ID:            tokenID,
		UserID:        owner,
		WorkerID:      workerID,
		SecretHash:    f.tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))

	bearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret)
	info, err := f.tv.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.IsAdmin)
	assert.True(t, info.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_READ))
	assert.False(t, info.Scopes.Allows(leapmuxv1.Scope_SCOPE_ADMIN_READ))

	_, err = f.adminClient.ListSettings(bearerReq(context.Background(), &leapmuxv1.ListSettingsRequest{}, "Bearer "+bearer))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "admin:read",
		"the SCOPE rung must refuse first, so the caller never learns whether its user is an administrator")
}

func TestAdminGate_SoloModeAutoAdmin(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st, SoloUser: soloUser})
	connectServer := connect.NewServer(interceptor)
	adminSvc := service.NewAdminSettingsService(servicetest.NewSettingsManager(t, st), &config.Config{SoloMode: true}, st)
	leapmuxv1connect.RegisterAdminSettingsServiceHandler(connectServer, adminSvc)
	connecthttp.Mount(mux, connectServer)

	server := httptest.NewUnstartedServer(mux)
	server.Config.BaseContext = func(net.Listener) context.Context {
		return peer.WithLocalIPC(context.Background())
	}
	server.Start()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAdminSettingsServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
	// No cookie, no bearer: the solo short-circuit authenticates the
	// request, and the bootstrapped solo user is an admin.
	resp, err := client.ListSettings(context.Background(), &leapmuxv1.ListSettingsRequest{})
	require.NoError(t, err)

	// Solo mode also omits the HiddenInSolo descriptors: sign-up, SMTP,
	// captcha, and the per-user rate limits. The exact set is asserted in
	// admin_settings_service_test.go; here the point is only that the
	// solo listing drops them rather than flagging them.
	for _, d := range resp.GetDescriptors() {
		assert.False(t, d.GetHiddenInSolo(), "solo listing must omit hidden-in-solo keys, not flag them: %s", d.GetKey())
		if d.GetKey() == "captcha.enabled" || d.GetKey() == "rate_limit.elevation" {
			t.Fatalf("solo listing must omit %s entirely", d.GetKey())
		}
	}
	assert.NotEmpty(t, resp.GetDescriptors())
}
