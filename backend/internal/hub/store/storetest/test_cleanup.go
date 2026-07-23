package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCleanup(t *testing.T) {
	t.Run("hard delete expired sessions", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-sess-user")

		// Create an expired session.
		sessID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID:        sessID,
			UserID:    userid.MustNew(user.ID),
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
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-ws-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Old WS")

		// Soft-delete the workspace.
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: userid.MustNew(user.ID),
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
		orgID := SeedOrg(t, st, "cleanup-org")
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

	t.Run("hard delete expired registration keys before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-keys-org")
		user := SeedUser(t, st, orgID, "cleanup-keys-user")

		// Create a key whose expires_at is already in the past — this is
		// the soft-deleted state our service layer leaves rows in after
		// either explicit Delete or successful Consume.
		regID := id.Generate()
		err := st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
			ID:        regID,
			CreatedBy: userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(-48 * time.Hour),
		})
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.RegistrationKeys().GetByID(ctx, regID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete registration keys skips live", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-live-keys-org")
		user := SeedUser(t, st, orgID, "cleanup-live-keys-user")

		// Create a live key (expires in the future). Even with an old
		// created_at it must NOT be deleted: only expires_at controls
		// retention now.
		regID := id.Generate()
		err := st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
			ID:        regID,
			CreatedBy: userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(1 * time.Hour),
		})
		require.NoError(t, err)

		err = st.TestHelper().SetCreatedAt(ctx, store.EntityWorkerRegistrationKeys, regID, time.Now().Add(-48*time.Hour))
		require.NoError(t, err)

		n, err := st.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		got, err := st.RegistrationKeys().GetByID(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, regID, got.ID)
	})

	t.Run("clear stale pending emails", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-stale-pending-org")
		user := SeedUser(t, st, orgID, "cleanup-stale-pending-user")

		// Seed a pending verification whose expires_at is in the past.
		// The cleanup loop frees the row's pending_email slot once the
		// expires_at is older than retention, so this exercises the
		// "expired-and-aged-out" path.
		past := time.Now().Add(-48 * time.Hour).UTC()
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "stale@example.com",
			PendingEmailToken:     "ABC123",
			PendingEmailExpiresAt: &past,
		})
		require.NoError(t, err)

		n, err := st.Cleanup().ClearStalePendingEmails(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// All four pending columns must be reset, including the attempts
		// counter — otherwise a later issuance starts mid-window.
		got, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, got.PendingEmail)
		assert.Empty(t, got.PendingEmailToken)
		assert.Nil(t, got.PendingEmailExpiresAt)
		assert.Zero(t, got.PendingEmailAttempts)
	})

	t.Run("clear stale pending emails skips live ones", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-live-pending-org")
		user := SeedUser(t, st, orgID, "cleanup-live-pending-user")

		// A live (future-dated) pending verification must NOT be cleared
		// — the code is still usable and wiping it would silently lock
		// the user out of completing verification.
		future := time.Now().Add(15 * time.Minute).UTC()
		err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "still-valid@example.com",
			PendingEmailToken:     "LIVE12",
			PendingEmailExpiresAt: &future,
		})
		require.NoError(t, err)

		n, err := st.Cleanup().ClearStalePendingEmails(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		got, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "still-valid@example.com", got.PendingEmail)
		assert.Equal(t, "LIVE12", got.PendingEmailToken)
	})

	t.Run("hard delete users before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org")
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
		orgID := SeedOrg(t, st, "cleanup-del-org")

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

	t.Run("hard delete orgs waits for referencing users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org-with-soft-deleted-user")
		user := SeedUser(t, st, orgID, "user-referencing-org")

		// Soft-delete both the user and the org it references and backdate them
		// past the retention cutoff. This mirrors the real cleanup shape: deleting
		// a user now soft-deletes its personal org in the same transaction, so both
		// become eligible for hard-delete together -- and the chunked users step
		// (LIMIT 1000) can leave a straggler soft-deleted user behind when the orgs
		// step runs.
		require.NoError(t, st.Users().Delete(ctx, user.ID))
		require.NoError(t, st.Orgs().SoftDelete(ctx, orgID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, user.ID, time.Now().Add(-48*time.Hour)))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, time.Now().Add(-48*time.Hour)))
		cutoff := time.Now().Add(-24 * time.Hour)

		// A referencing user -- even a soft-deleted one -- must keep the org from
		// being hard-deleted. users.org_id has no ON DELETE clause, so deleting the
		// org while a user references it would abort on a foreign-key violation
		// (and, where FKs are not enforced, leak a dangling user reference).
		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "an org a soft-deleted user still references must not be hard-deleted")

		// Once the referencing user is hard-deleted, the org is hard-deletable.
		n, err = st.Cleanup().HardDeleteUsersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		n, err = st.Cleanup().HardDeleteOrgsBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete users waits for referencing workspaces and workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "org-user-fk-gate")
		cutoff := time.Now().Add(-24 * time.Hour)
		back := time.Now().Add(-48 * time.Hour)

		// A soft-deleted user whose workspace has not yet been hard-deleted. Its
		// workspaces.owner_user_id references the user with no ON DELETE, so hard
		// deleting the user would abort on a foreign-key violation.
		wsUser := SeedUser(t, st, orgID, "user-with-straggler-ws")
		wsID := SeedWorkspace(t, st, orgID, wsUser.ID, "Straggler WS")
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(wsUser.ID)})
		require.NoError(t, err)
		require.NoError(t, st.Users().Delete(ctx, wsUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, wsUser.ID, back))

		// A soft-deleted user whose worker has not yet been hard-deleted.
		// workers.registered_by is the symmetric no-ON-DELETE reference.
		wkUser := SeedUser(t, st, orgID, "user-with-straggler-wk")
		wk := SeedWorker(t, st, wkUser.ID)
		require.NoError(t, st.Workers().MarkDeleted(ctx, wk.ID))
		require.NoError(t, st.Users().Delete(ctx, wkUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, wkUser.ID, back))

		// A soft-deleted user with no straggler references at all.
		cleanUser := SeedUser(t, st, orgID, "user-fk-free")
		require.NoError(t, st.Users().Delete(ctx, cleanUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, cleanUser.ID, back))

		// Without the gate this aborts the whole batch on the FK violation (err != nil,
		// 0 rows), so the FK-free user is never reaped despite being eligible. The gate
		// skips the two referenced users and reaps only the clean one.
		n, err := st.Cleanup().HardDeleteUsersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n, "one straggler reference must not poison the whole users batch")

		_, err = st.Users().GetByIDIncludeDeleted(ctx, cleanUser.ID)
		assert.ErrorIs(t, err, store.ErrNotFound, "the FK-free user must be reaped")
		_, err = st.Users().GetByIDIncludeDeleted(ctx, wsUser.ID)
		require.NoError(t, err, "a user still referenced by a workspace must not be reaped yet")
		_, err = st.Users().GetByIDIncludeDeleted(ctx, wkUser.ID)
		require.NoError(t, err, "a user still referenced by a worker must not be reaped yet")

		// Once the referencing rows drain, the blocked users become reapable.
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkspaces, wsID, back))
		_, err = st.Cleanup().HardDeleteWorkspacesBefore(ctx, cutoff)
		require.NoError(t, err)
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, wk.ID, back))
		_, err = st.Cleanup().HardDeleteWorkersBefore(ctx, cutoff)
		require.NoError(t, err)

		n, err = st.Cleanup().HardDeleteUsersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(2), n, "both previously-blocked users are reaped once their references drain")
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

		n, err = st.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		n, err = st.Cleanup().ClearStalePendingEmails(ctx, cutoff)
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
		orgID := SeedOrg(t, st, "idem-org")

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
		orgID := SeedOrg(t, st, "cutoff-org")

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
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-active-sess-user")

		// Create an active session.
		activeSess := SeedSession(t, st, user.ID)

		// Create an expired session.
		expiredID := id.Generate()
		err := st.Sessions().Create(ctx, store.CreateSessionParams{
			ID: expiredID, UserID: userid.MustNew(user.ID),
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
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-ws-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Cascade WS")

		// Create child records.
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			OrgID: orgID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "f1",
		}))
		secID := id.Generate()
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secID, UserID: userid.MustNew(user.ID), Name: "Sec",
			Position: "a0", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsID, SectionID: secID, Position: "a0",
		}))

		// Soft-delete and backdate.
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(user.ID)})
		require.NoError(t, err)
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkspaces, wsID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteWorkspacesBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		_, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		tabs, err := st.WorkspaceTabIndex().ListOwnedByWorkspace(ctx, wsID)
		require.NoError(t, err)
		assert.Empty(t, tabs)
	})

	t.Run("hard delete workers cascades to children", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-wk-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Worker WS")

		// Create child records.
		require.NoError(t, st.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
			ID: id.Generate(), WorkerID: worker.ID,
			Type:    leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER,
			Payload: `{"test":true}`,
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
			OrgID: orgID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "wk-f1",
		}))

		// Soft-delete and backdate.
		require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, worker.ID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteWorkersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		notifs, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker.ID)
		require.NoError(t, err)
		assert.Empty(t, notifs)
	})

	t.Run("hard delete users cascades to remaining children", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org")
		user := SeedUser(t, st, orgID, "cleanup-user-cascade")

		// Create a second user to own the workspace that will outlive the user
		// being deleted (simulating the real cleanup order where workspaces are
		// cleaned before users).
		otherUser := SeedUser(t, st, orgID, "cleanup-other-user")
		wsID := SeedWorkspace(t, st, orgID, otherUser.ID, "User WS")

		// Create child records for user (not covered by workspace cleanup).
		secID := id.Generate()
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secID, UserID: userid.MustNew(user.ID), Name: "UserSec",
			Position: "a0", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsID, SectionID: secID, Position: "a0",
		}))
		prov := SeedOAuthProvider(t, st, "cleanup-user-cascade-prov")
		require.NoError(t, st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID: userid.MustNew(user.ID), ProviderID: prov.ID,
			AccessToken: []byte("a"), RefreshToken: []byte("r"),
			TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour), KeyVersion: 1,
		}))
		require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(user.ID), ProviderID: prov.ID, ProviderSubject: "user-cascade-sub",
		}))

		// Soft-delete and backdate.
		require.NoError(t, st.Users().Delete(ctx, user.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, user.ID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteUsersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		sections, err := st.WorkspaceSections().ListByUserID(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		assert.Empty(t, sections)

		items, err := st.WorkspaceSectionItems().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		assert.Empty(t, items)

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: userid.MustNew(user.ID), ProviderID: prov.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("hard delete orgs reaps backdated soft-deleted orgs", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-cascade-org")

		// Soft-delete and backdate.
		require.NoError(t, st.Orgs().SoftDelete(ctx, orgID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteOrgsBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// The org row is hard-deleted.
		_, err = st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("hard delete workers preserves non-deleted workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "cleanup-org")
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
