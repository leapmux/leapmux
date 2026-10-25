package inputqueue

import (
	"database/sql"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The priority model: one comparator governs reads, moves, and every dispatch
// guard, and the Worker's clock decides maturity. These tests drive the store
// through a mock clock so the group boundaries (empty, past, exactly-now,
// future) are pinned to instants the test chooses, not to real time.

func priorityBaseTime() time.Time {
	return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
}

func newPriorityFixture(t *testing.T) (*quartz.Mock, *sql.DB, *Store) {
	t.Helper()
	database := newQueueDatabase(t)
	clock := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	clock.Set(priorityBaseTime())
	return clock, database, NewStore(database, clock)
}

func enqueueAt(t *testing.T, store *Store, id string, scheduledAt *time.Time, kind leapmuxv1.AgentInputKind) {
	t.Helper()
	_, err := store.Enqueue(t.Context(), NewItem{
		ID: id, AgentID: "agent-1", Kind: kind, Text: "Continue.", ScheduledAt: scheduledAt,
	})
	require.NoError(t, err)
}

func scheduled(t *testing.T, offset time.Duration) *time.Time {
	t.Helper()
	at := priorityBaseTime().Add(offset)
	return &at
}

func itemIDs(snapshot Snapshot) []string {
	ids := make([]string, len(snapshot.Items))
	for i := range snapshot.Items {
		ids[i] = snapshot.Items[i].ID
	}
	return ids
}

func TestPriorityGroupsEmptyPastNowAndFutureSchedules(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "empty", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "past", scheduled(t, -time.Minute), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "exactly-now", scheduled(t, 0), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	future := scheduled(t, 90*time.Minute)
	enqueueAt(t, store, "future", future, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	// The immediate group holds insertion order; the clock has not moved.
	assert.Equal(t, []string{"empty", "past", "exactly-now", "future"}, itemIDs(snapshot))
	assert.Equal(t, "", snapshot.Items[0].ReorderGroup)
	assert.Equal(t, "", snapshot.Items[1].ReorderGroup, "a past deadline counts as immediate")
	assert.Equal(t, "", snapshot.Items[2].ReorderGroup, "a deadline at exactly now counts as immediate")
	assert.Equal(t, "2026-10-24T10:30:00.000Z", snapshot.Items[3].ReorderGroup)

	// Crossing the deadline moves the item into the immediate group without
	// any database rewrite: only the read instant changed.
	clock.Advance(91 * time.Minute)
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"empty", "past", "exactly-now", "future"}, itemIDs(snapshot))
	assert.Equal(t, "", snapshot.Items[3].ReorderGroup)
}

func TestPriorityImmediateItemPrecedesFutureItemEnqueuedEarlier(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "immediate", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"immediate", "future"}, itemIDs(snapshot))
	assert.Equal(t, int64(1), snapshot.Items[0].Position, "the snapshot states the effective position")
	assert.Equal(t, int64(2), snapshot.Items[1].Position)
}

func TestPriorityFutureItemsSortByTimeThenInsertionOrder(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	// Ordinary kinds carry the schedules here: one pending retry per kind may
	// sit in the queue, so equal-instant future pairs come from user input.
	enqueueAt(t, store, "late", scheduled(t, 2*time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "early-first", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "early-second", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"early-first", "early-second", "late"}, itemIDs(snapshot))
	// The two equal instants share one reorder group; the later instant does not.
	assert.Equal(t, snapshot.Items[0].ReorderGroup, snapshot.Items[1].ReorderGroup)
	assert.NotEqual(t, snapshot.Items[1].ReorderGroup, snapshot.Items[2].ReorderGroup)
}

func TestPriorityNewlyImmediateItemSortsAheadOfLaterImmediateItems(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "older", scheduled(t, time.Minute), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "newer", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	clock.Advance(2 * time.Minute)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"older", "newer"}, itemIDs(snapshot),
		"the matured item keeps its earlier insertion order inside the immediate group")
}

func TestPriorityMoveRefusesIncompatibleGroups(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "immediate-one", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "immediate-two", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	// Two items share the +1h instant, which makes them one reorder group;
	// the third future item sits at its own instant.
	enqueueAt(t, store, "future-one", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "future-two", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "later", scheduled(t, 2*time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	// Same immediate group moves freely.
	snapshot, err := store.Move(ctx, "agent-1", "immediate-two", "immediate-one")
	require.NoError(t, err)
	assert.Equal(t, []string{"immediate-two", "immediate-one"}, itemIDs(snapshot)[:2])

	// Same future instant moves freely.
	snapshot, err = store.Move(ctx, "agent-1", "future-two", "future-one")
	require.NoError(t, err)
	assert.Equal(t, []string{"immediate-two", "immediate-one", "future-two", "future-one", "later"}, itemIDs(snapshot))

	// Different future instants never share a segment.
	_, err = store.Move(ctx, "agent-1", "later", "future-one")
	assert.ErrorIs(t, err, ErrScheduleOrder)

	// Immediate and future never share a segment, in either direction.
	_, err = store.Move(ctx, "agent-1", "future-one", "immediate-one")
	assert.ErrorIs(t, err, ErrScheduleOrder)
	_, err = store.Move(ctx, "agent-1", "immediate-one", "future-one")
	assert.ErrorIs(t, err, ErrScheduleOrder)

	// An empty target lands at the end of the source segment, which for an
	// immediate item is the end of the immediate group, not the queue.
	snapshot, err = store.Move(ctx, "agent-1", "immediate-two", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"immediate-one", "immediate-two", "future-two", "future-one", "later"}, itemIDs(snapshot))

	// The move rewrote only the immediate segment's slots: the future rows keep
	// their raw order_index values, visible as an unchanged future order.
	assert.Equal(t, []string{"future-two", "future-one", "later"}, itemIDs(snapshot)[2:])
}

func TestPriorityReorderSegments(t *testing.T) {
	t.Parallel()
	now := priorityBaseTime()
	later := now.Add(time.Hour)
	queued := leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED
	item := func(id string, at *time.Time, state leapmuxv1.AgentInputState) StoredItem {
		return StoredItem{ID: id, ScheduledAt: at, State: state}
	}
	for _, tc := range []struct {
		name  string
		items []StoredItem
		want  []uint32
	}{
		{"an empty queue", nil, []uint32{}},
		{"a locked row after the segment", []StoredItem{
			item("a", nil, queued), item("b", nil, queued), item("d", nil, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING),
		}, []uint32{1, 1, 0}},
		{"a group change and a lock inside one group", []StoredItem{
			item("now", nil, queued), item("t1-a", &later, queued), item("t1-b", &later, queued),
			item("echo", nil, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO), item("t1-c", &later, queued),
		}, []uint32{1, 2, 2, 0, 3}},
		{"a leading lock", []StoredItem{
			item("echo", nil, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO), item("a", nil, queued),
		}, []uint32{0, 1}},
		{"failed and uncertain rows are unlocked", []StoredItem{
			item("failed", nil, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED),
			item("uncertain", nil, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN), item("a", nil, queued),
		}, []uint32{1, 1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorderSegments(tc.items, now))
		})
	}
}

// The snapshot sends the segments that Move enforces: two items with one
// segment trade places, and items with different segments do not.
func TestPrioritySnapshotSegmentsMatchWhatMoveAccepts(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	ctx := t.Context()
	for _, id := range []string{"one", "two", "three", "four"} {
		enqueueAt(t, store, id, nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	}
	_, err := database.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ? WHERE id = 'three'`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	require.NoError(t, err)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	segment := map[string]uint32{}
	for _, item := range snapshot.Items {
		segment[item.ID] = item.ReorderSegment
	}
	assert.Equal(t, map[string]uint32{"one": 1, "two": 1, "three": 0, "four": 2}, segment)

	_, err = store.Move(ctx, "agent-1", "two", "one")
	assert.NoError(t, err, "one segment: the move is accepted")
	_, err = store.Move(ctx, "agent-1", "four", "one")
	assert.ErrorIs(t, err, ErrConflict, "two segments: the move is refused")
}

func TestPriorityMoveTreatsALockedRowAsASegmentBoundary(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "one", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "two", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "three", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	// Lock the middle row the way a dispatch in flight does.
	_, err := database.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ? WHERE id = 'two'`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	require.NoError(t, err)

	_, err = store.Move(ctx, "agent-1", "one", "three")
	assert.ErrorIs(t, err, ErrConflict, "a move across the locked dispatching row is refused")
	_, err = store.Move(ctx, "agent-1", "one", "two")
	assert.ErrorIs(t, err, ErrConflict, "a locked row is not a destination")
	// An empty target lands at the end of the source's unlocked segment, which
	// the locked row ends: "one" stays before it, not at the queue's end.
	snapshot, err := store.Move(ctx, "agent-1", "one", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "three"}, itemIDs(snapshot))
}

func TestPriorityGuardsNeverBypassAFutureDeadline(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	// With only a future head, every dispatch guard refuses.
	prepared, snapshot, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, prepared)
	require.Len(t, snapshot.Items, 1)
	assert.False(t, snapshot.Items[0].CanSteer)

	_, _, err = store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	_, _, err = store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	// A steer of the future head states the schedule, not a turn end: the
	// turn still runs.
	steered, _, err := store.PrepareSteer(ctx, "agent-1", "future", false)
	assert.ErrorIs(t, err, ErrScheduled)
	assert.NotErrorIs(t, err, ErrTurnEnded)
	assert.Nil(t, steered)
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Items[0].CanSteer, "a future head is not steerable")
	assert.False(t, snapshot.Items[0].CanPreempt, "a future head is not preemptable")
	assert.False(t, canDrainAutomatically(snapshot), "a future head does not drain")

	// Immediate work behind a future head still dispatches, and the dispatch
	// selects the immediate item, never the future one.
	_, _, err = store.TurnEnded(ctx, "agent-1", false)
	require.NoError(t, err)
	enqueueAt(t, store, "immediate", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, "immediate", prepared.Item.ID)

	// Exactly at the deadline the item matures and dispatches.
	clock.Advance(time.Hour)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{})
	require.NoError(t, err)
	prepared, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, "future", prepared.Item.ID)
}

func TestPriorityScheduledAndSourcePayloadRoundTrip(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	payload := []byte(`{"retry":"usage-limit"}`)
	snapshot := scheduleRetry(t, store, kind, *scheduled(t, time.Hour), payload)
	require.Len(t, snapshot.Items, 1)
	retryID := snapshot.Items[0].ID
	require.NotNil(t, snapshot.Items[0].ScheduledAt)
	assert.False(t, snapshot.Items[0].ScheduledAt.Before(*scheduled(t, time.Hour)), "the deadline is not before the provider's reset")
	assert.Equal(t, payload, storedItem(t, store, retryID).SourcePayload)

	// A full read through BeginEdit returns the same schedule.
	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", retryID, "client", false)
	require.NoError(t, err)

	// An edit moves the schedule and keeps the payload. The moved deadline is
	// still ahead of the clock, so the item stays queued until it matures.
	moved := priorityBaseTime().Add(2 * time.Hour)
	snapshot, err = store.Update(ctx, "agent-1", retryID, "client", snapshot.Items[0].Version, "Continue.", nil, ReplaceSchedule(&moved))
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	require.NotNil(t, snapshot.Items[0].ScheduledAt)
	assert.True(t, moved.Equal(*snapshot.Items[0].ScheduledAt))
	assert.Equal(t, payload, storedItem(t, store, retryID).SourcePayload, "an edit preserves the source payload")
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, prepared)

	// Past the moved deadline the dispatch carries both fields.
	clock.Set(moved)
	prepared, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, payload, prepared.Item.SourcePayload)
	require.NotNil(t, prepared.Item.ScheduledAt)

	// Only the Worker creates retry items: an enqueue of the kind is refused,
	// whatever the caller.
	_, err = store.Enqueue(ctx, NewItem{
		ID: "external", AgentID: "agent-1", Kind: kind, Text: "Continue.",
		ScheduledAt: &moved, SourcePayload: payload,
	})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// A repeated enqueue of an accepted scheduled input with the same identity
// adds nothing; one with a changed schedule conflicts instead of duplicating.
func TestPriorityRepeatedEnqueueComparesTheSchedule(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	at := scheduled(t, time.Hour)
	enqueueAt(t, store, "scheduled", at, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	clock.Set(*at)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{})
	require.NoError(t, err)

	_, added, err := store.enqueue(ctx, NewItem{
		ID: "scheduled", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue.",
		ScheduledAt: at,
	}, nil)
	require.NoError(t, err)
	assert.False(t, added)
	later := at.Add(time.Hour)
	_, _, err = store.enqueue(ctx, NewItem{
		ID: "scheduled", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue.",
		ScheduledAt: &later,
	}, nil)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestPriorityControlReplyPrecedesAFutureRetry(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	retryID := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, nil).Items[0].ID
	enqueueAt(t, store, "ordinary", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	_, err := store.Enqueue(ctx, NewItem{
		ID: "reply", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "allowed",
	})
	require.NoError(t, err)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"reply", "ordinary", retryID}, itemIDs(snapshot),
		"an immediate control reply precedes ordinary input and future retries alike")
}

// A dispatch takes the raw slot of its effective position, so an item whose
// deadline passes later never sorts ahead of it: not while it is in flight,
// not after it fails, and not after Retry returns it to QUEUED.
func TestPriorityMaturedItemNeverOvertakesAnAttemptedDispatch(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "scheduled", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "sent", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, "sent", prepared.Item.ID)
	assert.Equal(t, int64(1), prepared.Item.Order, "the dispatch holds the slot of its effective position")

	clock.Set(*scheduled(t, time.Hour))
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"sent", "scheduled"}, itemIDs(snapshot), "the dispatch in flight stays the head")
	for _, item := range snapshot.Items {
		assert.False(t, item.CanPreempt, "%s must offer no Preempt that cancels the dispatch's own turn", item.ID)
	}

	snapshot, err = store.FailDispatch(ctx, "agent-1", "sent", assert.AnError, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"sent", "scheduled"}, itemIDs(snapshot), "the failed delivery stays the head")
	_, err = store.Retry(ctx, "agent-1", "sent", false)
	require.NoError(t, err, "the failed head stays retryable")
	retried, _, err := store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, "sent", retried.Item.ID, "Retry sends the failed item, not the matured one")
}

// A Worker restart after a deadline passed leaves the unconfirmed delivery at
// the head: the matured retry neither hides its Retry nor dispatches ahead of it.
func TestPriorityMaturedRetryStaysBehindAnUncertainDelivery(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	retry := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, *scheduled(t, time.Hour), nil).Items[0]
	enqueueAt(t, store, "message", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, "message", prepared.Item.ID)

	clock.Set(*retry.ScheduledAt)
	snapshots, err := store.Recover(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, []string{"message", retry.ID}, itemIDs(snapshots[0]))

	_, err = store.SetPaused(ctx, "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
	require.NoError(t, err)
	next, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, next, "the uncertain head holds the queue; the matured retry must not dispatch ahead of it")

	_, err = store.Retry(ctx, "agent-1", "message", true)
	require.NoError(t, err)
	retried, _, err := store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, "message", retried.Item.ID)
}

// A control reply takes its place behind a failure at the front, also when a
// future item holds an older slot. No deadline needs to pass.
func TestPriorityControlReplyStaysBehindAFailedHeadWithAFutureItemQueued(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "sent", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(ctx, "agent-1", "sent", assert.AnError, false)
	require.NoError(t, err)

	snapshot, err := store.Enqueue(ctx, NewItem{
		ID: "reply", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "allowed",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sent", "reply", "future"}, itemIDs(snapshot))
	_, err = store.Retry(ctx, "agent-1", "sent", false)
	assert.NoError(t, err, "the failed head stays retryable")
}

// A busy refusal returns the dispatch to QUEUED in its own slot, so the end of
// the provider's turn delivers it before an item that matured meanwhile.
func TestPriorityBusyRequeueKeepsTheDispatchAheadOfAMaturedItem(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "scheduled", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "sent", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	clock.Set(*scheduled(t, time.Hour))
	snapshot, err := store.RequeueBusy(ctx, "agent-1", "sent", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"sent", "scheduled"}, itemIDs(snapshot))
	_, _, err = store.TurnEnded(ctx, "agent-1", false)
	require.NoError(t, err)
	next, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, "sent", next.Item.ID)
}

// A failed row is not locked, so a user may still move queued input ahead of
// it, and that input stays the head while it dispatches.
func TestPriorityMoveStillPlacesQueuedInputAheadOfAFailedHead(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "sent", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "next", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(ctx, "agent-1", "sent", assert.AnError, false)
	require.NoError(t, err)

	snapshot, err := store.Move(ctx, "agent-1", "next", "sent")
	require.NoError(t, err)
	require.Equal(t, []string{"next", "sent"}, itemIDs(snapshot))
	_, err = store.SetPaused(ctx, "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
	require.NoError(t, err)
	moved, snapshot, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, moved)
	assert.Equal(t, "next", moved.Item.ID)
	assert.Equal(t, []string{"next", "sent"}, itemIDs(snapshot), "the dispatch keeps the place that the user gave it")
}

func TestPriorityEchoBatchKeepsIdentityAndOrderWithScheduledItems(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	// Two echo-held steers, then a future item enqueued behind them. The echo
	// batch must keep its own identity and provider order.
	for _, id := range []string{"echo-one", "echo-two"} {
		_, _, err := store.TurnStarted(ctx, "agent-1", true)
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Text: id,
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
		require.NoError(t, err)
		prepared, _, err := store.PrepareSteer(ctx, "agent-1", id, true)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		_, err = store.HoldForEcho(ctx, *prepared)
		require.NoError(t, err)
	}
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	rows, _, snapshot, err := store.AcceptEchoedSteers(ctx, "agent-1", 2, "[]", "")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "echo-one", rows[0].ID)
	assert.Equal(t, "echo-two", rows[1].ID)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "future", snapshot.Items[0].ID)
	assert.Equal(t, "2026-10-24T10:00:00.000Z", snapshot.Items[0].ReorderGroup)
}

func TestPriorityValidateSchedule(t *testing.T) {
	t.Parallel()
	retryKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	userKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE
	assert.ErrorIs(t, validateSchedule(retryKind, nil, []Attachment{{Filename: "a.txt"}}), ErrInvalidInput,
		"an automatic retry accepts no attachments")
	assert.NoError(t, validateSchedule(retryKind, []byte("{}"), nil))
	assert.NoError(t, validateSchedule(retryKind, nil, nil), "a retry without a source is valid")
	assert.ErrorIs(t, validateSchedule(retryKind, make([]byte, MaxItemBytes+1), nil), ErrItemTooLarge)
	assert.NoError(t, validateSchedule(retryKind, make([]byte, MaxItemBytes), nil), "the limit itself is admitted")
	assert.ErrorIs(t, validateSchedule(userKind, []byte("{}"), nil), ErrInvalidInput,
		"an ordinary item never carries a retry source payload")
	assert.NoError(t, validateSchedule(userKind, nil, nil))
}

// storedItem reads one item's full row, the way the dispatch path does.
func storedItem(t *testing.T, store *Store, id string) StoredItem {
	t.Helper()
	item, _, found, err := getItem(t.Context(), store.db, "agent-1", id, false)
	require.NoError(t, err)
	require.True(t, found)
	return item
}

func TestPriorityMoveRefusesUnknownIds(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "one", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "two", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	_, err := store.Move(ctx, "agent-1", "missing", "one")
	assert.ErrorIs(t, err, ErrNotFound, "an unknown source item is not a move")
	_, err = store.Move(ctx, "agent-1", "one", "missing")
	assert.ErrorIs(t, err, ErrNotFound, "an unknown destination is not a move")

	// The refusals changed no durable order.
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, itemIDs(snapshot))
}

// A snapshot states each item's effective position, and keeps the durable
// order_index slot apart from it: a future item enqueued first holds slot 1
// but renders second.
func TestPrioritySnapshotKeepsTheDurableSlotApartFromThePosition(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "immediate", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)

	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Equal(t, []string{"immediate", "future"}, itemIDs(snapshot))
	assert.Equal(t, int64(2), snapshot.Items[0].Order, "the durable slot of the item enqueued second")
	assert.Equal(t, int64(1), snapshot.Items[1].Order, "the durable slot of the item enqueued first")
}

// A snapshot carries no provider source: no client reads it, and it can hold
// megabytes per row. The dispatch reads it from the item's full row.
func TestPrioritySnapshotCarriesNoSourcePayload(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	snapshot := scheduleRetry(t, store, kind, time.Time{}, []byte(`{"error":"overloaded"}`))
	require.Len(t, snapshot.Items, 1)
	assert.Empty(t, snapshot.Items[0].SourcePayload)
	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items[0].SourcePayload)
}
