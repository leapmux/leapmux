// Package periodic provides a small scheduling helper for long-lived
// background tasks (cleanup loops, archive rollups, etc.). It owns the
// goroutine + ticker + jitter + panic-recovery boilerplate so callers can
// describe just the cadence and the work.
package periodic

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
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
// Panics if Schedule.Interval <= 0 (which would otherwise crash inside
// time.NewTicker). This is a programmer-error check intended to fail at
// process startup, not at runtime.
func Start(ctx context.Context, schedule Schedule, task func(context.Context)) {
	if schedule.Interval <= 0 {
		panic("periodic.Start: Schedule.Interval must be > 0")
	}
	go func() {
		runOnce := func() {
			if !waitJitter(ctx, schedule.Jitter) {
				return
			}
			defer func() {
				if r := recover(); r != nil {
					slog.Error("periodic.Start: task panic recovered", "panic", r)
				}
			}()
			task(ctx)
		}

		if !schedule.SkipFirstRun {
			runOnce()
		}

		ticker := time.NewTicker(schedule.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()
}

// waitJitter blocks for a random duration in [0, jitter) (or returns
// immediately if jitter <= 0). Returns false if ctx was canceled while
// waiting, so the caller can short-circuit before invoking the task.
func waitJitter(ctx context.Context, jitter time.Duration) bool {
	if jitter <= 0 {
		return ctx.Err() == nil
	}
	d := time.Duration(rand.Int64N(int64(jitter)))
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
