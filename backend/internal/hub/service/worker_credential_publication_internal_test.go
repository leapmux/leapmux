package service

import (
	"context"
	"errors"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

type credentialLockStore struct {
	store.Store
	check                              func()
	workerReads, ownerReads, rotations int
	readErr                            error
	rotationErr                        error
}
type credentialLockWorkers struct {
	store.WorkerStore
	parent *credentialLockStore
}
type credentialLockUsers struct {
	store.UserStore
	parent *credentialLockStore
}

func (s *credentialLockStore) Workers() store.WorkerStore {
	return credentialLockWorkers{s.Store.Workers(), s}
}
func (s *credentialLockStore) Users() store.UserStore { return credentialLockUsers{s.Store.Users(), s} }
func (w credentialLockWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	w.parent.check()
	w.parent.workerReads++
	if w.parent.readErr != nil {
		return nil, w.parent.readErr
	}
	return w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
}
func (w credentialLockWorkers) RotateAuthToken(ctx context.Context, p store.RotateWorkerAuthTokenParams) (int64, error) {
	w.parent.check()
	w.parent.rotations++
	if w.parent.rotationErr != nil {
		return 0, w.parent.rotationErr
	}
	return w.WorkerStore.RotateAuthToken(ctx, p)
}
func (u credentialLockUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	u.parent.check()
	u.parent.ownerReads++
	return u.UserStore.GetByID(ctx, id)
}

func TestWorkerCredentialReadPublicationAndRotationHoldTheSameLock(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "publication-lock-owner")
	worker, _ := hubtestutil.CreateTestWorker(t, st, owner.ID)
	wrapped := &credentialLockStore{Store: st}
	manager := workermgr.New(workermgr.DenyAllReach())
	svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{Store: wrapped, Hasher: hubtestutil.TestSecretHasher(), WorkerManager: manager, Clock: quartz.NewReal(), QueuePool: sendq.NewMaxBytesPoolForTest(), DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil)})
	wrapped.check = func() {
		mu := svc.credentialPublications.forKey(worker.ID)
		if mu.TryLock() {
			mu.Unlock()
			t.Error("credential state and publication must share the rotation lock")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn, pump, _, err := svc.registerGreetedConn(ctx, cancel, worker, &leapmuxv1.WorkerHello{Keys: worker.PublicKeys.Proto(), EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM}, func(context.Context, *leapmuxv1.ConnectResponse) error { return nil })
	require.NoError(t, err)
	t.Cleanup(conn.Fence)
	require.NoError(t, pump.Drain())
	assert.Equal(t, 1, wrapped.workerReads)
	assert.Equal(t, 1, wrapped.ownerReads)
	rows, err := svc.RotateWorkerAuthToken(t.Context(), store.RotateWorkerAuthTokenParams{ID: worker.ID, ExpectedAuthTokenHash: worker.AuthTokenHash, AuthTokenHash: hubtestutil.TestSecretHasher().Hash("replacement-token")})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	assert.Equal(t, 1, wrapped.rotations)
	assert.Same(t, conn, manager.ConnForTrustedPath(worker.ID))
	failure := errors.New("publication state unavailable")
	wrapped.readErr = failure
	_, _, _, err = svc.registerGreetedConn(ctx, cancel, worker, &leapmuxv1.WorkerHello{Keys: worker.PublicKeys.Proto()}, func(context.Context, *leapmuxv1.ConnectResponse) error { return nil })
	require.ErrorIs(t, err, failure)
	assert.Same(t, conn, manager.ConnForTrustedPath(worker.ID))
	select {
	case <-conn.Done():
	default:
		t.Fatal("successful rotation must fence the established stream")
	}
}

type waitingCredentialStore struct{ store.Store }
type waitingCredentialWorkers struct{ store.WorkerStore }

func (waitingCredentialStore) Workers() store.WorkerStore { return waitingCredentialWorkers{} }
func (waitingCredentialWorkers) GetByIDIncludeDeleted(ctx context.Context, _ string) (*store.Worker, error) {
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func TestWorkerCredentialReadReleasesItsLockWhenTheDeadlineExpires(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().AfterFunc(workerCredentialReadTimerTag)
	stopTimer := clock.Trap().TimerStop(workerCredentialReadTimerTag)
	t.Cleanup(func() { newTimer.Close(); stopTimer.Close() })
	svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{Store: waitingCredentialStore{}, Hasher: hubtestutil.TestSecretHasher(), Clock: clock, QueuePool: sendq.NewMaxBytesPoolForTest(), DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil)})
	ctx := testutil.DeadlineContext(t)
	result := make(chan error, 1)
	go func() {
		_, _, _, err := svc.registerGreetedConn(ctx, func() {}, &store.Worker{ID: "worker"}, nil, nil)
		result <- err
	}()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, workerCredentialReadTimeout, delay)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	mu := svc.credentialPublications.forKey("worker")
	require.True(t, mu.TryLock(), "a failed read must release the credential stripe")
	mu.Unlock()
}

func TestWorkerCredentialFailedRotationKeepsTheEstablishedStream(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale digest", true: "store failure"}[storageFailure], func(t *testing.T) {
			st := hubtestutil.OpenTestStore(t)
			owner := storetest.SeedUser(t, st, "failed-rotation-owner")
			worker, _ := hubtestutil.CreateTestWorker(t, st, owner.ID)
			wrapped := &credentialLockStore{Store: st, check: func() {}}
			manager := workermgr.New(workermgr.DenyAllReach())
			svc := NewWorkerConnectorService(WorkerConnectorServiceDeps{Store: wrapped, Hasher: hubtestutil.TestSecretHasher(), WorkerManager: manager, Clock: quartz.NewReal(), QueuePool: sendq.NewMaxBytesPoolForTest(), DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil)})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			conn, pump, _, err := svc.registerGreetedConn(ctx, cancel, worker, &leapmuxv1.WorkerHello{Keys: worker.PublicKeys.Proto(), EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM}, func(context.Context, *leapmuxv1.ConnectResponse) error { return nil })
			require.NoError(t, err)
			t.Cleanup(conn.Fence)
			require.NoError(t, pump.Drain())
			p := store.RotateWorkerAuthTokenParams{ID: worker.ID, ExpectedAuthTokenHash: hubtestutil.TestSecretHasher().Hash("stale token"), AuthTokenHash: hubtestutil.TestSecretHasher().Hash("replacement token")}
			failure := errors.New("rotation unavailable")
			if storageFailure {
				wrapped.rotationErr = failure
				p.ExpectedAuthTokenHash = worker.AuthTokenHash
			}
			rows, err := svc.RotateWorkerAuthToken(t.Context(), p)
			if storageFailure {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, rows)
			assert.Same(t, conn, manager.ConnForTrustedPath(worker.ID))
			select {
			case <-conn.Done():
				t.Fatal("failed rotation must preserve the established stream")
			default:
			}
		})
	}
}
