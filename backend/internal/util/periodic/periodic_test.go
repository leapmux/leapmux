package periodic

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFor polls until cond returns true or timeout elapses. Used to keep
// goroutine-timing tests deterministic without arbitrary sleeps.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func TestStart_FirstRunFiresThenTickerFires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	Start(ctx, Schedule{Interval: 20 * time.Millisecond}, func(context.Context) {
		calls.Add(1)
	})

	require.True(t, waitFor(t, 500*time.Millisecond, func() bool { return calls.Load() >= 3 }),
		"expected at least 3 invocations; got %d", calls.Load())
}

func TestStart_CancellationDuringJitterStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int64
	// Long jitter so the first run is still waiting when we cancel.
	Start(ctx, Schedule{Interval: time.Hour, Jitter: time.Hour}, func(context.Context) {
		calls.Add(1)
	})

	cancel()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int64(0), calls.Load(), "task must not run after pre-tick cancel")
}

func TestStart_CancellationBetweenTicksStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int64
	Start(ctx, Schedule{Interval: 10 * time.Millisecond}, func(context.Context) {
		calls.Add(1)
	})

	require.True(t, waitFor(t, 500*time.Millisecond, func() bool { return calls.Load() >= 1 }),
		"expected first run to complete")

	cancel()
	snapshot := calls.Load()
	time.Sleep(50 * time.Millisecond)
	// At most one more invocation can race the cancel signal in the select
	// (Go's select is non-deterministic between ready channels).
	assert.LessOrEqual(t, calls.Load()-snapshot, int64(1), "loop must stop firing after cancel")
}

func TestStart_TaskPanicIsRecoveredAndLoopContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	Start(ctx, Schedule{Interval: 10 * time.Millisecond}, func(context.Context) {
		calls.Add(1)
		panic("boom")
	})

	require.True(t, waitFor(t, 500*time.Millisecond, func() bool { return calls.Load() >= 3 }),
		"loop must survive panicking task; got %d invocations", calls.Load())
}

func TestStart_ZeroIntervalPanics(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r, "Start must panic when Schedule.Interval <= 0")
	}()
	Start(context.Background(), Schedule{Interval: 0}, func(context.Context) {})
}

func TestStart_NegativeIntervalPanics(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r, "Start must panic when Schedule.Interval is negative")
	}()
	Start(context.Background(), Schedule{Interval: -1 * time.Second}, func(context.Context) {})
}

func TestStart_SkipFirstRunWaitsForFirstTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	start := time.Now()
	Start(ctx, Schedule{Interval: 50 * time.Millisecond, SkipFirstRun: true}, func(context.Context) {
		calls.Add(1)
	})

	// At ~25ms (well before the first tick at 50ms), the task must not have
	// run yet — proving the eager invocation was skipped.
	time.Sleep(25 * time.Millisecond)
	assert.Equal(t, int64(0), calls.Load(), "task must not run before the first tick when SkipFirstRun is true")

	// After the first tick, it should fire normally.
	require.True(t, waitFor(t, 500*time.Millisecond, func() bool { return calls.Load() >= 1 }),
		"task must fire on the first tick")
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "first invocation must wait for at least one Interval")
}

func TestStart_SkipFirstRunHonorsJitterBeforeFirstTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const interval = 50 * time.Millisecond
	const jitter = 50 * time.Millisecond

	var firstSeen atomic.Int64
	start := time.Now()
	Start(ctx, Schedule{Interval: interval, Jitter: jitter, SkipFirstRun: true}, func(context.Context) {
		firstSeen.CompareAndSwap(0, time.Since(start).Nanoseconds())
	})

	require.True(t, waitFor(t, time.Second, func() bool { return firstSeen.Load() > 0 }),
		"first invocation must occur")

	got := time.Duration(firstSeen.Load())
	// First tick lands at Interval; jitter then adds [0, jitter) before the
	// task body runs — so the lower bound is Interval (no jitter), and the
	// upper bound is Interval + jitter (plus scheduler slack).
	assert.GreaterOrEqual(t, got, interval, "first invocation must wait at least Interval")
	assert.Less(t, got, interval+jitter+200*time.Millisecond, "first invocation should fire within Interval+Jitter+slack")
}

func TestStart_SkipFirstRunStillStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int64
	Start(ctx, Schedule{Interval: 10 * time.Millisecond, SkipFirstRun: true}, func(context.Context) {
		calls.Add(1)
	})

	cancel()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(0), calls.Load(), "cancellation before any tick must short-circuit the loop")
}

func TestStart_ZeroJitterDoesNotPanic(t *testing.T) {
	// rand.Int64N(0) panics; the helper must guard against jitter == 0.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var once sync.Once
	Start(ctx, Schedule{Interval: time.Hour}, func(context.Context) {
		once.Do(func() { close(done) })
	})

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("zero-jitter run did not fire")
	}
}
