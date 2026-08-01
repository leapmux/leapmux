package hub

import (
	"time"

	"github.com/cenkalti/backoff/v6"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
)

const (
	// resetThreshold is the duration after which a successful connection
	// resets the backoff interval.
	resetThreshold = 30 * time.Second
)

// newDefaultBackoff creates an exponential backoff: 1s → 180s, multiplier 2x, ±20% jitter.
//
// Jitter matters here specifically: every worker reconnecting to one hub after
// an outage would otherwise retry in lockstep.
func newDefaultBackoff() *backoff.ExponentialBackOff {
	return backoffutil.NewCapped(1*time.Second, 180*time.Second, 0.2)
}
