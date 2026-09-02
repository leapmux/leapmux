package auth_test

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
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// adminGateFixture mounts the REAL AdminSettingsService (plus AuthService
// for logins) behind the real auth interceptor, with an admin and a
// non-admin user seeded.
type adminGateFixture struct {
	authClient  leapmuxv1connect.AuthServiceClient
	adminClient leapmuxv1connect.AdminSettingsServiceClient
	st          store.Store
	tv          *auth.TokenValidator
	adminUserID string
	plainUserID string
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
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	interceptors := connect.WithInterceptors(interceptor)

	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(authPath, authHandler)

	adminSvc := service.NewAdminSettingsService(servicetest.NewSettingsManager(t, st, nil), &config.Config{}, st)
	adminPath, adminHandler := leapmuxv1connect.NewAdminSettingsServiceHandler(adminSvc, interceptors)
	mux.Handle(adminPath, adminHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &adminGateFixture{
		authClient:  leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		adminClient: leapmuxv1connect.NewAdminSettingsServiceClient(server.Client(), server.URL),
		st:          st,
		tv:          tv,
		adminUserID: func() string {
			u, err := st.Users().GetByUsername(context.Background(), "admin")
			require.NoError(t, err)
			return u.ID
		}(),
		plainUserID: plainUserID,
	}
}

func (f *adminGateFixture) adminCookie(t *testing.T) string {
	resp, err := f.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin", Password: "admin123",
	}))
	require.NoError(t, err)
	return hubtestutil.SessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func (f *adminGateFixture) plainUserCookie(t *testing.T) string {
	resp, err := f.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "plainuser", Password: "plainpass123",
	}))
	require.NoError(t, err)
	return hubtestutil.SessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func TestAdminGate_AdminCookieAllowed(t *testing.T) {
	f := setupAdminGateServer(t)
	req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+f.adminCookie(t))
	_, err := f.adminClient.ListSettings(context.Background(), req)
	require.NoError(t, err)
}

func TestAdminGate_NonAdminCookieDenied(t *testing.T) {
	f := setupAdminGateServer(t)
	req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+f.plainUserCookie(t))
	_, err := f.adminClient.ListSettings(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "administrator privileges are required")
}

func TestAdminGate_Unauthenticated(t *testing.T) {
	f := setupAdminGateServer(t)
	_, err := f.adminClient.ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
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

	mint := func(grant string) string {
		tokenID := id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, f.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           owner,
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "test",
			GrantedScopes:    grant,
			SecretHash:       f.tv.HashSecret(secret),
		}))
		return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	}
	listSettings := func(bearer string) error {
		req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
		req.Header().Set("Authorization", "Bearer "+bearer)
		_, err := f.adminClient.ListSettings(context.Background(), req)
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
		ID:               tokenID,
		UserID:           owner,
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		// Every admin scope, on an account that is not an administrator.
		GrantedScopes: "admin:read admin:users admin:settings admin:workers",
		SecretHash:    f.tv.HashSecret(secret),
	}))

	req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err := f.adminClient.ListSettings(context.Background(), req)
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
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    owner,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, f.st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        owner,
		WorkerID:      workerID,
		SecretHash:    f.tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))

	req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	_, callErr := f.adminClient.ListSettings(context.Background(), req)
	require.Error(t, callErr)
	err := callErr
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
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, SoloUser: soloUser})
	interceptors := connect.WithInterceptors(interceptor)
	adminSvc := service.NewAdminSettingsService(servicetest.NewSettingsManager(t, st, nil), &config.Config{SoloMode: true}, st)
	adminPath, adminHandler := leapmuxv1connect.NewAdminSettingsServiceHandler(adminSvc, interceptors)
	mux.Handle(adminPath, adminHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAdminSettingsServiceClient(server.Client(), server.URL)
	// No cookie, no bearer: the solo short-circuit authenticates the
	// request, and the bootstrapped solo user is an admin.
	resp, err := client.ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
	require.NoError(t, err)

	// Solo mode also omits the HiddenInSolo descriptors: sign-up, SMTP,
	// captcha, and the per-user rate limits. The exact set is asserted in
	// admin_settings_service_test.go; here the point is only that the
	// solo listing drops them rather than flagging them.
	for _, d := range resp.Msg.GetDescriptors() {
		assert.False(t, d.GetHiddenInSolo(), "solo listing must omit hidden-in-solo keys, not flag them: %s", d.GetKey())
		if d.GetKey() == "captcha.enabled" || d.GetKey() == "rate_limit.elevation" {
			t.Fatalf("solo listing must omit %s entirely", d.GetKey())
		}
	}
	assert.NotEmpty(t, resp.Msg.GetDescriptors())
}
