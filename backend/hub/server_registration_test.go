package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func TestRegisterWorkerRequiresALiveOwner(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "live owner", true: "deleted owner"}[deleted], func(t *testing.T) {
			st := hubtestutil.OpenTestStore(t)
			ctx := context.Background()
			owner := storetest.SeedUser(t, st, "auto-worker-owner")
			if deleted {
				require.NoError(t, st.Users().Delete(ctx, owner.ID))
			}
			keys := workerkeystest.Keys(0x31)
			credentials, err := (&Server{store: st}).RegisterWorker(ctx, owner.ID, keys)
			if deleted {
				assert.ErrorIs(t, err, store.ErrNotFound)
				assert.Nil(t, credentials)
			} else {
				require.NoError(t, err)
				worker, err := st.Workers().GetByID(ctx, credentials.WorkerID)
				require.NoError(t, err)
				assert.True(t, worker.AutoRegistered)
				assert.Equal(t, owner.ID, worker.RegisteredBy)
				assert.Equal(t, store.WorkerPublicKeysFromProto(keys), worker.PublicKeys,
					"the in-process registration pins the keys it was handed")
				// The co-located worker connects with the token it was handed,
				// so that token must resolve to this row.
				byToken, err := st.Workers().GetByAuthTokenIncludeDeleted(ctx, credentials.AuthToken)
				require.NoError(t, err)
				assert.Equal(t, credentials.WorkerID, byToken.ID)
			}
		})
	}
}

// A worker with a blank registrant would be owned by nobody, so its real
// owner's gate would refuse it for the life of the process.
func TestRegisterWorkerRefusesABlankRegistrant(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)

	credentials, err := (&Server{store: st}).RegisterWorker(context.Background(), "", workerkeystest.Keys(0x31))
	require.ErrorContains(t, err, "registeredBy is required")
	assert.Nil(t, credentials)
}

// An incomplete key set is refused by the store, which is the only write path,
// and no row is created.
func TestRegisterWorkerRefusesAnIncompleteKeySet(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "auto-worker-keys-owner")

	short := workerkeystest.Keys(0x31)
	short.Mlkem = short.Mlkem[:100]

	credentials, err := (&Server{store: st}).RegisterWorker(ctx, owner.ID, short)
	require.ErrorIs(t, err, store.ErrInvalidArgument)
	assert.Nil(t, credentials)

	page, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{PageParams: store.PageParams{Limit: 10}})
	require.NoError(t, err)
	assert.Empty(t, page.Rows, "a refused registration must create no row")
}

// GetWorkerRegistration answers the launcher's one question -- owner, state and
// pinned keys -- and a soft-deleted row reads as absent, which is what the solo
// launcher's not-found branch and RetireLocalWorker rely on.
func TestGetWorkerRegistrationReturnsTheOwnerStatusAndKeys(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "registration-owner")
	keys := workerkeystest.Keys(0x31)
	server := &Server{store: st}

	credentials, err := server.RegisterWorker(ctx, owner.ID, keys)
	require.NoError(t, err)

	got, err := server.GetWorkerRegistration(ctx, credentials.WorkerID)
	require.NoError(t, err)
	assert.Equal(t, owner.ID, got.Owner)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, got.Status)
	assert.Equal(t, keys.GetX25519(), got.PublicKeys.GetX25519())
	assert.Equal(t, keys.GetMlkem(), got.PublicKeys.GetMlkem())
	assert.Equal(t, keys.GetSlhdsa(), got.PublicKeys.GetSlhdsa())

	require.NoError(t, st.Workers().MarkDeleted(ctx, credentials.WorkerID))
	_, err = server.GetWorkerRegistration(ctx, credentials.WorkerID)
	assert.ErrorIs(t, err, store.ErrNotFound, "a soft-deleted row must read as absent")
}

// retireFaultStore fails one worker read or write with a configured error, the
// shape of service/worker_owned_tabs_test.go's workerFaultStore.
type retireFaultStore struct {
	store.Store
	failGetByID               error
	failGetByIDIncludeDeleted error
	failMarkDeleted           error
}

func (s retireFaultStore) Workers() store.WorkerStore {
	return retireFaultWorkers{WorkerStore: s.Store.Workers(), retireFaultStore: s}
}

type retireFaultWorkers struct {
	store.WorkerStore
	retireFaultStore
}

func (s retireFaultWorkers) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	if s.failGetByID != nil {
		return nil, s.failGetByID
	}
	return s.WorkerStore.GetByID(ctx, id)
}

func (s retireFaultWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	if s.failGetByIDIncludeDeleted != nil {
		return nil, s.failGetByIDIncludeDeleted
	}
	return s.WorkerStore.GetByIDIncludeDeleted(ctx, id)
}

func (s retireFaultWorkers) MarkDeleted(ctx context.Context, id string) error {
	if s.failMarkDeleted != nil {
		return s.failMarkDeleted
	}
	return s.WorkerStore.MarkDeleted(ctx, id)
}

// RetireLocalWorker removes a bundled Worker row at once and runs the
// deregistration effects; it refuses any other row and treats an absent one as
// nothing to retire.
func TestRetireLocalWorkerDeletesOnlyAnAutoRegisteredWorker(t *testing.T) {
	t.Parallel()

	newRecord := func() (*[]string, func(string)) {
		var owners []string
		return &owners, func(ownerID string) { owners = append(owners, ownerID) }
	}

	t.Run("an auto-registered row is retired", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		ctx := context.Background()
		owner := storetest.SeedUser(t, st, "retire-owner")
		owners, record := newRecord()
		server := &Server{store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

		credentials, err := server.RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
		require.NoError(t, err)

		require.NoError(t, server.RetireLocalWorker(ctx, credentials.WorkerID))
		_, err = st.Workers().GetByID(ctx, credentials.WorkerID)
		assert.ErrorIs(t, err, store.ErrNotFound, "the row must be gone")
		assert.Equal(t, []string{owner.ID}, *owners, "the effects run for the worker's owner")
	})

	t.Run("a registration-key row is refused", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		ctx := context.Background()
		owner := storetest.SeedUser(t, st, "retire-key-owner")
		owners, record := newRecord()
		server := &Server{store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

		workerID := id.Generate()
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:           workerID,
			AuthToken:    id.Generate(),
			RegisteredBy: userid.MustNew(owner.ID),
			PublicKeys:   storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
		}))

		err := server.RetireLocalWorker(ctx, workerID)
		require.ErrorIs(t, err, ErrNotAutoRegistered)
		_, readErr := st.Workers().GetByID(ctx, workerID)
		assert.NoError(t, readErr, "a refused retire must leave the row alone")
		assert.Empty(t, *owners, "a refused retire must run no effects")
	})

	t.Run("an absent row is a no-op", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		owners, record := newRecord()
		server := &Server{store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

		require.NoError(t, server.RetireLocalWorker(context.Background(), "missing"))
		assert.Empty(t, *owners)
	})

	t.Run("a GetByID fault is returned", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		ctx := context.Background()
		owner := storetest.SeedUser(t, st, "retire-read-fault-owner")
		owners, record := newRecord()
		boom := errors.New("database is locked")
		server := &Server{
			store:         retireFaultStore{Store: st, failGetByID: boom},
			workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record),
		}

		credentials, err := (&Server{store: st}).RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
		require.NoError(t, err)

		err = server.RetireLocalWorker(ctx, credentials.WorkerID)
		require.ErrorIs(t, err, boom)
		assert.Contains(t, err.Error(), credentials.WorkerID)

		row, readErr := st.Workers().GetByID(ctx, credentials.WorkerID)
		require.NoError(t, readErr)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, row.Status, "nothing was deleted")
		assert.Empty(t, *owners)
	})

	t.Run("a MarkDeleted fault is returned", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		ctx := context.Background()
		owner := storetest.SeedUser(t, st, "retire-delete-fault-owner")
		owners, record := newRecord()
		boom := errors.New("database is locked")
		server := &Server{
			store:         retireFaultStore{Store: st, failMarkDeleted: boom},
			workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record),
		}

		credentials, err := (&Server{store: st}).RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
		require.NoError(t, err)

		err = server.RetireLocalWorker(ctx, credentials.WorkerID)
		require.ErrorIs(t, err, boom)

		row, readErr := st.Workers().GetByID(ctx, credentials.WorkerID)
		require.NoError(t, readErr)
		assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, row.Status)
		assert.Empty(t, *owners, "a failed delete must run no effects")
	})

	t.Run("an Apply fault is logged, not returned", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		ctx := context.Background()
		owner := storetest.SeedUser(t, st, "retire-apply-fault-owner")
		owners, record := newRecord()
		boom := errors.New("database is locked")
		// SendDeregister reads the row through the notifier's own store, so a
		// wrapper that fails that read makes Apply return before it reaches the
		// workers-changed hook. A notifier wired to the plain store by mistake
		// would fill the record instead.
		faulting := retireFaultStore{Store: st, failGetByIDIncludeDeleted: boom}
		server := &Server{
			store: st,
			workerEffects: service.NewWorkerDeregisterEffects(nil, notifier.New(
				faulting,
				workermgr.New(workermgr.DenyAllReach()),
				workermgr.NewPendingRequests(func() time.Duration { return 30 * time.Second }),
				func() time.Duration { return 30 * time.Second },
				nil,
			), record),
		}

		credentials, err := server.RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
		require.NoError(t, err)

		require.NoError(t, server.RetireLocalWorker(ctx, credentials.WorkerID),
			"the row is already deleted; a failed effect must not fail the launch")
		_, readErr := st.Workers().GetByID(ctx, credentials.WorkerID)
		assert.ErrorIs(t, readErr, store.ErrNotFound)
		assert.Empty(t, *owners, "the failure path must have been taken")
	})
}
