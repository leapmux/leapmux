// Package periodic provides a small scheduling helper for long-lived
// background tasks (cleanup loops, archive rollups, etc.). It owns the
// goroutine + ticker + jitter + panic-recovery boilerplate so callers can
// describe just the cadence and the work.
package periodic

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/panicsafe"
)

// tagTicker and tagJitter name the two waits of the loop. A test that drives a
// mock clock traps a clock call by tag, so the interval ticker and the pre-run
// jitter timer must carry different tags: a test that releases one must not
// release the other.
const (
	tagTicker = "periodic.ticker"
	tagJitter = "periodic.jitter"
)

// Schedule defines the cadence of a periodic background task.
type Schedule struct {
	// Interval between successive runs after the first.
	Interval time.Duration
	// Jitter is the maximum random delay applied before each run. If
	// <= 0, no jitter is applied. Use jitter to spread load when many
	// instances start near-simultaneously.
	Jitter time.Duration
	// SkipFirstRun, when true, makes Start wait for the first ticker
	// tick (Interval, plus any Jitter) before invoking task. The
	// default (false) runs task eagerly at startup, which suits cleanup
	// loops where stale data should be reaped at boot. Set to true for
	// tasks that have nothing to do at startup — e.g., token refreshes
	// that need tokens to age, or cache sweeps that need cache entries
	// to accumulate.
	SkipFirstRun bool
}

// Start runs `task(ctx)` in a background goroutine immediately (after a
// random delay in [0, Schedule.Jitter)) and then once per Schedule.Interval
// thereafter, with the same jitter applied before every run. The goroutine
// returns when ctx is canceled.
//
// A panic inside `task` is recovered and logged so the loop survives. The
// next scheduled run will fire normally.
//
// Panics if Schedule.Interval <= 0 (which would otherwise crash inside the
// ticker that the loop arms). This is a programmer-error check intended to
// fail at process startup, not at runtime.
func Start(ctx context.Context, schedule Schedule, task func(context.Context)) {
	start(ctx, quartz.NewReal(), schedule, task)
}

// start is Start with the clock supplied, and with the exit of the goroutine
// observable through the returned channel, which closes after the loop stops.
//
// The tests take both. A mock clock lets a test assert the ORDER of the steps
// of the loop and the exact delay each step asks for, instead of racing two
// real timers: a window that a test sizes at a fraction of Interval holds only
// while the sleep of the host is accurate, and Windows rounds a sleep up to a
// timer granularity of about 15.6ms, which is wide enough to step over a whole
// interval. The exit channel then replaces "wait and see whether the task runs
// again" with the statement that the loop stopped.
func start(
	ctx context.Context,
	clock quartz.Clock,
	schedule Schedule,
	task func(context.Context),
) <-chan struct{} {
	if schedule.Interval <= 0 {
		panic("periodic.Start: Schedule.Interval must be > 0")
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)

		runOnce := func() {
			if !waitJitter(ctx, clock, schedule.Jitter) {
				return
			}
			defer panicsafe.RecoverAndLog(nil, "periodic.Start: task panic recovered")
			task(ctx)
		}

		if !schedule.SkipFirstRun {
			runOnce()
		}

		ticker := clock.NewTicker(schedule.Interval, tagTicker)
		defer ticker.Stop(tagTicker)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()
	return stopped
}

// waitJitter blocks for a random duration in [0, jitter) (or returns
// immediately if jitter <= 0). Returns false if ctx was canceled while
// waiting, so the caller can short-circuit before invoking the task.
func waitJitter(ctx context.Context, clock quartz.Clock, jitter time.Duration) bool {
	if jitter <= 0 {
		return ctx.Err() == nil
	}
	d := time.Duration(rand.Int64N(int64(jitter)))
	timer := clock.NewTimer(d, tagJitter)
	// Stop releases the timer on the cancel path. time.After, which this
	// replaced, holds its timer until it fires -- for a loop that a canceled
	// context takes out of a long jitter, that is the whole jitter.
	defer timer.Stop(tagJitter)
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
