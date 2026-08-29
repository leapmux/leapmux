package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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
	client  leapmuxv1connect.AdminIdPServiceClient
	st      store.Store
	ks      *keystore.Keystore
	token   string
	adminID string
	cache   *recordingProviderCache
}

// setupAdminOAuthTest is the DEFAULT fixture, and its session is elevated.
//
// Every write on this surface runs under the elevation window, exactly as
// every hub-settings write does: an identity provider row installs a sign-in
// route for the whole hub. Almost every test here exercises what a verb DOES
// rather than whether the gate is there, so supplying the elevation is what
// keeps those tests about their own subject.
// TestAdminIdPService_WritesNeedAProvenFactor asserts the refusal.
func setupAdminOAuthTest(t *testing.T) *adminOAuthEnv {
	t.Helper()
	env := setupAdminOAuthTestUnelevated(t)
	hubtestutil.ElevateSession(t, env.st, env.token, env.adminID)
	return env
}

// setupAdminOAuthTestUnelevated builds the same environment with a session
// that proved no factor. It is what the gate tests use.
func setupAdminOAuthTestUnelevated(t *testing.T) *adminOAuthEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	admin, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
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
	cache := &recordingProviderCache{}
	path, handler := leapmuxv1connect.NewAdminIdPServiceHandler(service.NewAdminIdPService(st, ks, cache), opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminOAuthEnv{
		client:  leapmuxv1connect.NewAdminIdPServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:      st,
		ks:      ks,
		token:   session,
		adminID: admin.ID,
		cache:   cache,
	}
}

func TestAdminIdPService_AddListRemoveGithub(t *testing.T) {
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

// TestAdminIdPService_OIDCRefusals pins the two refusals a generic OIDC
// provider needs, and the preset fallback that makes them unnecessary for
// a known provider.
//
// `trust_email` is a SECURITY decision — it says "believe this issuer's
// email_verified claim" — so a generic OIDC provider must state it rather
// than inherit a default. A preset carries its own answer, which is why
// the GitHub path below needs no flag. Collapse the fallback chain to a
// plain getter and the hub creates a provider with an unintended trust
// setting, and nothing fails.
func TestAdminIdPService_OIDCRefusals(t *testing.T) {
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

	// Both refusals run BEFORE the hub contacts the issuer, which is what makes
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

// TestAdminIdPService_SetEnabledAndRemove covers the two verbs that had
// no test at all. Both change who can log in, for every user.
func TestAdminIdPService_SetEnabledAndRemove(t *testing.T) {
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
// every provider the caller lists.
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

// TestAdminIdPService_RemoveRefusesToLockAccountsOut pins the guard on
// the one admin verb that could take away every login method an account
// has.
//
// Removing the provider row cascades every oauth_user_links row away, so a
// password-less account whose only link was this provider can no longer
// sign in and only `leapmux recover` restores it. The hub already refuses
// that outcome for one account (UserService.UnlinkOAuthProvider) and puts
// the analogous loss behind force (DeleteUser), so this verb takes the
// same shape.
func TestAdminIdPService_RemoveRefusesToLockAccountsOut(t *testing.T) {
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

	// force admits the removal, and reports exactly what it cost.
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

// TestAdminIdPService_RemoveAllowedWhenNobodyIsOrphaned pins the other
// side: the guard must not block an ordinary removal, and
// the count travels in the reply either way.
func TestAdminIdPService_RemoveAllowedWhenNobodyIsOrphaned(t *testing.T) {
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

// recordingProviderCache stands in for the OAuth login handler's built-provider
// cache. Removing or disabling a provider must drop its entries, because
// loadEnabledProvider refuses such a row BEFORE it would rebuild -- so the
// login handler's own eviction can never reach them, and the entry holds the
// client secret the keystore decrypted.
type recordingProviderCache struct {
	mu          sync.Mutex
	invalidated []string
}

func (c *recordingProviderCache) InvalidateProvider(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidated = append(c.invalidated, providerID)
}

func (c *recordingProviderCache) taken() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]string(nil), c.invalidated...)
	c.invalidated = nil
	return out
}

// TestAdminIdPService_EvictsTheProviderCache pins the call the admin verbs
// owe the login handler.
//
// A removed or disabled provider can never be rebuilt -- loadEnabledProvider
// refuses its row first -- so the login handler's own eviction cannot reach
// the entry, and it holds the client secret the keystore decrypted.
func TestAdminIdPService_EvictsTheProviderCache(t *testing.T) {
	env := setupAdminOAuthTest(t)
	ctx := context.Background()

	added, err := env.client.AddOAuthProvider(ctx, authedReq(&leapmuxv1.AddOAuthProviderRequest{
		ProviderType: "github", ClientId: "gh-client", ClientSecret: "gh-secret",
	}, env.token))
	require.NoError(t, err)
	id := added.Msg.GetProvider().GetId()
	require.NotEmpty(t, id)
	// An add rebuilds on demand, so only remove and disable are pinned here.
	env.cache.taken()

	_, err = env.client.SetOAuthProviderEnabled(ctx, authedReq(&leapmuxv1.SetOAuthProviderEnabledRequest{
		Id: id, Enabled: false,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, []string{id}, env.cache.taken(), "a disabled provider's built instance must go")

	_, err = env.client.RemoveOAuthProvider(ctx, authedReq(&leapmuxv1.RemoveOAuthProviderRequest{
		Id: id,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, []string{id}, env.cache.taken(), "a removed provider's built instance must go")
}

// unelevatedSession signs the same administrator in a SECOND time and returns
// that session, which proved no factor.
//
// A second session rather than a drop on the first: the elevation lives on the
// session row, so this is the state a user reaches by opening the app in
// another browser, and the fixture's own seeding writes stay admitted.
func (e *adminOAuthEnv) unelevatedSession(t *testing.T) string {
	t.Helper()
	session, _, _, err := auth.Login(context.Background(), e.st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	return session
}

// TestAdminIdPService_WritesNeedAProvenFactor drives every write verb on
// this service through a caller that proved no factor.
//
// The three verbs shipped with NO gate at all while the sign-up toggle beside
// them had one, and nothing observed the omission: the classification record
// in admin_procedures_internal_test.go says which side of the gate each
// procedure sits on, and only a request can show what the handler does with
// that decision.
//
// Each call is a REAL write that succeeds once the caller is elevated, never a
// deliberately malformed request: a refusal these tests could not tell from an
// argument error would pass whether the gate existed or not.
func TestAdminIdPService_WritesNeedAProvenFactor(t *testing.T) {
	ctx := context.Background()

	// The id an elevated fixture already holds, so Remove and SetEnabled
	// address a provider that exists. Add needs none.
	type oauthWrite func(env *adminOAuthEnv, providerID string, authorize requestAuth) error

	writes := map[string]oauthWrite{
		"AddOAuthProvider": func(env *adminOAuthEnv, _ string, authorize requestAuth) error {
			_, err := env.client.AddOAuthProvider(ctx, authorized(&leapmuxv1.AddOAuthProviderRequest{
				ProviderType: "github", Name: "second", ClientId: "gh2", ClientSecret: "ghs2",
			}, authorize))
			return err
		},
		"SetOAuthProviderEnabled": func(env *adminOAuthEnv, providerID string, authorize requestAuth) error {
			_, err := env.client.SetOAuthProviderEnabled(ctx, authorized(&leapmuxv1.SetOAuthProviderEnabledRequest{
				Id: providerID, Enabled: false,
			}, authorize))
			return err
		},
		"RemoveOAuthProvider": func(env *adminOAuthEnv, providerID string, authorize requestAuth) error {
			_, err := env.client.RemoveOAuthProvider(ctx, authorized(&leapmuxv1.RemoveOAuthProviderRequest{
				Id: providerID,
			}, authorize))
			return err
		},
	}

	for name, write := range writes {
		t.Run(name+" refuses an un-elevated session", func(t *testing.T) {
			// The provider is seeded through an ELEVATED session, then the
			// window is dropped: the fixture's own writes must not be what the
			// gate refuses.
			env := setupAdminOAuthTest(t)
			id := addProvider(t, env, "seeded")

			err := write(env, id, cookieAuth(env.unelevatedSession(t)))
			require.Error(t, err, "a write on this surface must not land without a proven factor")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
				"the refusal must be the one a step-up prompt can clear")
		})

		t.Run(name+" admits an elevated session", func(t *testing.T) {
			env := setupAdminOAuthTest(t)
			id := addProvider(t, env, "seeded")
			assert.NoError(t, write(env, id, cookieAuth(env.token)))
		})
	}

	// A READ takes nothing. The window guards what a stolen administrator
	// cookie could CHANGE for every account on the hub; the inventory it can
	// already see is not that.
	t.Run("ListOAuthProviders needs no elevation", func(t *testing.T) {
		env := setupAdminOAuthTestUnelevated(t)
		_, err := env.client.ListOAuthProviders(ctx, authedReq(&leapmuxv1.ListOAuthProvidersRequest{}, env.token))
		assert.NoError(t, err)
	})
}
