package inputqueue

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// skewClock is a quartz mock whose Now runs ahead of or behind its timers.
// A host sleep or a wall-clock step separates the monotonic clock that a
// timer measures from the wall clock that every schedule decision reads, and
// this clock reproduces that separation without real time.
type skewClock struct {
	*quartz.Mock
	mu   sync.Mutex
	skew time.Duration
}

func (c *skewClock) Now(tags ...string) time.Time {
	c.mu.Lock()
	skew := c.skew
	c.mu.Unlock()
	return c.Mock.Now(tags...).Add(skew)
}

func (c *skewClock) setSkew(skew time.Duration) {
	c.mu.Lock()
	c.skew = skew
	c.mu.Unlock()
}

func newSkewedDeadlineFixture(t *testing.T) (*skewClock, *recordingDispatcher, *Manager, *Store) {
	t.Helper()
	mock := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	mock.Set(priorityBaseTime())
	clock := &skewClock{Mock: mock}
	store := NewStore(newQueueDatabase(t), clock)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)
	return clock, dispatcher, manager, store
}

// waitForDrainToSettle returns once no drain loop runs for the agent, so a
// test can assert that nothing was dispatched.
func waitForDrainToSettle(t *testing.T, manager *Manager, agentID string) {
	t.Helper()
	c := manager.coordinator(agentID)
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.draining
	}, eventuallyWait, 10*time.Millisecond)
}

// A host sleep leaves the monotonic timer owing time after the wall clock
// passed the deadline. The next read of the queue must deliver the matured
// item instead of discarding the timer that still owes it.
func TestMaturityDeliversADeadlineThatTheWallClockPassedBeforeTheTimer(t *testing.T) {
	t.Parallel()
	clock, dispatcher, manager, _ := newSkewedDeadlineFixture(t)

	enqueueScheduled(t, manager, "scheduled", time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	clock.setSkew(2 * time.Hour)
	_, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)

	eventuallyDispatches(t, dispatcher, []string{"scheduled"})
}

// A timer that fires while the wall clock still reads before its deadline
// must arm again for the rest of the wait.
func TestMaturityRearmsWhenTheTimerFiresBeforeTheWallClock(t *testing.T) {
	t.Parallel()
	clock, dispatcher, manager, _ := newSkewedDeadlineFixture(t)

	enqueueScheduled(t, manager, "scheduled", time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	clock.setSkew(-time.Second)
	// The timer reaches the deadline while the wall clock still reads one
	// second before it.
	advanceClock(t, clock.Mock, time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "the wall clock has not reached the deadline")

	clock.setSkew(0)
	advanceClock(t, clock.Mock, time.Second)
	eventuallyDispatches(t, dispatcher, []string{"scheduled"})
}

// A callback that already fired keeps its deadline even when a read arms the
// timer for a later deadline while the callback waits for the lock.
func TestMaturityKeepsAFiredCallbackWhenALaterDeadlineArms(t *testing.T) {
	t.Parallel()
	clock, dispatcher, manager, store := newSkewedDeadlineFixture(t)

	enqueueScheduled(t, manager, "first", time.Hour)
	enqueueScheduled(t, manager, "second", 2*time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	// Step through the rechecks to the last one before the first deadline.
	advanceClock(t, clock.Mock, time.Hour-maturityRecheckInterval)
	c := manager.coordinator("agent-1")
	c.mu.Lock()
	waiter := clock.Advance(maturityRecheckInterval)
	// The mock removes a timer just before it calls the callback, so an empty
	// timer list means that the callback now waits for c.mu.
	require.Eventually(t, func() bool {
		next, ok := clock.Peek()
		return !ok || next > 0
	}, eventuallyWait, time.Millisecond)
	snapshot, err := store.Snapshot(context.Background(), "agent-1")
	if err == nil {
		manager.rearmMaturityLocked(c, snapshot)
	}
	c.mu.Unlock()
	require.NoError(t, err)
	waiter.MustWait(t.Context())

	eventuallyDispatches(t, dispatcher, []string{"first"})
}

// With no read of the queue at all, a deadline that the wall clock passed
// during a host sleep still delivers within one recheck interval.
func TestMaturityRechecksTheWallClockWhileTheTimerWaits(t *testing.T) {
	t.Parallel()
	clock, dispatcher, manager, _ := newSkewedDeadlineFixture(t)

	enqueueScheduled(t, manager, "scheduled", time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	clock.setSkew(2 * time.Hour)
	clock.Advance(maturityRecheckInterval).MustWait(t.Context())

	eventuallyDispatches(t, dispatcher, []string{"scheduled"})
}

// Recovery arms the maturity timer, but a recovered queue dispatches nothing
// until DrainRecovered confirms the Worker owner. A deadline that passes in
// between waits for that confirmation.
func TestMaturityWaitsForDrainRecoveredAfterRecovery(t *testing.T) {
	t.Parallel()
	clock, _, managerOne, store := newSkewedDeadlineFixture(t)
	enqueueScheduled(t, managerOne, "recovered", 10*time.Minute)
	waitForDrainToSettle(t, managerOne, "agent-1")
	managerOne.StopAndWait()

	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)
	require.NoError(t, manager.RecoverState(t.Context()))
	advanceClock(t, clock.Mock, 10*time.Minute)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "a recovered queue waits for DrainRecovered")

	require.NoError(t, manager.DrainRecovered(t.Context()))
	eventuallyDispatches(t, dispatcher, []string{"recovered"})
}

// A closed agent takes no input, so its scheduled item never dispatches, and
// the queue neither fails the item nor pauses.
func TestMaturityNeverDispatchesToAClosedAgent(t *testing.T) {
	t.Parallel()
	clock, dispatcher, manager, store := newSkewedDeadlineFixture(t)
	enqueueScheduled(t, manager, "scheduled", time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	_, err := store.db.ExecContext(t.Context(), `UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = 'agent-1'`)
	require.NoError(t, err)

	advanceClock(t, clock.Mock, time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher))
	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.False(t, snapshot.Paused)
}

// Retiring a closed agent cancels its retries, keeps its other input, and
// stops its maturity timer. A later read must not arm the timer again.
func TestRetireAgentCancelsRetriesAndStopsTheTimer(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, store := newDeadlineFixture(t)
	enqueueScheduled(t, manager, "scheduled", time.Hour)
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		ResetAt: clock.Now().Add(2 * time.Hour),
	}))
	waitForDrainToSettle(t, manager, "agent-1")
	_, err := store.db.ExecContext(t.Context(), `UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = 'agent-1'`)
	require.NoError(t, err)

	require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1, "the retry is cancelled and the scheduled message stays")
	assert.Equal(t, "scheduled", snapshot.Items[0].ID)
	_, ok := clock.Peek()
	assert.False(t, ok, "neither the retirement nor the read leaves a maturity timer")

	// No timer survived, so the mock may pass every deadline in one step.
	clock.Advance(3 * time.Hour).MustWait(t.Context())
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher))
}

// An agent that never queued input retires without a failure, and a stopped
// manager refuses the retirement.
func TestRetireAgentWithoutQueueStateAndAfterStop(t *testing.T) {
	t.Parallel()
	_, _, _, manager, _ := newDeadlineFixture(t)
	require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))
	manager.StopAndWait()
	assert.ErrorIs(t, manager.RetireAgent(t.Context(), "agent-1"), ErrManagerStopped)
}

// advanceClock moves the mock clock forward by d through every timer on the
// way, waiting for each callback, so a test never trips the mock's refusal to
// advance past a pending timer.
func advanceClock(t *testing.T, clock *quartz.Mock, d time.Duration) {
	t.Helper()
	for d > 0 {
		next, ok := clock.Peek()
		if !ok || next > d {
			clock.Advance(d).MustWait(t.Context())
			return
		}
		step, waiter := clock.AdvanceNext()
		waiter.MustWait(t.Context())
		d -= step
	}
}

// Deadline maturity through the ordinary paths: one timer per agent on the
// queue's clock drives every scheduled item. The timer records and publishes
// the reorder; the ordinary drain, and nothing else, delivers. The schedules
// are pinned to the mock clock, so each test advances the exact remaining
// offset -- never a sleep, and never a wait that measures the delay it is
// asserting.

func newDeadlineFixture(t *testing.T) (*quartz.Mock, *recordingDispatcher, *recordingObserver, *Manager, *Store) {
	t.Helper()
	clock := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	clock.Set(priorityBaseTime())
	store := NewStore(newQueueDatabase(t), clock)
	dispatcher := &recordingDispatcher{}
	observer := &recordingObserver{}
	manager := NewManager(store, dispatcher, observer)
	t.Cleanup(manager.StopAndWait)
	return clock, dispatcher, observer, manager, store
}

func dispatchedIDs(d *recordingDispatcher) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dispatched...)
}

func lastObservedRevision(o *recordingObserver) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.snapshots) == 0 {
		return 0
	}
	return o.snapshots[len(o.snapshots)-1].Revision
}

func enqueueScheduled(t *testing.T, manager *Manager, id string, offset time.Duration) {
	t.Helper()
	_, err := manager.Enqueue(t.Context(), NewItem{
		ID: id, AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id,
		ScheduledAt: scheduled(t, offset),
	})
	require.NoError(t, err)
}

func eventuallyDispatches(t *testing.T, dispatcher *recordingDispatcher, want []string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return slices.Equal(dispatchedIDs(dispatcher), want)
	}, eventuallyWait, 10*time.Millisecond)
}

func TestMaturityDispatchesExactlyAtTheDueInstant(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, _ := newDeadlineFixture(t)

	enqueueScheduled(t, manager, "scheduled", time.Hour)
	advanceClock(t, clock, 59*time.Minute)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "one minute short of the deadline, nothing dispatches")

	advanceClock(t, clock, time.Minute)
	eventuallyDispatches(t, dispatcher, []string{"scheduled"})

	// The deadline delivered once: advancing far past it adds no dispatch.
	advanceClock(t, clock, time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Equal(t, []string{"scheduled"}, dispatchedIDs(dispatcher), "maturity delivers one dispatch")
}

func TestMaturityPublishesThroughAPause(t *testing.T) {
	t.Parallel()
	clock, dispatcher, observer, manager, _ := newDeadlineFixture(t)

	_, err := manager.SetPaused(t.Context(), "agent-1", true)
	require.NoError(t, err)
	enqueueScheduled(t, manager, "held", 30*time.Minute)

	before := lastObservedRevision(observer)
	advanceClock(t, clock, 30*time.Minute)
	require.Eventually(t, func() bool {
		return lastObservedRevision(observer) > before
	}, eventuallyWait, 10*time.Millisecond, "maturity publishes a new revision even while paused")
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "a paused queue sends nothing at maturity")

	observer.mu.Lock()
	last := observer.snapshots[len(observer.snapshots)-1]
	observer.mu.Unlock()
	require.Len(t, last.Items, 1)
	assert.Equal(t, "", last.Items[0].ReorderGroup, "the matured item joined the immediate group")

	_, err = manager.SetPaused(t.Context(), "agent-1", false)
	require.NoError(t, err)
	eventuallyDispatches(t, dispatcher, []string{"held"})
}

// A reschedule stops the timer of the old deadline. The trap proves the stop
// itself: the old deadline delivers nothing either way, because the dispatch
// gate refuses the moved item until its new deadline.
func TestMaturityRescheduleStopsTheFirstTimer(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, _ := newDeadlineFixture(t)

	enqueueScheduled(t, manager, "moved", 2*time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	snapshot, text, _, err := manager.BeginEdit(t.Context(), "agent-1", "moved", "client", false)
	require.NoError(t, err)
	require.Equal(t, "moved", text)

	stops := clock.Trap().TimerStop(queueMaturityTimerTag)
	defer stops.Close()
	updated := make(chan error, 1)
	go func() {
		_, err := manager.Update(t.Context(), "agent-1", "moved", "client", snapshot.Items[0].Version, "moved", nil, ReplaceSchedule(scheduled(t, 3*time.Hour)))
		updated <- err
	}()
	stops.MustWait(t.Context()).MustRelease(t.Context())
	require.NoError(t, <-updated)

	advanceClock(t, clock, 2*time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "a rescheduled item does not dispatch at its old deadline")
	advanceClock(t, clock, time.Hour)
	eventuallyDispatches(t, dispatcher, []string{"moved"})
}

func TestMaturityStopPreventsLaterDelivery(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, _ := newDeadlineFixture(t)

	enqueueScheduled(t, manager, "late", time.Hour)
	waitForDrainToSettle(t, manager, "agent-1")
	manager.StopAndWait()
	// Stop removed the only timer, so the mock may advance past the deadline
	// in one step; a timer that survived Stop would fail this Advance.
	clock.Advance(2 * time.Hour).MustWait(t.Context())
	assert.Empty(t, dispatchedIDs(dispatcher), "no timer delivers after Stop")
}

func TestMaturityImmediateWorkRunsWhileAFutureItemWaits(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, _ := newDeadlineFixture(t)

	enqueueScheduled(t, manager, "future", time.Hour)
	_, err := manager.Enqueue(t.Context(), NewItem{
		ID: "now", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "now",
	})
	require.NoError(t, err)
	eventuallyDispatches(t, dispatcher, []string{"now"})
	// The dispatcher reports a turn in flight; the queue correctly holds the
	// next item until the turn ends, so report its end before the deadline.
	_, err = manager.TurnEnded(t.Context(), "agent-1")
	require.NoError(t, err)

	advanceClock(t, clock, time.Hour)
	eventuallyDispatches(t, dispatcher, []string{"now", "future"})
}

func TestMaturityRecoveryRestoresTheTimer(t *testing.T) {
	t.Parallel()
	database := newQueueDatabase(t)

	first := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	first.Set(priorityBaseTime())
	managerOne := NewManager(NewStore(database, first), &recordingDispatcher{}, &recordingObserver{})
	enqueueScheduled(t, managerOne, "across-restart", 45*time.Minute)
	waitForDrainToSettle(t, managerOne, "agent-1")
	managerOne.StopAndWait()

	// A second life over the same durable queue arms the timer at recovery.
	second := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	second.Set(priorityBaseTime())
	dispatcher := &recordingDispatcher{}
	managerTwo := NewManager(NewStore(database, second), dispatcher, &recordingObserver{})
	t.Cleanup(managerTwo.StopAndWait)
	require.NoError(t, managerTwo.RecoverState(t.Context()))
	require.NoError(t, managerTwo.DrainRecovered(t.Context()))
	waitForDrainToSettle(t, managerTwo, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher), "a future head waits for its deadline after recovery")

	advanceClock(t, second, 45*time.Minute)
	eventuallyDispatches(t, dispatcher, []string{"across-restart"})
}
