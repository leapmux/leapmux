package hub

import (
	"context"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
)

const (
	// resetThreshold is the duration after which a successful connection
	// resets the backoff interval.
	resetThreshold = 30 * time.Second
)

// backoff is the subset of *backoffutil.Backoff the hub reconnect loops use.
// Declared locally so the hub depends on backoffutil, not on cenkalti/backoff/v7.
type backoff interface {
	Next() time.Duration
	Reset()
}

// newDefaultBackoff creates an exponential backoff: 1s → 180s, multiplier 2x, ±20% jitter.
//
// Jitter matters here specifically: every worker reconnecting to one hub after
// an outage would otherwise retry in lockstep.
func newDefaultBackoff() *backoffutil.Backoff {
	return backoffutil.NewBackoff(1*time.Second, 180*time.Second, 0.2)
}

// waitOrCancel waits d on clock and reports whether the wait finished. It
// reports false when ctx ended first, which every caller here treats as "stop
// retrying". It releases the timer on both paths.
//
// This is a function rather than a `defer` in the caller. Both callers wait
// inside a retry LOOP, and a defer there runs only when the enclosing function
// returns. For the reconnect loop that moment is the end of the worker process,
// so every retry would hold its timer until then. The tag identifies which wait
// it is, so a test traps one loop's timer without catching the other's.
func waitOrCancel(ctx context.Context, clock quartz.Clock, d time.Duration, tag string) bool {
	timer := clock.NewTimer(d, tag)
	defer timer.Stop(tag)
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
