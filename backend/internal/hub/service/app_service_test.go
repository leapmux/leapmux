package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type appEnv struct {
	client    leapmuxv1connect.AppServiceClient
	store     store.Store
	validator *auth.TokenValidator
	// closer records the in-process teardown each revoked credential owes.
	// Revoking the ROW is only half of a disconnect: a channel the credential
	// opened stays live until something closes it, and the Hub relays its
	// traffic without being able to read it.
	closer *recordingCredentialCloser
	// contexts is the validation cache the handler evicts through, and
	// interceptor is the rung that reads it. A test that changes a row the
	// cache already answered for mounts elevationProbe behind that interceptor
	// and asks it again.
	contexts    *auth.AuthContextRegistry
	interceptor connect.Interceptor
	probeClient leapmuxv1connect.WorkspaceServiceClient
	adminID     string
	admin       string
	userID      string
	user        string
}

func setupAppService(t *testing.T) *appEnv {
	t.Helper()
	ctx := context.Background()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	newUser := func(username string, isAdmin bool) (id, session string) {
		t.Helper()
		hash, err := password.Hash("password123")
		require.NoError(t, err)
		u, err := service.CreateUser(ctx, st, service.CreateUserParams{
			Username: username, PasswordHash: hash, DisplayName: username,
			PasswordSet: true, IsAdmin: isAdmin,
		})
		require.NoError(t, err)
		token, _, _, err := auth.Login(ctx, st, username, "password123", auth.DefaultSessionDuration)
		require.NoError(t, err)
		return u.ID, token
	}
	adminID, adminSession := newUser("admin", true)
	userID, userSession := newUser("plain", false)
	// Every write on this surface takes a proven factor (see
	// AppService.requireElevatedOwner), so
	// both sessions carry a live window. TestAppService_WritesNeedAProvenFactor
	// drives an UN-elevated one, which is what keeps this line from silently
	// making the gate untested.
	hubtestutil.ElevateSession(t, st, adminSession, adminID)
	hubtestutil.ElevateSession(t, st, userSession, userID)

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	closer := &recordingCredentialCloser{}
	lifecycle := auth.NewCredentialLifecycleEffects(contexts, closer, nil)
	path, handler := leapmuxv1connect.NewAppServiceHandler(
		service.NewAppService(st, servicetest.NewSettingsManager(t, st, nil), tv, lifecycle),
		connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &appEnv{
		client:      leapmuxv1connect.NewAppServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		store:       st,
		validator:   tv,
		closer:      closer,
		contexts:    contexts,
		interceptor: interceptor,
		adminID:     adminID,
		admin:       adminSession,
		userID:      userID,
		user:        userSession,
	}
}

// registerApp is the ordinary private registration these tests start from.
func (e *appEnv) registerApp(t *testing.T, session, name string, mutate func(*leapmuxv1.RegisterAppRequest)) *leapmuxv1.RegisterAppResponse {
	t.Helper()
	msg := &leapmuxv1.RegisterAppRequest{
		ClientName:   name,
		RedirectUris: []string{"https://app.example.com/callback"},
		Scopes:       []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
		Visibility:   leapmuxv1.AppVisibility_APP_VISIBILITY_PRIVATE,
		ClientType:   leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC,
	}
	if mutate != nil {
		mutate(msg)
	}
	resp, err := e.client.RegisterApp(context.Background(), authedReq(msg, session))
	require.NoError(t, err)
	return resp.Msg
}

// The visibility rule, on both of the questions it answers.
//
// A private app is invisible to every other account, and the refusal is NOT
// FOUND rather than permission-denied: telling a stranger "that exists but is
// not yours" answers the question the rule exists to refuse. An administrator's
// app is hub-wide, so it appears for everybody.
func TestAppService_PrivateAppIsInvisibleToEverybodyElse(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	mine := env.registerApp(t, env.user, "my-app", nil)

	// The owner sees it.
	listed, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{}, env.user))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetApps(), 1)
	assert.Equal(t, mine.GetApp().GetClientId(), listed.Msg.GetApps()[0].GetClientId())

	// A SECOND ordinary account does not, and cannot address it by id either.
	otherID, otherSession := env.newPlainUser(t, "second")
	require.NotEqual(t, env.userID, otherID)

	theirs, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{}, otherSession))
	require.NoError(t, err)
	assert.Empty(t, theirs.Msg.GetApps(), "another account's private app must not appear")

	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: mine.GetApp().GetClientId(), ClientName: ptr("stolen"),
	}, otherSession))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a private app must not be distinguishable from one that does not exist")

	// And the row is untouched, which is what the store-level owner filter
	// guarantees independently of the message above.
	row, err := env.store.OAuthClients().Get(ctx, mine.GetApp().GetClientId())
	require.NoError(t, err)
	assert.Equal(t, "my-app", row.ClientName)
}

// An administrator's app is hub-wide, and an ordinary user may neither edit nor
// delete it -- but the hub-wide catalogue is what every account authorizes
// against, so its existence is not a secret the way a private app is.
func TestAppService_HubWideAppNeedsAnAdministrator(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	_, err := env.client.RegisterApp(ctx, authedReq(&leapmuxv1.RegisterAppRequest{
		ClientName:   "hub-app",
		RedirectUris: []string{"https://app.example.com/callback"},
		Scopes:       []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
		Visibility:   leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE,
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	hubWide := env.registerApp(t, env.admin, "hub-app", func(m *leapmuxv1.RegisterAppRequest) {
		m.Visibility = leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE
	})
	assert.Equal(t, leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE, hubWide.GetApp().GetVisibility())

	row, err := env.store.OAuthClients().Get(ctx, hubWide.GetApp().GetClientId())
	require.NoError(t, err)
	assert.Empty(t, row.OwnerUserID, "one column carries the whole visibility rule")
	assert.Equal(t, store.OAuthClientSourceAdmin, row.RegistrationSource)

	// An ordinary account may authorize it but not edit it.
	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: hubWide.GetApp().GetClientId(), ClientName: ptr("stolen"),
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A non-administrator's app cannot even ASK for an admin permission.
//
// The ceiling is refused at REGISTRATION rather than at the consent screen. A
// refusal that arrived later would come after the app existed and its operator
// was told it was registered, and the person who then met it is the user, not
// the registrant who could act on it.
func TestAppService_NonAdministratorCannotRegisterAnAdminCeiling(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	_, err := env.client.RegisterApp(ctx, authedReq(&leapmuxv1.RegisterAppRequest{
		ClientName:   "escalate",
		RedirectUris: []string{"https://app.example.com/callback"},
		Scopes: []leapmuxv1.Scope{
			leapmuxv1.Scope_SCOPE_WORKSPACE_READ, leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		},
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "admin:users", "the refusal must name the permission that caused it")

	// The same rule on the EDIT path, which is the one a registration could
	// otherwise walk around: register something ordinary, then widen it.
	app := env.registerApp(t, env.user, "ordinary", nil)
	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId:      app.GetApp().GetClientId(),
		ReplaceScopes: true,
		Scopes:        []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS},
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	row, err := env.store.OAuthClients().Get(ctx, app.GetApp().GetClientId())
	require.NoError(t, err)
	assert.NotContains(t, row.Scopes, "admin:", "a refused edit must not widen the stored ceiling")

	// An ADMINISTRATOR may, which is what makes the rule about the account
	// rather than about the scope.
	admin := env.registerApp(t, env.admin, "admin-app", func(m *leapmuxv1.RegisterAppRequest) {
		m.Scopes = []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ADMIN_USERS}
	})
	assert.Contains(t, admin.GetApp().GetScopes(), leapmuxv1.Scope_SCOPE_ADMIN_USERS)
}

// The client secret crosses ONCE and is never readable again.
//
// A public client gets none at all, which is the honest answer for a binary a
// user holds: handing a secret to a registrant who did not ask would let them
// believe they had a confidential client while the secret sat in a distributed
// binary.
func TestAppService_ClientSecretCrossesOnce(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	public := env.registerApp(t, env.user, "public-app", nil)
	assert.Empty(t, public.GetClientSecret(), "a public client has no secret")
	assert.Equal(t, leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC, public.GetApp().GetClientType())

	confidential := env.registerApp(t, env.user, "server-app", func(m *leapmuxv1.RegisterAppRequest) {
		m.ClientType = leapmuxv1.AppClientType_APP_CLIENT_TYPE_CONFIDENTIAL
	})
	secret := confidential.GetClientSecret()
	require.NotEmpty(t, secret)
	assert.Equal(t, leapmuxv1.AppClientType_APP_CLIENT_TYPE_CONFIDENTIAL, confidential.GetApp().GetClientType())

	// The store holds the HASH, and the hash is what the token endpoint
	// compares -- so the secret is usable exactly once it has been copied.
	row, err := env.store.OAuthClients().Get(ctx, confidential.GetApp().GetClientId())
	require.NoError(t, err)
	assert.Equal(t, env.validator.HashSecret(secret), row.SecretHash)
	assert.NotContains(t, string(row.SecretHash), secret, "the plaintext must not be stored")

	// No later read returns it.
	listed, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{}, env.user))
	require.NoError(t, err)
	require.NotEmpty(t, listed.Msg.GetApps())
	for _, app := range listed.Msg.GetApps() {
		assertNoSecretField(t, app.ProtoReflect(), secret)
	}
}

// The App wire shape carries NO field that could hold a secret, checked by
// walking the descriptor rather than by naming the fields.
//
// A named list would go stale the moment somebody added a field, which is
// exactly when this matters.
func TestAppService_AppShapeHasNoSecretField(t *testing.T) {
	t.Parallel()

	fields := (&leapmuxv1.App{}).ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		assert.NotContains(t, name, "secret",
			"App must carry no secret-shaped field; the secret crosses once, in RegisterAppResponse")
		assert.NotContains(t, name, "hash", "App must carry no hash either")
	}
}

// The two rows the build seeds (store.SeedBuiltIns, at every store open) are
// constants of the BUILD, so an edit would
// leave the database disagreeing with the binary that reads it.
//
// elevation_allowed is the ONE exception, and it has to be: an operator who
// does not want `leapmux control admin` to elevate must be able to say so.
func TestAppService_BuiltInRegistrationTakesOnlyItsElevationSetting(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	for _, verb := range []struct {
		name string
		call func() error
	}{
		{"update", func() error {
			_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
				ClientId: oauthapp.ControlCLIClientID, ClientName: ptr("renamed"),
			}, env.admin))
			return err
		}},
		{"revoke", func() error {
			_, err := env.client.RevokeApp(ctx, authedReq(&leapmuxv1.RevokeAppRequest{
				ClientId: oauthapp.ControlCLIClientID,
			}, env.admin))
			return err
		}},
		{"delete", func() error {
			_, err := env.client.DeleteApp(ctx, authedReq(&leapmuxv1.DeleteAppRequest{
				ClientId: oauthapp.ControlCLIClientID,
			}, env.admin))
			return err
		}},
	} {
		t.Run(verb.name+" is refused", func(t *testing.T) {
			err := verb.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		})
	}

	// The row survived every refusal above.
	row, err := env.store.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.Equal(t, oauthapp.ControlCLIName, row.ClientName)
	assert.Nil(t, row.RevokedAt)
	require.True(t, row.ElevationAllowed, "the seeded row ships with elevation on")

	// And the exception works, in both directions.
	_, err = env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: oauthapp.ControlCLIClientID, Allowed: false,
	}, env.admin))
	require.NoError(t, err)
	row, err = env.store.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.False(t, row.ElevationAllowed, "an operator can take the step-up leg away from the CLI")

	_, err = env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: oauthapp.ControlCLIClientID, Allowed: true,
	}, env.admin))
	require.NoError(t, err)
	row, err = env.store.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.True(t, row.ElevationAllowed)
}

// Revoking retires the app AND takes every credential it holds, for every
// account, in one transaction.
//
// The count is what the surface reports, so it must be the real number rather
// than the rows the caller happens to own.
func TestAppService_RevokeCascadesToEveryAccountsCredentials(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	app := env.registerApp(t, env.admin, "shared-app", func(m *leapmuxv1.RegisterAppRequest) {
		m.Visibility = leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE
	})
	clientID := app.GetApp().GetClientId()

	// TWO accounts authorized it, which is what makes the cascade's reach the
	// point rather than an implementation detail.
	tokens := map[string]string{}
	for _, owner := range []string{env.adminID, env.userID} {
		tokenID := id.Generate()
		require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID: tokenID, UserID: userid.MustNew(owner), ClientID: clientID,
			InstallationName: "laptop", GrantedScopes: "workspace:read",
			SecretHash: []byte("hash"),
		}))
		tokens[owner] = tokenID
	}

	resp, err := env.client.RevokeApp(ctx, authedReq(&leapmuxv1.RevokeAppRequest{ClientId: clientID}, env.admin))
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.Msg.GetRevokedCredentialCount(),
		"the cascade reaches every account, not only the caller's")

	for owner, tokenID := range tokens {
		row, err := env.store.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.NotNilf(t, row.RevokedAt, "the credential held for %s must be revoked", owner)
	}
	row, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt, "the app itself is retired")

	// Every revoked credential also gets its in-process TEARDOWN, and that is
	// the half a row check cannot see. A channel the credential opened carries
	// its own encrypted session, which the Hub relays without being able to
	// read -- so a revocation that only wrote the row would leave the app
	// working over the connection it already held.
	//
	// The effects run AFTER the transaction commits, on purpose: they
	// accumulate, and the store may retry a transaction.
	closed := env.closer.closedBearers()
	closedIDs := make([]string, 0, len(closed))
	for _, ref := range closed {
		closedIDs = append(closedIDs, ref.TokenID())
	}
	for owner, tokenID := range tokens {
		assert.Containsf(t, closedIDs, tokenID,
			"the channels %s opened with that credential must be closed too", owner)
	}
}

// A delete is REFUSED while a credential lives, and the refusal says how many.
//
// The foreign key would refuse it anyway; what this adds is a message an
// operator can act on. "Delete failed" leaves them nowhere; "it holds one live
// credential" tells them to revoke instead.
func TestAppService_DeleteIsRefusedWhileACredentialLives(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	app := env.registerApp(t, env.user, "doomed", nil)
	clientID := app.GetApp().GetClientId()

	tokenID := id.Generate()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: clientID,
		InstallationName: "laptop", GrantedScopes: "workspace:read", SecretHash: []byte("hash"),
	}))

	_, err := env.client.DeleteApp(ctx, authedReq(&leapmuxv1.DeleteAppRequest{ClientId: clientID}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "1 credential(s), 1 of them live")

	_, err = env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err, "a refused delete leaves the app alone")

	// REVOKING is not enough, and the message says so. The foreign key counts
	// every row, so a revoked credential still blocks the delete -- and that is
	// right: the credential list shows it, and erasing the app it belonged to
	// would leave that history naming nothing.
	_, err = env.store.APITokens().Revoke(ctx, tokenID)
	require.NoError(t, err)
	_, err = env.client.DeleteApp(ctx, authedReq(&leapmuxv1.DeleteAppRequest{ClientId: clientID}, env.user))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 credential(s), 0 of them live",
		"the two counts must be distinguishable, because they call for different actions")

	// An app that never held one deletes cleanly, so the refusal is a real
	// precondition rather than a verb that never works.
	fresh := env.registerApp(t, env.user, "never-used", nil)
	_, err = env.client.DeleteApp(ctx, authedReq(&leapmuxv1.DeleteAppRequest{
		ClientId: fresh.GetApp().GetClientId(),
	}, env.user))
	require.NoError(t, err)
	_, err = env.store.OAuthClients().Get(ctx, fresh.GetApp().GetClientId())
	assert.Error(t, err)
}

// A vouch needs an administrator, and AppService is NOT an admin service -- an
// ordinary user registers apps through it -- so the check lives in the handler
// rather than in the interceptor's admin gate.
func TestAppService_OnlyAnAdministratorVouches(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.user, "unverified", nil)
	clientID := app.GetApp().GetClientId()

	assert.Nil(t, app.GetApp().VerifiedAt, "a new registration is unverified")

	// The OWNER cannot vouch for their own app, which is the whole point: a
	// vouch that the registrant could write would say nothing.
	_, err := env.client.VerifyApp(ctx, authedReq(&leapmuxv1.VerifyAppRequest{
		ClientId: clientID, Verified: true,
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	verified, err := env.client.VerifyApp(ctx, authedReq(&leapmuxv1.VerifyAppRequest{
		ClientId: clientID, Verified: true,
	}, env.admin))
	require.NoError(t, err)
	require.NotNil(t, verified.Msg.GetApp().VerifiedAt)
	assert.Equal(t, "admin", verified.Msg.GetApp().GetVerifiedByUsername())

	// Both columns move together, which is what the half-vouch CHECK enforces.
	row, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	require.NotNil(t, row.VerifiedAt)
	assert.Equal(t, env.adminID, row.VerifiedBy)

	// And a withdrawal clears both.
	withdrawn, err := env.client.VerifyApp(ctx, authedReq(&leapmuxv1.VerifyAppRequest{
		ClientId: clientID, Verified: false,
	}, env.admin))
	require.NoError(t, err)
	assert.Nil(t, withdrawn.Msg.GetApp().VerifiedAt)
	row, err = env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.Nil(t, row.VerifiedAt)
	assert.Empty(t, row.VerifiedBy)
}

// An UPDATE leaves an absent field alone.
//
// A surface that required the whole registration back would make every edit a
// read-modify-write, and a caller that read the app minutes ago would overwrite
// a concurrent change with its stale copy.
func TestAppService_UpdateLeavesAbsentFieldsAlone(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.user, "original", func(m *leapmuxv1.RegisterAppRequest) {
		m.ClientUri = "https://example.com"
		m.RedirectUris = []string{"https://app.example.com/callback", "https://app.example.com/other"}
	})
	clientID := app.GetApp().GetClientId()

	updated, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: clientID, ClientName: ptr("renamed"),
	}, env.user))
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Msg.GetApp().GetClientName())
	assert.Equal(t, "https://example.com", updated.Msg.GetApp().GetClientUri(), "an absent field is untouched")
	assert.Len(t, updated.Msg.GetApp().GetRedirectUris(), 2, "an absent repeated field is untouched")

	// A repeated field cannot distinguish empty from absent, so the flag is
	// what says "I mean to replace it".
	replaced, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId:            clientID,
		ReplaceRedirectUris: true,
		RedirectUris:        []string{"https://app.example.com/only"},
	}, env.user))
	require.NoError(t, err)
	assert.Equal(t, []string{"https://app.example.com/only"}, replaced.Msg.GetApp().GetRedirectUris())
	assert.Equal(t, "renamed", replaced.Msg.GetApp().GetClientName(), "the earlier rename survives")
}

// An app that can redirect needs somewhere to redirect TO, and the check runs
// against the values that WILL be stored rather than the ones that arrived.
//
// Two requests are the same defect: adding authorization_code without an
// address, and removing the last address from an app that already has the
// grant. One check catches both because it reads the merged result.
func TestAppService_AuthorizationCodeNeedsARedirectAddress(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	_, err := env.client.RegisterApp(ctx, authedReq(&leapmuxv1.RegisterAppRequest{
		ClientName: "no-address",
		Scopes:     []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
		GrantTypes: []string{"authorization_code"},
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	app := env.registerApp(t, env.user, "has-address", nil)
	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId:            app.GetApp().GetClientId(),
		ReplaceRedirectUris: true,
		RedirectUris:        nil,
	}, env.user))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	row, err := env.store.OAuthClients().Get(ctx, app.GetApp().GetClientId())
	require.NoError(t, err)
	assert.NotEmpty(t, row.RedirectURIs, "a refused edit must leave the app usable")
}

// A registration must NAME the permissions it wants.
//
// SCOPE_ALL is the explicit absence of a limit, and no registration may claim
// it: an app's ceiling is what a consent screen shows, and "everything,
// including whatever is added later" cannot be shown to anybody.
func TestAppService_RegistrationMustNameItsPermissions(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	for name, scopes := range map[string][]leapmuxv1.Scope{
		"an empty ask":     nil,
		"the unscoped ask": {leapmuxv1.Scope_SCOPE_ALL},
		"a refusal marker": {leapmuxv1.Scope_SCOPE_NEVER},
		"the zero value":   {leapmuxv1.Scope_SCOPE_UNSPECIFIED},
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			_, err := env.client.RegisterApp(ctx, authedReq(&leapmuxv1.RegisterAppRequest{
				ClientName:   "asks-badly",
				RedirectUris: []string{"https://app.example.com/callback"},
				Scopes:       scopes,
			}, env.user))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

// --- helpers ---------------------------------------------------------------

func (e *appEnv) newPlainUser(t *testing.T, username string) (id, session string) {
	t.Helper()
	ctx := context.Background()
	hash, err := password.Hash("password123")
	require.NoError(t, err)
	u, err := service.CreateUser(ctx, e.store, service.CreateUserParams{
		Username: username, PasswordHash: hash, DisplayName: username, PasswordSet: true,
	})
	require.NoError(t, err)
	token, _, _, err := auth.Login(ctx, e.store, username, "password123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	// ELEVATED, like the two sessions setupAppService builds. The elevation
	// gate runs before the ownership rung, so a stranger with no window is
	// refused for the window and never reaches the answer these tests measure
	// -- which would make an ownership test pass for the wrong reason.
	hubtestutil.ElevateSession(t, e.store, token, u.ID)
	return u.ID, token
}

// assertNoSecretField walks a message and fails if any string field holds the
// secret. It reads the VALUES rather than the field names, so a field added
// later is covered without editing this.
func assertNoSecretField(t *testing.T, msg protoreflect.Message, secret string) {
	t.Helper()
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() == protoreflect.StringKind && !fd.IsList() {
			assert.NotEqualf(t, secret, v.String(), "field %s carries the client secret", fd.Name())
		}
		return true
	})
}

// TestAppService_WritesNeedAProvenFactor drives the gate itself, with an
// UN-elevated session.
//
// It is the behavioral half of appProcedureElevation: that record says which
// verbs the gate covers, and a record cannot observe a handler. Every other
// test in this file runs with an elevated session, so without this one the
// gate could be deleted and the suite would stay green.
//
// The four gated verbs are driven as a table, and the three ungated ones are
// driven beside them -- an assertion that only proves the refusals would pass
// for a service that refused everything.
func TestAppService_WritesNeedAProvenFactor(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.admin, "gate-app", nil)
	clientID := app.GetApp().GetClientId()

	// A SECOND session for the same administrator, deliberately not elevated.
	// A fresh login rather than a dropped window, because that is the state a
	// person is actually in: signed in for hours, with the step-up window long
	// expired.
	plain, _, _, err := auth.Login(ctx, env.store, "admin", "password123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	refused := map[string]func() error{
		"RegisterApp": func() error {
			_, err := env.client.RegisterApp(ctx, authedReq(&leapmuxv1.RegisterAppRequest{
				ClientName:   "second-app",
				RedirectUris: []string{"https://app.example.com/callback"},
				Scopes:       []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
				Visibility:   leapmuxv1.AppVisibility_APP_VISIBILITY_PRIVATE,
				ClientType:   leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC,
			}, plain))
			return err
		},
		"UpdateApp": func() error {
			_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
				ClientId: clientID, ReplaceRedirectUris: true,
				RedirectUris: []string{"https://attacker.example.com/callback"},
			}, plain))
			return err
		},
		"SetAppElevationAllowed": func() error {
			_, err := env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
				ClientId: clientID, Allowed: true,
			}, plain))
			return err
		},
		"VerifyApp": func() error {
			_, err := env.client.VerifyApp(ctx, authedReq(&leapmuxv1.VerifyAppRequest{
				ClientId: clientID, Verified: true,
			}, plain))
			return err
		},
	}
	for name, call := range refused {
		t.Run(name+" is refused", func(t *testing.T) {
			err := call()
			require.Error(t, err, "%s must demand a proven factor", name)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "recent sign-in")
		})
	}

	// The redirect list is UNCHANGED, which is the property the gate exists
	// for: the refusal above must be a refusal to write, not a message.
	row, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.Equal(t, "https://app.example.com/callback", row.RedirectURIs)
	assert.False(t, row.ElevationAllowed)
	assert.Nil(t, row.VerifiedAt)

	t.Run("ListApps is admitted", func(t *testing.T) {
		_, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{}, plain))
		assert.NoError(t, err, "a read must not demand a factor")
	})
	t.Run("RevokeApp is admitted", func(t *testing.T) {
		_, err := env.client.RevokeApp(ctx, authedReq(&leapmuxv1.RevokeAppRequest{ClientId: clientID}, plain))
		assert.NoError(t, err, "retiring an app only reduces access; a delay is the attacker's gain")
	})
	t.Run("DeleteApp is admitted", func(t *testing.T) {
		_, err := env.client.DeleteApp(ctx, authedReq(&leapmuxv1.DeleteAppRequest{ClientId: clientID}, plain))
		assert.NoError(t, err, "the registration holds no credential, so this removes an empty record")
	})
}

// elevationProbe mounts a bare unary handler behind the SAME auth interceptor
// the AppService runs under, and answers whether the calling credential carries
// a live elevation window.
//
// It exists because the validation cache is not observable from outside
// internal/hub/auth: the only honest way to ask "does the next request still
// see the window" is to make one. The procedure is a real classified path so
// the scope rung admits a workspace:read credential; the handler itself is a
// stub, because what is under test is everything BEFORE it.
func (e *appEnv) elevationProbe(t *testing.T, interceptor connect.Interceptor) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, connect.NewUnaryHandler(
		leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		func(ctx context.Context, _ *connect.Request[leapmuxv1.ListWorkspacesRequest],
		) (*connect.Response[leapmuxv1.ListWorkspacesResponse], error) {
			user, err := auth.MustGetUser(ctx)
			if err != nil {
				return nil, err
			}
			// The workspace id field carries the answer, so the probe needs no
			// message of its own.
			marker := "not-elevated"
			if user.Elevated(time.Now().UTC()) {
				marker = "elevated"
			}
			return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{
				Workspaces: []*leapmuxv1.Workspace{{Id: marker}},
			}), nil
		},
		connect.WithInterceptors(interceptor)))
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	e.probeClient = leapmuxv1connect.NewWorkspaceServiceClient(server.Client(), server.URL, connect.WithGRPC())
	return server.URL
}

// probeElevation asks the probe whether this bearer is elevated RIGHT NOW,
// through the interceptor's cached validation path.
func (e *appEnv) probeElevation(t *testing.T, bearer string) string {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{})
	req.Header().Set("Authorization", "Bearer "+bearer)
	resp, err := e.probeClient.ListWorkspaces(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkspaces(), 1)
	return resp.Msg.GetWorkspaces()[0].GetId()
}

// TestAppService_TurningElevationOffDropsTheCachedWindow drives the whole
// withdrawal through the real RPC, with nothing hand-invalidated.
//
// The column's own comment promises that turning the flag off closes every
// live window ON THE NEXT REQUEST, and loadBearer is only half of that: it
// re-reads the flag and zeroes the window, but it runs on a cache MISS, and
// the interceptor holds the whole UserInfo for 30 seconds. So the property is
// the handler's to keep, and the store-level test of the same rule
// (TestElevationAllowed_TurningItOffClosesALiveWindow) drops the cache by hand
// because nothing else is present there to do it.
//
// This is the pole that catches the omission: the credential makes a REQUEST
// first, so the cache holds an elevated UserInfo before the flag moves.
func TestAppService_TurningElevationOffDropsTheCachedWindow(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	env.elevationProbe(t, env.interceptor)

	app := env.registerApp(t, env.admin, "elevating-app", nil)
	clientID := app.GetApp().GetClientId()
	_, err := env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: clientID, Allowed: true,
	}, env.admin))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.adminID), ClientID: clientID,
		InstallationName: "laptop", GrantedScopes: "workspace:read",
		SecretHash: env.validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)

	now := time.Now().UTC()
	rows, err := env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: tokenID, UserID: userid.MustNew(env.adminID),
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows, "the store must admit the elevation while the app is allowed")

	// POPULATES the cache. Without this request the assertion below would pass
	// on a cache miss and prove nothing about the invalidation.
	require.Equal(t, "elevated", env.probeElevation(t, bearer),
		"the window is live before the flag changes")

	// The owner takes the right away, through the surface a person uses.
	_, err = env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: clientID, Allowed: false,
	}, env.admin))
	require.NoError(t, err)

	assert.Equal(t, "not-elevated", env.probeElevation(t, bearer),
		"the cached UserInfo must not answer for the row the write just changed")
}

// mintCredentialFor mints one credential of an app with a stated grant, and
// returns its row id.
func (e *appEnv) mintCredentialFor(t *testing.T, clientID, installation, granted string) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, e.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(e.adminID), ClientID: clientID,
		InstallationName: installation, GrantedScopes: granted,
		SecretHash: e.validator.HashSecret(auth.MintAccessSecret()),
	}))
	return tokenID
}

// TestAppService_NarrowingTheCeilingTearsDownOnlyWhatLostSomething is the
// in-process half of the registered permission ceiling.
//
// loadBearer already narrows every stored grant to oauth_clients.scopes, so the
// new value is authoritative the moment it is written. What a column change
// cannot reach is the 30-second validation cache and an OPEN Noise channel,
// which carries the scope set announced at its handshake and which the Hub
// cannot renegotiate because it cannot read the session. Closing it is the only
// way to withdraw authority from it.
//
// The teardown is PER CREDENTIAL, and that is what this pins. A hub-wide app
// holds credentials for many accounts on many machines; deciding once for the
// whole app would close every one of those channels for a permission most of
// them never held. The credential that lost nothing must keep its channel.
func TestAppService_NarrowingTheCeilingTearsDownOnlyWhatLostSomething(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.admin, "ceiling-app", func(m *leapmuxv1.RegisterAppRequest) {
		m.Scopes = []leapmuxv1.Scope{
			leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
			leapmuxv1.Scope_SCOPE_FILE_READ,
		}
	})
	clientID := app.GetApp().GetClientId()

	// One credential HOLDS the permission about to be removed; the other does
	// not. Both are live credentials of the same app.
	losing := env.mintCredentialFor(t, clientID, "laptop", "workspace:read file:read worker:read")
	keeping := env.mintCredentialFor(t, clientID, "desktop", "workspace:read")

	_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: clientID, ReplaceScopes: true,
		Scopes: []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
	}, env.admin))
	require.NoError(t, err)

	closed := make([]string, 0)
	for _, ref := range env.closer.closedBearers() {
		closed = append(closed, ref.TokenID())
	}
	assert.Contains(t, closed, losing,
		"a credential that held the removed permission keeps it inside an open channel until the channel closes")
	assert.NotContains(t, closed, keeping,
		"a credential that never held it loses nothing, so closing its channel would be an outage for no gain")

	// Neither credential is REVOKED. The ceiling narrows what they reach; it
	// does not end them, and an account that consented to the rest keeps it.
	for _, tokenID := range []string{losing, keeping} {
		row, err := env.store.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Nil(t, row.RevokedAt, "narrowing a registration must not revoke a credential")
	}
}

// TestAppService_WideningTheCeilingClosesNothing is the other direction.
//
// Nothing already running exceeds a wider ceiling, so there is no authority to
// withdraw and no channel to tear down. Without this assertion the test above
// would pass for an implementation that closed every channel on any edit.
func TestAppService_WideningTheCeilingClosesNothing(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.admin, "widening-app", nil)
	clientID := app.GetApp().GetClientId()
	env.mintCredentialFor(t, clientID, "laptop", "workspace:read")

	_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: clientID, ReplaceScopes: true,
		Scopes: []leapmuxv1.Scope{
			leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
			leapmuxv1.Scope_SCOPE_FILE_READ,
		},
	}, env.admin))
	require.NoError(t, err)

	assert.Empty(t, env.closer.closedBearers(),
		"a wider ceiling withdraws nothing, so it must close no channel")
}

// TestAppService_AnEditThatLeavesTheCeilingAloneClosesNothing.
//
// UpdateApp's fields are all optional, so renaming an app runs the same code
// path. It must not read as a narrowing: `replace_scopes` absent means the
// stored value is carried forward unchanged, and an edit that touches a
// different field would otherwise close every channel the app holds.
func TestAppService_AnEditThatLeavesTheCeilingAloneClosesNothing(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.admin, "renamed-app", nil)
	clientID := app.GetApp().GetClientId()
	env.mintCredentialFor(t, clientID, "laptop", "workspace:read")

	_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: clientID, ClientName: ptr("a new name"),
	}, env.admin))
	require.NoError(t, err)

	assert.Empty(t, env.closer.closedBearers(),
		"renaming an app must not disturb the credentials it holds")
}

// TestAppService_ANarrowingSucceedsWhenTheOLDCeilingIsUnreadable.
//
// The edge the ceiling change exposes: applyCeilingChange parses BOTH sides,
// and only the new one is validated by this handler -- the old one is whatever
// the column already held. A registration whose stored value drifted out of the
// vocabulary therefore reaches the parse failure, and the write must still
// succeed: the column is already committed by then, and validation re-reads the
// ceiling on every request, so the effect lands one cache window later rather
// than being lost. Refusing here would report a write that did happen as a
// failure, and would leave an operator unable to repair a drifted row at all.
func TestAppService_ANarrowingSucceedsWhenTheOLDCeilingIsUnreadable(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.admin, "drifted-app", nil)
	clientID := app.GetApp().GetClientId()
	tokenID := env.mintCredentialFor(t, clientID, "laptop", "workspace:read")

	// A vocabulary that drifted, written through the store because the handler
	// refuses to write one -- which is exactly why only the OLD side can be
	// unreadable.
	rows, err := env.store.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: clientID, ClientName: "drifted-app",
		RedirectURIs: "https://app.example.com/callback",
		Scopes:       "workspace:read invented:permission",
		GrantTypes:   "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.adminID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// The repair: an administrator replaces the drifted ceiling with a legible
	// one.
	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId: clientID, ReplaceScopes: true,
		Scopes: []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
	}, env.admin))
	require.NoError(t, err, "an unreadable old ceiling must not refuse the write that repairs it")

	updated, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.Equal(t, "workspace:read", updated.Scopes, "the repair landed")

	// No channel was torn down, because the pair could not be compared. The
	// credential is unaffected either way -- validation reads the repaired
	// ceiling from the next request.
	assert.Empty(t, env.closer.closedBearers())
	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt)
}

// TestAppService_UpdateClosesTheCeiling pins the one rule an edit could
// silently break: the stored ceiling states its implications, exactly as both
// registration builders do. The edit screen pre-fills independent checkboxes
// from the closed set, so an owner who unchecks an implied permission writes
// an unclosed ceiling -- and everything downstream (loadBearer's narrowing,
// the consent leg's subset test, applyCeilingChange) reads the raw column.
// Without the closure, terminal:write alone silently stripped terminal:read
// and worker:read from every credential the app held.
func TestAppService_UpdateClosesTheCeiling(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	app := env.registerApp(t, env.admin, "edit-me", func(m *leapmuxv1.RegisterAppRequest) {
		m.Scopes = []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_TERMINAL_WRITE}
	})
	// The registration surface already closes: terminal:write arrives with
	// its implications.
	row, err := env.store.OAuthClients().Get(ctx, app.GetApp().GetClientId())
	require.NoError(t, err)
	assert.Contains(t, row.Scopes, "terminal:read", "registration closes the ceiling")

	_, err = env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId:      app.GetApp().GetClientId(),
		ReplaceScopes: true,
		// terminal:write ALONE, the exact list the pre-filled checkboxes send
		// when an owner unchecks the implied members.
		Scopes: []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_TERMINAL_WRITE},
	}, env.admin))
	require.NoError(t, err)

	row, err = env.store.OAuthClients().Get(ctx, app.GetApp().GetClientId())
	require.NoError(t, err)
	assert.Contains(t, row.Scopes, "terminal:read",
		"an edit closes the ceiling, so the implied permission survives the uncheck")
	assert.Contains(t, row.Scopes, "worker:read",
		"the channel a terminal needs stays open")
}

// TestAppService_IncludeRevokedListsRetiredRegistrations pins the widened
// listing: the flag existed on the wire, the CLI exposed it, and the store
// queries hard-coded revoked_at IS NULL -- so an operator asking for retired
// rows received a page indistinguishable from "none".
func TestAppService_IncludeRevokedListsRetiredRegistrations(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	live := env.registerApp(t, env.admin, "live-app", nil)
	retired := env.registerApp(t, env.admin, "retired-app", nil)
	// The store's own ownership guard: an administrator retires a hub-wide
	// registration.
	adminUID, ok := userid.New(env.adminID)
	require.True(t, ok)
	rows, err := env.store.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
		ClientID:      retired.GetApp().GetClientId(),
		CallerUserID:  adminUID,
		CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows, "the retirement must land")

	plain, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{}, env.admin))
	require.NoError(t, err)
	ids := func(resp *connect.Response[leapmuxv1.ListAppsResponse]) map[string]bool {
		out := map[string]bool{}
		for _, a := range resp.Msg.GetApps() {
			out[a.GetClientId()] = true
		}
		return out
	}
	assert.False(t, ids(plain)[retired.GetApp().GetClientId()], "the default listing stays live-only")
	assert.True(t, ids(plain)[live.GetApp().GetClientId()])

	widened, err := env.client.ListApps(ctx, authedReq(&leapmuxv1.ListAppsRequest{
		IncludeRevoked: true,
	}, env.admin))
	require.NoError(t, err)
	assert.True(t, ids(widened)[retired.GetApp().GetClientId()],
		"the widened listing surfaces the retired registration")
	for _, a := range widened.Msg.GetApps() {
		if a.GetClientId() == retired.GetApp().GetClientId() {
			assert.NotNil(t, a.GetRevokedAt(), "the row reports its retirement")
		}
	}
}

// TestAppService_UpdateValidatesTheIconBeforeAnyWriteLands pins the ordering:
// every other field validation answers before a write does, and an invalid
// icon must too. The validation used to run after the row rewrite and the
// ceiling cascade, so one bad icon answered "update failed" for an update
// that had already landed -- the caller retried or reverted nothing while the
// hub had already narrowed the ceiling and torn down channels.
func TestAppService_UpdateValidatesTheIconBeforeAnyWriteLands(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()

	app := env.registerApp(t, env.admin, "icon-order", nil)
	clientID := app.GetApp().GetClientId()

	_, err := env.client.UpdateApp(ctx, authedReq(&leapmuxv1.UpdateAppRequest{
		ClientId:    clientID,
		ClientName:  proto.String("renamed-mid-flight"),
		ReplaceIcon: true,
		// maxIconBytes is unexported; the literal states the cap this test
		// overruns (64 KiB, maxIconBytes in register.go).
		Icon:          []byte(strings.Repeat("x", 64<<10+1)),
		IconMediaType: "image/png",
	}, env.admin))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// NOTHING landed: neither the rename, nor any other column.
	row, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.Equal(t, "icon-order", row.ClientName,
		"a refused icon must not leave a half-landed update behind")
}

// TestAppService_VouchOnARetiredAppAnswersNotFound pins the rows-affected
// half of VerifyApp: the statement filters revoked_at IS NULL while load
// deliberately reads a retired row, so a vouch on an app somebody retired a
// moment ago must answer NOT FOUND rather than success and a projected vouch
// the row never took -- the lie every sibling write verb refuses.
func TestAppService_VouchOnARetiredAppAnswersNotFound(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	app := env.registerApp(t, env.user, "retire-then-vouch", nil)
	clientID := app.GetApp().GetClientId()

	// The OWNER retires their own registration; the ADMINISTRATOR's vouch then
	// meets the retired row.
	_, err := env.client.RevokeApp(ctx, authedReq(&leapmuxv1.RevokeAppRequest{ClientId: clientID}, env.user))
	require.NoError(t, err)

	_, err = env.client.VerifyApp(ctx, authedReq(&leapmuxv1.VerifyAppRequest{
		ClientId: clientID, Verified: true,
	}, env.admin))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a retired app takes no vouch, and the verb must say so rather than report success")

	// And the row really is untouched.
	row, err := env.store.OAuthClients().Get(ctx, clientID)
	require.NoError(t, err)
	assert.Nil(t, row.VerifiedAt, "no vouch landed")
}

// TestAppService_NoOpElevationToggleKeepsTheCachedWindow pins the skip half
// of SetAppElevationAllowed: a toggle that does not move the flag must not
// drop the cached credentials, because a cached UserInfo validated under the
// current flag already agrees with the row. The cache is made observable by
// flipping the flag out-of-band after warming it (the concurrent-opposite-
// toggle race the handler's comment names): a surviving entry still answers
// "elevated"; an invalidation forces a revalidation that reads the cleared
// flag and answers "not-elevated".
func TestAppService_NoOpElevationToggleKeepsTheCachedWindow(t *testing.T) {
	t.Parallel()

	env := setupAppService(t)
	ctx := context.Background()
	env.elevationProbe(t, env.interceptor)

	app := env.registerApp(t, env.admin, "steady-app", nil)
	clientID := app.GetApp().GetClientId()
	_, err := env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: clientID, Allowed: true,
	}, env.admin))
	require.NoError(t, err)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.adminID), ClientID: clientID,
		InstallationName: "laptop", GrantedScopes: "workspace:read",
		SecretHash: env.validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	now := time.Now().UTC()
	rows, err := env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: tokenID, UserID: userid.MustNew(env.adminID),
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// POPULATES the cache while the flag allows the window: without this
	// request the assertion below would pass on a cache miss.
	require.Equal(t, "elevated", env.probeElevation(t, bearer),
		"precondition: the window is live and the cache holds it")

	// The flag flips out-of-band -- a store write no handler runs, so no
	// invalidation fires. The cached UserInfo is now stale in exactly the way
	// the skip's comment describes.
	flipped, err := env.store.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
		ClientID: clientID, ElevationAllowed: false,
		CallerUserID: userid.MustNew(env.adminID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, flipped)

	// The NO-OP: the request carries the value the row already holds, so the
	// handler must treat the flag as unmoved and keep the cache.
	_, err = env.client.SetAppElevationAllowed(ctx, authedReq(&leapmuxv1.SetAppElevationAllowedRequest{
		ClientId: clientID, Allowed: false,
	}, env.admin))
	require.NoError(t, err)

	require.Equal(t, "elevated", env.probeElevation(t, bearer),
		"a toggle that changed nothing must not drop the cached UserInfo")
}
