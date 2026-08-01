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
)

func TestNotifyShutdownAndFence_SendsToAllWorkers(t *testing.T) {
	m := New(DenyAllReach())

	var mu sync.Mutex
	var received []*leapmuxv1.ConnectResponse

	makeMockConn := func(workerID string) *Conn {
		return &Conn{
			WorkerID: workerID,
			SendFn: func(msg *leapmuxv1.ConnectResponse) error {
				mu.Lock()
				defer mu.Unlock()
				received = append(received, msg)
				return nil
			},
		}
	}

	_, _ = m.Register(makeMockConn("w1"))
	_, _ = m.Register(makeMockConn("w2"))
	_, _ = m.Register(makeMockConn("w3"))

	m.NotifyShutdownAndFence(context.Background(), 10)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, received, 3)
	for _, msg := range received {
		payload, ok := msg.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
		require.True(t, ok, "expected HubShuttingDown payload")
		assert.Equal(t, int32(10), payload.HubShuttingDown.GetRetryDelaySeconds())
	}
}

func TestNotifyShutdownAndFence_CustomRetryDelay(t *testing.T) {
	m := New(DenyAllReach())

	var received *leapmuxv1.ConnectResponse
	_, _ = m.Register(&Conn{
		WorkerID: "w1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			received = msg
			return nil
		},
	})

	m.NotifyShutdownAndFence(context.Background(), 30)

	require.NotNil(t, received)
	payload, ok := received.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
	require.True(t, ok)
	assert.Equal(t, int32(30), payload.HubShuttingDown.GetRetryDelaySeconds())
}

func TestNotifyShutdownAndFence_NoWorkers(t *testing.T) {
	m := New(DenyAllReach())
	// Should not panic when no workers are connected.
	m.NotifyShutdownAndFence(context.Background(), 10)
}

func TestNotifyShutdownAndFence_ContinuesOnSendError(t *testing.T) {
	m := New(DenyAllReach())

	sendCount := 0
	var mu sync.Mutex

	// First worker: send fails.
	_, _ = m.Register(&Conn{
		WorkerID: "w-fail",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return fmt.Errorf("connection reset")
		},
	})

	// Second worker: send succeeds.
	_, _ = m.Register(&Conn{
		WorkerID: "w-ok",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return nil
		},
	})

	// Should not panic or abort; best-effort delivery.
	m.NotifyShutdownAndFence(context.Background(), 10)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, sendCount, "should attempt to send to all workers even on error")
}

// The notification alone leaves the stream open -- a worker holds it until it
// decides to reconnect -- so NotifyShutdownAndFence must also end the handler, or the
// Hub's HTTP drain waits on a connection nothing is going to close.
func TestNotifyShutdownAndFence_FencesConnectionsAfterDelivering(t *testing.T) {
	m := New(DenyAllReach())

	var delivered, cancelled atomic.Int32
	makeConn := func(workerID string) *Conn {
		return &Conn{
			WorkerID: workerID,
			SendFn: func(*leapmuxv1.ConnectResponse) error {
				// Fencing must come after delivery: a worker that never hears
				// the notification reconnects on its ordinary backoff instead
				// of the delay the Hub asked for.
				assert.Zero(t, cancelled.Load(), "connection fenced before its notification was sent")
				delivered.Add(1)
				return nil
			},
			Cancel: func() { cancelled.Add(1) },
		}
	}
	_, _ = m.Register(makeConn("w1"))
	_, _ = m.Register(makeConn("w2"))

	m.NotifyShutdownAndFence(context.Background(), 10)

	assert.Equal(t, int32(2), delivered.Load(), "both workers notified")
	assert.Equal(t, int32(2), cancelled.Load(), "both Connect handlers cancelled")
	assert.ErrorIs(t, m.ConnForTrustedPath("w1").Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed,
		"a fenced connection must refuse further sends")
}

// A worker that cannot be notified inside the budget is exactly the one that
// would otherwise hold the drain open for the full idle timeout, so the fence
// has to run on the deadline path too.
func TestNotifyShutdownAndFence_FencesConnectionsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	var cancelled atomic.Bool
	_, _ = m.Register(&Conn{
		WorkerID: "wedged",
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			close(started)
			<-release
			return nil
		},
		Cancel: func() { cancelled.Store(true) },
	})

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
	// Nothing registered is the state a Hub that never had a worker shuts down
	// in; the copy-then-fence walk must not panic on it.
	assert.NotPanics(t, m.FenceAll)
}

// A registered connection is not obliged to carry a Cancel -- the test doubles
// throughout this package do not, and neither would a conn published before its
// handler context exists. Fencing one must still stop its sends.
func TestFenceAll_ConnectionWithoutCancel(t *testing.T) {
	m := New(DenyAllReach())
	_, _ = m.Register(&Conn{
		WorkerID: "no-cancel",
		SendFn:   func(*leapmuxv1.ConnectResponse) error { return nil },
	})

	assert.NotPanics(t, m.FenceAll)
	assert.ErrorIs(t, m.ConnForTrustedPath("no-cancel").Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
}

// The registry lock must not be held while a connection is cancelled. Cancel
// wakes the Connect handler, whose defer runs Unregister -- and Unregister
// takes the write lock. Fencing under the read lock would make the Hub's own
// shutdown the thing that wedges it.
func TestFenceAll_DoesNotHoldManagerLockDuringCancel(t *testing.T) {
	m := New(DenyAllReach())

	reRegistered := make(chan struct{})
	_, _ = m.Register(&Conn{
		WorkerID: "fenced",
		Cancel: func() {
			go func() {
				// Refused (the latch is already set) -- what matters here is
				// that it RETURNS rather than blocking, which it could only do
				// by acquiring the registry lock.
				_, _ = m.Register(&Conn{WorkerID: "successor"})
				close(reRegistered)
			}()
			select {
			case <-reRegistered:
			case <-time.After(time.Second):
				assert.Fail(t, "Register blocked behind FenceAll's registry lock")
			}
		},
	})

	m.FenceAll()
	<-reRegistered
}

// A Connect handler can pass the shutdown interceptor -- a one-shot check --
// microseconds before the Hub starts shutting down, then spend a store round
// trip and a greeting write getting here. Fencing a snapshot would let it
// publish a connection nothing ever cancels, and that handler holds the Hub's
// HTTP drain open until workerIdleTimeout.
func TestRegister_RefusedOnceFenced(t *testing.T) {
	m := New(DenyAllReach())
	m.FenceAll()

	var cancelled atomic.Bool
	var sends atomic.Int32
	late := &Conn{
		WorkerID: "late",
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			sends.Add(1)
			return nil
		},
		Cancel: func() { cancelled.Store(true) },
		Greeting: &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
				WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "someone"},
			},
		},
	}
	replaced, err := m.Register(late)

	require.ErrorIs(t, err, ErrRegistryFenced, "a fenced registry must refuse a late registration")
	assert.False(t, replaced)
	assert.True(t, cancelled.Load(), "the refused connection's handler must still be ended")
	// The refusal has to come BEFORE the greeting write, or the worker reads
	// its own identity and only then sees the stream end -- a sequence that
	// reads like the Hub accepted it and then dropped it.
	assert.Zero(t, sends.Load(), "a refused connection must not be greeted first")
	assert.ErrorIs(t, late.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
	assert.Nil(t, m.ConnForTrustedPath("late"), "a refused connection must not be published")
}

// The latch is what makes the fence total rather than point-in-time: whichever
// order these two run in, the connection must end up fenced. Without it, a
// Register that lands after FenceAll's snapshot leaves a live, unfenced conn.
func TestRegister_RacingFenceAllNeverLeavesALiveConn(t *testing.T) {
	for i := range 200 {
		m := New(DenyAllReach())
		conn := &Conn{
			WorkerID: "racer",
			SendFn:   func(*leapmuxv1.ConnectResponse) error { return nil },
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.FenceAll() }()
		go func() { defer wg.Done(); _, _ = m.Register(conn) }()
		wg.Wait()

		require.ErrorIs(t, conn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed,
			"iteration %d: a connection that raced FenceAll survived unfenced", i)
	}
}

// Unregister must wait for an in-flight send on BOTH paths, replaced or not.
// The caller is the connection's own handler, about to return; if it returns
// while a background sender is inside Stream.Send, net/http panics
// ("Write called after Handler finished") on the sender's goroutine and the
// response-writer state has already been recycled into a pool.
func TestUnregister_WaitsForInFlightSendWhenReplaced(t *testing.T) {
	m := New(DenyAllReach())

	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	old := &Conn{
		WorkerID: "w",
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			close(sendStarted)
			<-releaseSend
			return nil
		},
	}
	_, _ = m.Register(old)

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_ = old.Send(&leapmuxv1.ConnectResponse{})
	}()
	<-sendStarted

	// A reconnect publishes a successor, so Unregister will take its
	// "not the registered conn" path -- the one that used to skip the wait.
	_, _ = m.Register(&Conn{WorkerID: "w", SendFn: func(*leapmuxv1.ConnectResponse) error { return nil }})

	unregistered := make(chan bool, 1)
	go func() { unregistered <- m.Unregister("w", old) }()

	select {
	case <-unregistered:
		t.Fatal("Unregister returned while a send was still on the old stream; the handler would return into a write-after-finish panic")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseSend)
	<-sendDone
	select {
	case removed := <-unregistered:
		assert.False(t, removed, "the superseded conn must not remove its replacement's registration")
	case <-time.After(5 * time.Second):
		t.Fatal("Unregister never returned after the in-flight send finished")
	}
}

func TestNotifyShutdownAndFence_DoesNotHoldManagerLockDuringSend(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	_, _ = m.Register(&Conn{WorkerID: "blocked", SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}})

	done := make(chan struct{})
	go func() {
		m.NotifyShutdownAndFence(context.Background(), 10)
		close(done)
	}()
	<-started

	registered := make(chan struct{})
	go func() {
		_, _ = m.Register(&Conn{WorkerID: "new"})
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("Register blocked behind a shutdown notification send")
	}
	close(release)
	<-done
}

func TestNotifyShutdownAndFence_ReturnsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, _ = m.Register(&Conn{WorkerID: "blocked", SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}})
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
