package inputqueue

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Automatic-retry scheduling: the queue owns the item, the deadline policy,
// and the server-error backoff. One pending item per kind, a fresh ID per
// cycle, and resets exactly on success-shaped endings.

func scheduleRetry(t *testing.T, store *Store, kind leapmuxv1.AgentInputKind, at time.Time, payload []byte) Snapshot {
	t.Helper()
	snapshot, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: kind, ScheduledAt: at, SourcePayload: payload,
	})
	require.NoError(t, err)
	return snapshot
}

func storedRetryRow(t *testing.T, database *sql.DB, kind leapmuxv1.AgentInputKind) (id string, scheduledAt *time.Time) {
	t.Helper()
	var storedID string
	var scheduled sql.NullString
	err := database.QueryRowContext(t.Context(),
		`SELECT id, CAST(scheduled_at AS TEXT) FROM agent_input_queue_items WHERE agent_id = 'agent-1' AND kind = ?`, kind).
		Scan(&storedID, &scheduled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	require.NoError(t, err)
	if scheduled.Valid {
		at, err := time.Parse("2006-01-02T15:04:05.000Z07:00", scheduled.String)
		require.NoError(t, err)
		return storedID, &at
	}
	return storedID, nil
}

func storedServerErrorDelay(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var delay int64
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT auto_retry_server_error_delay_ms FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&delay))
	return delay
}

func TestAutoRetryRepeatedSchedulingUpdatesOneRowAndKeepsItsPosition(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR

	_, err := store.Enqueue(t.Context(), NewItem{ID: "ordinary", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)

	first := scheduleRetry(t, store, kind, time.Time{}, []byte(`{"e":1}`))
	require.Len(t, first.Items, 2)
	firstID, firstAt := storedRetryRow(t, database, kind)
	require.NotNil(t, firstAt)

	second := scheduleRetry(t, store, kind, time.Time{}, []byte(`{"e":2}`))
	secondID, secondAt := storedRetryRow(t, database, kind)
	assert.Equal(t, firstID, secondID, "rescheduling the pending item retains its ID")
	assert.NotEqual(t, firstAt, secondAt, "rescheduling moves the deadline")
	// The retry keeps the raw queue position its first cycle took: it stays
	// behind the ordinary item that was queued ahead of it.
	require.Len(t, second.Items, 2)
	assert.Equal(t, "ordinary", second.Items[0].ID)
	assert.Equal(t, secondID, second.Items[1].ID)
	assert.Equal(t, []byte(`{"e":2}`), second.Items[1].SourcePayload)
}

func TestAutoRetryBothKindsCoexistAndCancelRemovesOnlyItsKind(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	usage := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	server := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	base := priorityBaseTime()

	scheduleRetry(t, store, usage, base.Add(time.Hour), nil)
	// The server-error deadline (now + backoff) lands sooner than the usage
	// limit's reset an hour out, so it heads the snapshot.
	snapshot := scheduleRetry(t, store, server, time.Time{}, nil)
	require.Len(t, snapshot.Items, 2)
	assert.Equal(t, server, snapshot.Items[0].Kind)
	assert.Equal(t, usage, snapshot.Items[1].Kind)

	snapshot, changed, err := store.cancelAutoRetry(t.Context(), "agent-1", usage, true)
	require.NoError(t, err)
	assert.True(t, changed)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, server, snapshot.Items[0].Kind)
	usageID, _ := storedRetryRow(t, database, usage)
	assert.Empty(t, usageID)
	serverID, _ := storedRetryRow(t, database, server)
	assert.NotEmpty(t, serverID)
}

func TestAutoRetryServerErrorBackoffDoublesAcrossAcceptedCycles(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR

	expected := []time.Duration{
		10 * time.Second, 20 * time.Second, 40 * time.Second,
		80 * time.Second, 160 * time.Second, 180 * time.Second,
	}
	for i, delay := range expected {
		before := clock.Now()
		snapshot := scheduleRetry(t, store, kind, time.Time{}, []byte(`{}`))
		require.Len(t, snapshot.Items, 1, "cycle %d", i)
		at := snapshot.Items[0].ScheduledAt
		require.NotNil(t, at)
		assert.GreaterOrEqual(t, at.Sub(before), time.Duration(float64(delay)*0.8),
			"cycle %d: the deadline includes at most -20%% jitter", i)
		assert.LessOrEqual(t, at.Sub(before), time.Duration(float64(delay)*1.2),
			"cycle %d: the deadline includes at most +20%% jitter", i)
		assert.Zero(t, at.Nanosecond()%int(time.Millisecond), "the deadline floors to the millisecond")

		// The accepted cycle removes the item; the delay it stored survives.
		clock.Set(*at)
		prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{})
		require.NoError(t, err)
	}

	// The cap holds: the stored delay stops at 180 seconds.
	assert.Equal(t, int64(180000), storedServerErrorDelay(t, database))
}

func TestAutoRetryCancelDeleteAndRefusalResetOrKeepBackoff(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR

	grow := func(cycles int) {
		for i := 0; i < cycles; i++ {
			snapshot := scheduleRetry(t, store, kind, time.Time{}, nil)
			clock.Set(*snapshot.Items[0].ScheduledAt)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{})
			require.NoError(t, err)
		}
	}

	// A refusal leaves the durable delay untouched: the transaction rolls back.
	grow(2)
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, database))
	_, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = store.scheduleAutoRetry(t.Context(), "closed-agent", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, database), "a refused schedule changes no backoff")

	// Cancelling the pending server-error retry resets the delay.
	scheduleRetry(t, store, kind, time.Time{}, nil)
	_, _, err = store.cancelAutoRetry(t.Context(), "agent-1", kind, true)
	require.NoError(t, err)
	assert.Equal(t, int64(10000), storedServerErrorDelay(t, database))

	// Deleting the pending retry through the composer path resets it too.
	grow(1)
	snapshot := scheduleRetry(t, store, kind, time.Time{}, nil)
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, database))
	_, err = store.Delete(t.Context(), "agent-1", snapshot.Items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10000), storedServerErrorDelay(t, database))
}

func TestAutoRetryUsageLimitJitterBounds(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	now := clock.Now()

	// A long window targets 5% of the remaining duration, capped at 30 seconds.
	reset := now.Add(10 * time.Minute)
	at := scheduleRetry(t, store, kind, reset, nil).Items[0].ScheduledAt
	require.NotNil(t, at)
	assert.True(t, at.After(reset), "the jitter is strictly positive")
	assert.LessOrEqual(t, at.Sub(reset), 30*time.Second, "the jitter caps at 30 seconds")

	// A short window takes the 5-second minimum instead of a slice of nothing.
	reset = now.Add(10 * time.Second)
	at = scheduleRetry(t, store, kind, reset, nil).Items[0].ScheduledAt
	require.NotNil(t, at)
	assert.True(t, at.After(reset))
	assert.LessOrEqual(t, at.Sub(reset), 5*time.Second, "a short window uses the 5-second minimum")

	// A reset instant already in the past takes the same minimum and stays a
	// past-due -- and therefore immediate -- item.
	reset = now.Add(-time.Minute)
	at = scheduleRetry(t, store, kind, reset, nil).Items[0].ScheduledAt
	require.NotNil(t, at)
	assert.True(t, at.After(reset))
	assert.LessOrEqual(t, at.Sub(reset), 5*time.Second)
}

func TestAutoRetryRefusesClosedAndArchivedAgents(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR

	// A closed agent: late provider output cannot recreate a retry after the
	// close removed it.
	_, err := database.ExecContext(t.Context(), `UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = 'agent-1'`)
	require.NoError(t, err)
	_, err = store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrConflict)
	var rows int
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = 'agent-1'`).Scan(&rows))
	assert.Zero(t, rows)

	// An archived workspace refuses just the same, without touching the row.
	_, err = database.ExecContext(t.Context(), `UPDATE agents SET closed_at = NULL, workspace_archived = 1 WHERE id = 'agent-1'`)
	require.NoError(t, err)
	_, err = store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrConflict)
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = 'agent-1'`).Scan(&rows))
	assert.Zero(t, rows)
}

func TestAutoRetryQueueFullRefusesWithoutTouchingDurableState(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	for i := 0; i < MaxItems; i++ {
		_, err := store.Enqueue(t.Context(), NewItem{
			ID: fmt.Sprintf("ordinary-%d", i), AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
		})
		require.NoError(t, err)
	}
	_, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrQueueFull)
	var rows int
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = 'agent-1'`).Scan(&rows))
	assert.Equal(t, MaxItems, rows)
}

func TestAutoRetryFreshCycleTakesAFreshIDAndClonesItsPayload(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	reset := clock.Now().Add(time.Hour)

	payload := []byte(`{"limit":"rate"}`)
	snapshot := scheduleRetry(t, store, kind, reset, payload)
	firstID := snapshot.Items[0].ID
	// Mutating the caller's buffer after the call must not reach the queue.
	copy(payload, []byte(`XXXXXX  `))

	// Past the deadline the dispatch carries the payload the queue cloned.
	clock.Set(*snapshot.Items[0].ScheduledAt)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, []byte(`{"limit":"rate"}`), prepared.Item.SourcePayload)
	_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{})
	require.NoError(t, err)

	// The next cycle is a NEW item: an accepted transcript's ID is never reused.
	next := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), nil)
	require.Len(t, next.Items, 1)
	assert.NotEqual(t, firstID, next.Items[0].ID)
}
