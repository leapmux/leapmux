package notifier

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

var errInjected = errors.New("notifier test: injected failure")

// fakeRegistry is the seam New's workerRegistry parameter exists to provide. A
// non-nil conn means the worker is connected. It records every call and keeps
// the flag the real registry would hold.
type fakeRegistry struct {
	mu      sync.Mutex
	conn    *workermgr.Conn
	marked  []string
	cleared []string
	flag    map[string]bool
}

func newFakeRegistry(connected bool) *fakeRegistry {
	r := &fakeRegistry{flag: map[string]bool{}}
	if connected {
		r.conn = &workermgr.Conn{}
	}
	return r
}

func (f *fakeRegistry) ConnForTrustedPath(string) *workermgr.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

func (f *fakeRegistry) MarkDeregistering(workerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, workerID)
	f.flag[workerID] = true
}

func (f *fakeRegistry) ClearDeregistering(workerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, workerID)
	delete(f.flag, workerID)
}

func (f *fakeRegistry) flagged(workerID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flag[workerID]
}

// fakeSender answers SendAndWait with respond, and counts the calls.
type fakeSender struct {
	mu      sync.Mutex
	calls   int
	respond func(ctx context.Context) (*leapmuxv1.ConnectRequest, error)
}

func (s *fakeSender) SendAndWait(ctx context.Context, _ *workermgr.Conn, msg *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if msg.GetDeregister() == nil {
		return nil, errors.New("fake sender: not a DEREGISTER instruction")
	}
	return s.respond(ctx)
}

func (s *fakeSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// ackSender acknowledges every instruction with the payload a real worker
// sends (lifecycle_handlers.go handleDeregister).
func ackSender() *fakeSender {
	return &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) {
		return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}}}, nil
	}}
}

// faultyWorkers fails the named methods of the wrapped WorkerStore.
type faultyWorkers struct {
	store.WorkerStore
	mu                sync.Mutex
	getIncludeDeleted error
	markDeletedOnce   error
}

func (w *faultyWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	if w.getIncludeDeleted != nil {
		return nil, w.getIncludeDeleted
	}
	return w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
}

func (w *faultyWorkers) MarkDeleted(ctx context.Context, id string) error {
	w.mu.Lock()
	err := w.markDeletedOnce
	w.markDeletedOnce = nil
	w.mu.Unlock()
	if err != nil {
		return err
	}
	return w.WorkerStore.MarkDeleted(ctx, id)
}

type faultyStore struct {
	store.Store
	workers *faultyWorkers
}

func (s faultyStore) Workers() store.WorkerStore { return s.workers }

type env struct {
	st      store.Store
	owner   string
	worker  string
	changed []string
}

func (e *env) workersChanged(ownerID string) { e.changed = append(e.changed, ownerID) }

// newEnv seeds an owner and one DEREGISTERING worker, the state every
// deregister surface commits before it calls the notifier.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "notifier-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	n, err := st.Workers().ForceDeregister(context.Background(), worker.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	return &env{st: st, owner: owner.ID, worker: worker.ID}
}

func (e *env) notifier(st store.Store, reg *fakeRegistry, sender *fakeSender) *Notifier {
	return New(st, reg, sender, func() time.Duration { return 5 * time.Second }, e.workersChanged)
}

func (e *env) status(t *testing.T) leapmuxv1.WorkerStatus {
	t.Helper()
	w, err := e.st.Workers().GetByIDIncludeDeleted(context.Background(), e.worker)
	require.NoError(t, err)
	return w.Status
}

func TestNew_PanicsOnANilDependency(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	timeout := func() time.Duration { return time.Second }
	var nilManager *workermgr.Manager
	var nilPending *workermgr.PendingRequests
	assert.Panics(t, func() { New(st, nilManager, ackSender(), timeout, nil) }, "a nil registry must fail at wiring time")
	assert.Panics(t, func() { New(st, newFakeRegistry(false), nilPending, timeout, nil) }, "a nil sender must fail at wiring time")
}

func TestSendDeregister_OnlineWorkerIsDeletedOnAck(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true)
	sender := ackSender()

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

	assert.Equal(t, 1, sender.callCount())
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, e.status(t), "a valid acknowledgement deletes the worker")
	assert.False(t, reg.flagged(e.worker), "the flag is cleared once the deletion is recorded")
	assert.Equal(t, []string{e.worker}, reg.marked)
	assert.Equal(t, []string{e.owner}, e.changed, "the owner's clients are told the list changed")
}

// A response is matched to its request by id alone, so neither a missing
// response nor a response of another kind is an acknowledgement.
func TestSendDeregister_NilResponseIsNotAnAck(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true)
	sender := &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) { return nil, nil }}

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
	assert.True(t, reg.flagged(e.worker))
	assert.Empty(t, e.changed)
}

func TestSendDeregister_OtherPayloadIsNotAnAck(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true)
	sender := &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) {
		return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}}}, nil
	}}

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t), "a response of another kind must not delete the worker")
	assert.True(t, reg.flagged(e.worker))
}

func TestSendDeregister_SendErrorKeepsTheIntent(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true)
	sender := &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) { return nil, errInjected }}

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker),
		"a failed delivery is recovered from the persisted intent, so it is no error for the caller")

	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
	assert.True(t, reg.flagged(e.worker))
	assert.Empty(t, e.changed)
}

func TestSendDeregister_OfflineWorkerKeepsTheFlag(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(false)
	sender := ackSender()

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

	assert.Zero(t, sender.callCount(), "an offline worker cannot be told")
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
	assert.True(t, reg.flagged(e.worker), "the flag keeps the worker unreachable until it connects")
}

// A worker deleted with its user has no deletion left to record, so the flag
// is cleared whatever happens to the instruction.
func TestSendDeregister_DeletedWorkerClearsTheFlag(t *testing.T) {
	t.Run("offline", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.st.Workers().MarkDeleted(context.Background(), e.worker))
		reg := newFakeRegistry(false)
		sender := ackSender()

		require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

		assert.Zero(t, sender.callCount())
		assert.False(t, reg.flagged(e.worker))
		assert.Empty(t, e.changed)
	})
	for name, respond := range map[string]func(context.Context) (*leapmuxv1.ConnectRequest, error){
		"online, acknowledged": ackSender().respond,
		"online, unacknowledged": func(context.Context) (*leapmuxv1.ConnectRequest, error) {
			return nil, errInjected
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			require.NoError(t, e.st.Workers().MarkDeleted(context.Background(), e.worker))
			reg := newFakeRegistry(true)
			sender := &fakeSender{respond: respond}

			require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(context.Background(), e.worker))

			assert.Equal(t, 1, sender.callCount(), "a connected worker is still told to stop")
			assert.False(t, reg.flagged(e.worker))
			assert.Empty(t, e.changed, "nothing changed in the list: the worker was already deleted")
		})
	}
}

// The worker exits right after it acknowledges, which ends its connection's
// context: the deletion must not depend on that context.
func TestSendDeregister_CompletionSurvivesTheCallersCancel(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true)
	ctx, cancel := context.WithCancel(context.Background())
	sender := &fakeSender{respond: func(sendCtx context.Context) (*leapmuxv1.ConnectRequest, error) {
		cancel()
		return ackSender().respond(sendCtx)
	}}

	require.NoError(t, e.notifier(e.st, reg, sender).SendDeregister(ctx, e.worker))

	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, e.status(t),
		"the deletion runs in a hub-owned context, so a cancelled caller cannot abort it")
	assert.False(t, reg.flagged(e.worker))
}

// A deletion that fails after a valid acknowledgement cannot be retried by the
// notifier: the worker has already deleted its own registration and will not
// connect again. The cleanup sweep is what finishes it.
func TestSendDeregister_FailedCompletionIsCompletedByTheSweep(t *testing.T) {
	e := newEnv(t)
	workers := &faultyWorkers{WorkerStore: e.st.Workers(), markDeletedOnce: errInjected}
	reg := newFakeRegistry(true)
	sender := ackSender()

	require.NoError(t, e.notifier(faultyStore{Store: e.st, workers: workers}, reg, sender).SendDeregister(context.Background(), e.worker))

	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
	assert.True(t, reg.flagged(e.worker))

	row, err := e.st.Workers().GetByID(context.Background(), e.worker)
	require.NoError(t, err)
	require.NotNil(t, row.DeregisteringAt)
	n, err := e.st.Cleanup().CompleteStaleDeregistrations(context.Background(), row.DeregisteringAt.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, e.status(t), "the sweep finishes the deregistration")
	assert.Equal(t, 1, sender.callCount(), "the worker was told exactly once")
}

// After a restart the registry holds no flag, but deregistering_at survives:
// the worker's next connect is told to stop, and the flag is set again.
func TestDeliverOnConnect_AfterARestart(t *testing.T) {
	e := newEnv(t)
	reg := newFakeRegistry(true) // a new notifier and registry: nothing flagged
	sender := ackSender()
	worker, err := e.st.Workers().GetByAuthToken(context.Background(), mustAuthToken(t, e.st, e.worker))
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, worker.Status)

	e.notifier(e.st, reg, sender).DeliverOnConnect(context.Background(), worker)

	assert.Equal(t, []string{e.worker}, reg.marked, "the flag is set again on connect")
	assert.Equal(t, 1, sender.callCount())
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, e.status(t))
	assert.False(t, reg.flagged(e.worker))
}

func mustAuthToken(t *testing.T, st store.Store, workerID string) string {
	t.Helper()
	w, err := st.Workers().GetByID(context.Background(), workerID)
	require.NoError(t, err)
	return w.AuthToken
}
