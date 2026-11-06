package inputqueue

import (
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryPausePreservesProviderTurnsAndQuestionAnswers(t *testing.T) {
	for _, nativeActive := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeActive), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{ID: "held", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "held"})
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			if nativeActive {
				_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
				require.NoError(t, err)
			}
			paused, err := store.RequeueAndPause(t.Context(), "agent-1", "held", assert.AnError,
				leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY)
			require.NoError(t, err)
			assert.True(t, paused.Paused)
			assert.Equal(t, nativeActive, paused.ActiveTurn, "only provisional dispatch ownership is released")
			_, err = store.Enqueue(t.Context(), NewItem{ID: "answer", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red"})
			require.NoError(t, err)
			answer, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			if nativeActive {
				require.NotNil(t, answer, "a question answer can still reach its existing turn")
				assert.Equal(t, "answer", answer.Item.ID)
				assert.True(t, answer.Steering)
			} else {
				assert.Nil(t, answer, "an idle question needs ordinary input readiness")
			}
		})
	}
}

func TestRecoveryResumeKeepsFailedUncertainAndManualState(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{ID: "held", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "new input"})
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, err = store.RequeueAndPause(t.Context(), "agent-1", "held", assert.AnError,
				leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY)
			require.NoError(t, err)
			for index, state := range []leapmuxv1.AgentInputState{leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN} {
				id := fmt.Sprintf("existing-%d", index)
				_, err := store.Enqueue(t.Context(), NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "previous input"})
				require.NoError(t, err)
				_, err = database.ExecContext(t.Context(), "UPDATE agent_input_queue_items SET state = ?, error = ? WHERE id = ?", state, "previous outcome", id)
				require.NoError(t, err)
			}
			if manual {
				_, err := store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			}
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			_, previous, err := store.pauseForInputRecovery(t.Context(), "agent-1")
			require.NoError(t, err)
			after, changed, err := store.finishInputRecovery(t.Context(), "agent-1", previous, true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Equal(t, manual, after.Paused)
			assert.Greater(t, after.Revision, before.Revision)
			require.Len(t, after.Items, 3)
			previousStates := make(map[string]leapmuxv1.AgentInputState)
			for _, item := range before.Items {
				previousStates[item.ID] = item.State
			}
			for _, item := range after.Items {
				if item.ID == "held" {
					assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
					assert.Empty(t, item.Error)
					continue
				}
				assert.Equal(t, "previous outcome", item.Error)
				assert.Equal(t, previousStates[item.ID], item.State)
			}
		})
	}
}

func TestRecoverySnapshotRefreshAdvancesOnlyTheRevision(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	before, err := store.Enqueue(t.Context(), NewItem{ID: "held", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued input"})
	require.NoError(t, err)
	after, err := store.RefreshSnapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before.Revision+1, after.Revision)
	assert.Equal(t, before.Items, after.Items)
	assert.Equal(t, before.PauseReason, after.PauseReason)
	assert.Equal(t, before.ActiveTurn, after.ActiveTurn)
}

func TestRecoveryRefusalRestoresThePreviousPause(t *testing.T) {
	for _, paused := range []struct {
		name   string
		reason leapmuxv1.AgentInputQueuePauseReason
		owner  pauseOwner
	}{
		{name: "unpaused"},
		{name: "recovery", reason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY, owner: pauseOwnerAgentStopped},
		{name: "stopped", reason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, owner: pauseOwnerAgentStopped},
		{name: "manual", reason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, owner: pauseOwnerManual},
		{name: "archive", reason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, owner: pauseOwnerArchive},
		{name: "uncertain delivery", reason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, owner: pauseOwnerDelivery},
	} {
		t.Run(paused.name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{ID: "held", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "held"})
			require.NoError(t, err)
			if paused.owner != pauseOwnerNone {
				_, err := database.ExecContext(t.Context(), "UPDATE agent_input_queue_state SET pause_reason = ?, pause_owner = ? WHERE agent_id = ?", paused.reason, paused.owner, "agent-1")
				require.NoError(t, err)
			}
			_, previous, err := store.pauseForInputRecovery(t.Context(), "agent-1")
			require.NoError(t, err)
			_, _, err = store.finishInputRecovery(t.Context(), "agent-1", previous, false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
			require.NoError(t, err)
			after := readQueuePauseState(t, database)
			assert.Equal(t, paused.reason, after.reason)
			assert.Equal(t, paused.owner, after.owner)
		})
	}
}

func TestRecoveryCompletionPreservesReplacementWorkAndNewPauseAuthority(t *testing.T) {
	for _, newPause := range []string{"none", "crash", "manual", "archive"} {
		t.Run(newPause, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, previous, err := store.pauseForInputRecovery(t.Context(), "agent-1")
			require.NoError(t, err)
			_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			switch newPause {
			case "crash":
				_, err = store.Pause(t.Context(), "agent-1", leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
			case "manual":
				_, err = store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
			case "archive":
				_, _, err = store.PauseForArchive(t.Context(), "agent-1")
			}
			require.NoError(t, err)
			after, _, err := store.finishInputRecovery(t.Context(), "agent-1", previous, true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			require.NoError(t, err)
			assert.Equal(t, newPause != "none", after.Paused)
			assert.Equal(t, newPause == "none" || newPause == "manual", after.ActiveTurn)
			assert.Equal(t, after.ActiveTurn, after.ActiveTurnSteerable)
			if newPause == "crash" {
				assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, after.PauseReason)
			}
		})
	}
}
