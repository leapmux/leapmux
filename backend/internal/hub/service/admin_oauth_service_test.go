package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type adminOAuthEnv struct {
	client leapmuxv1connect.AdminOAuthServiceClient
	st     store.Store
	ks     *keystore.Keystore
	token  string
}

func setupAdminOAuthTest(t *testing.T) *adminOAuthEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	_, err = service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:     "admin",
		PasswordHash: hash,
		DisplayName:  "Admin",
		PasswordSet:  true,
		IsAdmin:      true,
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	ks, err := keystore.LoadOrGenerate(filepath.Join(t.TempDir(), "encryption.key"))
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	path, handler := leapmuxv1connect.NewAdminOAuthServiceHandler(service.NewAdminOAuthService(st, ks), opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminOAuthEnv{
		client: leapmuxv1connect.NewAdminOAuthServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:     st,
		ks:     ks,
		token:  session,
	}
}

func TestAdminOAuthService_AddListRemoveGithub(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	_, err := env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ClientId: "c1", ClientSecret: "s1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "provider_type is required")

	_, err = env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", ClientSecret: "s1",
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id is required")

	_, err = env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", ClientId: "c1",
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_secret is required")

	_, err = env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "nope", ClientId: "c1", ClientSecret: "s1",
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider type")

	added, err := env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github",
		ClientId:     "gh-client",
		ClientSecret: "gh-secret",
	}, env.token))
	require.NoError(t, err)
	prov := added.Msg.GetProvider()
	require.NotNil(t, prov)
	assert.Equal(t, "github", prov.GetProviderType())
	assert.Equal(t, "gh-client", prov.GetClientId())
	assert.True(t, prov.GetEnabled())
	assert.NotContains(t, added.Msg.String(), "gh-secret")

	listed, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetProviders(), 1)
	assert.Equal(t, "gh-client", listed.Msg.GetProviders()[0].GetClientId())
	assert.NotContains(t, listed.Msg.String(), "gh-secret")

	stored, err := env.st.OAuthProviders().GetByID(ctx, prov.GetId())
	require.NoError(t, err)
	plain, err := env.ks.Decrypt(stored.ClientSecret, keystore.ProviderAAD(prov.GetId()))
	require.NoError(t, err)
	assert.Equal(t, "gh-secret", string(plain))

	_, err = env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "id is required")

	_, err = env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
		Id: "missing",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
		Id: prov.GetId(),
	}, env.token))
	require.NoError(t, err)

	listed, err = env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.GetProviders())
}

// TestAdminOAuthService_OIDCRefusals pins the two refusals a generic OIDC
// provider needs, and the preset fallback that makes them unnecessary for
// a known provider.
//
// `trust_email` is a SECURITY decision — it says "believe this issuer's
// email_verified claim" — so a generic OIDC provider must state it rather
// than inherit a default. A preset carries its own answer, which is why
// the GitHub path below needs no flag. Collapse the fallback chain to a
// plain getter and a provider is created with an unintended trust setting
// and nothing fails.
func TestAdminOAuthService_OIDCRefusals(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	add := func(req *leapmuxv1.AddOAuthProviderRequest) error {
		_, err := env.client.AddOAuthProvider(ctx, authedReq(req, env.token))
		return err
	}
	oidc := func() *leapmuxv1.AddOAuthProviderRequest {
		return &leapmuxv1.AddOAuthProviderRequest{
			ProviderType: "oidc", Name: "corp", ClientId: "cid", ClientSecret: "secret",
			IssuerUrl: "https://issuer.example.com", TrustEmail: ptrBool(true),
		}
	}

	missingTrust := oidc()
	missingTrust.TrustEmail = nil
	err := add(missingTrust)
	require.Error(t, err, "a generic OIDC provider must state trust_email")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "trust_email")

	missingIssuer := oidc()
	missingIssuer.IssuerUrl = ""
	err = add(missingIssuer)
	require.Error(t, err, "a generic OIDC provider must state its issuer")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Both refusals run BEFORE the issuer is contacted, which is what makes
	// them assertable without a network. Stating both fields therefore gets
	// past this gate and on to OIDC discovery, which a unit test cannot
	// reach — so the happy path stops here, at "no longer refused for a
	// missing field".
	err = add(oidc())
	if err != nil {
		assert.NotContains(t, err.Error(), "trust_email",
			"stating trust_email must clear that refusal")
		assert.Contains(t, err.Error(), "discovery",
			"the only remaining failure is reaching the issuer")
	}

	// A PRESET carries its own answer, so neither flag is required, and its
	// stored row never carries the client secret back.
	require.NoError(t, add(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", Name: "github", ClientId: "gh", ClientSecret: "ghs",
	}))
	listed, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetProviders(), 1)
	assert.NotContains(t, listed.Msg.GetProviders()[0].String(), "ghs",
		"the client secret never crosses back")
}

// TestAdminOAuthService_SetEnabledAndRemove covers the two verbs that had
// no test at all. Both change who can log in, for every user.
func TestAdminOAuthService_SetEnabledAndRemove(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	added, err := env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", Name: "github", ClientId: "gh", ClientSecret: "ghs",
	}, env.token))
	require.NoError(t, err)
	id := added.Msg.GetProvider().GetId()
	require.True(t, added.Msg.GetProvider().GetEnabled(), "a new provider starts enabled")

	enabledNow := func() bool {
		listed, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
		require.NoError(t, err)
		require.Len(t, listed.Msg.GetProviders(), 1)
		return listed.Msg.GetProviders()[0].GetEnabled()
	}

	_, err = env.client.SetOAuthProviderEnabled(ctx, authedReq(&leapmuxv1.SetOAuthProviderEnabledRequest{
		Id: id, Enabled: false,
	}, env.token))
	require.NoError(t, err)
	assert.False(t, enabledNow(), "disabling removes the login method for every user")

	_, err = env.client.SetOAuthProviderEnabled(ctx, authedReq(&leapmuxv1.SetOAuthProviderEnabledRequest{
		Id: id, Enabled: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, enabledNow())

	_, err = env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{Id: id}, env.token))
	require.NoError(t, err)
	listed, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.GetProviders())

	// An unknown id is the caller's selector, not a hub fault.
	_, err = env.client.SetOAuthProviderEnabled(ctx, authedReq(&leapmuxv1.SetOAuthProviderEnabledRequest{
		Id: "no-such-provider", Enabled: false,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// linkUser creates a user with the given password state and links it to
// every provider named.
func linkUser(t *testing.T, env *adminOAuthEnv, username string, passwordSet bool, providerIDs ...string) *store.User {
	t.Helper()
	ctx := context.Background()
	hash := ""
	if passwordSet {
		var err error
		hash, err = password.Hash("userpass12345")
		require.NoError(t, err)
	}
	user, err := service.CreateUser(ctx, env.st, service.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		PasswordSet:  passwordSet,
	})
	require.NoError(t, err)
	for _, providerID := range providerIDs {
		require.NoError(t, env.st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          userid.MustNew(user.ID),
			ProviderID:      providerID,
			ProviderSubject: username + "@" + providerID,
		}))
	}
	return user
}

// addProvider adds a preset provider and returns its id.
func addProvider(t *testing.T, env *adminOAuthEnv, name string) string {
	t.Helper()
	added, err := env.client.AddOAuthProvider(context.Background(), authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", Name: name, ClientId: "c-" + name, ClientSecret: "s-" + name,
	}, env.token))
	require.NoError(t, err)
	return added.Msg.GetProvider().GetId()
}

// TestAdminOAuthService_RemoveRefusesToLockAccountsOut pins the guard on
// the one admin verb that could take away every login method an account
// has.
//
// Removing the provider row cascades every oauth_user_links row away, so a
// password-less account whose only link was this provider can no longer
// sign in and only `leapmux recover` restores it. The hub already refuses
// that outcome for one account (UserService.UnlinkOAuthProvider) and puts
// the analogous loss behind force (DeleteUser), so this verb takes the
// same shape.
func TestAdminOAuthService_RemoveRefusesToLockAccountsOut(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	primary := addProvider(t, env, "primary")
	backup := addProvider(t, env, "backup")

	// The account at risk: no password, and this provider is its only link.
	linkUser(t, env, "sso-only", false, primary)
	// Two accounts that are NOT at risk, so the count reports one.
	linkUser(t, env, "has-password", true, primary)
	linkUser(t, env, "two-links", false, primary, backup)

	_, err := env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
		Id: primary,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "1 user(s) have no other login method")
	assert.Contains(t, err.Error(), "or pass force")

	listed, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	assert.Len(t, listed.Msg.GetProviders(), 2, "the refused removal deletes nothing")

	// force lets it through, and reports exactly what it cost.
	removed, err := env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
		Id: primary, Force: true,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed.Msg.GetLockedOutUsers())

	listed, err = env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetProviders(), 1)
	assert.Equal(t, backup, listed.Msg.GetProviders()[0].GetId())
}

// TestAdminOAuthService_RemoveAllowedWhenNobodyIsOrphaned pins the other
// side: the guard must not stand in the way of an ordinary removal, and
// the count travels in the reply either way.
func TestAdminOAuthService_RemoveAllowedWhenNobodyIsOrphaned(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	t.Run("no linked users at all", func(t *testing.T) {
		id := addProvider(t, env, "unused")
		removed, err := env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
			Id: id,
		}, env.token))
		require.NoError(t, err)
		assert.Zero(t, removed.Msg.GetLockedOutUsers())
	})

	t.Run("every linked user keeps a password or another link", func(t *testing.T) {
		id := addProvider(t, env, "safe")
		other := addProvider(t, env, "other")
		linkUser(t, env, "safe-password", true, id)
		linkUser(t, env, "safe-two-links", false, id, other)

		removed, err := env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
			Id: id,
		}, env.token))
		require.NoError(t, err)
		assert.Zero(t, removed.Msg.GetLockedOutUsers(), "no force needed, and nothing was lost")
	})
}
