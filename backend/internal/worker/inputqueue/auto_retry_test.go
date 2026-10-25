package inputqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Automatic-retry scheduling: the queue owns the item, the deadline policy,
// and the server-error backoff. One pending item per kind, a fresh ID per
// cycle, and resets exactly on success-shaped endings.

func scheduleRetry(t *testing.T, store *Store, kind leapmuxv1.AgentInputKind, at time.Time, payload []byte) Snapshot {
	t.Helper()
	snapshot, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: kind, ResetAt: at, SourcePayload: payload,
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

// acceptDueRetry moves the clock to the retry's deadline and accepts the
// dispatch that the deadline releases.
func acceptDueRetry(t *testing.T, clock *quartz.Mock, store *Store, at time.Time) PreparedDispatch {
	t.Helper()
	clock.Set(at)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{})
	require.NoError(t, err)
	return *prepared
}

// countQueueRows counts every row of agent-1's queue, whatever its state.
func countQueueRows(t *testing.T, database *sql.DB) int {
	t.Helper()
	var rows int
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = 'agent-1'`).Scan(&rows))
	return rows
}

func storedServerErrorDelay(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var delay int64
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT auto_retry_server_error_delay_ms FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&delay))
	return delay
}

func TestAutoRetryMessageDescribesItsCauseThroughDispatchAndTranscript(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind leapmuxv1.AgentInputKind
		want string
	}{
		{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, "You were temporarily interrupted due to usage limit, but the usage limit has been cleared. Please continue working."},
		{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, "You were temporarily interrupted due to an infra issue, but the infra issue is now gone. Please continue working."},
	} {
		t.Run(tc.kind.String(), func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			snapshot := scheduleRetry(t, store, tc.kind, clock.Now().Add(time.Hour), nil)
			require.Len(t, snapshot.Items, 1)
			firstID := snapshot.Items[0].ID
			assert.Equal(t, tc.want, snapshot.Items[0].TextPreview)
			snapshot = scheduleRetry(t, store, tc.kind, clock.Now().Add(2*time.Hour), nil)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, firstID, snapshot.Items[0].ID)
			assert.Equal(t, tc.want, snapshot.Items[0].TextPreview)
			clock.Set(*snapshot.Items[0].ScheduledAt)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			assert.Equal(t, tc.want, prepared.Item.Text)
			accepted, _, err := store.Accept(t.Context(), *prepared, DispatchResult{})
			require.NoError(t, err)
			content, err := msgcodec.Decompress(accepted.Content, accepted.ContentCompression)
			require.NoError(t, err)
			var body struct {
				Content string `json:"content"`
			}
			require.NoError(t, json.Unmarshal(content, &body))
			assert.Equal(t, tc.want, body.Content)
		})
	}
}

func TestAutoRetryReschedulingPreservesEditedMessageText(t *testing.T) {
	t.Parallel()
	for _, kind := range autoRetryKinds {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			snapshot := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), nil)
			id := snapshot.Items[0].ID
			defaultText := snapshot.Items[0].TextPreview
			snapshot, _, _, err := store.BeginEdit(t.Context(), "agent-1", id, "client", false)
			require.NoError(t, err)
			_, err = store.Update(t.Context(), UpdateRequest{
				AgentID: "agent-1", InputID: id, ClientID: "client", ExpectedVersion: snapshot.Items[0].Version,
				Text: "Resume the verification from its last completed step.", Schedule: KeepSchedule(),
			})
			require.NoError(t, err)
			snapshot = scheduleRetry(t, store, kind, clock.Now().Add(2*time.Hour), nil)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, id, snapshot.Items[0].ID)
			assert.Equal(t, "Resume the verification from its last completed step.", snapshot.Items[0].TextPreview)
			clock.Set(*snapshot.Items[0].ScheduledAt)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			assert.Equal(t, "Resume the verification from its last completed step.", prepared.Item.Text)
			_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{})
			require.NoError(t, err)
			next := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), nil)
			require.Len(t, next.Items, 1)
			assert.NotEqual(t, id, next.Items[0].ID, "a new cycle receives its own input identity")
			assert.Equal(t, defaultText, next.Items[0].TextPreview, "an edit belongs to its completed cycle")
		})
	}
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
	assert.Equal(t, []byte(`{"e":2}`), storedItem(t, store, secondID).SourcePayload)
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

	snapshot, changed, err := store.cancelAutoRetries(t.Context(), "agent-1", usage)
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
		acceptDueRetry(t, clock, store, *at)
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
			acceptDueRetry(t, clock, store, *snapshot.Items[0].ScheduledAt)
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
	_, _, err = store.cancelAutoRetries(t.Context(), "agent-1", kind)
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

func TestAutoRetryUsageLimitJitterLimits(t *testing.T) {
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
	assert.Zero(t, countQueueRows(t, database))

	// An archived workspace refuses just the same, without touching the row.
	_, err = database.ExecContext(t.Context(), `UPDATE agents SET closed_at = NULL, workspace_archived = 1 WHERE id = 'agent-1'`)
	require.NoError(t, err)
	_, err = store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrConflict)
	assert.Zero(t, countQueueRows(t, database))
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
	assert.Equal(t, MaxItems, countQueueRows(t, database))
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

	// Past the deadline the dispatch carries the payload the queue copied.
	prepared := acceptDueRetry(t, clock, store, *snapshot.Items[0].ScheduledAt)
	assert.Equal(t, []byte(`{"limit":"rate"}`), prepared.Item.SourcePayload)

	// The next cycle is a NEW item: an accepted transcript's ID is never reused.
	next := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), nil)
	require.Len(t, next.Items, 1)
	assert.NotEqual(t, firstID, next.Items[0].ID)
}

// An agent that never queued input has no queue state row. Cancelling its
// retries is then a no-op, not a failure: every close of such an agent, and
// every child cleanup, cancels.
func TestAutoRetryCancelForAnAgentWithoutQueueStateSucceeds(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)

	require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, nil))
	require.NoError(t, manager.CancelAutoRetries(t.Context(), "agent-1"))
}

// A retry that the user steers into a turn, on a provider that writes the
// row at the echo, keeps its provider source exactly as a dispatched retry
// does.
func TestAutoRetrySteeredAtTheEchoKeepsItsSource(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	payload := []byte(`{"error":"overloaded"}`)
	snapshot := scheduleRetry(t, store, kind, time.Time{}, payload)
	require.Len(t, snapshot.Items, 1)
	retryID := snapshot.Items[0].ID
	clock.Set(*snapshot.Items[0].ScheduledAt)
	_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)

	prepared, _, err := store.PrepareSteer(t.Context(), "agent-1", retryID, true)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.HoldForEcho(t.Context(), *prepared)
	require.NoError(t, err)
	rows, _, _, err := store.AcceptEchoedSteers(t.Context(), "agent-1", 1, "[]", "")
	require.NoError(t, err)
	require.Len(t, rows, 1)

	supplement, err := msgcodec.Decompress(rows[0].SupplementalContent, rows[0].SupplementalContentCompression)
	require.NoError(t, err)
	decoded, err := agent.DecodeMessageSupplement(nil, supplement)
	require.NoError(t, err)
	assert.JSONEq(t, string(payload), string(decoded.Supplemental))
}

// A dispatched retry of either kind keeps its provider source as the
// supplemental content of its transcript, so the browser can extract the
// provider's own view of what failed. A retry with no source stores none.
func TestAutoRetryDispatchedKeepsItsSource(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			for _, payload := range [][]byte{[]byte(`{"error":"overloaded"}`), nil} {
				clock, _, store := newPriorityFixture(t)
				snapshot := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), payload)
				require.Len(t, snapshot.Items, 1)
				clock.Set(*snapshot.Items[0].ScheduledAt)
				prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
				require.NoError(t, err)
				require.NotNil(t, prepared)
				accepted, _, err := store.Accept(t.Context(), *prepared, DispatchResult{})
				require.NoError(t, err)

				if payload == nil {
					assert.Empty(t, accepted.SupplementalContent, "no source means no supplemental content")
					assert.Equal(t, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, accepted.SupplementalContentCompression)
					continue
				}
				supplement, err := msgcodec.Decompress(accepted.SupplementalContent, accepted.SupplementalContentCompression)
				require.NoError(t, err)
				decoded, err := agent.DecodeMessageSupplement(nil, supplement)
				require.NoError(t, err)
				assert.JSONEq(t, string(payload), string(decoded.Supplemental))
			}
		})
	}
}

// Only a kind that the agent accepts may be queued, and a retry is no
// exception: a child agent takes messages only.
func TestAutoRetryRefusesAKindTheAgentDoesNotAccept(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	manager := NewManager(store, &recordingDispatcher{refusedKind: kind}, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)

	err := manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind})
	assert.ErrorIs(t, err, ErrInvalidInput)
	id, _ := storedRetryRow(t, database, kind)
	assert.Empty(t, id, "a refused kind leaves no row")
}

// A provider source that the queue cannot store is dropped, and the retry it
// came with is kept: the retry is the purpose, the source only explains it.
func TestAutoRetryKeepsTheRetryWhenTheSourceCannotBeStored(t *testing.T) {
	t.Parallel()
	_, database, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)

	for name, payload := range map[string][]byte{
		"too large": make([]byte, MaxItemBytes+1),
		"not JSON":  []byte("Error: overloaded"),
	} {
		require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind, SourcePayload: payload}), name)
		id, _ := storedRetryRow(t, database, kind)
		require.NotEmpty(t, id, name)
		var stored []byte
		require.NoError(t, database.QueryRowContext(t.Context(),
			`SELECT source_payload FROM agent_input_queue_items WHERE id = ?`, id).Scan(&stored), name)
		assert.Empty(t, stored, "%s: the source is dropped", name)
	}
}

// The usage-limit jitter is a whole number of milliseconds and at least one,
// so the millisecond floor of the stored deadline can never erase it.
func TestAutoRetryUsageLimitJitterIsWholeMilliseconds(t *testing.T) {
	t.Parallel()
	for _, remaining := range []time.Duration{-time.Minute, 0, 10 * time.Second, time.Hour} {
		for i := 0; i < 1000; i++ {
			jitter := positiveRateLimitJitter(remaining)
			require.Zero(t, jitter%time.Millisecond, "remaining %s: jitter %s", remaining, jitter)
			require.GreaterOrEqual(t, jitter, time.Millisecond)
			require.LessOrEqual(t, jitter, rateLimitJitterMax)
		}
	}
}

// An edit can clear a retry's schedule, as it can for any other row: a nil
// schedule and a past one are the same immediate item.
func TestAutoRetryEditCanClearTheSchedule(t *testing.T) {
	t.Parallel()
	_, _, store := newPriorityFixture(t)
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	snapshot := scheduleRetry(t, store, kind, time.Time{}, nil)
	require.Len(t, snapshot.Items, 1)
	retryID := snapshot.Items[0].ID

	snapshot, _, _, err := store.BeginEdit(t.Context(), "agent-1", retryID, "client", false)
	require.NoError(t, err)
	snapshot, err = store.Update(t.Context(), UpdateRequest{
		AgentID:         "agent-1",
		InputID:         retryID,
		ClientID:        "client",
		ExpectedVersion: snapshot.Items[0].Version,
		Text:            snapshot.Items[0].TextPreview,
		Attachments:     nil,
		Schedule:        ReplaceSchedule(nil),
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Nil(t, snapshot.Items[0].ScheduledAt)
	assert.Empty(t, snapshot.Items[0].ReorderGroup, "a cleared retry joins the immediate group")
}

// otherRetry returns the snapshot item whose ID is not exceptID, or fails the
// test.
func otherRetry(t *testing.T, snapshot Snapshot, exceptID string) SnapshotItem {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.ID != exceptID {
			return item
		}
	}
	require.FailNow(t, "the snapshot holds no other retry")
	return SnapshotItem{}
}

// dispatchDueRetry schedules a server-error retry with the given source, moves
// the clock to its deadline, and dispatches it.
func dispatchDueRetry(t *testing.T, clock *quartz.Mock, store *Store, payload string) string {
	t.Helper()
	snapshot := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(payload))
	require.Len(t, snapshot.Items, 1)
	clock.Set(*snapshot.Items[0].ScheduledAt)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, snapshot.Items[0].ID, prepared.Item.ID)
	return prepared.Item.ID
}

// failDueRetry dispatches a due server-error retry and records the dispatch
// as failed.
func failDueRetry(t *testing.T, clock *quartz.Mock, store *Store, payload string) string {
	t.Helper()
	retryID := dispatchDueRetry(t, clock, store, payload)
	_, err := store.FailDispatch(t.Context(), "agent-1", retryID, errors.New("the write failed"), false)
	require.NoError(t, err)
	return retryID
}

// A dispatch that returns a retry row to QUEUED without delivering it meets
// the newer retry of its kind that a report scheduled meanwhile. The returning
// row absorbs it: one row stays, with the first row's ID and the newer
// deadline and source, as a reschedule after the return would have left it.
func TestAutoRetryReturnedDispatchAbsorbsTheNewerCycleOfItsKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		requeue func(context.Context, *Store, string) (Snapshot, error)
	}{
		{"busy refusal", func(ctx context.Context, store *Store, id string) (Snapshot, error) {
			return store.RequeueBusy(ctx, "agent-1", id, false)
		}},
		{"steer requeue", func(ctx context.Context, store *Store, id string) (Snapshot, error) {
			return store.RequeuePrepared(ctx, "agent-1", id)
		}},
		{"requeue and pause", func(ctx context.Context, store *Store, id string) (Snapshot, error) {
			return store.RequeueAndPause(ctx, "agent-1", id, errors.New("the agent is starting"), leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock, database, store := newPriorityFixture(t)
			firstID := dispatchDueRetry(t, clock, store, `{"e":1}`)
			newer := otherRetry(t, scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":2}`)), firstID)
			require.NotNil(t, newer.ScheduledAt)

			snapshot, err := tc.requeue(t.Context(), store, firstID)
			require.NoError(t, err)
			require.Len(t, snapshot.Items, 1, "one pending retry of the kind remains")
			item := snapshot.Items[0]
			assert.Equal(t, firstID, item.ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
			require.NotNil(t, item.ScheduledAt)
			assert.True(t, newer.ScheduledAt.Equal(*item.ScheduledAt), "the returning row takes the newer deadline")
			assert.JSONEq(t, `{"e":2}`, string(storedItem(t, store, firstID).SourcePayload))
			assert.Equal(t, int64(40000), storedServerErrorDelay(t, database), "the merge leaves the live cycle's backoff alone")
		})
	}
}

// A newer usage-limit report whose reset already passed can carry the earlier
// deadline. The returning row then keeps its own deadline and source, and the
// newer row still leaves the queue.
func TestAutoRetryReturnedDispatchKeepsItsOwnLaterDeadline(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	first := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), []byte(`{"e":1}`)).Items[0]
	clock.Set(*first.ScheduledAt)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	newer := otherRetry(t, scheduleRetry(t, store, kind, clock.Now().Add(-2*time.Hour), []byte(`{"e":2}`)), first.ID)
	require.True(t, newer.ScheduledAt.Before(*first.ScheduledAt), "the newer report carries the earlier deadline")

	snapshot, err := store.RequeueBusy(ctx, "agent-1", first.ID, false)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, first.ID, snapshot.Items[0].ID)
	assert.True(t, first.ScheduledAt.Equal(*snapshot.Items[0].ScheduledAt), "the returning row keeps its own later deadline")
	assert.JSONEq(t, `{"e":1}`, string(storedItem(t, store, first.ID).SourcePayload))
}

// A steered retry that the provider drops at the turn end returns to QUEUED
// in the same transaction that ends the turn. A refused return would leave the
// turn running forever.
func TestAutoRetryTurnEndRequeueAbsorbsTheNewerCycleOfItsKind(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	first := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":1}`))
	firstID := first.Items[0].ID
	clock.Set(*first.Items[0].ScheduledAt)
	_, _, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	prepared, _, err := store.PrepareSteer(ctx, "agent-1", firstID, true)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.HoldForEcho(ctx, *prepared)
	require.NoError(t, err)
	newer := otherRetry(t, scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":2}`)), firstID)

	snapshot, changed, err := store.TurnEnded(ctx, "agent-1", true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, snapshot.ActiveTurn, "the turn ends")
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, firstID, snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.True(t, newer.ScheduledAt.Equal(*snapshot.Items[0].ScheduledAt))
	assert.JSONEq(t, `{"e":2}`, string(storedItem(t, store, firstID).SourcePayload))
}

// Two steers of one retry kind that the provider drops together merge into
// one row, and the count states both drops.
func TestAutoRetryTwoReturnedSteersOfOneKindMergeIntoOneRow(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	steerHead := func(id string) {
		t.Helper()
		prepared, _, err := store.PrepareSteer(ctx, "agent-1", id, true)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		_, err = store.HoldForEcho(ctx, *prepared)
		require.NoError(t, err)
	}
	first := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":1}`))
	clock.Set(*first.Items[0].ScheduledAt)
	_, _, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	steerHead(first.Items[0].ID)
	second := otherRetry(t, scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":2}`)), first.Items[0].ID)
	clock.Set(*second.ScheduledAt)
	steerHead(second.ID)

	snapshot, requeued, err := store.RequeueUnechoedSteers(ctx, "agent-1", -1)
	require.NoError(t, err)
	assert.Equal(t, 2, requeued)
	require.Len(t, snapshot.Items, 1)
	item := snapshot.Items[0]
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
	assert.True(t, second.ScheduledAt.Equal(*item.ScheduledAt), "the merged row keeps the later deadline")
	assert.JSONEq(t, `{"e":2}`, string(storedItem(t, store, item.ID).SourcePayload))
}

// A busy refusal of a dispatched retry, with a newer retry of its kind
// scheduled during the dispatch, returns the row to the queue. A refused
// return would leave it DISPATCHING, and the queue would stall until a
// restart.
func TestAutoRetryBusyRefusalAbsorbsTheRetryScheduledDuringTheDispatch(t *testing.T) {
	t.Parallel()
	clock, dispatcher, _, manager, _ := newDeadlineFixture(t)
	release := make(chan struct{})
	dispatcher.mu.Lock()
	dispatcher.dispatchRelease = release
	dispatcher.fail = &DeliveryError{Err: errors.New("a turn already runs"), Outcome: DispatchBusy}
	dispatcher.mu.Unlock()
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT

	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind, ResetAt: clock.Now().Add(-time.Minute)}))
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	retryID := snapshot.Items[0].ID
	eventuallyDispatches(t, dispatcher, []string{retryID})
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind, ResetAt: clock.Now().Add(time.Hour)}))
	close(release)
	waitForDrainToSettle(t, manager, "agent-1")

	snapshot, err = manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, retryID, snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	require.NotNil(t, snapshot.Items[0].ScheduledAt)
	assert.True(t, snapshot.Items[0].ScheduledAt.After(clock.Now()), "the row waits for the newer reset")
	assert.Equal(t, []string{retryID}, dispatchedIDs(dispatcher))
}

// The user's Retry of a failed retry, with a newer retry of its kind queued,
// sends the user's row now and removes the newer one. Only the user's retry
// message is sent, and the live cycle keeps its backoff.
func TestAutoRetryUserRetryAbsorbsTheQueuedRetryOfItsKind(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	failedID := failDueRetry(t, clock, store, `{"e":1}`)
	scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":2}`))

	snapshot, err := store.Retry(ctx, "agent-1", failedID, false)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, failedID, snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.JSONEq(t, `{"e":1}`, string(storedItem(t, store, failedID).SourcePayload), "the user's row keeps its own source")
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, database))
	retried, _, err := store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, failedID, retried.Item.ID)
}

// The user's save of an edit of a failed retry keeps the user's text and
// schedule, and removes the newer retry of its kind.
func TestAutoRetryEditSaveOfAFailedRetryAbsorbsTheQueuedRetryOfItsKind(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	failedID := failDueRetry(t, clock, store, `{"e":1}`)
	scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, []byte(`{"e":2}`))
	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", failedID, "client", false)
	require.NoError(t, err)
	var version uint64
	for _, item := range snapshot.Items {
		if item.ID == failedID {
			version = item.Version
		}
	}
	chosen := clock.Now().Add(2 * time.Hour)

	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         failedID,
		ClientID:        "client",
		ExpectedVersion: version,
		Text:            "Continue, carefully.",
		Attachments:     nil,
		Schedule:        ReplaceSchedule(&chosen),
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	item := snapshot.Items[0]
	assert.Equal(t, failedID, item.ID)
	assert.Equal(t, "Continue, carefully.", item.TextPreview)
	require.NotNil(t, item.ScheduledAt)
	assert.True(t, chosen.Equal(*item.ScheduledAt), "the user's schedule wins")
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, database))
}

// Deleting a server-error retry resets the backoff only when no pending retry
// of the kind remains. A stale failed row beside a live cycle keeps the
// cycle's backoff.
func TestAutoRetryDeleteResetsTheBackoffOnlyWhenNoRetryOfTheKindRemainsPending(t *testing.T) {
	t.Parallel()
	t.Run("beside a pending retry", func(t *testing.T) {
		t.Parallel()
		clock, database, store := newPriorityFixture(t)
		failedID := failDueRetry(t, clock, store, `{"e":1}`)
		pending := otherRetry(t, scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}, nil), failedID)
		require.Equal(t, int64(40000), storedServerErrorDelay(t, database))

		_, err := store.Delete(t.Context(), "agent-1", failedID)
		require.NoError(t, err)
		assert.Equal(t, int64(40000), storedServerErrorDelay(t, database), "the live cycle keeps its backoff")
		_, err = store.Delete(t.Context(), "agent-1", pending.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(10000), storedServerErrorDelay(t, database), "deleting the pending retry ends the cycle")
	})
	t.Run("without a pending retry", func(t *testing.T) {
		t.Parallel()
		clock, database, store := newPriorityFixture(t)
		failedID := failDueRetry(t, clock, store, `{"e":1}`)
		require.Equal(t, int64(20000), storedServerErrorDelay(t, database))

		_, err := store.Delete(t.Context(), "agent-1", failedID)
		require.NoError(t, err)
		assert.Equal(t, int64(10000), storedServerErrorDelay(t, database))
	})
}

// A save that keeps the schedule keeps a deadline that a provider moved while
// the edit was open; a save that replaces it writes the user's value.
func TestAutoRetryEditSaveKeepsAScheduleThatMovedDuringTheEdit(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	snapshot := scheduleRetry(t, store, kind, clock.Now().Add(time.Hour), nil)
	retryID := snapshot.Items[0].ID
	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", retryID, "client", false)
	require.NoError(t, err)
	version := snapshot.Items[0].Version
	moved := scheduleRetry(t, store, kind, clock.Now().Add(3*time.Hour), nil)
	require.Len(t, moved.Items, 1)
	movedAt := *moved.Items[0].ScheduledAt

	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         retryID,
		ClientID:        "client",
		ExpectedVersion: version,
		Text:            snapshot.Items[0].TextPreview,
		Attachments:     nil,
		Schedule:        KeepSchedule(),
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.True(t, movedAt.Equal(*snapshot.Items[0].ScheduledAt), "a save that kept the schedule keeps the provider's new deadline")

	snapshot, _, _, err = store.BeginEdit(ctx, "agent-1", retryID, "client", false)
	require.NoError(t, err)
	chosen := clock.Now().Add(5 * time.Hour)
	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         retryID,
		ClientID:        "client",
		ExpectedVersion: snapshot.Items[0].Version,
		Text:            snapshot.Items[0].TextPreview,
		Attachments:     nil,
		Schedule:        ReplaceSchedule(&chosen),
	})
	require.NoError(t, err)
	assert.True(t, chosen.Equal(*snapshot.Items[0].ScheduledAt))
}

type retryTestAdmission struct {
	held     atomic.Bool
	acquired atomic.Int32
	released atomic.Int32
	refused  bool
}

type retryTestLease struct {
	admission *retryTestAdmission
	once      sync.Once
}

func (admission *retryTestAdmission) Acquire() agent.AutoRetryLease {
	admission.acquired.Add(1)
	if admission.refused {
		return nil
	}
	admission.held.Store(true)
	return &retryTestLease{admission: admission}
}

func (lease *retryTestLease) Release() {
	lease.once.Do(func() {
		lease.admission.held.Store(false)
		lease.admission.released.Add(1)
	})
}

func TestAutoRetryAdmissionReleasesBeforeObserversAndOnEveryExit(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"schedule", "cancel without a row", "store failure", "retired owner", "stopped manager"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			admission := &retryTestAdmission{refused: operation == "retired owner"}
			var manager *Manager
			var publications atomic.Int32
			observer := &reentryQueueObserver{changed: func(Snapshot) {
				publications.Add(1)
				assert.False(t, admission.held.Load(), "observer delivery must release provider admission")
				c := manager.coordinator("agent-1")
				unlocked := c.mu.TryLock()
				assert.True(t, unlocked, "observer delivery must release the coordinator")
				if unlocked {
					c.mu.Unlock()
					_, err := manager.Snapshot(t.Context(), "agent-1")
					assert.NoError(t, err)
				}
			}}
			manager = NewManager(store, &recordingDispatcher{}, observer)
			t.Cleanup(manager.StopAndWait)
			if operation == "store failure" {
				_, err := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_retry BEFORE INSERT ON agent_input_queue_items BEGIN SELECT RAISE(ABORT, 'refuse retry'); END`)
				require.NoError(t, err)
			}
			if operation == "stopped manager" {
				manager.StopAndWait()
			}
			kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
			var err error
			if operation == "cancel without a row" {
				err = manager.CancelAutoRetry(t.Context(), "agent-1", kind, admission)
			} else {
				err = manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind, ResetAt: time.Now().Add(time.Hour), Admission: admission})
			}
			switch operation {
			case "store failure":
				require.ErrorContains(t, err, "refuse retry")
			case "stopped manager":
				require.ErrorIs(t, err, ErrManagerStopped)
			default:
				require.NoError(t, err)
			}
			assert.False(t, admission.held.Load())
			if operation == "stopped manager" {
				assert.Zero(t, admission.acquired.Load())
			} else {
				assert.Equal(t, int32(1), admission.acquired.Load())
			}
			if operation == "retired owner" || operation == "stopped manager" {
				assert.Zero(t, admission.released.Load())
			} else {
				assert.Equal(t, int32(1), admission.released.Load())
			}
			if operation == "schedule" {
				assert.Equal(t, int32(1), publications.Load())
			} else {
				assert.Zero(t, publications.Load())
			}
		})
	}
}
