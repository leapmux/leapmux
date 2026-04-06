package storetest

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCleanup(t *testing.T) {
	t.Run("hard delete expired sessions", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-sess-user")

		// Create an expired session.
		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "test",
			IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("hard delete workspaces before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", false)
		user := SeedUser(t, st, orgID, "cleanup-ws-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Old WS")

		// Soft-delete the workspace.
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)

		// Backdate the deleted_at timestamp.
		err = st.TestHelper().SetDeletedAt(ctx, store.EntityWorkspaces, wsID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteWorkspacesBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Workspace should be completely gone.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete workers before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-worker-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		err = st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, worker.ID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteWorkersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Workers().GetByID(ctx, worker.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete expired registrations before cutoff", func(t *testing.T) {
		st := s.NewStore(t)

		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(-48 * time.Hour),
		})
		require.NoError(t, err)

		// Expire it first.
		err = st.Registrations().ExpirePending(ctx)
		require.NoError(t, err)

		// Backdate created_at so the record falls before the cutoff.
		err = st.TestHelper().SetCreatedAt(ctx, store.EntityWorkerRegistrations, regID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteExpiredRegistrationsBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Registrations().GetByID(ctx, regID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete registrations skips pending", func(t *testing.T) {
		st := s.NewStore(t)

		// Create a pending registration (not yet expired).
		regID := id.Generate()
		err := st.Registrations().Create(ctx, store.CreateRegistrationParams{
			ID:              regID,
			Version:         "1.0.0",
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ExpiresAt:       time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		// Backdate created_at so it falls before the cutoff.
		err = st.TestHelper().SetCreatedAt(ctx, store.EntityWorkerRegistrations, regID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		// Cleanup should NOT delete a pending registration, even though
		// its created_at is before the cutoff.
		n, err := st.Cleanup().HardDeleteExpiredRegistrationsBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		// The pending registration should still exist.
		_, err = st.Registrations().GetByID(ctx, regID)
		require.NoError(t, err)
	})

	t.Run("hard delete users before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-del-user")

		err := st.Users().Delete(ctx, user.ID)
		require.NoError(t, err)

		err = st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, user.ID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteUsersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Users().GetByIDIncludeDeleted(ctx, user.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete orgs before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-del-org", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		err = st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete expired oauth states", func(t *testing.T) {
		st := s.NewStore(t)
		state := id.Generate()
		prov := SeedOAuthProvider(t, st, "cleanup-oauth-state-prov")

		err := st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        state,
			ProviderID:   prov.ID,
			PkceVerifier: "v",
			RedirectURI:  "https://example.com/cb",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		_, err = st.Cleanup().DeleteExpiredOAuthStates(ctx)
		require.NoError(t, err)

		_, err = st.OAuthStates().Get(ctx, state)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete expired pending oauth signups", func(t *testing.T) {
		st := s.NewStore(t)
		token := id.Generate()
		prov := SeedOAuthProvider(t, st, "cleanup-pending-signup-prov")

		err := st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
			Token:           token,
			ProviderID:      prov.ID,
			ProviderSubject: "sub",
			Email:           "expired@example.com",
			DisplayName:     "Exp",
			AccessToken:     []byte("a"),
			RefreshToken:    []byte("r"),
			TokenType:       "Bearer",
			TokenExpiresAt:  time.Now().Add(-2 * time.Hour),
			KeyVersion:      1,
			RedirectURI:     "https://example.com/cb",
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		_, err = st.Cleanup().DeleteExpiredPendingOAuthSignups(ctx)
		require.NoError(t, err)

		_, err = st.PendingOAuthSignups().Get(ctx, token)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("cleanup empty tables", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := time.Now()

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().HardDeleteWorkspacesBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().HardDeleteWorkersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().HardDeleteExpiredRegistrationsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().HardDeleteUsersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().HardDeleteOrgsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("cleanup idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "idem-org", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		err = st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		cutoff := time.Now().Add(-24 * time.Hour)

		// First cleanup should delete 1.
		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Second cleanup should delete 0.
		n, err = st.Cleanup().HardDeleteOrgsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("cleanup respects cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cutoff-org", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// Set deleted_at to exactly 24 hours ago, truncated to millisecond
		// precision so the value roundtrips identically across all backends
		// (e.g. TiDB's DATETIME(3) stores only milliseconds).
		deletedAt := time.Now().Add(-24 * time.Hour).Truncate(time.Millisecond)
		err = st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, deletedAt)
		require.NoError(t, err)

		// Use a cutoff exactly at the deleted_at time.
		// Records at exactly the cutoff should NOT be deleted (cutoff is exclusive).
		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, deletedAt)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		// Use a cutoff 1 second after deleted_at — now it should be deleted.
		n, err = st.Cleanup().HardDeleteOrgsBefore(ctx, deletedAt.Add(1*time.Second))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("hard delete expired sessions preserves active sessions", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-active-sess-user")

		// Create an active session.
		activeSess := SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: expiredID, UserID: user.ID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			UserAgent: "test", IPAddress: "127.0.0.1",
		})
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Active session should still exist.
		_, err = st.Sessions().GetByID(ctx, activeSess.ID)
		require.NoError(t, err)
	})

	t.Run("hard delete workspaces cascades to children", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", false)
		user := SeedUser(t, st, orgID, "cleanup-ws-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Cascade WS")

		// Create child records.
		require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: user.ID,
		}))
		require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "f1",
		}))
		require.NoError(t, st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID, LayoutJSON: `{"test":true}`,
		}))
		secID := id.Generate()
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secID, UserID: user.ID, Name: "Sec",
			Position: "a0", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: secID, Position: "a0",
		}))

		// Soft-delete and backdate.
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: user.ID})
		require.NoError(t, err)
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkspaces, wsID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteWorkspacesBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		access, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		assert.Empty(t, access)

		_, err = st.WorkspaceLayouts().Get(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		_, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		assert.Empty(t, tabs)
	})

	t.Run("hard delete workers cascades to children", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-wk-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Worker WS")

		// Create child records.
		require.NoError(t, st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: user.ID, GrantedBy: user.ID,
		}))
		require.NoError(t, st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID: id.Generate(), WorkerID: worker.ID,
			Type:    leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload: `{"test":true}`,
		}))
		require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "wk-f1",
		}))

		// Soft-delete and backdate.
		require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, worker.ID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteWorkersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		assert.Empty(t, grants)

		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		assert.Empty(t, notifs)
	})

	t.Run("hard delete users cascades to remaining children", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", false)
		user := SeedUser(t, st, orgID, "cleanup-user-cascade")

		// Create a second user to own the worker and workspace that will outlive
		// the user being deleted (simulating the real cleanup order where
		// workspaces/workers are cleaned before users).
		otherUser := SeedUser(t, st, orgID, "cleanup-other-user")
		worker := SeedWorker(t, st, otherUser.ID)
		wsID := SeedWorkspace(t, st, orgID, otherUser.ID, "User WS")

		// Create child records for user (not covered by workspace/worker cleanup).
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
		secID := id.Generate()
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secID, UserID: user.ID, Name: "UserSec",
			Position: "a0", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: secID, Position: "a0",
		}))
		require.NoError(t, st.WorkerAccessGrants().Grant(ctx, store.GrantWorkerAccessParams{
			WorkerID: worker.ID, UserID: user.ID, GrantedBy: user.ID,
		}))
		require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: user.ID,
		}))
		prov := SeedOAuthProvider(t, st, "cleanup-user-cascade-prov")
		require.NoError(t, st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID: user.ID, ProviderID: prov.ID,
			AccessToken: []byte("a"), RefreshToken: []byte("r"),
			TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour), KeyVersion: 1,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: user.ID, ProviderID: prov.ID, ProviderSubject: "user-cascade-sub",
		}))

		// Soft-delete and backdate.
		require.NoError(t, st.Users().Delete(ctx, user.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, user.ID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteUsersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		members, err := st.OrgMembers().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		for _, m := range members {
			assert.NotEqual(t, user.ID, m.UserID, "org member for deleted user should be cleaned up")
		}

		sections, err := st.WorkspaceSections().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, sections)

		items, err := st.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, items)

		grants, err := st.WorkerAccessGrants().List(ctx, worker.ID)
		require.NoError(t, err)
		for _, g := range grants {
			assert.NotEqual(t, user.ID, g.UserID, "worker access grant for deleted user should be cleaned up")
		}

		access, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		for _, a := range access {
			assert.NotEqual(t, user.ID, a.UserID, "workspace access for deleted user should be cleaned up")
		}

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: user.ID, ProviderID: prov.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		links, err := st.OAuthUserLinks().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("hard delete orgs cascades to org members", func(t *testing.T) {
		st := s.NewStore(t)
		// Use a separate org for the user's home org so the test org can be
		// deleted without FK violations (users.org_id → orgs(id) is RESTRICT).
		homeOrgID := SeedOrg(t, st, "cleanup-home-org", true)
		user := SeedUser(t, st, homeOrgID, "cleanup-org-cascade-user")

		orgID := SeedOrg(t, st, "cleanup-cascade-org", false)
		SeedOrgMember(t, st, orgID, user.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)

		// Soft-delete and backdate.
		require.NoError(t, st.Orgs().SoftDelete(ctx, orgID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify org members are gone.
		members, err := st.OrgMembers().ListByOrgID(ctx, orgID)
		require.NoError(t, err)
		assert.Empty(t, members)
	})

	t.Run("hard delete workers preserves non-deleted workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org", true)
		user := SeedUser(t, st, orgID, "cleanup-alive-worker-user")
		alive := SeedWorker(t, st, user.ID)
		dead := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, dead.ID)
		require.NoError(t, err)

		err = st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, dead.ID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteWorkersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Alive worker should still exist.
		_, err = st.Workers().GetByID(ctx, alive.ID)
		require.NoError(t, err)

		// Dead worker should be gone.
		_, err = st.Workers().GetByID(ctx, dead.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
