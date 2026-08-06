package workermgr

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
)

func boundedNotifyCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func TestNotifyShutdownAndFence_SendsToAllWorkers(t *testing.T) {
	m := New(DenyAllReach())

	var mu sync.Mutex
	var received []*leapmuxv1.ConnectResponse

	makeConn := func(workerID string) *Conn {
		conn, pump := newTestConn(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, msg)
			return nil
		}, nil)
		pumpStart(t, pump, conn)
		return conn
	}
	_, _ = m.Register(makeConn("w1"))
	_, _ = m.Register(makeConn("w2"))
	_, _ = m.Register(makeConn("w3"))

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, received, 3)
	for _, msg := range received {
		payload, ok := msg.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
		require.True(t, ok, "expected HubShuttingDown payload")
		assert.Equal(t, int32(10), payload.HubShuttingDown.GetRetryDelaySeconds())
	}
}

func pumpStart(t *testing.T, pump *SendPump, conn *Conn) {
	t.Helper()
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()
}

func TestNotifyShutdownAndFence_CustomRetryDelay(t *testing.T) {
	m := New(DenyAllReach())

	var mu sync.Mutex
	var received *leapmuxv1.ConnectResponse
	conn, pump := newTestConn(t, "w1", func(msg *leapmuxv1.ConnectResponse) error {
		mu.Lock()
		defer mu.Unlock()
		received = msg
		return nil
	}, nil)
	pumpStart(t, pump, conn)
	_, _ = m.Register(conn)

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 30)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, received)
	payload, ok := received.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
	require.True(t, ok)
	assert.Equal(t, int32(30), payload.HubShuttingDown.GetRetryDelaySeconds())
}

func TestNotifyShutdownAndFence_NoWorkers(t *testing.T) {
	m := New(DenyAllReach())
	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)
}

func TestNotifyShutdownAndFence_ContinuesOnSendError(t *testing.T) {
	m := New(DenyAllReach())

	sendCount := 0
	var mu sync.Mutex

	failConn, failPump := newTestConn(t, "w-fail", func(*leapmuxv1.ConnectResponse) error {
		mu.Lock()
		defer mu.Unlock()
		sendCount++
		return fmt.Errorf("connection reset")
	}, nil)
	pumpStart(t, failPump, failConn)
	_, _ = m.Register(failConn)

	okConn, okPump := newTestConn(t, "w-ok", func(*leapmuxv1.ConnectResponse) error {
		mu.Lock()
		defer mu.Unlock()
		sendCount++
		return nil
	}, nil)
	pumpStart(t, okPump, okConn)
	_, _ = m.Register(okConn)

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, sendCount, "should attempt to send to all workers even on error")
}

func TestNotifyShutdownAndFence_FencesConnectionsAfterDelivering(t *testing.T) {
	m := New(DenyAllReach())

	var delivered, cancelled atomic.Int32
	makeConn := func(workerID string) *Conn {
		ctx, cancel := context.WithCancel(context.Background())
		conn, pump := NewConn(ctx, func() {
			cancelled.Add(1)
			cancel()
		}, workerID, testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
			assert.Zero(t, cancelled.Load(), "connection fenced before its notification was sent")
			delivered.Add(1)
			return nil
		}, nil)
		t.Cleanup(conn.Fence)
		go func() {
			for {
				select {
				case <-pump.Ready():
					_ = pump.Drain()
				case <-conn.Done():
					return
				}
			}
		}()
		return conn
	}
	_, _ = m.Register(makeConn("w1"))
	_, _ = m.Register(makeConn("w2"))

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)

	assert.Equal(t, int32(2), delivered.Load(), "both workers notified")
	assert.Equal(t, int32(2), cancelled.Load(), "both Connect handlers cancelled")
	assert.ErrorIs(t, m.ConnForTrustedPath("w1").Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed,
		"a fenced connection must refuse further sends")
}

func TestNotifyShutdownAndFence_FencesConnectionsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	var cancelled atomic.Bool

	ctxConn, cancelConn := context.WithCancel(context.Background())
	conn, pump := NewConn(ctxConn, func() {
		cancelled.Store(true)
		cancelConn()
	}, "wedged", testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	t.Cleanup(conn.Fence)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()
	_, _ = m.Register(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.NotifyShutdownAndFence(ctx, 10)
		close(done)
	}()
	<-started
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyShutdownAndFence ignored context expiration")
	}
	assert.True(t, cancelled.Load(), "a worker that outlasted the notification budget must still be fenced")
}

func TestFenceAll_NoConnections(t *testing.T) {
	m := New(DenyAllReach())
	assert.NotPanics(t, m.FenceAll)
}

func TestFenceAll_ConnectionWithoutCancel(t *testing.T) {
	m := New(DenyAllReach())
	conn, _ := newAutoDrainedConn(t, "no-cancel", nil)
	// NewConn always has cancel; clearing it still must stop sends via queue Close.
	conn.cancel = nil
	_, _ = m.Register(conn)

	assert.NotPanics(t, m.FenceAll)
	assert.ErrorIs(t, m.ConnForTrustedPath("no-cancel").Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
}

func TestFenceAll_DoesNotHoldManagerLockDuringCancel(t *testing.T) {
	m := New(DenyAllReach())

	reRegistered := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	conn, _ := NewConn(ctx, func() {
		cancel()
		go func() {
			succ, _ := newTestConn(t, "successor", nil, nil)
			_, _ = m.Register(succ)
			once.Do(func() { close(reRegistered) })
		}()
		select {
		case <-reRegistered:
		case <-time.After(time.Second):
			assert.Fail(t, "Register blocked behind FenceAll's registry lock")
		}
	}, "fenced", testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error { return nil }, nil)
	t.Cleanup(conn.Fence)
	_, _ = m.Register(conn)

	m.FenceAll()
	<-reRegistered
}

func TestRegister_RefusedOnceFenced(t *testing.T) {
	m := New(DenyAllReach())
	m.FenceAll()

	var cancelled atomic.Bool
	var writes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	late, pump := NewConn(ctx, func() {
		cancelled.Store(true)
		cancel()
	}, "late", testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		writes.Add(1)
		return nil
	}, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "someone"},
		},
	})
	t.Cleanup(late.Fence)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-late.Done():
				return
			}
		}
	}()

	replaced, err := m.Register(late)

	require.ErrorIs(t, err, ErrRegistryFenced, "a fenced registry must refuse a late registration")
	assert.False(t, replaced)
	assert.True(t, cancelled.Load(), "the refused connection's handler must still be ended")
	assert.Zero(t, writes.Load(), "a refused connection must not write a greeting")
	assert.ErrorIs(t, late.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
	assert.Nil(t, m.ConnForTrustedPath("late"), "a refused connection must not be published")
}

func TestRegister_RacingFenceAllNeverLeavesALiveConn(t *testing.T) {
	for i := range 200 {
		m := New(DenyAllReach())
		conn, _ := newTestConn(t, "racer", nil, nil)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.FenceAll() }()
		go func() { defer wg.Done(); _, _ = m.Register(conn) }()
		wg.Wait()

		require.ErrorIs(t, conn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed,
			"iteration %d: a connection that raced FenceAll survived unfenced", i)
	}
}

func TestNotifyShutdownAndFence_DoesNotBlockRegisterWhileAWriteIsParked(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctxConn, cancelConn := context.WithCancel(context.Background())
	conn, pump := NewConn(ctxConn, cancelConn, "blocked", testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	t.Cleanup(conn.Fence)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()
	_, _ = m.Register(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.NotifyShutdownAndFence(ctx, 10)
		close(done)
	}()
	<-started

	registered := make(chan struct{})
	go func() {
		// Registry is fenced by Notify's deferred FenceAll once ctx expires
		// or delivery completes; registering a new worker while the write is
		// parked must not block on the manager lock either way.
		succ, _ := newTestConn(t, "new", nil, nil)
		_, _ = m.Register(succ)
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("Register blocked behind a parked shutdown write")
	}
	<-done
}

func TestNotifyShutdownAndFence_ReturnsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctxConn, cancelConn := context.WithCancel(context.Background())
	conn, pump := NewConn(ctxConn, cancelConn, "blocked", testConnOwner, sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	t.Cleanup(conn.Fence)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()
	_, _ = m.Register(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.NotifyShutdownAndFence(ctx, 10)
		close(done)
	}()
	<-started
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyShutdownAndFence ignored context expiration")
	}
}

func TestNotifyShutdownAndFence_CountsOnlyFramesThatReachedTheWire(t *testing.T) {
	m := New(DenyAllReach())

	var wrote atomic.Int32
	drained, drainedPump := newTestConn(t, "drained", func(*leapmuxv1.ConnectResponse) error {
		wrote.Add(1)
		return nil
	}, nil)
	pumpStart(t, drainedPump, drained)
	_, _ = m.Register(drained)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	parked, pump := newTestConn(t, "parked", func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-parked.Done():
				return
			}
		}
	}()
	_, _ = m.Register(parked)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)
	<-started

	// Only the drained conn's write completed inside the budget; the parked
	// one is still mid-write when Flush times out. NotifySuccess counts
	// frames that reached the wire — drained succeeded, parked did not.
	assert.Equal(t, int32(1), wrote.Load(), "only the drained conn's notification reached the wire")
}

func TestNotifyShutdownAndFence_FencesAfterTheNotificationDrains(t *testing.T) {
	m := New(DenyAllReach())

	var mu sync.Mutex
	var order []string
	conn, pump := newTestConn(t, "w1", func(*leapmuxv1.ConnectResponse) error {
		mu.Lock()
		order = append(order, "write")
		mu.Unlock()
		return nil
	}, nil)
	// Observe cancel ordering.
	prev := conn.cancel
	conn.cancel = func() {
		mu.Lock()
		order = append(order, "fence")
		mu.Unlock()
		prev()
	}
	pumpStart(t, pump, conn)
	_, _ = m.Register(conn)

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	m.NotifyShutdownAndFence(ctx, 10)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"write", "fence"}, order,
		"notification must reach the wire before the connection is fenced")
}
