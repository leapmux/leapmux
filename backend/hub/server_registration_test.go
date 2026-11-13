package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
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
			credentials, err := (&Server{hasher: hubtestutil.TestSecretHasher(), store: st}).RegisterWorker(ctx, owner.ID, keys)
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
				byToken, err := st.Workers().GetByAuthTokenHashIncludeDeleted(ctx, hubtestutil.TestSecretHasher().Hash(credentials.AuthToken))
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

	credentials, err := (&Server{hasher: hubtestutil.TestSecretHasher(), store: st}).RegisterWorker(context.Background(), "", workerkeystest.Keys(0x31))
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

	credentials, err := (&Server{hasher: hubtestutil.TestSecretHasher(), store: st}).RegisterWorker(ctx, owner.ID, short)
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
	server := &Server{hasher: hubtestutil.TestSecretHasher(), store: st}

	credentials, err := server.RegisterWorker(ctx, owner.ID, keys)
	require.NoError(t, err)

	got, err := server.GetWorkerRegistration(ctx, credentials.WorkerID, credentials.AuthToken)
	require.NoError(t, err)
	assert.True(t, got.AuthTokenAccepted)
	assert.Equal(t, owner.ID, got.Owner)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, got.Status)
	assert.Equal(t, keys.GetX25519(), got.PublicKeys.GetX25519())
	assert.Equal(t, keys.GetMlkem(), got.PublicKeys.GetMlkem())
	assert.Equal(t, keys.GetSlhdsa(), got.PublicKeys.GetSlhdsa())

	require.NoError(t, st.Workers().MarkDeleted(ctx, credentials.WorkerID))
	_, err = server.GetWorkerRegistration(ctx, credentials.WorkerID, credentials.AuthToken)
	assert.ErrorIs(t, err, store.ErrNotFound, "a soft-deleted row must read as absent")
}

// retireFaultStore fails one worker read or write with a configured error, the
// shape of service/worker_owned_tabs_test.go's workerFaultStore.
type retireFaultStore struct {
	store.Store
	failGetByID               error
	failGetByIDIncludeDeleted error
	failMarkDeleted           error
	failTokenLookup           error
	failTokenRotation         error
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
		server := &Server{hasher: hubtestutil.TestSecretHasher(), store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

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
		server := &Server{hasher: hubtestutil.TestSecretHasher(), store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

		workerID := id.Generate()
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:            workerID,
			AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
			RegisteredBy:  userid.MustNew(owner.ID),
			PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
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
		server := &Server{hasher: hubtestutil.TestSecretHasher(), store: st, workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record)}

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
		server := &Server{hasher: hubtestutil.TestSecretHasher(),
			store:         retireFaultStore{Store: st, failGetByID: boom},
			workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record),
		}

		credentials, err := (&Server{hasher: hubtestutil.TestSecretHasher(), store: st}).RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
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
		server := &Server{hasher: hubtestutil.TestSecretHasher(),
			store:         retireFaultStore{Store: st, failMarkDeleted: boom},
			workerEffects: service.NewWorkerDeregisterEffects(nil, nil, record),
		}

		credentials, err := (&Server{hasher: hubtestutil.TestSecretHasher(), store: st}).RegisterWorker(ctx, owner.ID, workerkeystest.Keys(0x31))
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
		server := &Server{hasher: hubtestutil.TestSecretHasher(),
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

func (s retireFaultWorkers) GetByAuthTokenHashIncludeDeleted(ctx context.Context, hash []byte) (*store.Worker, error) {
	if s.failTokenLookup != nil {
		return nil, s.failTokenLookup
	}
	return s.WorkerStore.GetByAuthTokenHashIncludeDeleted(ctx, hash)
}

func (s retireFaultWorkers) RotateAuthToken(ctx context.Context, p store.RotateWorkerAuthTokenParams) (int64, error) {
	if s.failTokenRotation != nil {
		return 0, s.failTokenRotation
	}
	return s.WorkerStore.RotateAuthToken(ctx, p)
}

func registeredLocalWorkerForTest(t *testing.T) (*Server, *WorkerCredentials) {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "local-token-owner")
	server := &Server{store: st, hasher: hubtestutil.TestSecretHasher(), workerConnector: localTokenConnectorForTest(st)}
	credentials, err := server.RegisterWorker(t.Context(), owner.ID, workerkeystest.Keys(0x31))
	require.NoError(t, err)
	return server, credentials
}

func TestGetWorkerRegistrationRefusesUnknownAndForeignTokens(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	owner := storetest.SeedUser(t, server.store, "another-token-owner")
	other, err := server.RegisterWorker(t.Context(), owner.ID, workerkeystest.Keys(0x32))
	require.NoError(t, err)
	original, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, credentials.AuthToken)
	require.NoError(t, err)
	for _, token := range []string{"", "unknown-token", other.AuthToken} {
		got, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, token)
		require.NoError(t, err, "a refused token must not look like a missing Worker row")
		assert.False(t, got.AuthTokenAccepted)
		assert.Equal(t, original.Owner, got.Owner)
		assert.Equal(t, original.PublicKeys, got.PublicKeys)
	}
}

func TestGetWorkerRegistrationPreservesTokenLookupFailures(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	failure := errors.New("token lookup failed")
	server.store = retireFaultStore{Store: server.store, failTokenLookup: failure}
	_, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, credentials.AuthToken)
	require.ErrorIs(t, err, failure)
	assert.NotErrorIs(t, err, store.ErrNotFound)
}

func TestReissueLocalWorkerTokenRotatesAfterAPepperChange(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	rotated, err := auth.NewSecretHasher([]byte("different-pepper-for-the-new-hub-epoch"))
	require.NoError(t, err)
	server.hasher = rotated
	before, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, credentials.AuthToken)
	require.NoError(t, err)
	assert.False(t, before.AuthTokenAccepted)
	token, err := server.ReissueLocalWorkerToken(t.Context(), credentials.WorkerID)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	assert.NotEqual(t, credentials.AuthToken, token)
	accepted, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, token)
	require.NoError(t, err)
	assert.True(t, accepted.AuthTokenAccepted)
	assert.Equal(t, before.Owner, accepted.Owner)
	assert.Equal(t, before.PublicKeys, accepted.PublicKeys)
	refused, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, credentials.AuthToken)
	require.NoError(t, err)
	assert.False(t, refused.AuthTokenAccepted)
}

func TestReissueLocalWorkerTokenRefusesAStandaloneWorker(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	registration, err := server.GetWorkerRegistration(t.Context(), credentials.WorkerID, credentials.AuthToken)
	require.NoError(t, err)
	worker, token := hubtestutil.CreateTestWorker(t, server.store, registration.Owner)
	_, err = server.ReissueLocalWorkerToken(t.Context(), worker.ID)
	require.ErrorIs(t, err, ErrNotAutoRegistered)
	hubtestutil.RequireWorkerToken(t, server.store, worker.ID, token)
}

func TestReissueLocalWorkerTokenPreservesInactiveDigests(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "deregistering", true: "deleted"}[deleted], func(t *testing.T) {
			server, credentials := registeredLocalWorkerForTest(t)
			if deleted {
				require.NoError(t, server.store.Workers().MarkDeleted(t.Context(), credentials.WorkerID))
			} else {
				rows, err := server.store.Workers().ForceDeregister(t.Context(), credentials.WorkerID)
				require.NoError(t, err)
				require.Equal(t, int64(1), rows)
			}
			token, err := server.ReissueLocalWorkerToken(t.Context(), credentials.WorkerID)
			require.ErrorIs(t, err, store.ErrNotFound)
			assert.Empty(t, token)
			old, err := server.store.Workers().GetByAuthTokenHashIncludeDeleted(t.Context(), server.hasher.Hash(credentials.AuthToken))
			require.NoError(t, err)
			assert.Equal(t, credentials.WorkerID, old.ID)
		})
	}
}

func TestReissueLocalWorkerTokenPreservesRotationFailures(t *testing.T) {
	server, credentials := registeredLocalWorkerForTest(t)
	failure := errors.New("token rotation failed")
	server.store = retireFaultStore{Store: server.store, failTokenRotation: failure}
	server.workerConnector = localTokenConnectorForTest(server.store)
	token, err := server.ReissueLocalWorkerToken(t.Context(), credentials.WorkerID)
	require.ErrorIs(t, err, failure)
	assert.NotErrorIs(t, err, store.ErrNotFound)
	assert.Empty(t, token)
	hubtestutil.RequireWorkerToken(t, server.store, credentials.WorkerID, credentials.AuthToken)
}

func localTokenConnectorForTest(st store.Store) *service.WorkerConnectorService {
	return service.NewWorkerConnectorService(service.WorkerConnectorServiceDeps{
		Store: st, Hasher: hubtestutil.TestSecretHasher(), Clock: quartz.NewReal(),
		QueuePool: sendq.NewMaxBytesPoolForTest(), DeregisterEffects: service.NewWorkerDeregisterEffects(nil, nil, nil),
	})
}
