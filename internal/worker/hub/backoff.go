package hub

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

const (
	// resetThreshold is the duration after which a successful connection
	// resets the backoff interval.
	resetThreshold = 30 * time.Second
)

// newDefaultBackoff creates an exponential backoff: 1s → 60s, multiplier 2x, ±20% jitter.
func newDefaultBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Second
	b.MaxInterval = 60 * time.Second
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.2
	b.Reset()
	return b
}
