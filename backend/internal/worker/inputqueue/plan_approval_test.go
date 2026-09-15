package inputqueue

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestApprovedPlanClearCanReplaceTheTurnThatAwaitsItsApproval(t *testing.T) {
	for _, tc := range []struct {
		name string
		// The zero value is UNSPECIFIED, which no answer row may hold, so it
		// doubles as "this case writes no approval row at all".
		approval      leapmuxv1.ControlResponseState
		clear, paused bool
		wantDispatch  bool
	}{
		{"recorded approval", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, true, false, true},
		{"unrecorded approval", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, true, false, false},
		{"pending approval", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING, true, false, false},
		{"no approval", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, true, false, false},
		{"manual pause", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, true, true, false},
		{"ordinary plan execution", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{
				ID: "plan", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
				Text: "Execute the approved plan.", PrepareContext: tc.clear,
			})
			require.NoError(t, err)
			_, _, err = store.setTurnState(t.Context(), "agent-1", true, false)
			require.NoError(t, err)
			if tc.approval != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED {
				_, err = database.ExecContext(t.Context(), `INSERT INTO control_response_answers
                (agent_id, request_id, claim_token, state, input_id, plan_approval_settings, agent_provider)
                VALUES ('agent-1','approval','claim',?,'plan',?,?)`,
					int64(tc.approval), []byte(`{"clearContext":true}`),
					int64(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
				require.NoError(t, err)
			}
			if tc.paused {
				_, err = store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			}
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1")
			require.NoError(t, err)
			if tc.wantDispatch {
				require.NotNil(t, prepared, "the old turn cannot finish until this approval clears its context")
			} else {
				require.Nil(t, prepared)
			}
		})
	}
}
