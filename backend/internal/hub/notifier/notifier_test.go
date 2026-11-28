package notifier

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
)

var errInjected = errors.New("notifier test: injected failure")

// fakeRegistry is the seam New's workerRegistry parameter exists to provide. A
// non-nil conn means the worker is connected. It records every call and keeps
// the flag the real registry would hold.
type fakeRegistry struct {
	mu     sync.Mutex
	conn   *workermgr.Conn
	marked []string
	flag   map[string]bool
}

func newFakeRegistry(t *testing.T, workerID string, connected bool) *fakeRegistry {
	r := &fakeRegistry{flag: map[string]bool{}}
	if connected {
		r.conn = workermgrtest.NewConn(t, workerID)
	}
	return r
}

func (f *fakeRegistry) ConnForTrustedPath(workerID string) *workermgr.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil && f.conn.WorkerID != workerID {
		return nil
	}
	return f.conn
}

func (f *fakeRegistry) MarkDeregistering(workerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, workerID)
	if f.conn != nil && f.conn.WorkerID == workerID {
		f.flag[workerID] = true
	}
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

func newTestNotifier(reg workerRegistry, sender requestSender) *Notifier {
	return New(reg, sender, func() time.Duration { return 5 * time.Second })
}

func TestNew_PanicsOnANilDependency(t *testing.T) {
	timeout := func() time.Duration { return time.Second }
	var nilManager *workermgr.Manager
	var nilPending *workermgr.PendingRequests
	assert.Panics(t, func() { New(nilManager, ackSender(), timeout) }, "a nil registry must fail at wiring time")
	assert.Panics(t, func() { New(newFakeRegistry(t, "", false), nilPending, timeout) }, "a nil sender must fail at wiring time")
}

func TestSendDeregister_OnlineWorkerIsContainedAndNotified(t *testing.T) {
	const workerID = "worker-online"
	reg := newFakeRegistry(t, workerID, true)
	sender := ackSender()

	newTestNotifier(reg, sender).SendDeregister(context.Background(), workerID)

	assert.Equal(t, 1, sender.callCount())
	assert.True(t, reg.flagged(workerID), "the published connection remains contained until it unregisters")
	assert.Equal(t, []string{workerID}, reg.marked)
	assertConnectionEnded(t, reg.conn)
}

func TestSendDeregister_OfflineWorkerSkipsDeliveryAndContainmentMark(t *testing.T) {
	const workerID = "worker-offline"
	reg := newFakeRegistry(t, workerID, false)
	sender := ackSender()

	newTestNotifier(reg, sender).SendDeregister(context.Background(), workerID)

	assert.Zero(t, sender.callCount(), "an offline worker cannot be told")
	assert.False(t, reg.flagged(workerID), "offline worker needs no registry containment entry")
}

func TestSendDeregister_FailedOrNonAckStillContainsAndFences(t *testing.T) {
	for name, respond := range map[string]func(context.Context) (*leapmuxv1.ConnectRequest, error){
		"nil response": func(context.Context) (*leapmuxv1.ConnectRequest, error) { return nil, nil },
		"wrong payload": func(context.Context) (*leapmuxv1.ConnectRequest, error) {
			return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{}}}, nil
		},
		"send error": func(context.Context) (*leapmuxv1.ConnectRequest, error) { return nil, errInjected },
	} {
		t.Run(name, func(t *testing.T) {
			const workerID = "worker-unacked"
			reg := newFakeRegistry(t, workerID, true)
			sender := &fakeSender{respond: respond}

			newTestNotifier(reg, sender).SendDeregister(context.Background(), workerID)

			assert.Equal(t, 1, sender.callCount())
			assert.True(t, reg.flagged(workerID))
			assertConnectionEnded(t, reg.conn)
		})
	}
}

func TestDeliverOnConnect_ContainsNotifiesAndFences(t *testing.T) {
	const workerID = "worker-connect"
	reg := newFakeRegistry(t, workerID, true)
	sender := ackSender()

	newTestNotifier(reg, sender).DeliverOnConnect(context.Background(), workerID, reg.conn)

	assert.Equal(t, []string{workerID}, reg.marked, "the containment flag is set on connect")
	assert.Equal(t, 1, sender.callCount())
	assert.True(t, reg.flagged(workerID))
	assertConnectionEnded(t, reg.conn)
}
