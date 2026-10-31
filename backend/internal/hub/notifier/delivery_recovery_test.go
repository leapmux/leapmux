package notifier

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
)

func TestEveryDeregisterAttemptEndsItsSelectedConnection(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		for _, response := range []string{"acknowledged", "send failed", "nil response", "wrong payload"} {
			name := response
			if deleted {
				name += " after deletion"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				if deleted {
					require.NoError(t, e.st.Workers().MarkDeleted(context.Background(), e.worker))
				}
				registry := newFakeRegistry(t, e.worker, true)
				sender := &fakeSender{respond: func(ctx context.Context) (*leapmuxv1.ConnectRequest, error) {
					switch response {
					case "acknowledged":
						return ackSender().respond(ctx)
					case "send failed":
						return nil, errInjected
					case "wrong payload":
						return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}}}, nil
					default:
						return nil, nil
					}
				}}
				require.NoError(t, e.notifier(e.st, registry, sender).SendDeregister(context.Background(), e.worker))
				assertConnectionEnded(t, registry.conn)
				if deleted || response == "acknowledged" {
					assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, e.status(t))
				} else {
					assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
				}
			})
		}
	}
}

func TestFailedDeregisterReadEndsTheConnectionWithoutLosingIntent(t *testing.T) {
	e := newEnv(t)
	registry := newFakeRegistry(t, e.worker, true)
	workers := &faultyWorkers{WorkerStore: e.st.Workers(), getIncludeDeleted: errInjected}
	sender := ackSender()
	err := e.notifier(faultyStore{Store: e.st, workers: workers}, registry, sender).SendDeregister(context.Background(), e.worker)
	require.ErrorIs(t, err, errInjected)
	assertConnectionEnded(t, registry.conn)
	assert.Zero(t, sender.callCount())
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
}

func TestLateDeregisterFailureCannotEndAReplacementConnection(t *testing.T) {
	for _, stage := range []string{"read", "send"} {
		t.Run(stage, func(t *testing.T) {
			e := newEnv(t)
			registry := newFakeRegistry(t, e.worker, true)
			selected := registry.conn
			replacement, _ := workermgrtest.NewRecordedConn(t, e.worker)
			replace := func() {
				registry.mu.Lock()
				registry.conn = replacement
				registry.mu.Unlock()
			}
			workers := &faultyWorkers{WorkerStore: e.st.Workers()}
			sender := &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) {
				replace()
				return nil, errInjected
			}}
			if stage == "read" {
				workers.beforeGet, workers.getIncludeDeleted = replace, errInjected
			}
			err := e.notifier(faultyStore{Store: e.st, workers: workers}, registry, sender).SendDeregister(context.Background(), e.worker)
			if stage == "read" {
				require.ErrorIs(t, err, errInjected)
			} else {
				require.NoError(t, err)
			}
			assertConnectionEnded(t, selected)
			select {
			case <-replacement.Done():
				t.Error("a late delivery failure ended the replacement connection")
			default:
			}
		})
	}
}

func assertConnectionEnded(t *testing.T, connection *workermgr.Conn) {
	t.Helper()
	select {
	case <-connection.Done():
	default:
		t.Error("the selected connection must end so persisted deregistration can resume on reconnect")
	}
}

func TestLateConnectDeliveryCannotEndAReplacementConnection(t *testing.T) {
	e := newEnv(t)
	registry := workermgr.New(workermgr.DenyAllReach())
	selected, _ := workermgrtest.NewRecordedConn(t, e.worker)
	_, err := registry.Register(selected)
	require.NoError(t, err)
	replacement, _ := workermgrtest.NewRecordedConn(t, e.worker)
	_, err = registry.Register(replacement)
	require.NoError(t, err)
	t.Cleanup(func() { registry.Unregister(e.worker, replacement) })
	assertConnectionEnded(t, selected)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker, err := e.st.Workers().GetByIDIncludeDeleted(context.Background(), e.worker)
	require.NoError(t, err)
	sender := &canceledConnectSender{}
	n := New(e.st, registry, sender, func() time.Duration { return time.Second }, nil)
	n.DeliverOnConnect(ctx, worker, selected)
	assert.Same(t, selected, sender.connection, "the delayed invocation must retain its handler's connection")
	select {
	case <-replacement.Done():
		t.Error("the old handler's canceled delivery ended its replacement")
	default:
	}
	assert.True(t, registry.IsDeregistering(e.worker), "the persisted worker-wide intent still contains the replacement")
}

type canceledConnectSender struct {
	connection *workermgr.Conn
}

func TestConnectDeliveryWithoutASelectedConnectionDoesNothing(t *testing.T) {
	e := newEnv(t)
	registry := newFakeRegistry(t, e.worker, true)
	sender := ackSender()
	worker, err := e.st.Workers().GetByIDIncludeDeleted(context.Background(), e.worker)
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		e.notifier(e.st, registry, sender).DeliverOnConnect(context.Background(), worker, nil)
	})
	assert.Zero(t, sender.callCount())
	assert.Empty(t, registry.marked)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, e.status(t))
	select {
	case <-registry.conn.Done():
		t.Error("an absent selected connection must not end another connection")
	default:
	}
}

func (s *canceledConnectSender) SendAndWait(ctx context.Context, connection *workermgr.Conn, _ *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error) {
	s.connection = connection
	return nil, ctx.Err()
}
