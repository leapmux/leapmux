package codebuddy

import (
	"encoding/json"
	"errors"
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
	steerReceipt := agent.NativeInputReceipt{
		SessionID: lead.SessionID,
		InputID:   "steer-early-1",
		TurnID:    lead.TurnID,
	}
	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-early",
		AttemptID: "attempt-early",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt:   steerReceipt,
	}, sink.SteerResolutions()[0])

	// Retiring the turn later must also retire the early steer's native receipt.
	a.setTurnActive(false)
	retirements := sink.NativeReplyRetirements()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[steerReceipt])
}

func TestCodebuddyLateAcceptedSteerAfterTurnEndDoesNotPoisonNextTurn(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		// End the output-started turn (which has no inputReply / turnID) before
		// the control_response accepts the steer.
		a.setTurnActive(false)
		return codebuddyControlAnswer(codebuddySteered)(requestID)
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false
	a.setTurnActive(true)

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-stale",
		AttemptID: "attempt-stale",
		Content:   "Late accepted steer.",
	}))
	require.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-stale",
		AttemptID: "attempt-stale",
		Outcome:   agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions(), "a steer accepted after its turn ended must resolve uncertain instead of remaining pending")

	// Start a later turn and verify its echoes are not stolen by the ended turn's steer.
	nextLead, err := a.SendInput("Next turn prompt.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(nextLead.SessionID, nextLead.TurnID))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"next-turn-echo","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Follow up."}}`, nextLead.SessionID, nextLead.TurnID))
	assert.Len(t, sink.SteerResolutions(), 1, "a later turn echo must not resolve a steer from an ended turn")
}

func TestCodebuddyLateAcceptedSteerWithTurnIDAfterTurnEndResolvesUncertain(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.setTurnActive(false)
		return codebuddyControlAnswer(codebuddySteered)(requestID)
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-late-id",
		AttemptID: "attempt-late-id",
		Content:   "Late steer with turn ID.",
	}))
	assert.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-late-id",
		AttemptID: "attempt-late-id",
		Outcome:   agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[lead])
}

func TestCodebuddyEarlySteerAcceptedAfterTurnRetirementRetiresItsReceipt(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-retired","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Early steer before exit."}}`, lead.SessionID, lead.TurnID))
		a.setTurnActive(false)
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
		InputID:   "queue-steer-early-retired",
		AttemptID: "attempt-early-retired",
		Content:   "Early steer before exit.",
	}))
	steerReceipt := agent.NativeInputReceipt{
		SessionID: lead.SessionID,
		InputID:   "steer-early-retired",
		TurnID:    lead.TurnID,
	}
	require.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-early-retired",
		AttemptID: "attempt-early-retired",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt:   steerReceipt,
	}}, sink.SteerResolutions())
	retirements := sink.NativeReplyRetirements()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[steerReceipt],
		"an early steer echoed before turn retirement must retire its native receipt when accepted later")
}

func TestCodebuddyEarlySteerAcceptedAfterProvedResultDoesNotRetireUnproved(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-proved","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Early steer before result."}}`, lead.SessionID, lead.TurnID))
		a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":%q,"result":"Done."}`, lead.SessionID, lead.TurnID))
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
		InputID:   "queue-steer-early-proved",
		AttemptID: "attempt-early-proved",
		Content:   "Early steer before result.",
	}))
	require.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-early-proved",
		AttemptID: "attempt-early-proved",
		Outcome:   agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{
			SessionID: lead.SessionID,
			InputID:   "steer-early-proved",
			TurnID:    lead.TurnID,
		},
	}}, sink.SteerResolutions())
	assert.Empty(t, sink.NativeReplyRetirements(), "a proved result already settled the turn, so the early steer receipt stays proved")
}

func TestCodebuddyEarlySteerRejectedCancelsReservationWithoutResolution(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-rejected","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Rejected steer."}}`, lead.SessionID, lead.TurnID))
		return codebuddyControlAnswer(`{"session_id":"session-1","steered":false,"reason":"stale"}`)(requestID)
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	var err error
	lead, err = a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	err = agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-early-rejected",
		AttemptID: "attempt-early-rejected",
		Content:   "Rejected steer.",
	})
	assert.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.Empty(t, sink.SteerResolutions(), "a rejected early steer must emit no resolution")
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestCodebuddyLateAcceptedSteerAfterResultResolvesUncertain(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":%q,"result":"Done."}`, lead.SessionID, lead.TurnID))
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
		InputID:   "queue-steer-after-result",
		AttemptID: "attempt-after-result",
		Content:   "Accepted after result.",
	}))
	assert.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-after-result",
		AttemptID: "attempt-after-result",
		Outcome:   agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestCodebuddyConsumeInputEchoEdgeCases(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	lead, err := a.SendInput("Run the tool.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))
	assert.Empty(t, sink.Messages(), "lead replay echo must not persist a transcript row")

	// Duplicate or stray root replay echo after admission stays suppressed.
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))
	assert.Empty(t, sink.Messages(), "a stray root replay echo after admission must stay suppressed")

	// Idle agent with no turn suppresses a stray root replay echo without persisting.
	idleSink := &agenttest.Sink{}
	idleAgent := newOfflineAgent(t, idleSink)
	assert.True(t, idleAgent.consumeInputEcho(codebuddyInputEcho("session-1", "req-idle")))
	assert.Empty(t, idleSink.Messages())

	// Empty or null parent_tool_use_id is treated as a root replay echo.
	assert.True(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":true,"parent_tool_use_id":"","session_id":"session-1"}`)))
	assert.True(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":true,"parent_tool_use_id":null,"session_id":"session-1"}`)))

	// Non-root or non-string parent_tool_use_id is not consumed as a root input echo.
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":true,"parent_tool_use_id":"child-1","session_id":"session-1"}`)))
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":true,"parent_tool_use_id":123,"session_id":"session-1"}`)))
	// Explicit isReplay:false, non-bool isReplay, and malformed JSON are not consumed as echoes.
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":false,"session_id":"session-1"}`)))
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":"true","session_id":"session-1"}`)))
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":1,"session_id":"session-1"}`)))
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","isReplay":`)))
	// Uppercase ISREPLAY is rejected case-sensitively.
	assert.False(t, a.consumeInputEcho([]byte(`{"type":"user","ISREPLAY":true,"session_id":"session-1"}`)))

	// A tool result whose escaped text mentions "isReplay" is not consumed and persists normally.
	toolResult := []byte(`{"type":"user","session_id":"session-1","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-1","content":"found \"isReplay\" in source"}]}}`)
	a.HandleOutput(toolResult)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(toolResult), string(sink.Messages()[0].Content))
}

func TestCodebuddySteerEchoWithoutUUIDGeneratesFallbackReceiptID(t *testing.T) {
	t.Parallel()
	a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-no-uuid",
		AttemptID: "attempt-no-uuid",
		Content:   "Follow up without uuid.",
	}))
	a.HandleOutput(fmt.Appendf(nil, `{"type":"user","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Follow up without uuid."}}`, lead.SessionID, lead.TurnID))

	resolutions := sink.SteerResolutions()
	require.Len(t, resolutions, 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, resolutions[0].Outcome)
	assert.Equal(t, lead.SessionID, resolutions[0].Receipt.SessionID)
	assert.Equal(t, lead.TurnID, resolutions[0].Receipt.TurnID)
	assert.Len(t, resolutions[0].Receipt.InputID, 32, "an echo without uuid must generate a fallback native input ID")
}

func TestCodebuddyEarlySteerErrorControlResponseCancelsReservation(t *testing.T) {
	t.Parallel()
	var (
		a    *Agent
		lead agent.NativeInputReceipt
	)
	a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-err","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Error steer."}}`, lead.SessionID, lead.TurnID))
		return codebuddyControlFailure("Session mismatch")(requestID)
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false
	// Also verify sendControlAndWaitObserved lazily initializes a nil pendingControl map.
	a.pendingControl = nil

	var err error
	lead, err = a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	err = agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-early-err",
		AttemptID: "attempt-early-err",
		Content:   "Error steer.",
	})
	require.ErrorContains(t, err, "Session mismatch")
	assert.Empty(t, sink.SteerResolutions(), "a failed control_response must cancel the early steer reservation")
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestCodebuddyMalformedResultMarksPendingSteerUncertainAndRetiresReply(t *testing.T) {
	t.Parallel()
	a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.active = false

	lead, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID:   "queue-steer-malformed",
		AttemptID: "attempt-malformed",
		Content:   "Pending steer.",
	}))
	a.HandleOutput([]byte(`{"type":"result","is_error":"not-a-bool"}`))

	assert.Equal(t, []agent.SteerResolution{{
		InputID:   "queue-steer-malformed",
		AttemptID: "attempt-malformed",
		Outcome:   agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions(), "a malformed result must resolve pending steers as uncertain, not dropped")
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[lead])
	assert.False(t, a.PublishTurnActive().Active)
}

type unqueuedSink struct {
	agent.ProviderServices
}

func (unqueuedSink) OutputQueue() *agent.OutputQueue { return nil }

func TestCodebuddyFailedPersistRetiresReplyAndSkipsUnretainedEarlySteerRetirement(t *testing.T) {
	t.Parallel()

	t.Run("PersistTurnEnd failure retires lead reply and echoed steer receipt", func(t *testing.T) {
		t.Parallel()
		a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
		sink := &agenttest.Sink{}
		a.sink = agent.NewProviderServices(sink)
		a.active = false

		lead, err := a.SendInput("Check the release.", nil)
		require.NoError(t, err)
		a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

		require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
			InputID:   "queue-steer-persist-fail",
			AttemptID: "attempt-persist-fail",
			Content:   "Echoed steer.",
		}))
		a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-persist-fail","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Echoed steer."}}`, lead.SessionID, lead.TurnID))

		sink.PersistErr = errors.New("turn end write failed")
		a.HandleOutput(fmt.Appendf(nil, `{"type":"result","subtype":"success","is_error":false,"session_id":%q,"_requestId":%q,"result":"Done."}`, lead.SessionID, lead.TurnID))

		steerReceipt := agent.NativeInputReceipt{
			SessionID: lead.SessionID,
			InputID:   "steer-persist-fail",
			TurnID:    lead.TurnID,
		}
		retirements := sink.NativeReplyRetirements()
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[steerReceipt])
	})

	t.Run("early steer accepted after turn retirement does not retire receipt when finish write fails", func(t *testing.T) {
		t.Parallel()
		var (
			a    *Agent
			sink *agenttest.Sink
			lead agent.NativeInputReceipt
		)
		a, _ = newCodebuddySteerAgent(t, nil, func(requestID string) string {
			a.HandleOutput(fmt.Appendf(nil, `{"type":"user","uuid":"steer-early-unretained","session_id":%q,"_requestId":%q,"isReplay":true,"message":{"role":"user","content":"Early steer."}}`, lead.SessionID, lead.TurnID))
			a.setTurnActive(false)
			sink.PersistErr = errors.New("steer resolution write failed")
			return codebuddyControlAnswer(codebuddySteered)(requestID)
		})
		sink = &agenttest.Sink{}
		a.sink = unqueuedSink{ProviderServices: agent.NewProviderServices(sink)}
		a.active = false

		var err error
		lead, err = a.SendInput("Check the release.", nil)
		require.NoError(t, err)
		a.HandleOutput(codebuddyInputEcho(lead.SessionID, lead.TurnID))

		require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
			InputID:   "queue-steer-early-unretained",
			AttemptID: "attempt-early-unretained",
			Content:   "Early steer.",
		}))
		assert.Empty(t, sink.SteerResolutions())
		retirements := sink.NativeReplyRetirements()
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, retirements[lead])
		steerReceipt := agent.NativeInputReceipt{
			SessionID: lead.SessionID,
			InputID:   "steer-early-unretained",
			TurnID:    lead.TurnID,
		}
		assert.NotContains(t, retirements, steerReceipt, "an unretained early steer must not retire its receipt")
	})
}

func TestCodebuddyConsumeInputEchoSkipsToolResultDecodeWithoutAllocations(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	// Build a large tool result whose escaped text even mentions "isReplay".
	payload := make([]byte, 0, 256*1024)
	payload = append(payload, `{"type":"user","session_id":"session-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-big","content":"`...)
	for range 4096 {
		payload = append(payload, `line with \"isReplay\" and tool output text\n`...)
	}
	payload = append(payload, `"}]}}`...)

	allocs := testing.AllocsPerRun(50, func() {
		if a.consumeInputEcho(payload) {
			t.Fatal("tool result must not be consumed as an input echo")
		}
	})
	assert.Zero(t, allocs, "consumeInputEcho must reject tool results before JSON decoding")
}
