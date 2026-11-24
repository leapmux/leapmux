package inputqueue

import (
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentifiedSteerEchoCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	f := newEchoQueueFixture(t, true)
	f.enqueue(t, "steer")
	f.dispatcher.steerFail = errors.New("the acknowledgement was lost after the native echo")
	receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "native-input", TurnID: "receiving-turn"}
	f.dispatcher.onSteer = func(item DispatchItem) {
		require.NotEmpty(t, item.SteerAttemptID)
		report := agent.SteerResolution{
			InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: receipt,
		}
		matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.True(t, matched)
		matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.False(t, matched, "a repeated echo writes no second row")
	}
	snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	require.Len(t, f.observer.accepted, 1)
	assert.Equal(t, "steer", f.observer.accepted[0].ID)
	var session, nativeID, turn string
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `
		SELECT m.agent_session_id, m.native_input_id, binding.native_turn_id
		FROM messages m JOIN native_agent_inputs binding USING (agent_id, agent_session_id, native_input_id)
		WHERE m.id = 'steer'`).Scan(&session, &nativeID, &turn))
	assert.Equal(t, receipt, agent.NativeInputReceipt{SessionID: session, InputID: nativeID, TurnID: turn})
}

func TestIdentifiedSteerDropCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	f := newEchoQueueFixture(t, true)
	f.enqueue(t, "steer")
	f.dispatcher.onSteer = func(item DispatchItem) {
		report := agent.SteerResolution{
			InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeDropped,
		}
		matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.True(t, matched)
		matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.False(t, matched)
	}
	snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "steer", snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	assert.Empty(t, f.observer.accepted)
}

func TestIdentifiedSteerUncertaintyCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		paused bool
		reason leapmuxv1.AgentInputQueuePauseReason
	}{
		{"open queue", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN},
		{"manual pause", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoQueueFixture(t, true)
			_, err := f.manager.SetPaused(t.Context(), "agent-1", tc.paused)
			require.NoError(t, err)
			f.enqueue(t, "steer")
			f.enqueue(t, "next")
			f.dispatcher.onSteer = func(item DispatchItem) {
				report := agent.SteerResolution{
					InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeUncertain,
				}
				matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
				require.NoError(t, err)
				assert.True(t, matched)
				matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
				require.NoError(t, err)
				assert.False(t, matched)
			}
			snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
			require.NoError(t, err)
			assert.Equal(t, tc.reason, snapshot.PauseReason)
			assert.False(t, snapshot.ActiveTurn)
			require.Len(t, snapshot.Items, 2)
			assert.Equal(t, "steer", snapshot.Items[0].ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
			assert.Equal(t, "next", snapshot.Items[1].ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[1].State)
			f.observer.mu.Lock()
			defer f.observer.mu.Unlock()
			assert.Empty(t, f.observer.accepted)
		})
	}
}

func TestIdentifiedSteerDropPreservesInputAndRejectsPreviousAttempt(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	holdEchoSteer(t, store, "older")
	first := prepareEchoSteer(t, store, "steer")
	_, err := store.HoldForEcho(ctx, first)
	require.NoError(t, err)
	drop := agent.SteerResolution{
		InputID: first.Item.ID, AttemptID: first.Item.SteerAttemptID, Outcome: agent.SteerOutcomeDropped,
	}
	result, err := store.resolveSteer(ctx, "agent-1", drop, "", "")
	require.NoError(t, err)
	require.True(t, result.matched)
	require.Len(t, result.snapshot.Items, 2)
	assert.Equal(t, "older", result.snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, result.snapshot.Items[0].State)
	assert.Equal(t, first.Item.ID, result.snapshot.Items[1].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, result.snapshot.Items[1].State)
	next, _, err := store.PrepareSteer(ctx, "agent-1", "steer", true)
	require.NoError(t, err)
	require.NotEqual(t, first.Item.SteerAttemptID, next.Item.SteerAttemptID)
	_, err = store.HoldForEcho(ctx, *next)
	require.NoError(t, err)
	echo := agent.SteerResolution{
		InputID: first.Item.ID, AttemptID: first.Item.SteerAttemptID, Outcome: agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "second-native-input", TurnID: "second-turn"},
	}
	for _, stale := range []agent.SteerResolution{drop, echo} {
		result, err = store.resolveSteer(ctx, "agent-1", stale, "", "")
		require.NoError(t, err)
		assert.False(t, result.matched)
		assert.Empty(t, result.accepted)
		snapshot, err := store.Snapshot(ctx, "agent-1")
		require.NoError(t, err)
		require.Len(t, snapshot.Items, 2)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, snapshot.Items[1].State)
	}
	echo.AttemptID = next.Item.SteerAttemptID
	result, err = store.resolveSteer(ctx, "agent-1", echo, "", "")
	require.NoError(t, err)
	require.True(t, result.matched)
	assert.Equal(t, first.Item.ID, result.accepted.ID)
	require.Len(t, result.snapshot.Items, 1)
	assert.Equal(t, "older", result.snapshot.Items[0].ID, "the echo resolves its own item")
}
