package workermgr

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

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

// notifyFailureRecorder swaps in an slog handler that records, under a lock,
// which workers' shutdown-notification failures have actually been logged.
//
// It exists for the deadline tests below. NotifyShutdownAndFence's deadline
// arm returns without collecting stragglers, so a parked conn's per-conn
// goroutine goes on to log its Warn whenever the scheduler admits it -- on a
// loaded runner that can be after this test has returned and a later test has
// installed its own CaptureDefaultLogger, where the line reads as that test's
// fault (CI has seen worker_id=parked fail a test that never created it).
// The Warn is the abandoned goroutine's last use of the default logger, so a
// test that waits it out before returning leaves nothing behind for the next
// one to swallow. CaptureDefaultLogger's bytes.Buffer cannot serve this wait:
// it is not safe to read while the goroutine under observation may still be
// writing.
type notifyFailureRecorder struct {
	mu     sync.Mutex
	logged map[string]bool
}

func recordNotifyFailures(t *testing.T) *notifyFailureRecorder {
	t.Helper()
	r := &notifyFailureRecorder{logged: map[string]bool{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(r))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return r
}

func (r *notifyFailureRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *notifyFailureRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "failed to send shutdown notification to worker" {
		return nil
	}
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "worker_id" {
			r.mu.Lock()
			r.logged[attr.Value.String()] = true
			r.mu.Unlock()
		}
		return true
	})
	return nil
}

// WithAttrs and WithGroup return the recorder itself: the manager logs through
// the bare default logger, so there is no attr or group state to carry.
func (r *notifyFailureRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *notifyFailureRecorder) WithGroup(string) slog.Handler      { return r }

// loggedFailure is safe to poll from the test goroutine while the abandoned
// notify goroutine may still be inside Handle.
func (r *notifyFailureRecorder) loggedFailure(workerID string) func() bool {
	return func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.logged[workerID]
	}
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
	logs := testutil.CaptureDefaultLogger(t)
	got := m.NotifyShutdownAndFence(ctx, 10)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, sendCount, "should attempt to send to all workers even on error")
	// The Failed bucket and its warning are the ONE operator-facing signal in
	// this function -- "the Hub could not tell a live worker" -- and a write
	// error is the way to reach them. Asserted here because a broken transport
	// fences the conn on its way out, so without the give-up latch this reports
	// as the worker having left and the warning silently becomes a Debug line.
	assert.Equal(t, ShutdownNotifyResult{Delivered: 1, Failed: 1, Total: 2}, got)
	assert.Contains(t, logs.String(), "failed to send shutdown notification to worker")
	assert.Contains(t, logs.String(), "level=WARN")
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
	// The broadcast below ends on its deadline and abandons the blocked conn's
	// notify goroutine; the recorder is what lets this test wait out that
	// goroutine's late Warn (see notifyFailureRecorder).
	failures := recordNotifyFailures(t)
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
	testutil.RequireEventually(t, failures.loggedFailure("blocked"),
		"the deadline arm abandons the blocked conn's goroutine; its Warn must land before this test ends")
}

func TestNotifyShutdownAndFence_ReturnsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())
	// The deadline arm abandons the blocked conn's notify goroutine; the
	// recorder is what lets this test wait out its late Warn.
	failures := recordNotifyFailures(t)
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
	testutil.RequireEventually(t, failures.loggedFailure("blocked"),
		"the deadline arm abandons the blocked conn's goroutine; its Warn must land before this test ends")
}

func TestNotifyShutdownAndFence_CountsOnlyFramesThatReachedTheWire(t *testing.T) {
	m := New(DenyAllReach())
	// The deadline arm abandons the parked conn's notify goroutine; the
	// recorder is what lets this test wait out its late Warn.
	failures := recordNotifyFailures(t)

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
	got := m.NotifyShutdownAndFence(ctx, 10)
	<-started

	// Only the drained conn's write completed inside the budget; the parked
	// one is still mid-write when Flush times out. The tally counts frames that
	// reached the wire — drained succeeded, parked did not.
	assert.Equal(t, int32(1), wrote.Load(), "only the drained conn's notification reached the wire")
	// Total is what was ATTEMPTED, so it stays 2 while the parked conn's own
	// outcome never lands: the deadline arm returns without collecting it.
	assert.Equal(t, ShutdownNotifyResult{Delivered: 1, Total: 2}, got)
	testutil.RequireEventually(t, failures.loggedFailure("parked"),
		"the deadline arm abandons the parked conn's goroutine; its Warn must land before this test ends")
}

// classifyNotifyErr is where "already gone" is told apart from "could not
// reach a live worker". Every Hub-side cause converges on the same observable
// state as a worker leaving -- a closed Done(), ErrConnectionClosed from later
// sends -- so the table's job is to pin that each one is still reported as the
// Hub's fault.
func TestClassifyNotifyErr(t *testing.T) {
	liveConn := func(t *testing.T) *Conn {
		t.Helper()
		conn, _ := newTestConn(t, "live", nil, nil)
		return conn
	}
	fencedConn := func(t *testing.T) *Conn {
		t.Helper()
		conn, _ := newTestConn(t, "fenced", nil, nil)
		conn.Fence()
		return conn
	}
	// The Hub reclaiming a worker whose transport broke: driven through a real
	// write failure rather than by setting a flag, because the ordering IS the
	// subject -- the queue must record the cause early enough for the Flush that
	// races it to see one, and the give-up callback alone fires too late.
	gaveUpConn := func(t *testing.T) *Conn {
		t.Helper()
		conn, pump := newTestConn(t, "gave-up", func(*leapmuxv1.ConnectResponse) error {
			return fmt.Errorf("connection reset")
		}, nil)
		pumpStart(t, pump, conn)
		require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{}))
		ctx, cancel := boundedNotifyCtx(t)
		defer cancel()
		_ = conn.Flush(ctx)
		require.True(t, conn.GaveUp(), "fixture must model a queue the Hub gave up on")
		return conn
	}
	expired := func(t *testing.T) context.Context {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	tests := []struct {
		name string
		conn func(*testing.T) *Conn
		// callerCtx is the broadcast's own budget; nil means a live one.
		callerCtx func(*testing.T) context.Context
		err       error
		want      notifyOutcome
	}{
		{name: "delivered", conn: liveConn, want: notifyDelivered},
		{name: "queue closed", conn: liveConn, err: ErrConnectionClosed, want: notifyAlreadyGone},
		{
			// The give-up path: same error a departed worker produces, but the
			// Hub abandoned this queue (write timeout / blown budget / pool
			// pressure). Filing it as "already gone" would hide the one thing an
			// operator can act on.
			name: "hub gave up on the queue",
			conn: gaveUpConn,
			err:  ErrConnectionClosed,
			want: notifyFailed,
		},
		{
			// The deferred FenceAll closes Done() on the deadline path while the
			// per-conn goroutines are still classifying, so past the caller's
			// budget Done() is a channel the Hub closed itself.
			name:      "caller budget expired against a fenced conn",
			conn:      fencedConn,
			callerCtx: expired,
			err:       context.Canceled,
			want:      notifyFailed,
		},
		{
			// SendControl fences before returning this, so the conn IS done --
			// and classifying on that alone would report the Hub overrunning its
			// own control reserve as the worker having left.
			name: "control saturated on a conn its own refusal fenced",
			conn: fencedConn,
			err:  ErrControlSaturated,
			want: notifyFailed,
		},
		{
			// What Flush surfaces when the worker's stream died mid-notification:
			// the CONN's context error, indistinguishable by value from the
			// caller's own budget expiring. Done() is what tells them apart.
			name: "conn context cancelled",
			conn: fencedConn,
			err:  context.Canceled,
			want: notifyAlreadyGone,
		},
		{
			// Same error value, live conn: the Hub's 2s budget ran out while the
			// worker was still there to hear it.
			name: "caller deadline on a live conn",
			conn: liveConn,
			err:  context.DeadlineExceeded,
			want: notifyFailed,
		},
		{name: "unclassified error on a live conn", conn: liveConn, err: fmt.Errorf("boom"), want: notifyFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callerCtx := context.Background()
			if tt.callerCtx != nil {
				callerCtx = tt.callerCtx(t)
			}
			assert.Equal(t, tt.want, classifyNotifyErr(callerCtx, tt.conn(t), tt.err))
		})
	}
}

func TestNotifyShutdownAndFence_ConnGoneMidNotificationIsNotAFailure(t *testing.T) {
	m := New(DenyAllReach())
	logs := testutil.CaptureDefaultLogger(t)

	conn, _ := newTestConn(t, "gone", nil, nil)
	_, _ = m.Register(conn)
	// No pump, so the queued notification can never drain and Flush has to
	// escape through the conn's own context -- which is the shape of a worker
	// that closed its Connect stream while the Hub was mid-broadcast.
	conn.cancel()

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	got := m.NotifyShutdownAndFence(ctx, 10)

	assert.Equal(t, ShutdownNotifyResult{AlreadyGone: 1, Total: 1}, got,
		"a worker that left before the Hub could speak is not a delivery failure")
	assert.NotContains(t, logs.String(), "level=WARN",
		"an ordinary co-shutdown must not be reported as a fault")
	assert.Contains(t, logs.String(), "worker disconnected before the shutdown notification",
		"it is still worth saying at Debug, so a real disappearance stays diagnosable")
}

func TestNotifyShutdownAndFence_FencedConnIsAlreadyGone(t *testing.T) {
	m := New(DenyAllReach())

	conn, _ := newTestConn(t, "fenced", nil, nil)
	_, _ = m.Register(conn)
	conn.Fence()

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	got := m.NotifyShutdownAndFence(ctx, 10)

	assert.Equal(t, ShutdownNotifyResult{AlreadyGone: 1, Total: 1}, got)
}

func TestNotifyShutdownAndFence_ReportsMixedOutcomes(t *testing.T) {
	m := New(DenyAllReach())

	live, livePump := newTestConn(t, "live", nil, nil)
	pumpStart(t, livePump, live)
	_, _ = m.Register(live)

	gone, _ := newTestConn(t, "gone", nil, nil)
	_, _ = m.Register(gone)
	gone.cancel()

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()
	got := m.NotifyShutdownAndFence(ctx, 10)

	assert.Equal(t, ShutdownNotifyResult{Delivered: 1, AlreadyGone: 1, Total: 2}, got)
}

// With an ordered solo teardown the Worker is gone before the Hub starts
// shutting down, which makes "no workers connected" the NORMAL case -- and a
// summary of nothing is the sort of line this whole change exists to remove.
func TestNotifyShutdownAndFence_SaysNothingWhenNoWorkerIsConnected(t *testing.T) {
	m := New(DenyAllReach())
	logs := testutil.CaptureDefaultLogger(t)

	ctx, cancel := boundedNotifyCtx(t)
	defer cancel()

	assert.Equal(t, ShutdownNotifyResult{}, m.NotifyShutdownAndFence(ctx, 10))
	assert.Empty(t, logs.String())
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
