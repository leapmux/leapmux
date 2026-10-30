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
		user := SeedUser(t, st, "cleanup-sess-user")

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

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("hard delete workspaces before cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-ws-user")
		wsID := SeedWorkspace(t, st, user.ID, "Old WS")

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
		user := SeedUser(t, st, "cleanup-worker-user")
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
		user := SeedUser(t, st, "cleanup-keys-user")

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
		user := SeedUser(t, st, "cleanup-live-keys-user")

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
		user := SeedUser(t, st, "cleanup-stale-pending-user")

		// Seed a pending verification whose expires_at is in the past.
		// The cleanup loop frees the row's pending_email slot once the
		// expires_at is older than retention, so this exercises the
		// "expired-and-aged-out" path.
		past := time.Now().Add(-48 * time.Hour).UTC()
		_, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      user.ID,
			PendingEmail:            "stale@example.com",
			PendingEmailToken:       "ABC123",
			PendingEmailExpiresAt:   &past,
			PendingEmailUnblockedAt: past.Add(time.Minute),
			Now:                     past,
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

	// A send the relay refused leaves the ADDRESS with no token and no
	// expiry, so the expiry compare can never see it. Without the second
	// branch that row outlived the database: the sweep it is subject to
	// matched neither predicate, and an abandoned address stayed forever.
	t.Run("clear stale pending emails reaps a codeless address", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-codeless-pending-user")

		expires := time.Now().Add(30 * time.Minute).UTC()
		minted, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      user.ID,
			PendingEmail:            "undelivered@example.com",
			PendingEmailToken:       "DEAD12",
			PendingEmailExpiresAt:   &expires,
			PendingEmailUnblockedAt: time.Now().UTC().Add(time.Minute),
			Now:                     time.Now().UTC(),
		})
		require.NoError(t, err)
		require.True(t, minted)
		// The failed-send state: the code is gone, the address remains. The
		// reap tests care about retention, not the retry window.
		require.NoError(t, st.Users().ClearPendingEmailCode(ctx, store.ClearPendingEmailCodeParams{
			ID:          user.ID,
			UnblockedAt: time.Time{},
		}))

		// A cutoff before the row's updated_at leaves it alone: the address
		// is recent, and the user can still ask for a fresh code.
		n, err := st.Cleanup().ClearStalePendingEmails(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "a recent codeless address is not stale yet")

		// A cutoff past it reaps the address.
		n, err = st.Cleanup().ClearStalePendingEmails(ctx, time.Now().Add(24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		got, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, got.PendingEmail, "an abandoned address must not outlive the retention window")
	})

	t.Run("clear stale pending emails skips live ones", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-live-pending-user")

		// A live (future-dated) pending verification must NOT be cleared
		// — the code is still usable and wiping it would silently lock
		// the user out of completing verification.
		future := time.Now().Add(15 * time.Minute).UTC()
		_, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                      user.ID,
			PendingEmail:            "still-valid@example.com",
			PendingEmailToken:       "LIVE12",
			PendingEmailExpiresAt:   &future,
			PendingEmailUnblockedAt: time.Now().UTC().Add(time.Minute),
			Now:                     time.Now().UTC(),
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
		user := SeedUser(t, st, "cleanup-del-user")

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

	t.Run("hard delete users waits for the workspaces and workers that refer to them", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := time.Now().Add(-24 * time.Hour)
		back := time.Now().Add(-48 * time.Hour)

		// A soft-deleted user whose workspace the sweep did not hard-delete yet.
		// Its workspaces.owner_user_id refers to the user with no ON DELETE, so
		// hard deleting the user would abort on a foreign-key violation.
		wsUser := SeedUser(t, st, "user-with-straggler-ws")
		wsID := SeedWorkspace(t, st, wsUser.ID, "Straggler WS")
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(wsUser.ID)})
		require.NoError(t, err)
		require.NoError(t, st.Users().Delete(ctx, wsUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, wsUser.ID, back))

		// A soft-deleted user whose worker the sweep did not hard-delete yet.
		// workers.registered_by is the symmetric no-ON-DELETE reference.
		wkUser := SeedUser(t, st, "user-with-straggler-wk")
		wk := SeedWorker(t, st, wkUser.ID)
		require.NoError(t, st.Workers().MarkDeleted(ctx, wk.ID))
		require.NoError(t, st.Users().Delete(ctx, wkUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, wkUser.ID, back))

		// A soft-deleted user with no straggler references at all.
		cleanUser := SeedUser(t, st, "user-fk-free")
		require.NoError(t, st.Users().Delete(ctx, cleanUser.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, cleanUser.ID, back))

		// Without the gate this aborts the whole batch on the FK violation (err != nil,
		// 0 rows), so the FK-free user is never reaped despite being eligible. The gate
		// skips the two users with live references and reaps only the clean one.
		n, err := st.Cleanup().HardDeleteUsersBefore(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n, "one straggler reference must not poison the whole users batch")

		_, err = st.Users().GetByIDIncludeDeleted(ctx, cleanUser.ID)
		assert.ErrorIs(t, err, store.ErrNotFound, "the FK-free user must be reaped")
		_, err = st.Users().GetByIDIncludeDeleted(ctx, wsUser.ID)
		require.NoError(t, err, "a user a workspace still refers to must not be reaped yet")
		_, err = st.Users().GetByIDIncludeDeleted(ctx, wkUser.ID)
		require.NoError(t, err, "a user a worker still refers to must not be reaped yet")

		// Once the rows that refer to them drain, the blocked users become reapable.
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
			Purpose:      store.OAuthStatePurposeLogin,
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		_, err = st.Cleanup().DeleteExpiredOAuthStates(ctx, time.Now().UTC())
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

		_, err = st.Cleanup().DeleteExpiredPendingOAuthSignups(ctx, time.Now().UTC())
		require.NoError(t, err)

		_, err = st.PendingOAuthSignups().Get(ctx, token)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("cleanup empty tables", func(t *testing.T) {
		st := s.NewStore(t)
		cutoff := time.Now()

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx, time.Now().UTC())
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
	})

	t.Run("hard delete expired sessions preserves active sessions", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-active-sess-user")

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

		n, err := st.Cleanup().HardDeleteExpiredSessions(ctx, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Active session should still exist.
		_, err = st.Sessions().GetByID(ctx, activeSess.ID, time.Now().UTC())
		require.NoError(t, err)
	})

	t.Run("hard delete workspaces cascades to children", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-ws-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, user.ID, "Cascade WS")

		// Create child records.
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
			UserID: userid.MustNew(user.ID), WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "f1",
			TileID: "tile-1", Position: "a0",
			Rendered: false,
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

		_, err = st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{
			UserID: userid.MustNew(user.ID), TabID: "f1",
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "the workspace delete must cascade to its owned-tab rows")
	})

	// delegation_tokens.worker_id is the one foreign key into workers, ON DELETE
	// CASCADE: a hard-deleted worker takes the tokens minted on it along.
	t.Run("hard delete workers cascades to children", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-wk-cascade-user")
		worker := SeedWorker(t, st, user.ID)
		tokenID := id.Generate()
		require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
			GrantedScopes: "workspace:read",
			ID:            tokenID,
			UserID:        userid.MustNew(user.ID),
			WorkerID:      worker.ID,
			SecretHash:    []byte("hash"),
			ExpiresAt:     time.Now().Add(time.Hour),
		}))

		// Soft-delete and backdate.
		require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
		require.NoError(t, st.TestHelper().SetDeletedAt(ctx, store.EntityWorkers, worker.ID, time.Now().Add(-48*time.Hour)))

		n, err := st.Cleanup().HardDeleteWorkersBefore(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Verify children are gone.
		_, err = st.DelegationTokens().GetByID(ctx, tokenID)
		assert.ErrorIs(t, err, store.ErrNotFound, "the worker's delegation tokens must go with it")
	})

	// The sweep that finishes every deregistration the notifier could not:
	// it deletes a worker deregistering since before the cutoff, and leaves
	// every other worker exactly as it was.
	t.Run("complete stale deregistrations deletes only workers deregistering since before the cutoff", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-stale-dereg-user")
		stale := SeedWorker(t, st, user.ID)
		recent := SeedWorker(t, st, user.ID)
		active := SeedWorker(t, st, user.ID)
		alreadyDeleted := SeedWorker(t, st, user.ID)
		for _, w := range []string{stale.ID, recent.ID} {
			_, err := st.Workers().ForceDeregister(ctx, w)
			require.NoError(t, err)
		}
		require.NoError(t, st.Workers().MarkDeleted(ctx, alreadyDeleted.ID))
		deletedBefore, err := st.Workers().GetByIDIncludeDeleted(ctx, alreadyDeleted.ID)
		require.NoError(t, err)
		recentRow, err := st.Workers().GetByID(ctx, recent.ID)
		require.NoError(t, err)
		require.NotNil(t, recentRow.DeregisteringAt)

		// Move the stale worker's intent back past a cutoff that sits just
		// before the recent worker's own stamp.
		cutoff := recentRow.DeregisteringAt.Add(-time.Minute)
		require.NoError(t, st.TestHelper().SetDeregisteringAt(ctx, stale.ID, cutoff.Add(-time.Hour)))

		n, err := st.Cleanup().CompleteStaleDeregistrations(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n, "only the worker deregistering since before the cutoff")

		got, err := st.Workers().GetByIDIncludeDeleted(ctx, stale.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, got.Status)
		got, err = st.Workers().GetByID(ctx, recent.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, got.Status, "a recent deregistration keeps waiting")
		got, err = st.Workers().GetByID(ctx, active.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, got.Status, "an active worker is untouched")
		got, err = st.Workers().GetByIDIncludeDeleted(ctx, alreadyDeleted.ID)
		require.NoError(t, err)
		require.NotNil(t, got.DeletedAt)
		assert.True(t, deletedBefore.DeletedAt.Equal(*got.DeletedAt), "an already-deleted worker keeps its deletion time")

		n, err = st.Cleanup().CompleteStaleDeregistrations(ctx, cutoff)
		require.NoError(t, err)
		assert.Zero(t, n, "a second pass finds nothing")
	})

	// The sweep's LIMIT 1000 must hold in every dialect, and the drain loop
	// must finish the rest.
	t.Run("complete stale deregistrations drains more than one batch", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-stale-dereg-bulk")
		const (
			seeded    = 1200
			sweepStep = 1000 // the LIMIT literal in every dialect's sweep
		)
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			for range seeded {
				workerID := id.Generate()
				require.NoError(t, tx.Workers().Create(ctx, store.CreateWorkerParams{
					ID: workerID, AuthToken: id.Generate(), RegisteredBy: userid.MustNew(user.ID),
					PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
				}))
				_, err := tx.Workers().ForceDeregister(ctx, workerID)
				require.NoError(t, err)
			}
			return nil
		}))
		// A cutoff after every stamp makes every seeded worker stale.
		cutoff := time.Now().UTC().Add(time.Hour)
		n, err := st.Cleanup().CompleteStaleDeregistrations(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(sweepStep), n, "one pass changes exactly LIMIT rows")
		n, err = st.Cleanup().CompleteStaleDeregistrations(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(seeded-sweepStep), n)
		n, err = st.Cleanup().CompleteStaleDeregistrations(ctx, cutoff)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("hard delete users cascades to remaining children", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-user-cascade")

		// Create child records for user (not covered by workspace cleanup).
		// No section item: a placement must name a workspace its own user
		// owns, and a user who still owns a workspace is not hard-deleted
		// (the cleanup loop removes workspaces first, which cascades their
		// placements away).
		secID := id.Generate()
		require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID: secID, UserID: userid.MustNew(user.ID), Name: "UserSec",
			Position: "a0", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT,
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

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: userid.MustNew(user.ID), ProviderID: prov.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("hard delete workers preserves non-deleted workers", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "cleanup-alive-worker-user")
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
