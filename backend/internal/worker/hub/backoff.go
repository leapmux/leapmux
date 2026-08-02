package hub

import (
	"time"

	"github.com/leapmux/leapmux/internal/util/backoffutil"
)

const (
	// resetThreshold is the duration after which a successful connection
	// resets the backoff interval.
	resetThreshold = 30 * time.Second
)

// backoff is the subset of *backoffutil.Backoff the hub reconnect loops use.
// Declared locally so the hub depends on backoffutil, not on cenkalti/backoff/v6.
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
