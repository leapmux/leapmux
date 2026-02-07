package hub

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

// newFastBackoff creates a fast exponential backoff for testing.
func newFastBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Millisecond
	b.MaxInterval = 10 * time.Millisecond
	b.Multiplier = 2.0
	b.RandomizationFactor = 0
	b.Reset()
	return b
}
