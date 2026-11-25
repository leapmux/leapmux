package codebuddy

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func codebuddyInputEcho(sessionID, requestID string) []byte {
	return fmt.Appendf(nil, `{"type":"user","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Check the release."}}`, sessionID, requestID)
}

func TestCodebuddyExactInputOwnsOnlyItsRootRowsAndControls(t *testing.T) {
	t.Parallel()
	a, stdin := newCodebuddyContentAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	require.Len(t, receipt.InputID, 32)
	nativeID, err := uuid.Parse(receipt.InputID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), nativeID.Version())
	assert.Equal(t, uuid.RFC4122, nativeID.Variant())
	assert.Equal(t, receipt.InputID, receipt.TurnID)
	var sent UserInputMessage
	require.NoError(t, json.Unmarshal([]byte(oneCodebuddyFrame(t, stdin.String())), &sent))
	require.NotNil(t, sent.Meta)
	assert.Equal(t, receipt.TurnID, sent.Meta.ConversationRequestID)

	a.HandleOutput(codebuddyInputEcho("other-session", receipt.TurnID))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"assistant","session_id":%q,"_requestId":"earlier-input","message":{"content":[{"type":"text","text":"An earlier reply."}]}}`, receipt.SessionID))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].NativeTurnID, "an echo for another session cannot admit the input")

	a.HandleOutput(codebuddyInputEcho(receipt.SessionID, receipt.TurnID))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":"earlier-input","result":"An earlier answer."}`, receipt.SessionID))
	require.True(t, a.PublishTurnActive().Active)
	assert.Len(t, sink.Messages(), 1, "the echo and stale result do not publish rows")

	a.HandleOutput(fmt.Appendf(nil, `{"type":"assistant","session_id":%q,"_requestId":%q,"message":{"content":[{"type":"tool_use","id":"root-tool","name":"AskUserQuestion"}]}}`, receipt.SessionID, receipt.TurnID))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"unowned","request":{"subtype":"can_use_tool","tool_use_id":"unknown-tool","tool_name":"AskUserQuestion"}}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"owned","request":{"subtype":"can_use_tool","tool_use_id":"root-tool","tool_name":"AskUserQuestion"}}`))
	controls := sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.Empty(t, controls[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, controls[1].NativeTurnID)
	assert.Equal(t, receipt.SessionID, controls[1].AgentSessionID)

	a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":%q,"result":"The release passed."}`, receipt.SessionID, receipt.TurnID))
	rows = sink.Messages()
	require.Len(t, rows, 4, "the result recovers the missing assistant text")
	assert.Equal(t, receipt.TurnID, rows[2].NativeTurnID)
	assert.Contains(t, string(rows[2].Content), "The release passed.")
	assert.Equal(t, receipt.TurnID, rows[3].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[3].InputTurnOutcome)
	assert.False(t, a.PublishTurnActive().Active)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestCodebuddyExactInputNeedsNativeOutcomeEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		outcome  string
		skipEcho bool
		want     leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "missing echo", skipEcho: true, outcome: `"subtype":"success","is_error":false`,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "missing error flag", outcome: `"subtype":"success"`,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "native failure", outcome: `"subtype":"error_during_execution","is_error":true`,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "native interruption", outcome: `"subtype":"success","is_error":false,"terminal_reason":"aborted_streaming"`,
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "process exit",
			want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			receipt, err := a.SendInput("Check the release.", nil)
			require.NoError(t, err)
			if !tc.skipEcho {
				a.HandleOutput(codebuddyInputEcho(receipt.SessionID, receipt.TurnID))
			}
			if tc.outcome == "" {
				a.cleanupAfterProcessExit()
			} else {
				a.HandleOutput(fmt.Appendf(nil, `{"type":"result","session_id":%q,"_requestId":%q,%s}`, receipt.SessionID, receipt.TurnID, tc.outcome))
			}
			assert.False(t, a.PublishTurnActive().Active)
			if tc.want == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
				assert.Equal(t, tc.want, sink.NativeReplyRetirements()[receipt])
				return
			}
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, receipt.TurnID, rows[0].NativeTurnID)
			assert.Equal(t, tc.want, rows[0].InputTurnOutcome)
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

func TestCodebuddySteerEchoBindsExactReplyAndDropsUnreadOnTurnEnd(t *testing.T) {
	t.Parallel()
	var capturedSteerBody string
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"assistant","session_id":%q,"_requestId":%q,"message":{"content":[{"type":"text","text":"Before steer."}]}}`, lead.SessionID, lead.TurnID))

	reporter, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok, "CodeBuddy must report steer echo resolution")
	assert.True(t, reporter.SteerRowAtEcho())

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Also check the changelog.",
	}))
	capturedSteerBody = peer.String()
	assert.Contains(t, capturedSteerBody, `"expected_request_id":"`+lead.TurnID+`"`)
	assert.Empty(t, sink.SteerResolutions(), "the steer stays pending until CodeBuddy echoes its admission")

	a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-native-1","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Also check the changelog."}}`, lead.SessionID, lead.TurnID))
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: lead.SessionID,
			InputID:   "steer-native-1",
			TurnID:    lead.TurnID,
		},
	}, sink.SteerResolutions()[0])

	// Queue a second steer that CodeBuddy never drains before the turn ends.
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Content:   "Unread steer before end.",
	}))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"assistant","session_id":%q,"_requestId":%q,"message":{"content":[{"type":"text","text":"After steer."}]}}`, lead.SessionID, lead.TurnID))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":%q,"result":"After steer."}`, lead.SessionID, lead.TurnID))

	require.Len(t, sink.SteerResolutions(), 2)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[1])
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestCodebuddySteerProcessExitRetiresEchoedAndMarksUnreadUncertain(t *testing.T) {
	t.Parallel()
	a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Echoed steer.",
	}))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-native-1","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Echoed steer."}}`, lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Content:   "Unread steer.",
	}))

	a.cleanupAfterProcessExit()

	require.Len(t, sink.SteerResolutions(), 2)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Outcome:   agent.SteerOutcomeUncertain,
	}, sink.SteerResolutions()[1])
	retirements := sink.NativeReplyRetirements()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[agent.NativeInputReceipt{
		SessionID: lead.SessionID,
		InputID:   "steer-native-1",
		TurnID:    lead.TurnID,
	}])
}

func TestCodebuddyEarlySteerEchoCommitsWhenControlResponseAccepts(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		// Emit the durable steer echo before the control_response returns.
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-1","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Early steer."}}`, lead.SessionID, lead.TurnID))
		return codebuddyControlAnswer(codebuddySteered)(requestID)
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	var err error
	lead, err = a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-early",
		AttemptID: "attempt-early",
		Content:   "Early steer.",
	}))
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-early",
		AttemptID: "attempt-early",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: lead.SessionID,
			InputID:   "steer-early-1",
			TurnID:    lead.TurnID,
		},
	}, sink.SteerResolutions()[0])
}
