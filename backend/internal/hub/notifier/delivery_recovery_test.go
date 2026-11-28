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
	for _, response := range []string{"acknowledged", "send failed", "nil response", "wrong payload"} {
		t.Run(response, func(t *testing.T) {
			const workerID = "worker-selected"
			registry := newFakeRegistry(t, workerID, true)
			sender := &fakeSender{respond: func(ctx context.Context) (*leapmuxv1.ConnectRequest, error) {
				switch response {
				case "acknowledged":
					return ackSender().respond(ctx)
				case "send failed":
					return nil, errInjected
				case "wrong payload":
					return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{}}}, nil
				default:
					return nil, nil
				}
			}}
			newTestNotifier(registry, sender).SendDeregister(context.Background(), workerID)
			assertConnectionEnded(t, registry.conn)
		})
	}
}

func TestLateDeregisterFailureCannotEndAReplacementConnection(t *testing.T) {
	const workerID = "worker-replaced"
	registry := newFakeRegistry(t, workerID, true)
	selected := registry.conn
	replacement := workermgrtest.NewConn(t, workerID)
	sender := &fakeSender{respond: func(context.Context) (*leapmuxv1.ConnectRequest, error) {
		registry.mu.Lock()
		registry.conn = replacement
		registry.mu.Unlock()
		return nil, errInjected
	}}
	newTestNotifier(registry, sender).SendDeregister(context.Background(), workerID)
	assertConnectionEnded(t, selected)
	select {
	case <-replacement.Done():
		t.Error("a late delivery failure ended the replacement connection")
	default:
	}
}

func assertConnectionEnded(t *testing.T, connection *workermgr.Conn) {
	t.Helper()
	select {
	case <-connection.Done():
	default:
		t.Error("the selected connection must end so the deleted worker stops")
	}
}

func TestLateConnectDeliveryCannotEndAReplacementConnection(t *testing.T) {
	const workerID = "worker-late-connect"
	registry := workermgr.New(workermgr.DenyAllReach())
	selected := workermgrtest.NewConn(t, workerID)
	_, err := registry.Register(selected)
	require.NoError(t, err)
	replacement := workermgrtest.NewConn(t, workerID)
	_, err = registry.Register(replacement)
	require.NoError(t, err)
	t.Cleanup(func() { registry.Unregister(workerID, replacement) })
	assertConnectionEnded(t, selected)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sender := &canceledConnectSender{}
	n := New(registry, sender, func() time.Duration { return time.Second })
	n.DeliverOnConnect(ctx, workerID, selected)
	assert.Same(t, selected, sender.connection, "the delayed invocation must retain its handler's connection")
	select {
	case <-replacement.Done():
		t.Error("the old handler's canceled delivery ended its replacement")
	default:
	}
	assert.True(t, registry.IsDeregistering(workerID), "containment still applies to the replacement connection")
}

type canceledConnectSender struct {
	connection *workermgr.Conn
}

func TestConnectDeliveryWithoutASelectedConnectionDoesNothing(t *testing.T) {
	const workerID = "worker-nil-conn"
	registry := newFakeRegistry(t, workerID, true)
	sender := ackSender()
	assert.NotPanics(t, func() {
		newTestNotifier(registry, sender).DeliverOnConnect(context.Background(), workerID, nil)
	})
	assert.Zero(t, sender.callCount())
	assert.Empty(t, registry.marked)
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
