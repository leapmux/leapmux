// Package backoffutil is the one place capped exponential backoff is
// configured.
//
// Three loops in this tree retry the same shape -- double, cap, reset on
// success -- and each had hand-rolled or hand-configured it: the hub client's
// reconnect, the `leapmux remote` follower's WatchEvents reconnect, and the
// worker's orphan-reconciler pass. Three copies of `min(prev*2, cap)` is three
// places to get the arithmetic wrong, and it HAD been: the follower carried a
// six-line comment about an overshoot its author had to clamp by hand, because
// `cur` runs past `max` whenever `max` is not a power-of-two multiple of the
// floor.
//
// What differs between the three is only WHEN the backoff resets -- a duration
// threshold, stream activity, or a converged pass -- so that stays at each call
// site, where it is the interesting part.
package backoffutil

import (
	"time"

	"github.com/cenkalti/backoff/v6"
)

// NewCapped returns an exponential backoff that starts at initial, doubles, and
// never exceeds maxInterval.
//
// jitter is the randomization factor (0 disables it entirely, which makes the
// sequence exactly initial, 2×initial, 4×initial, … capped). Reserve 0 for
// loops whose timing is asserted; anything retrying against a shared peer wants
// jitter, so a fleet that failed together does not retry together.
//
// The returned value is NOT safe for concurrent use -- each retry loop owns its
// own, which every current caller does (a single goroutine driving one loop).
func NewCapped(initial, maxInterval time.Duration, jitter float64) *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = initial
	b.MaxInterval = maxInterval
	b.Multiplier = 2.0
	b.RandomizationFactor = jitter
	b.Reset()
	return b
}
