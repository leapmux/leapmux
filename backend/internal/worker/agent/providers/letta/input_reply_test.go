package letta

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func replyFrame(t *testing.T, a *Agent, frame map[string]any) {
	t.Helper()
	if _, present := frame["runtime"]; !present {
		frame["runtime"] = a.runtime()
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	a.HandleOutput(raw)
}

func TestLettaExactReplyFollowsItsMappedRuns(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	fake, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	command := fake.nextCommand(t)
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, receipt.InputID, sentClientMessageID(t, payload))
	assert.Equal(t, a.runtime().ConversationID, receipt.SessionID)
	replyFrame(t, a, map[string]any{"type": "input_accepted", "request_id": command["request_id"], "accepted": true})
	setRuns := func(runID, status string) {
		replyFrame(t, a, map[string]any{
			"type": "update_loop_status",
			"loop_status": lettaLoopStatus{
				Status:                  status,
				ActiveRunIDs:            []string{runID},
				ClientMessageIDsByRunID: map[string][]string{runID: {receipt.InputID}, "earlier-run": {"earlier-input"}},
			},
		})
	}
	setRuns("tool-run", "WAITING_ON_APPROVAL")
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "permission-id",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "tool-id", "input": map[string]string{"command": "echo status"}},
	})
	controls := sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Equal(t, receipt.TurnID, controls[0].NativeTurnID)

	setRuns("final-run", "PROCESSING_API_RESPONSE")
	replyFrame(t, a, map[string]any{
		"type":  "stream_delta",
		"delta": map[string]any{"message_type": "assistant_message", "run_id": "final-run", "content": "The release passed."},
	})
	replyFrame(t, a, map[string]any{
		"type": "turn_finished", "turn_id": "foreign-end", "run_id": "final-run", "stop_reason": "end_turn",
		"runtime": runtimeScope{AgentID: a.runtime().AgentID, ConversationID: "other-conversation"},
	})
	assert.Empty(t, sink.Messages())
	replyFrame(t, a, map[string]any{
		"type": "turn_finished", "turn_id": "native-end", "run_id": "final-run", "stop_reason": "end_turn",
	})
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), "The release passed.")
	assert.Equal(t, receipt.TurnID, rows[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, rows[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[1].InputTurnOutcome)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestLettaExactReplyRejectsMissingOrMixedEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		otherRun  bool
		sharedRun bool
		finish    string
		exit      bool
	}{
		{name: "missing final run"},
		{name: "foreign final run", finish: "foreign-run"},
		{name: "mixed text fragments", otherRun: true, finish: "owned-run"},
		{name: "shared run", sharedRun: true, finish: "owned-run"},
		{name: "process exit", exit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a, sink := newLettaCompactionTestAgent(t)
			receipt, err := a.SendInput("Check the release.", nil)
			require.NoError(t, err)
			command := fake.nextCommand(t)
			replyFrame(t, a, map[string]any{"type": "input_accepted", "request_id": command["request_id"], "accepted": true})
			clientIDs := []string{receipt.InputID}
			if tc.sharedRun {
				clientIDs = append(clientIDs, "another-input")
			}
			replyFrame(t, a, map[string]any{
				"type": "update_loop_status",
				"loop_status": lettaLoopStatus{
					Status: "PROCESSING_API_RESPONSE", ActiveRunIDs: []string{"owned-run"},
					ClientMessageIDsByRunID: map[string][]string{"owned-run": clientIDs},
				},
			})
			replyFrame(t, a, map[string]any{
				"type": "stream_delta", "delta": map[string]any{"message_type": "assistant_message", "run_id": "owned-run", "content": "The native reply."},
			})
			if tc.otherRun {
				replyFrame(t, a, map[string]any{
					"type": "stream_delta", "delta": map[string]any{"message_type": "assistant_message", "run_id": "foreign-run", "content": "Foreign text."},
				})
			}
			if tc.exit {
				a.finishOutputAfterExit(agent.MessageCompletionError)
			} else {
				replyFrame(t, a, map[string]any{
					"type": "turn_finished", "turn_id": "native-end", "run_id": tc.finish, "stop_reason": "end_turn",
				})
			}
			rows := sink.Messages()
			require.NotEmpty(t, rows)
			assert.Contains(t, string(rows[0].Content), "The native reply.")
			if tc.otherRun || tc.sharedRun {
				assert.Empty(t, rows[0].NativeTurnID)
			}
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		})
	}
}

func TestLettaInputRejectionRequiresTheMatchingRequest(t *testing.T) {
	t.Parallel()
	fake, a, sink := newLettaCompactionTestAgent(t)
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	command := fake.nextCommand(t)
	replyFrame(t, a, map[string]any{"type": "input_accepted", "request_id": "earlier-request", "accepted": false})
	assert.True(t, a.PublishTurnActive().Active)
	assert.Empty(t, sink.NativeReplyRetirements())

	replyFrame(t, a, map[string]any{"type": "input_accepted", "request_id": command["request_id"], "accepted": false})
	assert.False(t, a.PublishTurnActive().Active)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink.NativeReplyRetirements()[receipt])
}

func TestLettaSendInputForSessionTracksReceipt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	fake, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	receipt, err := a.SendInputForSession(a.runtime().ConversationID, "Check the session.", nil)
	require.NoError(t, err)
	command := fake.nextCommand(t)
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, receipt.InputID, sentClientMessageID(t, payload))
	assert.Equal(t, a.runtime().ConversationID, receipt.SessionID)
	assert.True(t, a.tracksInput())
}
