package letta

import (
	"context"
	"encoding/json"
	"errors"
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

// Permission frames from a subagent or a foreign runtime must not inherit the
// main input turn ID, while a main-runtime tool call inherits its owning run's
// turn ID.
func TestLettaPermissionTurnIDScopesByRuntimeAndSubagent(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	fake, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
	a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
	receipt, err := a.SendInput("Run a command.", nil)
	require.NoError(t, err)
	command := fake.nextCommand(t)
	replyFrame(t, a, map[string]any{"type": "input_accepted", "request_id": command["request_id"], "accepted": true})
	replyFrame(t, a, map[string]any{
		"type": "update_loop_status",
		"loop_status": lettaLoopStatus{
			Status:                  "WAITING_ON_APPROVAL",
			ActiveRunIDs:            []string{"owned-run"},
			ClientMessageIDsByRunID: map[string][]string{"owned-run": {receipt.InputID}},
		},
	})

	// Subagent permission request gets no native turn ID.
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "subagent-perm", "subagent_id": "sub-1",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "sub-tool", "input": map[string]string{"command": "ls"}},
	})
	// Foreign runtime permission request gets no native turn ID.
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "foreign-perm",
		"runtime": runtimeScope{AgentID: a.runtime().AgentID, ConversationID: "other-conv"},
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "foreign-tool", "input": map[string]string{"command": "ls"}},
	})
	// Missing tool_call_id gets no native turn ID even when request_id is set.
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "no-call-perm",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "", "input": map[string]string{"command": "ls"}},
	})
	// Missing runtime (scope == nil) gets no native turn ID.
	noRuntimeFrame, err := json.Marshal(map[string]any{
		"type": "control_request", "request_id": "no-runtime-perm",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "no-runtime-tool", "input": map[string]string{"command": "ls"}},
	})
	require.NoError(t, err)
	a.HandleOutput(noRuntimeFrame)
	// Open tool from a foreign run overrides the owned approvalRunID.
	a.Mu.Lock()
	a.tools = map[string]*lettaTool{
		lettaToolKey("", "foreign-run-tool"): {id: "foreign-run-tool", runID: "foreign-run"},
		lettaToolKey("", "owned-run-tool"):   {id: "owned-run-tool", runID: "owned-run"},
	}
	a.Mu.Unlock()
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "foreign-run-perm",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "foreign-run-tool", "input": map[string]string{"command": "ls"}},
	})
	// Main runtime permission request inherits the turn ID via approvalRunID.
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "main-perm",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "main-tool", "input": map[string]string{"command": "ls"}},
	})
	// Open tool from an owned run inherits the turn ID even when approvalRunID is cleared.
	replyFrame(t, a, map[string]any{
		"type": "update_loop_status",
		"loop_status": lettaLoopStatus{
			Status:                  "PROCESSING_API_RESPONSE",
			ActiveRunIDs:            []string{"owned-run"},
			ClientMessageIDsByRunID: map[string][]string{"owned-run": {receipt.InputID}},
		},
	})
	replyFrame(t, a, map[string]any{
		"type": "control_request", "request_id": "owned-tool-perm",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_call_id": "owned-run-tool", "input": map[string]string{"command": "ls"}},
	})

	controls := sink.PublishedControls()
	require.Len(t, controls, 7)
	assert.Empty(t, controls[0].NativeTurnID, "subagent permission must not inherit main turn ID")
	assert.Empty(t, controls[1].NativeTurnID, "foreign runtime permission must not inherit main turn ID")
	assert.Empty(t, controls[2].NativeTurnID, "permission without tool_call_id must not inherit main turn ID")
	assert.Empty(t, controls[3].NativeTurnID, "permission without runtime must not inherit main turn ID")
	assert.Empty(t, controls[4].NativeTurnID, "foreign-run tool must override owned approvalRunID")
	assert.Equal(t, receipt.TurnID, controls[5].NativeTurnID)
	assert.Equal(t, receipt.TurnID, controls[6].NativeTurnID)
}

func TestLettaSendInputRefusesEmptyRuntimeWithoutMutatingTurnState(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	_, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)

	_, err := a.SendInput("Too early.", nil)
	require.Error(t, err)
	assert.False(t, a.tracksInput())
	assert.False(t, a.PublishTurnActive().Active)
}

func TestLettaSendInputClassifiesWriteFailures(t *testing.T) {
	t.Parallel()

	t.Run("websocket write failure reports uncertain without retiring unreturned receipt", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		_, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
		a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
		a.Mu.Lock()
		a.cancelRuntime = nil
		ws := a.ws
		a.Mu.Unlock()
		ws.close()

		receipt, err := a.SendInput("Check the release.", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.False(t, a.tracksInput())
		assert.False(t, a.PublishTurnActive().Active)
		assert.Empty(t, sink.NativeReplyRetirements())
	})

	t.Run("manual compaction websocket write failure reports uncertain and rolls back", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		_, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
		a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
		a.Mu.Lock()
		a.cancelRuntime = nil
		ws := a.ws
		a.Mu.Unlock()
		ws.close()

		receipt, err := a.SendInput("/compact", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.False(t, a.PublishTurnActive().Active)
		a.Mu.Lock()
		assert.Nil(t, a.manualCompaction)
		a.Mu.Unlock()
	})

	t.Run("missing socket remains a definite error", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		_, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
		a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
		a.Mu.Lock()
		a.ws = nil
		a.Mu.Unlock()

		receipt, err := a.SendInput("Check the release.", nil)
		require.ErrorIs(t, err, errAgentStopped)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.False(t, a.tracksInput())
		assert.False(t, a.PublishTurnActive().Active)
		assert.Empty(t, sink.NativeReplyRetirements())
	})

	t.Run("cancelled context before write remains a definite error", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		fake, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)
		a.HandleOutput([]byte(lettaLiveRuntimeStartResponse))
		a.cancelRuntime()

		receipt, err := a.SendInput("Check the release.", nil)
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.False(t, a.tracksInput())
		assert.False(t, a.PublishTurnActive().Active)
		assert.Empty(t, sink.NativeReplyRetirements())
		select {
		case cmd := <-fake.commands:
			t.Fatalf("cancelled context wrote a command to the socket: %v", cmd)
		default:
		}
	})

	t.Run("sendCommandContext rejects nil context and unencodable payload without wsWriteError", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		_, a := newFakeAppServerWithServices(t, agent.NewProviderServices(sink), nil)

		var writeErr wsWriteError
		var nilCtx context.Context
		err := a.sendCommandContext(nilCtx, map[string]any{"type": "input"})
		require.ErrorIs(t, err, errAgentStopped)
		assert.False(t, errors.As(err, &writeErr))

		err = a.sendCommandContext(t.Context(), make(chan int))
		require.Error(t, err)
		assert.False(t, errors.As(err, &writeErr))
	})
}
