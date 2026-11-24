package qoder

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func commandFrame(sessionID, commandID, state string) []byte {
	return fmt.Appendf(nil, `{"type":"command_lifecycle","uuid":"event-id","command_uuid":%q,"session_id":%q,"state":%q}`, commandID, sessionID, state)
}

func userEchoFrame(receipt agent.NativeInputReceipt) []byte {
	return fmt.Appendf(nil, `{"type":"user","uuid":%q,"session_id":%q,"parent_tool_use_id":null,"isReplay":true,"message":{"role":"user","content":"Check the release."}}`, receipt.InputID, receipt.SessionID)
}

func TestQoderExactReplyWaitsForItsCommandCompletion(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	receipt, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	_, err = uuid.Parse(receipt.InputID)
	require.NoError(t, err)
	assert.Equal(t, receipt.InputID, receipt.TurnID)
	var sent struct {
		UUID      string `json:"uuid"`
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin.String()), &sent))
	assert.Equal(t, receipt.InputID, sent.UUID)
	assert.Equal(t, receipt.SessionID, sent.SessionID)

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, "queued"))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"An earlier reply."}]}}`))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].NativeTurnID)

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput(qoderToolUseFrame("owned"))
	a.HandleOutput([]byte(`{"type":"control_request","session_id":"other-session","request_id":"foreign","request":{"tool_use_id":"owned"}}`))
	a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"root","request":{"tool_use_id":"owned"}}`))
	controls := sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.Empty(t, controls[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, controls[1].NativeTurnID)

	a.HandleOutput(commandFrame(receipt.SessionID, "queued-command", "queued"))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"The release passed."}]}}`))
	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput(commandFrame(receipt.SessionID, "queued-command", commandCompleted))
	a.HandleOutput(commandFrame("other-session", receipt.InputID, commandCompleted))
	assert.True(t, a.PublishTurnActive().Active)
	assert.Empty(t, turnEndEntries(&sink.Sink))

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))
	assert.False(t, a.PublishTurnActive().Active)
	rows = sink.Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, receipt.TurnID, rows[2].NativeTurnID)
	assert.Equal(t, receipt.TurnID, rows[3].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[3].InputTurnOutcome)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestQoderExactReplyRequiresNativeOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		echo       bool
		invalidate bool
		result     string
		stop       bool
		finish     string
		outcome    leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "missing echo", result: qoderSuccessResult, finish: commandCompleted,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "competing command", echo: true, invalidate: true, result: qoderSuccessResult, finish: commandCompleted,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "native failure", echo: true, result: qoderAbortedResult, finish: commandCancelled,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "native interruption", echo: true, result: qoderAbortedResult, stop: true, finish: commandCancelled,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "process ended before the command completion", echo: true, result: qoderSuccessResult,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGoalAgent(t)
			receipt, err := a.SendInputWithReceipt("Check the release.", nil)
			require.NoError(t, err)
			a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
			if tc.echo {
				a.HandleOutput(userEchoFrame(receipt))
			}
			if tc.invalidate {
				a.HandleOutput(commandFrame(receipt.SessionID, "other-command", commandStarted))
			}
			if tc.stop {
				a.noteInterruptRequested()
			}
			a.HandleOutput([]byte(tc.result))
			if tc.finish == "" {
				a.setTurnActive(false)
			} else {
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, tc.finish))
			}
			end := onlyTurnEnd(t, sink)
			assert.False(t, a.PublishTurnActive().Active)
			if tc.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
				assert.Equal(t, tc.outcome, sink.NativeReplyRetirements()[receipt])
				assert.Empty(t, end.NativeTurnID)
			} else {
				assert.Equal(t, tc.outcome, end.InputTurnOutcome)
				assert.Equal(t, receipt.TurnID, end.NativeTurnID)
				assert.Empty(t, sink.NativeReplyRetirements())
			}
		})
	}
}

func TestQoderQueuedSteersKeepTheirOwnReplies(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newGoalAgent(t)
	root, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(commandFrame(root.SessionID, root.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(root))
	first := agent.SteerRequest{InputID: "first", AttemptID: "first-attempt", Content: "Check the release."}
	second := agent.SteerRequest{InputID: "second", AttemptID: "second-attempt", Content: "Check the release."}
	require.NoError(t, a.SteerInput(first))
	require.NoError(t, a.SteerInput(second))
	lines := strings.Split(strings.TrimSpace(stdin.String()), "\n")
	require.Len(t, lines, 3)
	firstReceipt := steerReceiptFromFrame(t, lines[1], root.SessionID)
	secondReceipt := steerReceiptFromFrame(t, lines[2], root.SessionID)
	assert.NotEqual(t, firstReceipt.InputID, secondReceipt.InputID)
	a.HandleOutput(commandFrame(root.SessionID, firstReceipt.InputID, "queued"))
	a.HandleOutput(commandFrame(root.SessionID, secondReceipt.InputID, "queued"))
	assert.Empty(t, sink.SteerResolutions())

	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput(commandFrame(root.SessionID, root.InputID, commandCompleted))
	assert.Empty(t, sink.SteerResolutions(), "the prior turn does not resolve either steer")
	for _, input := range []struct {
		request agent.SteerRequest
		receipt agent.NativeInputReceipt
	}{
		{first, firstReceipt},
		{second, secondReceipt},
	} {
		a.HandleOutput(commandFrame(root.SessionID, input.receipt.InputID, commandStarted))
		assert.True(t, a.PublishTurnActive().Active)
		a.HandleOutput(userEchoFrame(input.receipt))
		a.HandleOutput(userEchoFrame(input.receipt))
		a.HandleOutput([]byte(qoderSuccessResult))
		a.HandleOutput(commandFrame(root.SessionID, input.receipt.InputID, commandCompleted))
	}
	assert.Equal(t, []agent.SteerResolution{
		{InputID: first.InputID, AttemptID: first.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: firstReceipt},
		{InputID: second.InputID, AttemptID: second.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: secondReceipt},
	}, sink.SteerResolutions())
	ends := turnEndEntries(sink)
	require.Len(t, ends, 3)
	for i, receipt := range []agent.NativeInputReceipt{root, firstReceipt, secondReceipt} {
		assert.Equal(t, receipt.TurnID, ends[i].NativeTurnID)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, ends[i].InputTurnOutcome)
	}
}

func TestQoderUnreadSteerUsesItsNativeDisposition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		state   string
		started bool
		outcome agent.SteerOutcome
	}{
		{name: "discarded in the native queue", state: commandDiscarded, outcome: agent.SteerOutcomeDropped},
		{name: "cancelled in the native queue", state: commandCancelled, outcome: agent.SteerOutcomeDropped},
		{name: "process exit before start", outcome: agent.SteerOutcomeUncertain},
		{name: "cancelled after start without echo", state: commandCancelled, started: true, outcome: agent.SteerOutcomeUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newGoalAgent(t)
			a.setTurnActive(true)
			request := agent.SteerRequest{InputID: "input", AttemptID: "attempt", Content: "Update the release check."}
			require.NoError(t, a.SteerInput(request))
			receipt := steerReceiptFromFrame(t, stdin.String(), a.nativeSessionID())
			if tc.started {
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
			}
			if tc.state == "" {
				a.retireQueuedSteers()
			} else {
				frame := commandFrame(receipt.SessionID, receipt.InputID, tc.state)
				a.HandleOutput(frame)
				a.HandleOutput(frame)
			}
			assert.Equal(t, []agent.SteerResolution{{
				InputID: request.InputID, AttemptID: request.AttemptID, Outcome: tc.outcome,
			}}, sink.SteerResolutions())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

func steerReceiptFromFrame(t *testing.T, raw, sessionID string) agent.NativeInputReceipt {
	t.Helper()
	var frame UserInputMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &frame))
	_, err := uuid.Parse(frame.UUID)
	require.NoError(t, err)
	return agent.NativeInputReceipt{SessionID: sessionID, InputID: frame.UUID, TurnID: frame.UUID}
}
