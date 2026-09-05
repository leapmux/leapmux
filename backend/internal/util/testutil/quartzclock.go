package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/coder/quartz"
)

// deadlineWindow limits every mock-clock wait these helpers make. It is a
// DEADLOCK GUARD, not a timing assumption: a passing test never reaches it,
// because each wait ends on a trap the test itself releases. A test that hangs
// -- a trap nobody releases, a timer nobody arms -- fails here with the call it
// was waiting for instead of running to the whole package's ten-minute panic.
const deadlineWindow = 10 * time.Second

// NewQuartzMock builds the mock clock these helpers drive.
//
// The logger is off because quartz logs every event through tb.Log, and a test
// that advances a clock many times buries its own failure in that output.
func NewQuartzMock(t *testing.T) *quartz.Mock {
	t.Helper()
	return quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
}

// DeadlineContext returns the context every wait below takes. See
// deadlineWindow for what the limit means.
func DeadlineContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), deadlineWindow)
	t.Cleanup(cancel)
	return ctx
}

// NewTimerTraps catches both halves of one timer's life for tag: the arm, and
// the stop that releases it. Both close when the test ends.
//
// A trap that stays open blocks the NEXT matching call for ever, because quartz
// parks the calling goroutine inside the trapped call until the test releases
// it. Registering the close here is what keeps that from reaching the test that
// runs next.
func NewTimerTraps(t *testing.T, clock *quartz.Mock, tag string) (newTimer, stopTimer *quartz.Trap) {
	t.Helper()
	newTimer = clock.Trap().NewTimer(tag)
	stopTimer = clock.Trap().TimerStop(tag)
	t.Cleanup(func() {
		newTimer.Close()
		stopTimer.Close()
	})
	return newTimer, stopTimer
}

// WaitForTimer catches the next timer the code arms on trap, returns the delay
// it ASKED for, and lets the call proceed.
//
// The requested delay, never the gap it produces. A measured gap carries the
// host's timer granularity too -- about 15.6ms on Windows -- which is wide
// enough to swallow the difference between a 10ms and a 40ms rung and report
// them in the wrong order, and that is how these tests failed there while the
// policy was correct. A measured gap also forces a tolerance far wider than the
// value under test, so it cannot catch a limit several times too large.
func WaitForTimer(t *testing.T, ctx context.Context, trap *quartz.Trap) time.Duration {
	t.Helper()
	call := trap.MustWait(ctx)
	delay := call.Duration
	call.MustRelease(ctx)
	return delay
}

// AdvanceAndAwaitStop moves the clock by delay and waits until the woken code
// released its timer.
//
// The stop must be released BEFORE the advance is awaited. The fired timer
// parks inside the trapped Stop, and the advance does not finish until that
// call returns -- so awaiting the advance first deadlocks, and the failure
// reads as "context expired while waiting for clock to advance" rather than as
// the missing release.
func AdvanceAndAwaitStop(
	t *testing.T,
	ctx context.Context,
	clock *quartz.Mock,
	delay time.Duration,
	stopTimer *quartz.Trap,
) {
	t.Helper()
	waiter := clock.Advance(delay)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	waiter.MustWait(ctx)
}
