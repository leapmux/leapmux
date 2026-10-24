package inputqueue

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
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
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "queue.sqlite"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), database))
	require.NoError(t, gendb.New(database).CreateAgent(context.Background(), gendb.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	clock := quartz.NewMock(t)
	clock.Set(priorityBaseTime())
	return clock, database, NewStore(database, clock)
}

func enqueueAt(t *testing.T, store *Store, id string, scheduledAt *time.Time, kind leapmuxv1.AgentInputKind) {
	t.Helper()
	_, err := store.Enqueue(t.Context(), NewItem{
		ID: id, AgentID: "agent-1", Kind: kind, Text: "Continue.", ScheduledAt: scheduledAt,
		SourcePayload: payloadForKind(kind),
	})
	require.NoError(t, err)
}

func payloadForKind(kind leapmuxv1.AgentInputKind) []byte {
	if autoRetryKind(kind) {
		return []byte(`{"retry":"` + kind.String() + `"}`)
	}
	return nil
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
	enqueueAt(t, store, "future", future, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)

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
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
	enqueueAt(t, store, "immediate", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"immediate", "future"}, itemIDs(snapshot))
	assert.Equal(t, int64(1), snapshot.Items[0].Order, "the public order states the effective position")
	assert.Equal(t, int64(2), snapshot.Items[1].Order)
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
	enqueueAt(t, store, "older", scheduled(t, time.Minute), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)
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
	// Two ordinary items share the +1h instant, which makes them one reorder
	// group; the retry kinds sit at their own instants.
	enqueueAt(t, store, "future-one", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "future-two", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	enqueueAt(t, store, "later", scheduled(t, 2*time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)

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
	// the locked row bounds: "one" stays before it, not at the queue's end.
	snapshot, err := store.Move(ctx, "agent-1", "one", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "three"}, itemIDs(snapshot))
}

func TestPriorityGuardsNeverBypassAFutureDeadline(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)

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
	_, snapshot, err = store.PrepareSteer(ctx, "agent-1", "future", false)
	require.NoError(t, err)
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, prepared.Item.Kind)
}

func TestPriorityScheduledAndSourcePayloadRoundTrip(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	at := scheduled(t, time.Hour)
	enqueueAt(t, store, "retry", at, kind)

	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	require.NotNil(t, snapshot.Items[0].ScheduledAt)
	assert.True(t, at.Equal(*snapshot.Items[0].ScheduledAt))
	assert.Equal(t, payloadForKind(kind), snapshot.Items[0].SourcePayload)

	// A full read through BeginEdit returns the same schedule.
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "retry", "client", false)
	require.NoError(t, err)

	// An edit moves the schedule and keeps the payload. The moved deadline is
	// still ahead of the clock, so the item stays queued until it matures.
	moved := priorityBaseTime().Add(2 * time.Hour)
	snapshot, err = store.Update(ctx, "agent-1", "retry", "client", snapshot.Items[0].Version, "Continue.", nil, &moved)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	require.NotNil(t, snapshot.Items[0].ScheduledAt)
	assert.True(t, moved.Equal(*snapshot.Items[0].ScheduledAt))
	assert.Equal(t, payloadForKind(kind), snapshot.Items[0].SourcePayload, "an edit preserves the source payload")
	_, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)

	// Past the moved deadline the dispatch carries both fields.
	clock.Advance(2 * time.Hour)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, payloadForKind(kind), prepared.Item.SourcePayload)
	require.NotNil(t, prepared.Item.ScheduledAt)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{})
	require.NoError(t, err)

	// A repeat enqueue of the accepted input with the same identity adds
	// nothing; one with a changed schedule conflicts instead of duplicating.
	_, added, err := store.enqueue(ctx, NewItem{
		ID: "retry", AgentID: "agent-1", Kind: kind, Text: "Continue.",
		ScheduledAt: &moved, SourcePayload: payloadForKind(kind),
	}, nil)
	require.NoError(t, err)
	assert.False(t, added)
	later := moved.Add(time.Hour)
	_, _, err = store.enqueue(ctx, NewItem{
		ID: "retry", AgentID: "agent-1", Kind: kind, Text: "Continue.",
		ScheduledAt: &later, SourcePayload: payloadForKind(kind),
	}, nil)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestPriorityControlReplyPrecedesAFutureRetry(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	ctx := t.Context()
	enqueueAt(t, store, "future-retry", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
	enqueueAt(t, store, "ordinary", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	_, err := store.Enqueue(ctx, NewItem{
		ID: "reply", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "allowed",
	})
	require.NoError(t, err)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"reply", "ordinary", "future-retry"}, itemIDs(snapshot),
		"an immediate control reply precedes ordinary input and future retries alike")
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
	enqueueAt(t, store, "future", scheduled(t, time.Hour), leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)

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
	assert.ErrorIs(t, validateSchedule(retryKind, nil, nil, nil), ErrInvalidInput)
	assert.ErrorIs(t, validateSchedule(retryKind, scheduled(t, time.Minute), nil,
		[]Attachment{{Filename: "a.txt"}}), ErrInvalidInput)
	assert.NoError(t, validateSchedule(retryKind, scheduled(t, time.Minute), []byte("{}"), nil))
	assert.ErrorIs(t, validateSchedule(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		nil, []byte("{}"), nil), ErrInvalidInput,
		"an ordinary item never carries a retry source payload")
	assert.NoError(t, validateSchedule(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, nil, nil, nil))
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
