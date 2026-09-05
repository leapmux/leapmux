package periodic

import (
	"context"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopWindow limits the wait for the loop to leave its goroutine. It is a
// DEADLOCK GUARD, not a timing assumption: a loop that honors its canceled
// context returns at once and never reaches it.
const stopWindow = 10 * time.Second

// loop drives one periodic loop on a mock clock. Every wait below ends on a
// step the loop itself takes -- a clock call the test traps, an invocation the
// task records, the exit of the goroutine -- so the assertions state the order
// of those steps and the exact delay each step asks for. None of them measures
// a wall-clock window.
type loop struct {
	t     *testing.T
	ctx   context.Context
	clock *quartz.Mock

	// tickerArm and jitterArm catch the loop as it arms each of its two waits.
	// A caught call parks the loop until the test releases it, which is what
	// makes "the task did not run yet" a statement about the position of the
	// loop rather than about elapsed time.
	tickerArm *quartz.Trap
	jitterArm *quartz.Trap

	// calls carries the mock-clock time elapsed at each invocation of the
	// task, stamped by the task itself.
	calls   chan time.Duration
	stopped <-chan struct{}
	cancel  context.CancelFunc
}

// startLoop starts one loop on a mock clock and registers the cleanup that
// stops it. body runs inside the task after the invocation is recorded; pass
// nil for a task that does nothing else.
func startLoop(t *testing.T, schedule Schedule, body func()) *loop {
	t.Helper()

	clock := testutil.NewQuartzMock(t)
	loopCtx, cancel := context.WithCancel(context.Background())
	l := &loop{
		t:      t,
		ctx:    testutil.DeadlineContext(t),
		clock:  clock,
		calls:  make(chan time.Duration, 16),
		cancel: cancel,
	}
	epoch := clock.Now()

	// A cleanup runs before the one registered ahead of it, so the traps close
	// FIRST and the wait for the exit comes after. A test that leaves the loop
	// parked inside a trapped clock call would otherwise never let it see the
	// cancel, and the wait would hold until stopWindow.
	t.Cleanup(func() {
		cancel()
		l.awaitStopped()
	})
	l.tickerArm = clock.Trap().NewTicker(tagTicker)
	l.jitterArm = clock.Trap().NewTimer(tagJitter)
	t.Cleanup(func() {
		l.tickerArm.Close()
		l.jitterArm.Close()
	})

	l.stopped = start(loopCtx, clock, schedule, func(context.Context) {
		l.calls <- clock.Since(epoch)
		if body != nil {
			body()
		}
	})
	return l
}

// awaitCall returns the mock-clock time elapsed at the next invocation of the
// task.
func (l *loop) awaitCall() time.Duration {
	l.t.Helper()
	select {
	case at := <-l.calls:
		return at
	case <-l.ctx.Done():
		l.t.Fatal("the task did not run")
		return 0
	}
}

// awaitTickerArmed blocks until the loop arms its interval ticker and returns
// the interval it asked for. It is also an ordering point: every step the loop
// takes before it arms the ticker is complete when this returns.
func (l *loop) awaitTickerArmed() time.Duration {
	l.t.Helper()
	return testutil.WaitForTimer(l.t, l.ctx, l.tickerArm)
}

// awaitJitterArmed blocks until the loop arms the jitter timer of the next run
// and returns the delay it drew.
func (l *loop) awaitJitterArmed() time.Duration {
	l.t.Helper()
	return testutil.WaitForTimer(l.t, l.ctx, l.jitterArm)
}

// advance moves the mock clock by d and waits until every timer that d fires
// delivered its tick.
func (l *loop) advance(d time.Duration) {
	l.t.Helper()
	l.clock.Advance(d).MustWait(l.ctx)
}

// awaitStopped waits for the goroutine of the loop to return.
//
// It takes its own deadline instead of l.ctx because the cleanup calls it too,
// and t.Context() -- the parent of l.ctx -- is already canceled by the time a
// cleanup runs. See stopWindow for what the limit means.
func (l *loop) awaitStopped() {
	l.t.Helper()
	timer := time.NewTimer(stopWindow)
	defer timer.Stop()
	select {
	case <-l.stopped:
	case <-timer.C:
		l.t.Error("the loop did not stop after its context was canceled")
	}
}

// requireNoCalls states that the task did not run. It is valid only at an
// ordering point -- a trap the test just released, the exit of the loop --
// where the loop sits at a known step and no invocation can still be in
// flight.
func (l *loop) requireNoCalls(msg string) {
	l.t.Helper()
	require.Zero(l.t, len(l.calls), msg)
}

func TestStart_FirstRunFiresThenTickerFires(t *testing.T) {
	const interval = 20 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval}, nil)

	assert.Equal(t, time.Duration(0), l.awaitCall(), "the first run must fire at once")
	assert.Equal(t, interval, l.awaitTickerArmed(), "the ticker must carry Interval")

	l.advance(interval)
	assert.Equal(t, interval, l.awaitCall(), "the second run must land on the first tick")

	l.advance(interval)
	assert.Equal(t, 2*interval, l.awaitCall(), "the third run must land on the second tick")
}

func TestStart_CancellationDuringJitterStopsLoop(t *testing.T) {
	l := startLoop(t, Schedule{Interval: time.Hour, Jitter: time.Hour}, nil)

	// The loop waits out its jitter. The clock never advances, so only the
	// cancel can release it.
	l.awaitJitterArmed()
	l.cancel()

	l.awaitTickerArmed()
	l.awaitStopped()
	l.requireNoCalls("the task must not run after a cancel that lands during the jitter")
}

func TestStart_CancellationBetweenTicksStopsLoop(t *testing.T) {
	l := startLoop(t, Schedule{Interval: 10 * time.Millisecond}, nil)

	require.Equal(t, time.Duration(0), l.awaitCall(), "the first run must fire at once")
	l.awaitTickerArmed()

	l.cancel()
	l.awaitStopped()
	l.requireNoCalls("the loop must not run the task again after the cancel")
}

func TestStart_TaskPanicIsRecoveredAndLoopContinues(t *testing.T) {
	const interval = 10 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval}, func() { panic("boom") })

	assert.Equal(t, time.Duration(0), l.awaitCall(), "the first run must fire at once")
	l.awaitTickerArmed()

	l.advance(interval)
	assert.Equal(t, interval, l.awaitCall(), "the loop must survive the panic of the first run")

	l.advance(interval)
	assert.Equal(t, 2*interval, l.awaitCall(), "the loop must keep its cadence after a panic")
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
	const interval = 50 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval, SkipFirstRun: true}, nil)

	// Arming the ticker is the first clock call of the loop when SkipFirstRun
	// is true. An eager run would already have invoked the task here, because
	// it comes before this call.
	assert.Equal(t, interval, l.awaitTickerArmed(), "the ticker must carry Interval")
	l.requireNoCalls("the task must not run before the first tick when SkipFirstRun is true")

	l.advance(interval)
	assert.Equal(t, interval, l.awaitCall(), "the first run must land on the first tick")
}

func TestStart_SkipFirstRunHonorsJitterBeforeFirstTick(t *testing.T) {
	const interval = 50 * time.Millisecond
	const jitter = 50 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval, Jitter: jitter, SkipFirstRun: true}, nil)

	l.awaitTickerArmed()
	l.advance(interval)

	// The first tick starts the run; the jitter is the delay the run then adds
	// on top of it.
	drawn := l.awaitJitterArmed()
	assert.GreaterOrEqual(t, drawn, time.Duration(0), "the jitter must not be negative")
	assert.Less(t, drawn, jitter, "the jitter must stay below Schedule.Jitter")
	l.requireNoCalls("the task must not run before its jitter elapses")

	l.advance(drawn)
	assert.Equal(t, interval+drawn, l.awaitCall(), "the first run must land at Interval plus the jitter")
}

func TestStart_JitterDelaysEagerFirstRun(t *testing.T) {
	const interval = 50 * time.Millisecond
	const jitter = 20 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval, Jitter: jitter}, nil)

	drawn := l.awaitJitterArmed()
	assert.GreaterOrEqual(t, drawn, time.Duration(0), "the jitter must not be negative")
	assert.Less(t, drawn, jitter, "the jitter must stay below Schedule.Jitter")
	l.requireNoCalls("the eager first run must wait out its jitter")

	l.advance(drawn)
	assert.Equal(t, drawn, l.awaitCall(), "the eager first run must land at the drawn jitter")
}

func TestStart_JitterAppliesToEveryRun(t *testing.T) {
	const interval = 50 * time.Millisecond
	const jitter = 20 * time.Millisecond

	l := startLoop(t, Schedule{Interval: interval, Jitter: jitter}, nil)

	first := l.awaitJitterArmed()
	l.advance(first)
	require.Equal(t, first, l.awaitCall(), "the eager first run must land at the drawn jitter")

	// The ticker starts at the end of the first run, so its first tick lands
	// one whole Interval later.
	require.Equal(t, interval, l.awaitTickerArmed(), "the ticker must carry Interval")
	l.advance(interval)

	second := l.awaitJitterArmed()
	assert.Less(t, second, jitter, "each run must draw its own jitter")
	l.requireNoCalls("the tick-driven run must wait out its jitter too")

	l.advance(second)
	assert.Equal(t, first+interval+second, l.awaitCall(),
		"the tick-driven run must land at its tick plus its own jitter")
}

func TestStart_SkipFirstRunStillStopsOnCancel(t *testing.T) {
	l := startLoop(t, Schedule{Interval: 10 * time.Millisecond, SkipFirstRun: true}, nil)

	l.awaitTickerArmed()
	l.cancel()

	l.awaitStopped()
	l.requireNoCalls("a cancel before any tick must short-circuit the loop")
}

func TestStart_ZeroJitterDoesNotPanic(t *testing.T) {
	// rand.Int64N(0) panics, and it panics OUTSIDE the recover of the loop,
	// which covers the task alone. The guard for jitter == 0 in waitJitter is
	// what keeps the run below from taking down the process.
	l := startLoop(t, Schedule{Interval: time.Hour}, nil)

	assert.Equal(t, time.Duration(0), l.awaitCall(), "a zero-jitter run must fire with no wait")
}
