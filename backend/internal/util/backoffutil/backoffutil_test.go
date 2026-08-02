package backoffutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustNewRetry wraps NewRetry for tests with known-valid policy; a bad test
// constant should fail the test, not compile.
func mustNewRetry(t *testing.T, initial, maxInterval time.Duration, jitter float64, maxAttempts int) *Retry {
	t.Helper()
	r, err := NewRetry(initial, maxInterval, jitter, maxAttempts)
	require.NoError(t, err)
	return r
}

// assertBaseSequence drives next with jitter==0 (deterministic) and asserts the
// un-jittered doubling sequence initial, 2*initial, ..., capped at maxInterval.
// It takes a next() closure so it can assert the same shape for both *Retry
// (via Peek+Commit) and the raw *Backoff.
func assertBaseSequence(t *testing.T, next func() time.Duration, initial, maxInterval time.Duration, n int) {
	t.Helper()
	want := initial
	for i := 0; i < n; i++ {
		assert.Equal(t, want, next(), "attempt %d base delay", i)
		nx := time.Duration(float64(want) * 2)
		if nx > maxInterval {
			nx = maxInterval
		}
		want = nx
	}
}

// commitPeek advances a Retry one real attempt (Peek then Commit), returning the
// peeked delay. Used by the sequence tests to drive the consume-on-fire API the
// way the production caller does.
func commitPeek(r *Retry) time.Duration {
	d, ok := r.Peek()
	if !ok {
		panic("commitPeek: budget spent")
	}
	r.Commit()
	return d
}

func TestRetry_DoublesAndCapsJitterless(t *testing.T) {
	// 100ms -> 200 -> 400 -> 500 (cap), 500 ... unlimited budget, 6 samples.
	r := mustNewRetry(t, 100*time.Millisecond, 500*time.Millisecond, 0, 0)
	assertBaseSequence(t, func() time.Duration { return commitPeek(r) },
		100*time.Millisecond, 500*time.Millisecond, 6)
}

func TestRetry_PeekReturnsFalseWhenBudgetSpent(t *testing.T) {
	r := mustNewRetry(t, 1*time.Millisecond, 10*time.Millisecond, 0, 3)
	for i := 0; i < 3; i++ {
		_, ok := r.Peek()
		assert.True(t, ok, "attempt %d within budget", i)
		r.Commit()
	}
	assert.True(t, r.Done(), "Done after budget spent")
	_, ok := r.Peek()
	assert.False(t, ok, "Peek past budget returns ok=false")
	// The exhausted Retry must not keep advancing: this is the property that
	// stops a flapping worker's LOOKUP_FAILED ladder from compounding.
	assert.Equal(t, 3, r.Attempts(), "exhausted Peek does not advance the counter")
}

func TestRetry_DoneFalseBeforeExhaustion(t *testing.T) {
	r := mustNewRetry(t, 1*time.Millisecond, 10*time.Millisecond, 0, 3)
	assert.False(t, r.Done(), "not done at zero attempts")
	r.Commit()
	assert.False(t, r.Done(), "not done one before the cap")
	r.Commit()
	assert.False(t, r.Done(), "not done at the last allowed attempt's start")
	r.Commit()
	assert.True(t, r.Done(), "done only once the budget is fully consumed")
}

func TestRetry_ResetRestartsUnboundedInterval(t *testing.T) {
	// Reset on an unbounded retry has no budget to restore, but must still
	// restart the interval at InitialInterval.
	r := mustNewRetry(t, 100*time.Millisecond, 1*time.Second, 0, 0)
	commitPeek(r) // 100ms
	commitPeek(r) // 200ms
	r.Reset()
	assert.False(t, r.Done(), "unbounded is never Done after Reset")
	d, ok := r.Peek()
	require.True(t, ok)
	assert.Equal(t, 100*time.Millisecond, d, "interval restarts at initial after Reset")
}

func TestRetry_DoneFalseForUnbounded(t *testing.T) {
	r := mustNewRetry(t, 1*time.Millisecond, 10*time.Millisecond, 0, 0)
	for i := 0; i < 50; i++ {
		commitPeek(r)
	}
	assert.False(t, r.Done(), "unbounded Retry is never Done")
}

func TestRetry_ResetRestoresBudgetAndInterval(t *testing.T) {
	r := mustNewRetry(t, 100*time.Millisecond, 1*time.Second, 0, 2)
	commitPeek(r) // 100ms, attempt 1
	commitPeek(r) // 200ms, attempt 2 -- budget spent
	_, ok := r.Peek()
	require.False(t, ok)

	r.Reset()
	assert.False(t, r.Done(), "budget restored")
	assert.Equal(t, 0, r.Attempts())
	d, ok := r.Peek()
	require.True(t, ok)
	assert.Equal(t, 100*time.Millisecond, d, "interval restarts at initial")
}

func TestRetry_AttemptsCounter(t *testing.T) {
	r := mustNewRetry(t, 1*time.Millisecond, 10*time.Millisecond, 0, 5)
	for i := 0; i < 3; i++ {
		r.Peek()
		r.Commit()
	}
	assert.Equal(t, 3, r.Attempts())
}

// TestRetry_PeekDoesNotConsumeSlot pins the consume-on-fire contract: a Peek
// that the caller discards (via Rollback or a fresh Peek) consumes NO budget
// slot and does NOT advance the interval. This is the property that makes a
// Cancel/generation race that retires an armed retry mechanically unable to
// erode the budget or ratchet the interval — the retry peeked but never
// committed.
func TestRetry_PeekDoesNotConsumeSlot(t *testing.T) {
	r := mustNewRetry(t, 100*time.Millisecond, 1*time.Second, 0, 3)
	d1, ok := r.Peek()
	require.True(t, ok)
	require.Equal(t, 100*time.Millisecond, d1, "first peek at the initial interval")
	// Discard the peek without committing — Rollback.
	r.Rollback()
	assert.Equal(t, 0, r.Attempts(), "a peeked-but-rolled-back attempt consumes no slot")
	assert.False(t, r.Done(), "the budget is untouched after a rolled-back peek")

	// The next peek must return the SAME interval (no advance): the rolled-back
	// peek left the base interval where it was.
	d2, ok := r.Peek()
	require.True(t, ok)
	assert.Equal(t, d1, d2, "a rolled-back peek does not advance the interval")

	// Only a Commit advances the interval and consumes the slot.
	r.Commit()
	assert.Equal(t, 1, r.Attempts(), "Commit consumes the slot")
	d3, ok := r.Peek()
	require.True(t, ok)
	assert.Equal(t, 200*time.Millisecond, d3, "Commit advanced the interval to 2x")
}

// TestRetry_RollbackNoOpWithoutPeek pins that Rollback is a harmless no-op when
// there is no pending peek — so a caller that bails unconditionally (without
// knowing whether it peeked) cannot corrupt the state.
func TestRetry_RollbackNoOpWithoutPeek(t *testing.T) {
	r := mustNewRetry(t, 1*time.Millisecond, 10*time.Millisecond, 0, 3)
	r.Rollback() // no pending peek
	r.Rollback() // still no pending peek
	assert.Equal(t, 0, r.Attempts(), "Rollback without a peek is a no-op")
	d, ok := r.Peek()
	require.True(t, ok)
	assert.Equal(t, 1*time.Millisecond, d, "interval unchanged after no-op Rollback")
}

// TestRetry_RepeatedPeekOverwrites pins that two Peeks in a row (without a
// Commit between them) overwrite the staged sample and advance nothing — the
// previous peek is simply discarded.
func TestRetry_RepeatedPeekOverwrites(t *testing.T) {
	r := mustNewRetry(t, 100*time.Millisecond, 1*time.Second, 0, 3)
	_, ok := r.Peek()
	require.True(t, ok)
	_, ok = r.Peek()
	require.True(t, ok)
	// Two peeks, zero commits: no slot consumed.
	assert.Equal(t, 0, r.Attempts(), "two peeks consume no slot")
	r.Commit()
	assert.Equal(t, 1, r.Attempts(), "a single Commit consumes one slot regardless of prior peeks")
}

// TestRetry_JitterStaysWithinWindow fuzzes many samples: with jitterFactor j,
// every returned delay must lie in [base*(1-j), base*(1+j)]. Because the first
// attempt's base is `initial`, all first-attempt samples are directly comparable.
func TestRetry_JitterStaysWithinWindow(t *testing.T) {
	const j = 0.2
	initial := 100 * time.Millisecond
	for s := 0; s < 1000; s++ {
		// Fresh Retry so every sample is the first attempt (base == initial).
		r := mustNewRetry(t, initial, 15*time.Second, j, 1)
		d, ok := r.Peek()
		require.True(t, ok)
		lo := time.Duration(float64(initial) * (1 - j))
		hi := time.Duration(float64(initial) * (1 + j))
		assert.GreaterOrEqual(t, int64(d), int64(lo), "below window: %v", d)
		// +1ns: the inclusive upper bound matches cenkalti v6's
		// getRandomValueFromInterval window callers have historically asserted.
		assert.LessOrEqual(t, int64(d), int64(hi)+1, "above window: %v", d)
	}
}

func TestBackoff_DoublesAndCapsJitterless(t *testing.T) {
	// NewBackoff coverage (the raw type Retry wraps); shares assertBaseSequence
	// with the Retry sequence test so the two cannot drift.
	b := NewBackoff(100*time.Millisecond, 500*time.Millisecond, 0)
	assertBaseSequence(t, b.Next, 100*time.Millisecond, 500*time.Millisecond, 6)
}

// TestBackoff_PeekCommitMatchesNext pins that a Peek+Commit cycle produces the
// same interval a Next would — the peek/commit split is a faithful
// decomposition, not a different sequence.
func TestBackoff_PeekCommitMatchesNext(t *testing.T) {
	peekCommit := NewBackoff(10*time.Millisecond, 100*time.Millisecond, 0)
	next := NewBackoff(10*time.Millisecond, 100*time.Millisecond, 0)
	for i := 0; i < 5; i++ {
		dPC := peekCommit.Peek()
		peekCommit.Commit()
		dN := next.Next()
		assert.Equal(t, dN, dPC, "attempt %d: Peek+Commit must match Next", i)
	}
}

// TestNewRetry_RejectsInvalidPolicy pins the validation that stops a bad
// runtime policy from silently producing a zero/negative-delay burst (initial
// <= 0, jitter >= 1) or an unbounded loop from a negative maxAttempts. These
// match the frontend's createExponentialBackoff rejections.
func TestNewRetry_RejectsInvalidPolicy(t *testing.T) {
	cases := []struct {
		name         string
		initial, max time.Duration
		jitter       float64
		maxAttempts  int
	}{{
		name: "zero initial", initial: 0, max: time.Second, jitter: 0, maxAttempts: 1,
	}, {
		name: "negative initial", initial: -1, max: time.Second, jitter: 0, maxAttempts: 1,
	}, {
		name: "max below initial", initial: time.Second, max: time.Millisecond, jitter: 0, maxAttempts: 1,
	}, {
		name: "negative jitter", initial: time.Millisecond, max: time.Second, jitter: -0.1, maxAttempts: 1,
	}, {
		name: "jitter at 1", initial: time.Millisecond, max: time.Second, jitter: 1, maxAttempts: 1,
	}, {
		name: "negative maxAttempts", initial: time.Millisecond, max: time.Second, jitter: 0, maxAttempts: -1,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewRetry(c.initial, c.max, c.jitter, c.maxAttempts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "backoffutil:")
		})
	}
}

// TestNewRetry_AcceptsBoundaryPolicy confirms the boundary values validation
// must NOT reject: initial == max (no doubling room), maxAttempts == 0
// (unbounded), jitter == 0 (disabled).
func TestNewRetry_AcceptsBoundaryPolicy(t *testing.T) {
	for _, c := range []struct {
		name         string
		initial, max time.Duration
		jitter       float64
		maxAttempts  int
	}{
		{name: "initial==max", initial: time.Second, max: time.Second, jitter: 0, maxAttempts: 1},
		{name: "unbounded", initial: time.Millisecond, max: time.Second, jitter: 0, maxAttempts: 0},
		{name: "jitterless", initial: time.Millisecond, max: time.Second, jitter: 0, maxAttempts: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewRetry(c.initial, c.max, c.jitter, c.maxAttempts)
			require.NoError(t, err)
		})
	}
}

// TestNewBackoff_PanicsOnInvalid pins that the unbounded constructor panics on
// invalid inputs (it has no error return), so a bad constant fails loudly at
// startup instead of producing a zero/negative-delay burst.
func TestNewBackoff_PanicsOnInvalid(t *testing.T) {
	cases := []struct {
		name         string
		initial, max time.Duration
		jitter       float64
		wantSubstr   string
	}{
		{name: "zero initial", initial: 0, max: time.Second, jitter: 0, wantSubstr: "initial must be > 0"},
		{name: "max below initial", initial: time.Second, max: time.Millisecond, jitter: 0, wantSubstr: "maxInterval"},
		{name: "jitter at 1", initial: time.Millisecond, max: time.Second, jitter: 1, wantSubstr: "jitter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var msg string
			func() {
				defer func() { msg, _ = recover().(string) }()
				_ = NewBackoff(c.initial, c.max, c.jitter)
			}()
			require.NotEmpty(t, msg, "NewBackoff must panic on invalid input")
			assert.Contains(t, msg, c.wantSubstr, "panic message names the invalid field")
			assert.Contains(t, msg, "backoffutil:", "panic message carries the package prefix")
		})
	}
}
