package storetest

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkers(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "worker-owner")
		worker := SeedWorker(t, st, user.ID)

		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, found.ID)
		assert.Equal(t, user.ID, found.RegisteredBy)
		assert.NotEmpty(t, found.AuthToken)
		assert.False(t, found.CreatedAt.IsZero())
		assert.Nil(t, found.DeletedAt)
	})

	t.Run("get by auth token", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "token-owner")
		worker := SeedWorker(t, st, user.ID)

		found, err := st.Workers().GetByAuthToken(ctx, worker.AuthToken)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, found.ID)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Workers().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get owned", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "owned-user")
		worker := SeedWorker(t, st, user.ID)

		found, err := st.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
			WorkerID: worker.ID,
			UserID:   user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, worker.ID, found.ID)
	})

	t.Run("get owned wrong user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "owned-user2")
		worker := SeedWorker(t, st, user.ID)

		_, err := st.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
			WorkerID: worker.ID,
			UserID:   "other-user",
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get owned rejects non-owner in same org", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		owner := SeedUser(t, st, orgID, "getowned-owner")
		other := SeedUser(t, st, orgID, "getowned-other")
		worker := SeedWorker(t, st, owner.ID)

		// A worker serves only the user it is registered to. Sharing an org --
		// or a workspace -- conveys no access to another user's worker.
		_, err := st.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
			WorkerID: worker.ID,
			UserID:   other.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		// The registering owner still gets it.
		found, err := st.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
			WorkerID: worker.ID,
			UserID:   owner.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, worker.ID, found.ID)
	})

	t.Run("list by user id excludes other users workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		owner := SeedUser(t, st, orgID, "listowned-owner")
		other := SeedUser(t, st, orgID, "listowned-other")
		ownWorker := SeedWorker(t, st, other.ID)
		foreignWorker := SeedWorker(t, st, owner.ID)

		// ListByUserID is scoped strictly to registered_by: a user sees their own
		// workers and nothing else, even for co-members of the same org.
		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: other.ID,
			Limit:        10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, ownWorker.ID, workers[0].ID)

		ids := map[string]bool{}
		for _, w := range workers {
			ids[w.ID] = true
		}
		assert.False(t, ids[foreignWorker.ID], "must not include another user's worker")
	})

	t.Run("list admin excludes deleted workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "admin-del-user")
		alive := SeedWorker(t, st, user.ID)
		dead := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, dead.ID)
		require.NoError(t, err)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Limit: 10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, alive.ID, workers[0].ID)
	})

	t.Run("list admin filter by user id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user1 := SeedUser(t, st, orgID, "admin-u1")
		user2 := SeedUser(t, st, orgID, "admin-u2")
		w1 := SeedWorker(t, st, user1.ID)
		SeedWorker(t, st, user2.ID)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			UserID: &user1.ID,
			Limit:  10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, w1.ID, workers[0].ID)
	})

	t.Run("get by auth token excluded after mark deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "token-del-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		_, err = st.Workers().GetByAuthToken(ctx, worker.AuthToken)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "list-user")
		SeedWorker(t, st, user.ID)
		SeedWorker(t, st, user.ID)

		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID,
			Limit:        10,
		})
		require.NoError(t, err)
		assert.Len(t, workers, 2)
	})

	t.Run("list admin", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "admin-list-user")
		SeedWorker(t, st, user.ID)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Limit: 10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, "admin-list-user", workers[0].OwnerUsername)
	})

	t.Run("list admin filter by status", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "status-filter-user")
		w := SeedWorker(t, st, user.ID)

		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID:     w.ID,
			Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Status: ptrconv.Ptr(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE),
			Limit:  10,
		})
		require.NoError(t, err)
		assert.Len(t, workers, 1)

		workers, err = st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Status: ptrconv.Ptr(leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING),
			Limit:  10,
		})
		require.NoError(t, err)
		require.NotNil(t, workers)
		assert.Empty(t, workers)
	})

	t.Run("set status", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "status-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID:     worker.ID,
			Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)

		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, found.Status)
	})

	t.Run("update last seen", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "lastseen-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().UpdateLastSeen(ctx, worker.ID)
		require.NoError(t, err)

		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.NotNil(t, found.LastSeenAt)
	})

	t.Run("update public key", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "pubkey-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().UpdatePublicKey(ctx, store.UpdateWorkerPublicKeyParams{
			ID:              worker.ID,
			PublicKey:       []byte("classic-key"),
			MlkemPublicKey:  []byte("mlkem-key"),
			SlhdsaPublicKey: []byte("slhdsa-key"),
		})
		require.NoError(t, err)

		keys, err := st.Workers().GetPublicKey(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, []byte("classic-key"), keys.PublicKey)
		assert.Equal(t, []byte("mlkem-key"), keys.MlkemPublicKey)
		assert.Equal(t, []byte("slhdsa-key"), keys.SlhdsaPublicKey)
	})

	t.Run("deregister", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "dereg-user")
		worker := SeedWorker(t, st, user.ID)

		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           worker.ID,
			RegisteredBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("deregister wrong user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "dereg-wrong-user")
		worker := SeedWorker(t, st, user.ID)

		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           worker.ID,
			RegisteredBy: "other-user",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("force deregister", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "force-dereg-user")
		worker := SeedWorker(t, st, user.ID)

		n, err := st.Workers().ForceDeregister(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("mark deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "markdel-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		// Soft-deleted rows are hidden from GetByID; use the audit variant.
		_, err = st.Workers().GetByID(ctx, worker.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		found, err := st.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
		require.NoError(t, err)
		assert.NotNil(t, found.DeletedAt)
	})

	t.Run("mark all deleted by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "markall-user")
		SeedWorker(t, st, user.ID)
		SeedWorker(t, st, user.ID)

		err := st.Workers().MarkAllDeletedByUser(ctx, user.ID)
		require.NoError(t, err)

		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID,
			Limit:        10,
		})
		require.NoError(t, err)
		for _, w := range workers {
			assert.NotNil(t, w.DeletedAt)
		}
	})

	t.Run("create with public keys", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "pk-worker-user")

		workerID := id.Generate()
		err := st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:              workerID,
			AuthToken:       id.Generate(),
			RegisteredBy:    user.ID,
			PublicKey:       []byte("pk"),
			MlkemPublicKey:  []byte("mlkem"),
			SlhdsaPublicKey: []byte("slhdsa"),
		})
		require.NoError(t, err)

		keys, err := st.Workers().GetPublicKey(ctx, workerID)
		require.NoError(t, err)
		assert.Equal(t, []byte("pk"), keys.PublicKey)
		assert.Equal(t, []byte("mlkem"), keys.MlkemPublicKey)
		assert.Equal(t, []byte("slhdsa"), keys.SlhdsaPublicKey)
	})

	t.Run("get by auth token not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Workers().GetByAuthToken(ctx, "nonexistent-token")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get public key of deleted worker returns not found", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "pubkey-del-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		_, err = st.Workers().GetPublicKey(ctx, worker.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get public key not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Workers().GetPublicKey(ctx, "nonexistent-worker")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "no-workers-user")

		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID,
			Limit:        10,
		})
		require.NoError(t, err)
		require.NotNil(t, workers)
		assert.Empty(t, workers)
	})

	t.Run("list admin empty", func(t *testing.T) {
		st := s.NewStore(t)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Limit: 10,
		})
		require.NoError(t, err)
		require.NotNil(t, workers)
		assert.Empty(t, workers)
	})

	t.Run("mark deleted excludes from list by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "markdel-list-user")
		alive := SeedWorker(t, st, user.ID)
		dead := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, dead.ID)
		require.NoError(t, err)

		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID,
			Limit:        10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, alive.ID, workers[0].ID)
	})

	t.Run("mark all deleted by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "markall-empty-user")

		// Should be a no-op when user has no workers.
		err := st.Workers().MarkAllDeletedByUser(ctx, user.ID)
		require.NoError(t, err)
	})

	t.Run("deregister changes status but worker still fetchable", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "dereg-fetch-user")
		worker := SeedWorker(t, st, user.ID)

		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           worker.ID,
			RegisteredBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// GetByID should still return the worker (not ErrNotFound).
		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, found.ID)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, found.Status)
	})

	t.Run("newly created worker has correct initial status", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "init-status-user")
		worker := SeedWorker(t, st, user.ID)

		// SQLite creates workers with DEFAULT 1 (WORKER_STATUS_ACTIVE).
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, worker.Status)
	})

	t.Run("deregister already deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "dereg-deleted-user")
		worker := SeedWorker(t, st, user.ID)

		// Deregister once.
		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           worker.ID,
			RegisteredBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Deregister again should return 0 (already deregistered).
		n, err = st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           worker.ID,
			RegisteredBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("get owned excludes deleted worker", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		owner := SeedUser(t, st, orgID, "getowned-del-owner")
		worker := SeedWorker(t, st, owner.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		// GetOwned returns ErrNotFound for a deleted worker, even for its owner.
		_, err = st.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
			WorkerID: worker.ID, UserID: owner.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list admin returns deregistering workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "admin-dereg-user")
		w := SeedWorker(t, st, user.ID)

		// Set worker to DEREGISTERING status.
		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID:     w.ID,
			Status: leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
		})
		require.NoError(t, err)

		// ListAdmin with status=DEREGISTERING should find it.
		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Status: ptrconv.Ptr(leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING),
			Limit:  10,
		})
		require.NoError(t, err)
		assert.Len(t, workers, 1)
		assert.Equal(t, w.ID, workers[0].ID)

		// ListAdmin with no status filter should also include it.
		workers, err = st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Limit: 10,
		})
		require.NoError(t, err)
		found := false
		for _, wo := range workers {
			if wo.ID == w.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "deregistering worker should appear in unfiltered admin list")
	})

	t.Run("list by user id excludes deregistering workers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "owned-dereg-user")
		active := SeedWorker(t, st, user.ID)
		dereg := SeedWorker(t, st, user.ID)

		// Set one worker to DEREGISTERING.
		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID:     dereg.ID,
			Status: leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
		})
		require.NoError(t, err)

		// ListByUserID filters on status = 1, so only the active worker comes back.
		workers, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID, Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, workers, 1)
		assert.Equal(t, active.ID, workers[0].ID)
	})

	t.Run("list admin filter by user and status", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "admin-combo-user")
		w1 := SeedWorker(t, st, user.ID)
		w2 := SeedWorker(t, st, user.ID)

		// Set w1 to ACTIVE, w2 stays at default.
		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID: w1.ID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)
		err = st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID: w2.ID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
		})
		require.NoError(t, err)

		workers, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			UserID: &user.ID,
			Status: ptrconv.Ptr(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE),
			Limit:  10,
		})
		require.NoError(t, err)
		require.Len(t, workers, 1)
		assert.Equal(t, w1.ID, workers[0].ID)
	})

	t.Run("set status on deleted worker is no-op", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "setstatus-del-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		// SetStatus on a deleted worker should not error.
		err = st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID: worker.ID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)
	})

	t.Run("force deregister deleted worker returns zero", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "forcedereg-del-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		n, err := st.Workers().ForceDeregister(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("duplicate worker id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "dup-worker-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().Create(ctx, store.CreateWorkerParams{
			ID: worker.ID, AuthToken: id.Generate(), RegisteredBy: user.ID,
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("list by user id with cursor and limit", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "page-owned-user")
		for i := 0; i < 5; i++ {
			if i > 0 {
				time.Sleep(5 * time.Millisecond)
			}
			SeedWorker(t, st, user.ID)
		}

		// First page: newest first (ORDER BY created_at DESC).
		page1, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID, Limit: 3,
		})
		require.NoError(t, err)
		require.Len(t, page1, 3)
		for i := 1; i < len(page1); i++ {
			assert.False(t, page1[i].CreatedAt.After(page1[i-1].CreatedAt),
				"page 1 must be ordered newest first")
		}

		// Second page using the cursor from the last item of page 1. The cursor
		// is exclusive (created_at < cursor), so the remaining 2 come back.
		cursor := page1[len(page1)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
		page2, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: user.ID, Cursor: cursor, Limit: 3,
		})
		require.NoError(t, err)
		assert.Len(t, page2, 2, "remaining 2 workers should be on page 2")

		// No overlap between pages, and page 2 is strictly older than the cursor.
		seen := map[string]bool{}
		for _, w := range page1 {
			seen[w.ID] = true
		}
		for _, w := range page2 {
			assert.False(t, seen[w.ID], "page 2 should not contain workers from page 1")
			assert.True(t, w.CreatedAt.Before(page1[len(page1)-1].CreatedAt),
				"page 2 rows must be strictly older than the cursor")
		}
	})

	t.Run("update public key reflected in get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "pk-getbyid-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().UpdatePublicKey(ctx, store.UpdateWorkerPublicKeyParams{
			ID:              worker.ID,
			PublicKey:       []byte("new-classic"),
			MlkemPublicKey:  []byte("new-mlkem"),
			SlhdsaPublicKey: []byte("new-slhdsa"),
		})
		require.NoError(t, err)

		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, []byte("new-classic"), found.PublicKey)
		assert.Equal(t, []byte("new-mlkem"), found.MlkemPublicKey)
		assert.Equal(t, []byte("new-slhdsa"), found.SlhdsaPublicKey)
	})

	t.Run("mark deleted is idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "markdel-idem-user")
		worker := SeedWorker(t, st, user.ID)

		err := st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)

		// Second call should not error.
		err = st.Workers().MarkDeleted(ctx, worker.ID)
		require.NoError(t, err)
	})

	t.Run("force deregister already deregistering returns zero", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "forcedereg-dereg-user")
		worker := SeedWorker(t, st, user.ID)

		// Deregister first.
		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID: worker.ID, RegisteredBy: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Force deregister an already-deregistering worker should return 0.
		n, err = st.Workers().ForceDeregister(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("set status idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "setstatus-idem-user")
		worker := SeedWorker(t, st, user.ID)

		// Set to ACTIVE.
		err := st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID: worker.ID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)

		// Set to ACTIVE again — should be a no-op, not an error.
		err = st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
			ID: worker.ID, Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		})
		require.NoError(t, err)

		found, err := st.Workers().GetByID(ctx, worker.ID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, found.Status)
	})

	t.Run("list admin with cursor and limit", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "worker-org")
		user := SeedUser(t, st, orgID, "page-admin-user")
		for i := 0; i < 5; i++ {
			if i > 0 {
				time.Sleep(5 * time.Millisecond)
			}
			SeedWorker(t, st, user.ID)
		}

		// First page.
		page1, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, page1, 2)

		// Second page using cursor.
		cursor := page1[len(page1)-1].CreatedAt.Format(time.RFC3339Nano)
		page2, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Cursor: cursor, Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, page2, 2)
	})
}
