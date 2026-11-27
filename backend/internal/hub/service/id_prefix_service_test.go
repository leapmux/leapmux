package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func idPrefixMakeID(t *testing.T, prefix, suffix string) string {
	t.Helper()
	padLen := id.GeneratedLength - len(prefix) - len(suffix)
	require.GreaterOrEqual(t, padLen, 0)
	return prefix + strings.Repeat("0", padLen) + suffix
}

type idPrefixWorkerPresence struct{}

func (*idPrefixWorkerPresence) OnlineForTrustedPath(string) bool { return false }

type idPrefixProviderCache struct{}

func (*idPrefixProviderCache) InvalidateProvider(string) {}
func (*idPrefixProviderCache) EvictProviderCache(string) {}

// idPrefixSpyStore wraps a store to record whether expensive fallback queries
// were avoided on ID-prefix fast paths (ListTabs without workspace_ids and
// ListSections when zero sections match).
type idPrefixSpyStore struct {
	store.Store
	listAccessibleCalls   int
	listSectionItemsCalls int
}

func (s *idPrefixSpyStore) Workspaces() store.WorkspaceStore {
	return idPrefixSpyWorkspaces{WorkspaceStore: s.Store.Workspaces(), parent: s}
}

func (s *idPrefixSpyStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return idPrefixSpySectionItems{WorkspaceSectionItemStore: s.Store.WorkspaceSectionItems(), parent: s}
}

type idPrefixSpyWorkspaces struct {
	store.WorkspaceStore
	parent *idPrefixSpyStore
}

func (w idPrefixSpyWorkspaces) ListAccessible(ctx context.Context, p store.ListAccessibleWorkspacesParams) ([]store.Workspace, error) {
	w.parent.listAccessibleCalls++
	return w.WorkspaceStore.ListAccessible(ctx, p)
}

type idPrefixSpySectionItems struct {
	store.WorkspaceSectionItemStore
	parent *idPrefixSpyStore
}

func (i idPrefixSpySectionItems) ListByUser(ctx context.Context, userID userid.UserID) ([]store.WorkspaceSectionItem, error) {
	i.parent.listSectionItemsCalls++
	return i.WorkspaceSectionItemStore.ListByUser(ctx, userID)
}

func TestIDPrefix_AdminUserService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	svc := service.NewAdminUserService(service.AdminUserServiceDeps{
		Store:   st,
		Notices: service.NewCredentialNotices(nil, mail.Renderer{}),
	})

	u1 := idPrefixMakeID(t, "usrA", "1")
	u2 := idPrefixMakeID(t, "usrA", "2")
	uOther := idPrefixMakeID(t, "usrB", "1")
	for _, u := range []struct{ id, name string }{
		{u2, "alice-two"},
		{u1, "alice-one"},
		{uOther, "bob-one"},
	} {
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: u.id, Username: u.name, PasswordHash: "hash", DisplayName: u.name,
			Email: u.name + "@example.com", EmailVerified: true, FirstCredentialExempt: true,
		}))
	}

	t.Run("ListUsers filters by id_prefix and rejects query+id_prefix", func(t *testing.T) {
		_, err := svc.ListUsers(ctx, &leapmuxv1.ListUsersRequest{
			Query:    "alice",
			IdPrefix: "usrA",
		})
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "query and id_prefix cannot be combined")

		// Whitespace-only query does not conflict with id_prefix.
		resp, err := svc.ListUsers(ctx, &leapmuxv1.ListUsersRequest{
			Query:    "   ",
			IdPrefix: "usrA",
		})
		require.NoError(t, err)
		require.Len(t, resp.GetUsers(), 2)
		assert.Equal(t, []string{u1, u2}, []string{resp.GetUsers()[0].GetId(), resp.GetUsers()[1].GetId()})

		// Exact 48-char ID.
		exact, err := svc.ListUsers(ctx, &leapmuxv1.ListUsersRequest{IdPrefix: u2})
		require.NoError(t, err)
		require.Len(t, exact.GetUsers(), 1)
		assert.Equal(t, u2, exact.GetUsers()[0].GetId())

		// Whitespace-only id_prefix allows query search.
		searched, err := svc.ListUsers(ctx, &leapmuxv1.ListUsersRequest{
			Query:    "bob",
			IdPrefix: "  ",
		})
		require.NoError(t, err)
		require.Len(t, searched.GetUsers(), 1)
		assert.Equal(t, uOther, searched.GetUsers()[0].GetId())

		// Invalid UTF-8 maps to InvalidArgument.
		_, err = svc.ListUsers(ctx, &leapmuxv1.ListUsersRequest{IdPrefix: "bad\xff"})
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("ListSessions and ListUserSessions filter by id_prefix", func(t *testing.T) {
		exp := time.Now().UTC().Add(time.Hour)
		s1 := idPrefixMakeID(t, "sesA", "1")
		s2 := idPrefixMakeID(t, "sesA", "2")
		sOther := idPrefixMakeID(t, "sesB", "1")
		for _, s := range []struct{ id, uid string }{
			{s1, u1},
			{s2, uOther},
			{sOther, u1},
		} {
			require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
				ID: s.id, UserID: userid.MustNew(s.uid), SecretHash: storetest.FixtureSecretHash(s.id),
				ExpiresAt: exp, UserAgent: "ua", IPAddress: "127.0.0.1",
			}))
		}

		allResp, err := svc.ListSessions(ctx, &leapmuxv1.ListSessionsRequest{IdPrefix: "sesA"})
		require.NoError(t, err)
		require.Len(t, allResp.GetSessions(), 2)
		assert.Equal(t, []string{s1, s2}, []string{allResp.GetSessions()[0].GetId(), allResp.GetSessions()[1].GetId()})

		userResp, err := svc.ListUserSessions(ctx, &leapmuxv1.ListUserSessionsRequest{
			User:     &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Id{Id: u1}},
			IdPrefix: "sesA",
		})
		require.NoError(t, err)
		require.Len(t, userResp.GetSessions(), 1)
		assert.Equal(t, s1, userResp.GetSessions()[0].GetId())

		_, err = svc.ListSessions(ctx, &leapmuxv1.ListSessionsRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		_, err = svc.ListUserSessions(ctx, &leapmuxv1.ListUserSessionsRequest{
			User:     &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Id{Id: u1}},
			IdPrefix: "bad\xff",
		})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("ListAPITokens filters by id_prefix", func(t *testing.T) {
		t1 := idPrefixMakeID(t, "tokA", "1")
		tRev := idPrefixMakeID(t, "tokA", "2")
		tOther := idPrefixMakeID(t, "tokB", "1")
		for _, tokID := range []string{t1, tRev, tOther} {
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID:               tokID,
				UserID:           userid.MustNew(u1),
				ClientID:         oauthapp.ControlCLIClientID,
				InstallationName: "cli",
				GrantedScopes:    authscope.NonAdminGrant().String(),
				Issuance:         store.APITokenIssuanceAuthorizationCode,
				SecretHash:       storetest.FixtureSecretHash("s-" + tokID),
				RefreshHash:      storetest.FixtureSecretHash("r-" + tokID),
			}))
		}
		_, err := st.APITokens().Revoke(ctx, tRev)
		require.NoError(t, err)

		liveResp, err := svc.ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{IdPrefix: "tokA"})
		require.NoError(t, err)
		require.Len(t, liveResp.GetTokens(), 1)
		assert.Equal(t, t1, liveResp.GetTokens()[0].GetId())

		allResp, err := svc.ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{
			IdPrefix:       "tokA",
			IncludeRevoked: true,
		})
		require.NoError(t, err)
		require.Len(t, allResp.GetTokens(), 2)
		assert.Equal(t, []string{t1, tRev}, []string{allResp.GetTokens()[0].GetId(), allResp.GetTokens()[1].GetId()})

		_, err = svc.ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("ListDelegationTokens filters by id_prefix", func(t *testing.T) {
		worker := storetest.SeedWorker(t, st, u1)
		d1 := idPrefixMakeID(t, "delA", "1")
		dRev := idPrefixMakeID(t, "delA", "2")
		dOther := idPrefixMakeID(t, "delB", "1")
		for _, tokID := range []string{d1, dRev, dOther} {
			require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
				ID:            tokID,
				UserID:        userid.MustNew(u1),
				WorkerID:      worker.ID,
				GrantedScopes: "workspace:read worker:read",
				SecretHash:    storetest.FixtureSecretHash("d-" + tokID),
				ExpiresAt:     time.Now().UTC().Add(time.Hour),
			}))
		}
		_, err := st.DelegationTokens().Revoke(ctx, dRev)
		require.NoError(t, err)

		liveResp, err := svc.ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{IdPrefix: "delA"})
		require.NoError(t, err)
		require.Len(t, liveResp.GetTokens(), 1)
		assert.Equal(t, d1, liveResp.GetTokens()[0].GetId())

		allResp, err := svc.ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{
			IdPrefix:       "delA",
			IncludeRevoked: true,
		})
		require.NoError(t, err)
		require.Len(t, allResp.GetTokens(), 2)
		assert.Equal(t, []string{d1, dRev}, []string{allResp.GetTokens()[0].GetId(), allResp.GetTokens()[1].GetId()})

		_, err = svc.ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

func TestIDPrefix_UserAndAppServices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	alice := storetest.SeedUser(t, st, "my-alice")
	bob := storetest.SeedUser(t, st, "my-bob")
	aliceCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(alice.ID)})
	adminCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(alice.ID), IsAdmin: true})

	t.Run("UserService.ListMyAPITokens filters by id_prefix", func(t *testing.T) {
		userSvc := service.NewUserService(service.UserServiceDeps{
			Store:     st,
			Lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil),
		})

		t1 := idPrefixMakeID(t, "mytA", "1")
		tBob := idPrefixMakeID(t, "mytA", "2")
		tOther := idPrefixMakeID(t, "mytB", "1")
		for _, tok := range []struct{ id, uid string }{
			{t1, alice.ID},
			{tBob, bob.ID},
			{tOther, alice.ID},
		} {
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID:               tok.id,
				UserID:           userid.MustNew(tok.uid),
				ClientID:         oauthapp.ControlCLIClientID,
				InstallationName: "laptop",
				GrantedScopes:    authscope.NonAdminGrant().String(),
				Issuance:         store.APITokenIssuanceAuthorizationCode,
				SecretHash:       storetest.FixtureSecretHash("s-" + tok.id),
				RefreshHash:      storetest.FixtureSecretHash("r-" + tok.id),
			}))
		}

		resp, err := userSvc.ListMyAPITokens(aliceCtx, &leapmuxv1.ListMyAPITokensRequest{IdPrefix: "mytA"})
		require.NoError(t, err)
		require.Len(t, resp.GetTokens(), 1)
		assert.Equal(t, t1, resp.GetTokens()[0].GetId())

		_, err = userSvc.ListMyAPITokens(aliceCtx, &leapmuxv1.ListMyAPITokensRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("AppService.ListApps filters by id_prefix", func(t *testing.T) {
		appSvc := service.NewAppService(st, servicetest.NewSettingsManager(t, st), nil, nil, nil)

		a1 := idPrefixMakeID(t, "appA", "1")
		aBob := idPrefixMakeID(t, "appA", "2")
		aOther := idPrefixMakeID(t, "appB", "1")
		for _, app := range []struct{ id, owner string }{
			{a1, alice.ID},
			{aBob, bob.ID},
			{aOther, alice.ID},
		} {
			_, err := st.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
				ClientID:           app.id,
				OwnerUserID:        app.owner,
				ClientName:         "App " + app.id,
				RedirectURIs:       "http://127.0.0.1/cb",
				Scopes:             "workspace:read",
				GrantTypes:         "authorization_code",
				RegistrationSource: store.OAuthClientSourceUser,
			})
			require.NoError(t, err)
		}

		resp, err := appSvc.ListApps(aliceCtx, &leapmuxv1.ListAppsRequest{IdPrefix: "appA"})
		require.NoError(t, err)
		require.Len(t, resp.GetApps(), 1)
		assert.Equal(t, a1, resp.GetApps()[0].GetClientId())

		builtInResp, err := appSvc.ListApps(adminCtx, &leapmuxv1.ListAppsRequest{IdPrefix: "leapmux-control"})
		require.NoError(t, err)
		require.Len(t, builtInResp.GetApps(), 1)
		assert.Equal(t, oauthapp.ControlCLIClientID, builtInResp.GetApps()[0].GetClientId())

		_, err = appSvc.ListApps(aliceCtx, &leapmuxv1.ListAppsRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

func TestIDPrefix_WorkerAndIdPServices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	alice := storetest.SeedUser(t, st, "wrk-svc-alice")
	bob := storetest.SeedUser(t, st, "wrk-svc-bob")
	aliceUID := userid.MustNew(alice.ID)
	aliceCtx := auth.WithUser(ctx, &auth.UserInfo{ID: aliceUID})

	w1 := idPrefixMakeID(t, "wrkA", "1")
	wBob := idPrefixMakeID(t, "wrkA", "2")
	wOther := idPrefixMakeID(t, "wrkB", "1")
	for _, w := range []struct{ id, owner string }{
		{w1, alice.ID},
		{wBob, bob.ID},
		{wOther, alice.ID},
	} {
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:            w.id,
			AuthTokenHash: storetest.FixtureSecretHash("w-" + w.id),
			RegisteredBy:  userid.MustNew(w.owner),
			PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
		}))
	}

	t.Run("WorkerManagementService.ListWorkers filters by id_prefix", func(t *testing.T) {
		workerSvc := service.NewWorkerManagementService(
			st,
			hubtestutil.TestSecretHasher(),
			workermgr.New(service.NewWorkerReachAuthorizer(st)),
			nil,
			mail.NewStubSender(),
			mail.Renderer{},
			servicetest.NewSettingsManager(t, st),
		)

		resp, err := workerSvc.ListWorkers(aliceCtx, &leapmuxv1.ListWorkersRequest{IdPrefix: "wrkA"})
		require.NoError(t, err)
		require.Len(t, resp.GetWorkers(), 1)
		assert.Equal(t, w1, resp.GetWorkers()[0].GetId())

		_, err = workerSvc.ListWorkers(aliceCtx, &leapmuxv1.ListWorkersRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("AdminWorkerService ListWorkers and ListRegistrationKeys filter by id_prefix", func(t *testing.T) {
		adminWorkerSvc := service.NewAdminWorkerService(st, &idPrefixWorkerPresence{}, nil)

		wResp, err := adminWorkerSvc.ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{IdPrefix: "wrkA"})
		require.NoError(t, err)
		require.Len(t, wResp.GetWorkers(), 2)
		assert.Equal(t, []string{w1, wBob}, []string{wResp.GetWorkers()[0].GetId(), wResp.GetWorkers()[1].GetId()})

		_, err = adminWorkerSvc.ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		k1 := idPrefixMakeID(t, "regA", "1")
		kExp := idPrefixMakeID(t, "regA", "2")
		kOther := idPrefixMakeID(t, "regB", "1")
		now := time.Now().UTC()
		for _, k := range []struct {
			id  string
			exp time.Time
		}{
			{k1, now.Add(time.Hour)},
			{kExp, now.Add(-time.Hour)},
			{kOther, now.Add(time.Hour)},
		} {
			require.NoError(t, st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
				ID:        k.id,
				KeyHash:   storetest.FixtureSecretHash("k-" + k.id),
				CreatedBy: aliceUID,
				ExpiresAt: k.exp,
			}))
		}

		kLive, err := adminWorkerSvc.ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{IdPrefix: "regA"})
		require.NoError(t, err)
		require.Len(t, kLive.GetKeys(), 1)
		assert.Equal(t, k1, kLive.GetKeys()[0].GetId())

		kAll, err := adminWorkerSvc.ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{
			IdPrefix:       "regA",
			IncludeExpired: true,
		})
		require.NoError(t, err)
		require.Len(t, kAll.GetKeys(), 2)
		assert.Equal(t, []string{k1, kExp}, []string{kAll.GetKeys()[0].GetId(), kAll.GetKeys()[1].GetId()})

		_, err = adminWorkerSvc.ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("AdminIdPService.ListOAuthProviders filters by id_prefix", func(t *testing.T) {
		idpSvc := service.NewAdminIdPService(st, &idPrefixProviderCache{})

		p1 := idPrefixMakeID(t, "idpA", "1")
		p2 := idPrefixMakeID(t, "idpA", "2")
		pOther := idPrefixMakeID(t, "idpB", "1")
		for _, p := range []struct{ id, name string }{
			{p2, "p2"},
			{p1, "p1"},
			{pOther, "pother"},
		} {
			require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
				ID:           p.id,
				ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
				Name:         p.name,
				IssuerURL:    "https://issuer.example.com",
				ClientID:     "cid-" + p.name,
				ClientSecret: []byte("sec-" + p.name),
				Scopes:       "openid",
				TrustEmail:   true,
				Enabled:      true,
			}))
		}

		filtered, err := idpSvc.ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{IdPrefix: "idpA"})
		require.NoError(t, err)
		require.Len(t, filtered.GetProviders(), 2)
		assert.Equal(t, []string{p1, p2}, []string{filtered.GetProviders()[0].GetId(), filtered.GetProviders()[1].GetId()})

		unfiltered, err := idpSvc.ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{IdPrefix: "   "})
		require.NoError(t, err)
		assert.Len(t, unfiltered.GetProviders(), 3)

		_, err = idpSvc.ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{IdPrefix: "bad\xff"})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

func TestIDPrefix_WorkspaceAndSectionServices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	alice := storetest.SeedUser(t, st, "ws-svc-alice")
	bob := storetest.SeedUser(t, st, "ws-svc-bob")
	aliceUID := userid.MustNew(alice.ID)
	bobUID := userid.MustNew(bob.ID)
	aliceCtx := auth.WithUser(ctx, &auth.UserInfo{ID: aliceUID})

	ws1 := idPrefixMakeID(t, "wspA", "1")
	ws2 := idPrefixMakeID(t, "wspA", "2")
	wsBob := idPrefixMakeID(t, "wspA", "3")
	wsOther := idPrefixMakeID(t, "wspB", "1")
	for _, ws := range []struct{ id, owner, title string }{
		{ws2, alice.ID, "WS 2"},
		{ws1, alice.ID, "WS 1"},
		{wsBob, bob.ID, "WS Bob"},
		{wsOther, alice.ID, "WS Other"},
	} {
		require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID: ws.id, OwnerUserID: userid.MustNew(ws.owner), Title: ws.title,
		}))
	}

	t.Run("WorkspaceService.ListWorkspaces filters by id_prefix", func(t *testing.T) {
		wsSvc := service.NewWorkspaceService(st, nil, nil)

		resp, err := wsSvc.ListWorkspaces(aliceCtx, &leapmuxv1.ListWorkspacesRequest{IdPrefix: "wspA"})
		require.NoError(t, err)
		require.Len(t, resp.GetWorkspaces(), 2)
		assert.Equal(t, []string{ws1, ws2}, []string{resp.GetWorkspaces()[0].GetId(), resp.GetWorkspaces()[1].GetId()})

		_, err = wsSvc.ListWorkspaces(aliceCtx, &leapmuxv1.ListWorkspacesRequest{IdPrefix: "bad\xff"})
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("WorkspaceService.ListTabs filters by id_prefix with and without workspace_ids", func(t *testing.T) {
		wAlice := storetest.SeedWorker(t, st, alice.ID)
		wBob := storetest.SeedWorker(t, st, bob.ID)

		tab1 := idPrefixMakeID(t, "tabA", "1")
		tab2 := idPrefixMakeID(t, "tabA", "2")
		tabOther := idPrefixMakeID(t, "tabB", "1")
		for _, row := range []store.UpsertTabParams{
			{UserID: aliceUID, WorkspaceID: ws1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab1, WorkerID: wAlice.ID, TileID: "t1", Position: "a0", Rendered: true},
			{UserID: aliceUID, WorkspaceID: ws2, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab2, WorkerID: wAlice.ID, TileID: "t2", Position: "a1", Rendered: true},
			{UserID: aliceUID, WorkspaceID: ws1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabOther, WorkerID: wAlice.ID, TileID: "t3", Position: "a2", Rendered: true},
			{UserID: bobUID, WorkspaceID: wsBob, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab1, WorkerID: wBob.ID, TileID: "tb", Position: "a0", Rendered: true},
		} {
			require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, row))
		}

		spy := &idPrefixSpyStore{Store: st}
		wsSvc := service.NewWorkspaceService(spy, nil, nil)

		// Without workspace_ids: queries ListRenderedByIDPrefix directly without ListAccessible.
		allMatching, err := wsSvc.ListTabs(aliceCtx, &leapmuxv1.ListTabsRequest{IdPrefix: "tabA"})
		require.NoError(t, err)
		require.Len(t, allMatching.GetTabs(), 2)
		assert.Equal(t, []string{tab1, tab2}, []string{allMatching.GetTabs()[0].GetTab().GetTabId(), allMatching.GetTabs()[1].GetTab().GetTabId()})
		assert.Zero(t, spy.listAccessibleCalls, "ListTabs with id_prefix and no workspace_ids must not call ListAccessible")

		// With workspace_ids: restricts to requested owned workspace.
		ws1Matching, err := wsSvc.ListTabs(aliceCtx, &leapmuxv1.ListTabsRequest{
			WorkspaceIds: []string{ws1},
			IdPrefix:     "tabA",
		})
		require.NoError(t, err)
		require.Len(t, ws1Matching.GetTabs(), 1)
		assert.Equal(t, tab1, ws1Matching.GetTabs()[0].GetTab().GetTabId())

		// With unowned workspace_id: returns empty.
		foreignResp, err := wsSvc.ListTabs(aliceCtx, &leapmuxv1.ListTabsRequest{
			WorkspaceIds: []string{wsBob},
			IdPrefix:     "tabA",
		})
		require.NoError(t, err)
		assert.Empty(t, foreignResp.GetTabs())

		// Invalid UTF-8 maps to InvalidArgument.
		_, err = wsSvc.ListTabs(aliceCtx, &leapmuxv1.ListTabsRequest{IdPrefix: "bad\xff"})
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("SectionService.ListSections filters sections and items by id_prefix", func(t *testing.T) {
		sec1 := idPrefixMakeID(t, "secA", "1")
		secOther := idPrefixMakeID(t, "secB", "1")
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: sec1, UserID: aliceUID, Name: "Alpha", Position: "a1",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secOther, UserID: aliceUID, Name: "Beta", Position: "a2",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: aliceUID, WorkspaceID: ws1, SectionID: sec1, Position: "a1",
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: aliceUID, WorkspaceID: ws2, SectionID: secOther, Position: "a1",
		}))

		spy := &idPrefixSpyStore{Store: st}
		secSvc := service.NewSectionService(spy)

		// Matching prefix returns only matching sections and their items.
		resp, err := secSvc.ListSections(aliceCtx, &leapmuxv1.ListSectionsRequest{IdPrefix: "secA"})
		require.NoError(t, err)
		require.Len(t, resp.GetSections(), 1)
		assert.Equal(t, sec1, resp.GetSections()[0].GetId())
		require.Len(t, resp.GetItems(), 1)
		assert.Equal(t, ws1, resp.GetItems()[0].GetWorkspaceId())
		assert.Equal(t, sec1, resp.GetItems()[0].GetSectionId())
		assert.Equal(t, 1, spy.listSectionItemsCalls)

		// Non-matching prefix returns empty without querying WorkspaceSectionItems.
		spy.listSectionItemsCalls = 0
		emptyResp, err := secSvc.ListSections(aliceCtx, &leapmuxv1.ListSectionsRequest{IdPrefix: "secNoMatch"})
		require.NoError(t, err)
		assert.Empty(t, emptyResp.GetSections())
		assert.Empty(t, emptyResp.GetItems())
		assert.Zero(t, spy.listSectionItemsCalls, "ListSections must skip WorkspaceSectionItems().ListByUser when zero sections match")

		// Invalid UTF-8 maps to InvalidArgument.
		_, err = secSvc.ListSections(aliceCtx, &leapmuxv1.ListSectionsRequest{IdPrefix: "bad\xff"})
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}
