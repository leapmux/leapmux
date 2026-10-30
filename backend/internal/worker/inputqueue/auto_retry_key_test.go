package inputqueue

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreKeyedUsageSchedulePersistsItsNativeOwner(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	snapshot, _, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		Key:  "native:A", ResetAt: clock.Now().Add(time.Hour), SourcePayload: []byte(`{"source":"A"}`),
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	item := storedItem(t, store, snapshot.Items[0].ID)
	assert.Equal(t, "native:A", item.AutoRetryKey)
	assert.JSONEq(t, `{"source":"A"}`, string(item.SourcePayload))
}

func TestStoreKeyedUsageSchedulePreservesUnkeyedIntent(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	before := scheduleRetry(t, store, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, clock.Now().Add(time.Hour), []byte(`{"source":"unkeyed"}`))
	require.Len(t, before.Items, 1)
	after, _, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		Key:  "native:A", ResetAt: clock.Now().Add(2 * time.Hour), SourcePayload: []byte(`{"source":"A"}`),
	})
	require.NoError(t, err)
	require.Len(t, after.Items, 1)
	assert.Equal(t, before.Revision, after.Revision)
	assert.Equal(t, before.Items[0].ID, after.Items[0].ID)
	assert.Equal(t, before.Items[0].ScheduledAt, after.Items[0].ScheduledAt)
	item := storedItem(t, store, after.Items[0].ID)
	assert.Empty(t, item.AutoRetryKey)
	assert.JSONEq(t, `{"source":"unkeyed"}`, string(item.SourcePayload))
}

func TestStoreKeyedReturnKeepsTheNewerNativeIntent(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"busy", "prepared", "pause", "echo"} {
		for _, equalDeadline := range []bool{false, true} {
			label := operation + "/past deadline"
			if equalDeadline {
				label = operation + "/equal deadline"
			}
			t.Run(label, func(t *testing.T) {
				t.Parallel()
				clock, database, store := newPriorityFixture(t)
				first := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour)).Items[0]
				clock.Set(*first.ScheduledAt).MustWait(testutil.DeadlineContext(t))
				prepareRetryReturnForTest(t, store, first.ID, operation)
				second := otherRetry(t, scheduleKeyedUsage(t, store, "native:B", clock.Now().Add(-2*time.Hour)), first.ID)
				if equalDeadline {
					_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET scheduled_at = (SELECT scheduled_at FROM agent_input_queue_items WHERE id = ?) WHERE id = ?`, first.ID, second.ID)
					require.NoError(t, err)
				}
				snapshot := returnRetryForTest(t, store, first.ID, operation)
				require.Len(t, snapshot.Items, 1)
				assert.Equal(t, first.ID, snapshot.Items[0].ID)
				assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
				assert.Equal(t, first.ScheduledAt, snapshot.Items[0].ScheduledAt, "the later dispatch deadline is retained")
				item := storedItem(t, store, first.ID)
				assert.Equal(t, "native:B", item.AutoRetryKey)
				assert.JSONEq(t, `{"key":"native:B"}`, string(item.SourcePayload))
				assert.Equal(t, operation == "pause", snapshot.Paused)
				if operation == "pause" {
					assert.False(t, snapshot.ActiveTurn)
					assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snapshot.PauseReason)
				}
			})
		}
	}
}

func prepareRetryReturnForTest(t *testing.T, store *Store, inputID, operation string) {
	t.Helper()
	var prepared *PreparedDispatch
	var err error
	if operation == "echo" {
		_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
		require.NoError(t, err)
		prepared, _, err = store.PrepareSteer(t.Context(), "agent-1", inputID, true)
	} else {
		prepared, _, err = store.PrepareDispatch(t.Context(), "agent-1", false)
	}
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, inputID, prepared.Item.ID)
	if operation == "echo" {
		_, err = store.HoldForEcho(t.Context(), *prepared)
		require.NoError(t, err)
	}
}

func returnRetryForTest(t *testing.T, store *Store, inputID, operation string) Snapshot {
	t.Helper()
	var snapshot Snapshot
	var err error
	switch operation {
	case "busy":
		snapshot, err = store.RequeueBusy(t.Context(), "agent-1", inputID, true)
	case "prepared":
		snapshot, err = store.RequeuePrepared(t.Context(), "agent-1", inputID)
	case "pause":
		snapshot, err = store.RequeueAndPause(t.Context(), "agent-1", inputID, assert.AnError, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
	case "echo":
		var count int
		snapshot, count, err = store.RequeueUnechoedSteers(t.Context(), "agent-1", -1)
		assert.Equal(t, 1, count)
	default:
		t.Fatalf("unknown retry return operation %q", operation)
	}
	require.NoError(t, err)
	return snapshot
}

func TestStoreKeyedUsageScheduleRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	for _, key := range []string{" ", "native\x00A", strings.Repeat("x", maxQueueIdentityByteCount+1)} {
		_, store := newStoreFixture(t)
		_, _, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
			Key:  key, ResetAt: time.Now().Add(time.Hour),
		})
		assert.ErrorIs(t, err, ErrInvalidInput)
	}
}

func scheduleKeyedUsage(t *testing.T, store *Store, key string, resetAt time.Time) Snapshot {
	t.Helper()
	snapshot, _, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		Key:  key, ResetAt: resetAt, SourcePayload: []byte(`{"key":"` + key + `"}`),
	})
	require.NoError(t, err)
	return snapshot
}

func TestStoreKeyedCancellationKeepsOtherOwnersAndBackoff(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	scheduleRetry(t, store, serverErrorRetry, time.Time{}, nil)
	delay := storedServerErrorDelay(t, database)
	first := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour))
	second := scheduleKeyedUsage(t, store, "native:B", clock.Now().Add(2*time.Hour))
	assert.Greater(t, second.Revision, first.Revision)
	for _, key := range []string{"native:A", "native:absent"} {
		_, removed, err := store.cancelUsageRetryWithKey(t.Context(), "agent-1", key)
		require.NoError(t, err)
		assert.Empty(t, removed)
		after, err := store.Snapshot(t.Context(), "agent-1")
		require.NoError(t, err)
		assert.Equal(t, second, after)
	}
	after, removed, err := store.cancelUsageRetryWithKey(t.Context(), "agent-1", "native:B")
	require.NoError(t, err)
	require.Len(t, removed, 1)
	assert.Equal(t, "native:B", removed[0].key)
	assert.Equal(t, second.Revision+1, after.Revision)
	require.Len(t, after.Items, 1)
	assert.Equal(t, serverErrorRetry, after.Items[0].Kind)
	assert.Equal(t, delay, storedServerErrorDelay(t, database))
	for _, key := range []string{"", " ", "bad\x00key", strings.Repeat("x", maxQueueIdentityByteCount+1)} {
		_, _, err := store.cancelUsageRetryWithKey(t.Context(), "agent-1", key)
		assert.ErrorIs(t, err, ErrInvalidInput)
	}
	// An explicit unkeyed retry has independent authority.
	unkeyed := scheduleKeyedUsage(t, store, "", clock.Now().Add(time.Hour))
	_, removed, err = store.cancelUsageRetryWithKey(t.Context(), "agent-1", "native:B")
	require.NoError(t, err)
	assert.Empty(t, removed)
	after, err = store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, unkeyed, after)
}

func TestStoreKeyedCancellationNeverRemovesAttemptedDeliveries(t *testing.T) {
	t.Parallel()
	for _, state := range []leapmuxv1.AgentInputState{
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN,
	} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			clock, database, store := newPriorityFixture(t)
			item := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour)).Items[0]
			_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = ?`, state, item.ID)
			require.NoError(t, err)
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			_, removed, err := store.cancelUsageRetryWithKey(t.Context(), "agent-1", "native:A")
			require.NoError(t, err)
			assert.Empty(t, removed)
			after, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestStoreKeyedReturnPreservesUnkeyedUserIntent(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"busy", "prepared", "pause", "echo"} {
		for _, keyedReturn := range []bool{true, false} {
			label := operation + "/unkeyed return"
			if keyedReturn {
				label = operation + "/keyed return"
			}
			t.Run(label, func(t *testing.T) {
				t.Parallel()
				clock, database, store := newPriorityFixture(t)
				firstKey, peerKey := "", "native:B"
				if keyedReturn {
					firstKey, peerKey = "native:A", ""
				}
				first := scheduleKeyedUsage(t, store, firstKey, clock.Now().Add(time.Hour)).Items[0]
				clock.Set(*first.ScheduledAt).MustWait(testutil.DeadlineContext(t))
				prepareRetryReturnForTest(t, store, first.ID, operation)
				peer := otherRetry(t, scheduleKeyedUsage(t, store, peerKey, clock.Now().Add(2*time.Hour)), first.ID)
				winnerID := first.ID
				if keyedReturn {
					winnerID = peer.ID
				}
				_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET text = 'Continue with the user edits', edit_owner = 'client' WHERE id = ?`, winnerID)
				require.NoError(t, err)
				winnerBefore := storedItem(t, store, winnerID)
				after := returnRetryForTest(t, store, first.ID, operation)
				require.Len(t, after.Items, 1)
				assert.Equal(t, winnerID, after.Items[0].ID)
				winner := storedItem(t, store, winnerID)
				assert.Empty(t, winner.AutoRetryKey)
				assert.Equal(t, winnerBefore.Text, winner.Text)
				assert.Equal(t, winnerBefore.EditOwner, winner.EditOwner)
				assert.Equal(t, winnerBefore.ScheduledAt, winner.ScheduledAt)
				assert.Equal(t, winnerBefore.SourcePayload, winner.SourcePayload)
				if keyedReturn {
					var activeInput string
					require.NoError(t, database.QueryRowContext(t.Context(), `SELECT active_input_id FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&activeInput))
					assert.NotEqual(t, first.ID, activeInput)
				}
			})
		}
	}
}

func TestStoreKeyedSteerBatchKeepsNewestIntent(t *testing.T) {
	t.Parallel()
	for _, withQueuedPeer := range []bool{false, true} {
		label := "without queued peer"
		if withQueuedPeer {
			label = "with newer queued peer"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			var newest SnapshotItem
			for _, key := range []string{"native:A", "native:B", "native:C"} {
				snapshot := scheduleKeyedUsage(t, store, key, clock.Now().Add(time.Hour))
				for _, item := range snapshot.Items {
					if item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED {
						newest = item
					}
				}
				clock.Set(*newest.ScheduledAt).MustWait(testutil.DeadlineContext(t))
				prepared, _, err := store.PrepareSteer(t.Context(), "agent-1", newest.ID, true)
				require.NoError(t, err)
				require.NotNil(t, prepared)
				_, err = store.HoldForEcho(t.Context(), *prepared)
				require.NoError(t, err)
			}
			wantKey := "native:C"
			if withQueuedPeer {
				scheduleKeyedUsage(t, store, "native:D", clock.Now().Add(-time.Hour))
				wantKey = "native:D"
			}
			snapshot, count, err := store.RequeueUnechoedSteers(t.Context(), "agent-1", -1)
			require.NoError(t, err)
			assert.Equal(t, 3, count)
			require.Len(t, snapshot.Items, 1)
			item := storedItem(t, store, snapshot.Items[0].ID)
			assert.Equal(t, wantKey, item.AutoRetryKey)
			assert.JSONEq(t, `{"key":"`+wantKey+`"}`, string(item.SourcePayload))
			assert.Equal(t, newest.ScheduledAt, item.ScheduledAt)
		})
	}
}

func TestStoreKeyedUserEditAndRetryClearNativeOwnership(t *testing.T) {
	t.Parallel()
	for _, edit := range []bool{false, true} {
		clock, database, store := newPriorityFixture(t)
		item := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour)).Items[0]
		if edit {
			snapshot, _, _, err := store.BeginEdit(t.Context(), "agent-1", item.ID, "client", false)
			require.NoError(t, err)
			_, err = store.Update(t.Context(), UpdateRequest{AgentID: "agent-1", InputID: item.ID, ClientID: "client", ExpectedVersion: snapshot.Items[0].Version, Text: "User choice", Schedule: KeepSchedule()})
			require.NoError(t, err)
		} else {
			_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = ?`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, item.ID)
			require.NoError(t, err)
			_, err = store.Retry(t.Context(), "agent-1", item.ID, false)
			require.NoError(t, err)
		}
		assert.Empty(t, storedItem(t, store, item.ID).AutoRetryKey)
		_, removed, err := store.cancelUsageRetryWithKey(t.Context(), "agent-1", "native:A")
		require.NoError(t, err)
		assert.Empty(t, removed)
	}
}

func TestStoreKeyedOwnershipSurvivesDatabaseReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	database, err := workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	require.NoError(t, gendb.New(database).CreateAgent(t.Context(), gendb.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}))
	store := NewStore(database, nil)
	item := scheduleKeyedUsage(t, store, "native:A", time.Now().Add(time.Hour)).Items[0]
	require.NoError(t, database.Close())
	database, err = workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	store = NewStore(database, nil)
	assert.Equal(t, "native:A", storedItem(t, store, item.ID).AutoRetryKey)
	snapshot, removed, err := store.cancelUsageRetryWithKey(context.Background(), "agent-1", "native:A")
	require.NoError(t, err)
	require.Len(t, removed, 1)
	assert.Equal(t, item.ID, removed[0].id)
	assert.Empty(t, snapshot.Items)
}

func TestStoreKeyedSkippedScheduleKeepsServerBackoff(t *testing.T) {
	t.Parallel()
	clock, database, store := newPriorityFixture(t)
	before := scheduleRetry(t, store, serverErrorRetry, time.Time{}, []byte(`{"source":"unkeyed"}`))
	delay := storedServerErrorDelay(t, database)
	after, scheduled, err := store.scheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: serverErrorRetry, Key: "native:A", ResetAt: clock.Now().Add(time.Hour), SourcePayload: []byte(`{"source":"A"}`),
	})
	require.NoError(t, err)
	assert.False(t, scheduled.applied)
	assert.Equal(t, before, after)
	assert.Equal(t, delay, storedServerErrorDelay(t, database))
	assert.JSONEq(t, `{"source":"unkeyed"}`, string(storedItem(t, store, before.Items[0].ID).SourcePayload))
}

func TestStoreKeyedStaleReturnKeepsBothRows(t *testing.T) {
	t.Parallel()
	for _, state := range []leapmuxv1.AgentInputState{
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO,
	} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			clock, database, store := newPriorityFixture(t)
			first := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour)).Items[0]
			_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = ?`, state, first.ID)
			require.NoError(t, err)
			before := scheduleKeyedUsage(t, store, "", clock.Now().Add(2*time.Hour))
			for _, requeue := range []func(context.Context, string, string) (Snapshot, error){
				store.RequeuePrepared,
				func(ctx context.Context, agentID, inputID string) (Snapshot, error) {
					return store.RequeueBusy(ctx, agentID, inputID, true)
				},
				func(ctx context.Context, agentID, inputID string) (Snapshot, error) {
					return store.RequeueAndPause(ctx, agentID, inputID, assert.AnError, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
				},
			} {
				_, err := requeue(t.Context(), "agent-1", first.ID)
				assert.ErrorIs(t, err, ErrConflict)
				after, err := store.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Equal(t, before, after)
			}
		})
	}
}

func TestStoreKeyedScheduleUpdatesTheSameOwner(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	key := strings.Repeat("x", maxQueueIdentityByteCount)
	before := scheduleKeyedUsage(t, store, key, time.Time{})
	after := scheduleKeyedUsage(t, store, key, clock.Now().Add(time.Hour))
	require.Len(t, before.Items, 1)
	require.Len(t, after.Items, 1)
	assert.Equal(t, before.Items[0].ID, after.Items[0].ID)
	assert.Equal(t, before.Revision+1, after.Revision)
	assert.Equal(t, key, storedItem(t, store, after.Items[0].ID).AutoRetryKey)
	assert.True(t, after.Items[0].ScheduledAt.After(*before.Items[0].ScheduledAt))
}
