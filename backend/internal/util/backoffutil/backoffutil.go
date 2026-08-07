// Package backoffutil is the one place capped exponential backoff is
// configured. It owns the doubling/jitter/cap math so callers depend on
// backoffutil, not on cenkalti/backoff/v7 — the third-party type never crosses
// the package boundary.
//
// Three loops in this tree retry the same shape — double, cap, reset on
// success — and each had hand-rolled or hand-configured it: the hub client's
// reconnect, the `leapmux remote` follower's WatchEvents reconnect, and the
// worker's orphan-reconciler pass. Three copies of `min(prev*2, cap)` is three
// places to get the arithmetic wrong, and it HAD been: the follower carried a
// six-line comment about an overshoot its author had to clamp by hand, because
// `cur` runs past `max` whenever `max` is not a power-of-two multiple of the
// floor.
//
// What differs between the three is only WHEN the backoff resets — a duration
// threshold, stream activity, or a converged pass — so that stays at each call
// site, where it is the interesting part.
//
// Two constructors cover the two shapes: NewBackoff for an unbounded loop that
// resets on success/activity (the three reconnect loops), and NewRetry for a
// count-bounded loop that gives up after a fixed attempt budget (cenkalti
// dropped MaxElapsedTime from ExponentialBackOff in v5, so the budget lives
// here). NewRetry wraps the same capped,
// jittered exponential NewBackoff produces, and consumes the budget only when
// an attempt actually fires (Peek + Commit), so a retry that arms but bails
// before firing never erodes the budget or ratchets the interval.
package backoffutil

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// Backoff is a capped, jittered exponential backoff that owns its interval
// math. It starts at initial, doubles on each advance, and never exceeds
// maxInterval. jitter fuzzes each sampled delay by ±jitter (0 disables it).
//
// The doubling sequence advances on the UN-jittered base interval, and jitter
// only fuzzes the sampled value — matching the frontend's
// createExponentialBackoff and cenkalti/v6's shape. Unlike cenkalti's
// ExponentialBackOff (which couples get-delay with advance inside NextBackOff),
// Backoff separates Peek (sample without advancing) from Commit (advance), so a
// caller that samples a delay but then bails before firing can discard the
// sample without ratcheting the interval forward.
//
// NOT safe for concurrent use — each retry loop owns its own, which every
// current caller does (a single goroutine driving one loop, or a caller that
// serializes all access under its own mutex, as the LOOKUP_FAILED retry does).
type Backoff struct {
	initial     time.Duration
	maxInterval time.Duration
	jitter      float64

	base    time.Duration // un-jittered interval the next sample is drawn from
	pending time.Duration // the most recent Peek result, awaiting Commit; 0 = none
}

// NewBackoff returns a capped, jittered exponential backoff that starts at
// initial, doubles, and never exceeds maxInterval.
//
// jitter is the randomization factor (0 disables it entirely, which makes the
// sequence exactly initial, 2×initial, 4×initial, … capped). Reserve 0 for
// loops whose timing is asserted; anything retrying against a shared peer wants
// jitter, so a fleet that failed together does not retry together.
//
// Panics on invalid inputs (initial <= 0, maxInterval < initial, jitter outside
// [0,1)) — these are programming errors that would produce zero/negative
// delays (which panic in time.NewTimer) or an unbounded loop, and every caller
// passes compile-time constants.
func NewBackoff(initial, maxInterval time.Duration, jitter float64) *Backoff {
	switch {
	case initial <= 0:
		panic(fmt.Sprintf("backoffutil: initial must be > 0 (got %v)", initial))
	case maxInterval < initial:
		panic(fmt.Sprintf("backoffutil: maxInterval (%v) must be >= initial (%v)", maxInterval, initial))
	case jitter < 0 || jitter >= 1:
		panic(fmt.Sprintf("backoffutil: jitter must be in [0, 1) (got %v)", jitter))
	}
	return &Backoff{
		initial:     initial,
		maxInterval: maxInterval,
		jitter:      jitter,
		base:        initial,
	}
}

// Next samples the next delay and advances the base interval, in one step. Use
// this for unbounded reconnect loops that always act on the sampled delay. For
// a loop that may sample-then-bail, use Peek + Commit so a discarded sample
// does not ratchet the interval forward.
func (b *Backoff) Next() time.Duration {
	d := b.sample(b.base)
	b.advance()
	return d
}

// Peek samples the next delay WITHOUT advancing the base interval, staging it
// for a later Commit. Calling Peek again before Commit overwrites the staged
// sample with a fresh draw (the previous peek is discarded, no advance). Use
// Commit once the attempt actually fires so the base interval advances only on
// a real attempt; a peeked-but-uncommitted sample is dropped on Reset or the
// next Peek with no effect on the sequence.
func (b *Backoff) Peek() time.Duration {
	b.pending = b.sample(b.base)
	return b.pending
}

// Commit advances the base interval, consuming the most recent Peek. A
// Commit with no pending Peek advances from the current base (treating the
// peek as implicit), matching Next's semantics for callers that do not peek.
func (b *Backoff) Commit() {
	b.pending = 0
	b.advance()
}

// Rollback discards the most recent Peek without advancing the base interval.
// No-op when there is no pending Peek. Use after a Peek when the attempt will
// not fire, so the interval the next Peek draws from is unchanged.
func (b *Backoff) Rollback() {
	b.pending = 0
}

// Reset restarts the base interval at initial and drops any pending Peek, so
// the next Peek/Next begins a fresh backoff sequence.
func (b *Backoff) Reset() {
	b.base = b.initial
	b.pending = 0
}

// sample draws a jittered delay around base. With jitter == 0 it returns base
// unchanged. The upper bound is inclusive (+1ns) to match the
// cenkalti/v6 RandomizationFactor window callers have historically asserted
// against.
func (b *Backoff) sample(base time.Duration) time.Duration {
	if b.jitter == 0 {
		return base
	}
	delta := b.jitter * float64(base)
	lo := float64(base) - delta
	hi := float64(base) + delta
	// rand/v2's Float64 is uniform [0,1); scale to [lo, hi] inclusive of hi.
	return time.Duration(lo + rand.Float64()*(hi-lo+1))
}

// advance doubles the base interval, capping at maxInterval.
func (b *Backoff) advance() {
	next := time.Duration(float64(b.base) * 2)
	if next > b.maxInterval || next < b.base {
		// Overflow guard: a doubling that wraps negative or exceeds the cap
		// pins to the cap.
		next = b.maxInterval
	}
	b.base = next
}

// Retry is a count-bounded, reset-aware exponential backoff for a single retry
// loop. It wraps the same capped, jittered exponential NewBackoff produces and
// adds a per-loop attempt budget, which cenkalti's ExponentialBackOff has not
// carried since v5 dropped MaxElapsedTime from it. The behavior mirrors the
// frontend's
// createExponentialBackoff: the doubling sequence advances on the un-jittered
// base interval, and jitter only fuzzes the value Peek returns.
//
// The budget is consumed only when an attempt actually fires: Peek samples a
// delay without consuming a slot, and Commit consumes the slot (and advances
// the interval) only when the caller confirms the attempt fired. A retry that
// peeks but bails before firing calls Rollback and the slot is untouched — so
// a Cancel/generation race that retires an armed retry cannot erode the budget
// or ratchet the interval. This makes the refund-and-erode class of bug
// mechanically impossible, instead of relying on every caller to thread a
// Release through every bail path.
//
// maxAttempts == 0 retries forever (use this for "keep trying" loops); any
// positive value turns "retry until it works" into "retry until it clearly
// won't" — the shape every bounded caller wants.
//
// NOT safe for concurrent use — each retry loop owns its own, or serializes all
// access under its own mutex (which is exactly what the LOOKUP_FAILED caller
// does under s.mu).
type Retry struct {
	b   *Backoff
	max int // 0 = unlimited

	attempt int
}

// NewRetry returns a bounded exponential backoff: starting at initial, doubling,
// capped at maxInterval, with symmetric jitter of `jitter` (0 disables it; see
// NewBackoff's jitter doc for why shared-peer loops want jitter). After
// maxAttempts successful Next calls the budget is spent and Next returns ok=false.
// Pass maxAttempts == 0 for an unbounded loop.
//
// Returns an error for invalid inputs: initial <= 0, maxInterval < initial,
// jitter outside [0,1), or negative maxAttempts. These mirror the frontend's
// createExponentialBackoff rejections with one deliberate exception: the
// backend accepts maxAttempts == 0 as a first-class "unbounded" mode (see
// Retry.max above), while the frontend rejects maxAttempts < 1. An out-of-range
// value otherwise silently turns a bounded caller into an unbounded one
// (negative maxAttempts) or produces zero/negative delays (initial <= 0,
// jitter >= 1), the latter of which panic in time.NewTimer.
func NewRetry(initial, maxInterval time.Duration, jitter float64, maxAttempts int) (*Retry, error) {
	switch {
	case initial <= 0:
		return nil, fmt.Errorf("backoffutil: initial must be > 0 (got %v)", initial)
	case maxInterval < initial:
		return nil, fmt.Errorf("backoffutil: maxInterval (%v) must be >= initial (%v)", maxInterval, initial)
	case jitter < 0 || jitter >= 1:
		return nil, fmt.Errorf("backoffutil: jitter must be in [0, 1) (got %v)", jitter)
	case maxAttempts < 0:
		return nil, fmt.Errorf("backoffutil: maxAttempts must be >= 0 (got %d)", maxAttempts)
	}
	return &Retry{b: NewBackoff(initial, maxInterval, jitter), max: maxAttempts}, nil
}

// Peek samples the delay for the next attempt WITHOUT consuming a slot or
// advancing the interval, and reports whether the budget has one. ok is false
// when the budget is spent (and always true when unbounded). A caller that
// proceeds to fire the attempt must call Commit to consume the slot; a caller
// that bails calls Rollback (or simply peeks again / resets) and the slot is
// untouched. Two peeks in a row overwrite the staged sample with no advance.
func (r *Retry) Peek() (delay time.Duration, ok bool) {
	if r.max > 0 && r.attempt >= r.max {
		return 0, false
	}
	return r.b.Peek(), true
}

// Commit consumes one attempt slot and advances the interval, finalizing the
// most recent Peek. Call this once the attempt the Peek armed has actually
// fired (or is about to fire irreversibly). No-op when unbounded AND the
// budget is irrelevant is NOT the rule: Commit always advances the interval
// (so an unbounded caller that peeks and commits still sees the doubling
// sequence progress), but only counts toward the budget when max > 0.
func (r *Retry) Commit() {
	r.b.Commit()
	if r.max > 0 {
		r.attempt++
	}
}

// Rollback discards the most recent Peek without consuming a slot or advancing
// the interval. Use after a Peek when the attempt will not fire, so neither
// the budget nor the interval is touched. No-op when there is no pending Peek
// or when unbounded (where the interval advance is the only effect, and a
// bail should not ratchet it — Rollback drops the pending Peek so a subsequent
// Commit-from-Next path still advances; for the peek/commit caller this keeps
// the interval a function of fired attempts only).
func (r *Retry) Rollback() {
	r.b.Rollback()
}

// Done reports whether the attempt budget is spent. Always false when the Retry
// is unbounded. It is a non-advancing peek of the identical predicate Peek's ok
// return tests (`r.max > 0 && r.attempt >= r.max`); the two are kept in
// lockstep so a caller that peeks with Done and then calls Peek sees the
// verdict the peek promised.
func (r *Retry) Done() bool {
	return r.max > 0 && r.attempt >= r.max
}

// Reset restores the full attempt budget and restarts the interval at
// initial, so the next Peek begins a fresh backoff sequence.
func (r *Retry) Reset() {
	r.attempt = 0
	r.b.Reset()
}

// Attempts returns the number of attempts consumed (via Commit) since
// construction or the last Reset. Intended for logging and assertions.
func (r *Retry) Attempts() int { return r.attempt }
