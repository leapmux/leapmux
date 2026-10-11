package providerkit

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// expiryCounter counts the expire calls of one request.
type expiryCounter struct{ calls atomic.Int32 }

func (c *expiryCounter) expire() { c.calls.Add(1) }

func TestControlDeadlinesExpireARequestWhenItsDeadlinePasses(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var expired expiryCounter

	deadlines.Arm(clock, "dialog-1", 30*time.Second, expired.expire)
	clock.Advance(29 * time.Second).MustWait(ctx)
	assert.Zero(t, expired.calls.Load(), "the deadline has not passed")
	clock.Advance(time.Second).MustWait(ctx)
	assert.Equal(t, int32(1), expired.calls.Load())

	assert.False(t, deadlines.Disarm("dialog-1"), "an expired request is no longer armed")
}

func TestControlDeadlinesDisarmStopsTheDeadline(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var expired expiryCounter

	deadlines.Arm(clock, "dialog-1", 30*time.Second, expired.expire)
	assert.True(t, deadlines.Disarm("dialog-1"))
	assert.False(t, deadlines.Disarm("dialog-1"), "a second disarm finds nothing")
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Zero(t, expired.calls.Load())
}

func TestControlDeadlinesKeepEachRequestApart(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var first, second expiryCounter

	deadlines.Arm(clock, "dialog-1", 10*time.Second, first.expire)
	deadlines.Arm(clock, "dialog-2", 20*time.Second, second.expire)
	require.True(t, deadlines.Disarm("dialog-1"))
	clock.Advance(20 * time.Second).MustWait(ctx)
	assert.Zero(t, first.calls.Load())
	assert.Equal(t, int32(1), second.calls.Load())
}

// A provider that states the same request again states its deadline again, so the
// later deadline replaces the earlier one.
func TestControlDeadlinesArmAgainReplacesTheDeadline(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var earlier, later expiryCounter

	deadlines.Arm(clock, "dialog-1", 10*time.Second, earlier.expire)
	deadlines.Arm(clock, "dialog-1", 30*time.Second, later.expire)
	clock.Advance(10 * time.Second).MustWait(ctx)
	assert.Zero(t, earlier.calls.Load(), "the replaced deadline never fires")
	assert.Zero(t, later.calls.Load())
	clock.Advance(20 * time.Second).MustWait(ctx)
	assert.Zero(t, earlier.calls.Load())
	assert.Equal(t, int32(1), later.calls.Load())
}

// A deadline of zero or less states no deadline: the provider waits with no limit.
func TestControlDeadlinesArmNothingForADeadlineOfZeroOrLess(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var expired expiryCounter

	deadlines.Arm(clock, "zero", 0, expired.expire)
	deadlines.Arm(clock, "negative", -time.Second, expired.expire)
	assert.False(t, deadlines.Disarm("zero"))
	assert.False(t, deadlines.Disarm("negative"))
	clock.Advance(time.Hour).MustWait(ctx)
	assert.Zero(t, expired.calls.Load())
}

// A process that ended withdraws its requests by its own path, so no deadline may
// fire after it, and no deadline may start after it either.
func TestControlDeadlinesStopAllEndsEveryDeadlineForGood(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var expired expiryCounter

	deadlines.Arm(clock, "dialog-1", 10*time.Second, expired.expire)
	deadlines.Arm(clock, "dialog-2", 20*time.Second, expired.expire)
	deadlines.StopAll()
	deadlines.Arm(clock, "dialog-3", 10*time.Second, expired.expire)
	assert.False(t, deadlines.Disarm("dialog-3"), "nothing arms after StopAll")
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Zero(t, expired.calls.Load())
}

// The zero value is ready to use, and a disarm of a request that was never armed
// is not a fault.
func TestControlDeadlinesZeroValue(t *testing.T) {
	t.Parallel()
	var deadlines ControlDeadlines
	assert.False(t, deadlines.Disarm("never-armed"))
	deadlines.StopAll()
}

// The deadline passes on the clock's goroutine while the provider disarms on its
// own, so the two race. Exactly one of them wins: the request expires once, or
// never.
func TestControlDeadlinesExpireOrDisarmNeverBoth(t *testing.T) {
	t.Parallel()
	for range 50 {
		clock := testutil.NewQuartzMock(t)
		ctx := testutil.DeadlineContext(t)
		var deadlines ControlDeadlines
		var expired expiryCounter
		deadlines.Arm(clock, "dialog-1", time.Second, expired.expire)
		waiter := clock.Advance(time.Second)
		disarmed := deadlines.Disarm("dialog-1")
		waiter.MustWait(ctx)
		if disarmed {
			assert.Zero(t, expired.calls.Load(), "a disarmed request never expires")
		} else {
			assert.Equal(t, int32(1), expired.calls.Load(), "a request that was not disarmed expired")
		}
	}
}

// expire runs with no lock of ControlDeadlines held, so it may arm the next
// deadline of the same request, or disarm another one, and neither waits.
func TestControlDeadlinesExpireMayArmAndDisarmAgain(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	var deadlines ControlDeadlines
	var rearmed, other expiryCounter

	deadlines.Arm(clock, "other", time.Minute, other.expire)
	deadlines.Arm(clock, "dialog-1", 10*time.Second, func() {
		assert.True(t, deadlines.Disarm("other"), "the expire of one request disarms another")
		deadlines.Arm(clock, "dialog-1", 10*time.Second, rearmed.expire)
	})
	clock.Advance(10 * time.Second).MustWait(ctx)
	assert.Zero(t, rearmed.calls.Load(), "the new deadline starts when the old one expires")
	clock.Advance(10 * time.Second).MustWait(ctx)
	assert.Equal(t, int32(1), rearmed.calls.Load())
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Zero(t, other.calls.Load(), "the disarmed request never expires")
}
