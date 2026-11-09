package drain

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

const testDeadlineBudget = 2 * time.Minute

func TestDeadlineIsNotInEffectBeforeStart(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, testDeadlineBudget)

	assert.Equal(t, time.Hour, deadline.Cap(time.Hour),
		"an unstarted deadline must leave a limit longer than its budget unchanged")
	assert.Equal(t, time.Duration(0), deadline.Cap(-time.Second), "Cap must never return a negative duration")
	assertOpen(t, deadline.Done(), "Done must stay open until Start")

	// Time that passes before Start does not count against the budget.
	clock.Advance(time.Hour).MustWait(testutil.DeadlineContext(t))
	assertOpen(t, deadline.Done(), "Done must stay open until Start, however much time passed")
	_, armed := clock.Peek()
	assert.False(t, armed, "an unstarted deadline must arm no timer")

	deadline.Start()
	assert.Equal(t, testDeadlineBudget, deadline.Cap(time.Hour),
		"the budget must count from Start, not from NewDeadline")
}

func TestDeadlineStartIsIdempotent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	arms := clock.Trap().AfterFunc(DeadlineTimerTag)
	t.Cleanup(arms.Close)
	deadline := NewDeadline(clock, testDeadlineBudget)

	started := make(chan struct{})
	go func() {
		defer close(started)
		deadline.Start()
	}()
	assert.Equal(t, testDeadlineBudget, testutil.WaitForTimer(t, ctx, arms),
		"the first Start must arm one timer for the whole budget")
	<-started
	// Closed before the second Start, so a second arm would not park it. That
	// arm would leave a second timer behind, which the last assertion finds.
	arms.Close()

	clock.Advance(30 * time.Second).MustWait(ctx)
	deadline.Start()
	assert.Equal(t, testDeadlineBudget-30*time.Second, deadline.Cap(time.Hour),
		"a later Start must not move the moment the first Start fixed")

	clock.Advance(testDeadlineBudget - 30*time.Second).MustWait(ctx)
	assertClosed(t, deadline.Done(), "Done must close at the moment the first Start fixed")
	_, armed := clock.Peek()
	assert.False(t, armed, "a later Start must arm no second timer")
}

func TestDeadlineCapReturnsTheSmallerOfTheLimitAndTheTimeLeft(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, testDeadlineBudget)
	deadline.Start()

	assert.Equal(t, 10*time.Second, deadline.Cap(10*time.Second), "a limit shorter than the time left must stand")
	assert.Equal(t, testDeadlineBudget, deadline.Cap(time.Hour), "a limit longer than the time left must shrink to it")
	assert.Equal(t, time.Duration(0), deadline.Cap(0), "a zero limit must stay zero")
	assert.Equal(t, time.Duration(0), deadline.Cap(-time.Second), "a negative limit must become zero")

	clock.Advance(testDeadlineBudget - time.Second).MustWait(ctx)
	assert.Equal(t, time.Second, deadline.Cap(10*time.Second), "Cap must follow the clock")

	clock.Advance(time.Second).MustWait(ctx)
	assert.Equal(t, time.Duration(0), deadline.Cap(10*time.Second), "Cap must return zero at the deadline")

	clock.Advance(time.Hour).MustWait(ctx)
	assert.Equal(t, time.Duration(0), deadline.Cap(10*time.Second),
		"Cap must return zero, not a negative duration, after the deadline")
}

func TestDeadlineDoneClosesAtTheDeadline(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, testDeadlineBudget)
	deadline.Start()

	clock.Advance(testDeadlineBudget - time.Nanosecond).MustWait(ctx)
	assertOpen(t, deadline.Done(), "Done must stay open before the deadline")

	clock.Advance(time.Nanosecond).MustWait(ctx)
	assertClosed(t, deadline.Done(), "Done must close at the deadline")
}

func TestDeadlineWithNoBudgetPassesAtStart(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, 0)
	deadline.Start()

	assertClosed(t, deadline.Done(), "a deadline with no budget must pass at Start")
	assert.Equal(t, time.Duration(0), deadline.Cap(time.Second))
	_, armed := clock.Peek()
	assert.False(t, armed, "a deadline with no budget must arm no timer")
}

func TestDeadlineWaitEndsAtTheDeadline(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	logs := testutil.CaptureDefaultLogger(t)
	deadline := NewDeadline(clock, testDeadlineBudget)
	deadline.Start()

	never := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- deadline.Wait(never, "abandoning the straggler") }()

	clock.Advance(testDeadlineBudget).MustWait(ctx)
	select {
	case finished := <-result:
		assert.False(t, finished, "Wait must report that the work did not finish")
	case <-ctx.Done():
		t.Fatal("Wait did not return at the deadline")
	}
	assert.Contains(t, logs.String(), "abandoning the straggler", "Wait must log what it abandoned")
}

// A wait that began before Start must still end at the deadline: a worker that
// the Hub deregisters starts its shutdown before any signal starts the
// deadline.
func TestDeadlineWaitFollowsALaterStart(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, testDeadlineBudget)

	never := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- deadline.Wait(never, "") }()

	deadline.Start()
	clock.Advance(testDeadlineBudget).MustWait(ctx)
	select {
	case finished := <-result:
		assert.False(t, finished)
	case <-ctx.Done():
		t.Fatal("a wait that began before Start did not end at the deadline")
	}
}

func TestDeadlineWaitReportsWorkThatFinished(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	logs := testutil.CaptureDefaultLogger(t)
	deadline := NewDeadline(clock, testDeadlineBudget)
	deadline.Start()

	release := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- deadline.Wait(release, "abandoning the straggler") }()
	close(release)
	select {
	case finished := <-result:
		assert.True(t, finished, "Wait must report work that finished before the deadline")
	case <-ctx.Done():
		t.Fatal("Wait did not return when the work finished")
	}

	// After the deadline, finished work still wins: select would otherwise pick
	// between two ready cases at random.
	clock.Advance(testDeadlineBudget).MustWait(ctx)
	for range 100 {
		assert.True(t, deadline.Wait(release, "abandoning the straggler"),
			"finished work must win over a passed deadline")
	}
	assert.NotContains(t, logs.String(), "abandoning the straggler", "finished work must log nothing")
}

func TestNilDeadlineNeverStarts(t *testing.T) {
	var deadline *Deadline
	deadline.Start()

	assert.Equal(t, time.Minute, deadline.Cap(time.Minute), "a nil deadline must leave the limit unchanged")
	assert.Equal(t, time.Duration(0), deadline.Cap(-time.Minute), "Cap must never return a negative duration")
	assert.Nil(t, deadline.Done(), "a nil deadline must have no Done channel")

	release := make(chan struct{})
	close(release)
	assert.True(t, deadline.Wait(release, ""), "a nil deadline must wait for the work alone")
}

func TestDeadlineIsSafeForConcurrentUse(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	deadline := NewDeadline(clock, testDeadlineBudget)

	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			deadline.Start()
			assert.LessOrEqual(t, deadline.Cap(time.Hour), testDeadlineBudget)
		})
	}
	callers.Wait()

	_, armed := clock.Peek()
	require.True(t, armed, "concurrent Starts must arm the timer")
	clock.Advance(testDeadlineBudget).MustWait(ctx)
	assertClosed(t, deadline.Done(), "concurrent Starts must arm exactly one timer that closes Done once")
}
