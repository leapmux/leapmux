package acp

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type acpInputReplySink struct {
	*agenttest.Sink
	mu       sync.Mutex
	retired  map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome
	controls []agent.ControlRequest
}

func (sink *acpInputReplySink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.retired[receipt] = outcome
	return nil
}

func (sink *acpInputReplySink) PublishControlRequest(request agent.ControlRequest) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.controls = append(sink.controls, request)
	return nil
}

func newACPInputReplyBase(t *testing.T) (*Base, *acpInputReplySink) {
	t.Helper()
	b, baseSink := newACPTurnBase(t, &agenttest.Stdin{})
	sink := &acpInputReplySink{Sink: baseSink, retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
	b.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	return b, sink
}

func TestACPInputReplyUsesTheDispatchedPrompt(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	waitForCompletion, resumeCompletion := pausePromptCompletion(t, b)
	sessionID := b.CurrentSessionID()
	b.hooks.PromptPrepared = func() {
		b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "before dispatch"))
	}

	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	require.Equal(t, sessionID, receipt.SessionID)
	require.NotEmpty(t, receipt.InputID)
	turnID := receipt.TurnID
	require.NotEmpty(t, turnID)

	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "owned answer"))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call-1"},"options":[{"optionId":"yes","name":"Allow","kind":"allow_once"}]}}`))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	waitForCompletion()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "after response"))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"later-permission","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call-2"},"options":[{"optionId":"yes","name":"Allow","kind":"allow_once"}]}}`))
	resumeCompletion()
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.controls, 2)
	assert.Equal(t, turnID, sink.controls[0].NativeTurnID)
	assert.Empty(t, sink.controls[1].NativeTurnID)
	messages := sink.Messages()
	assert.Equal(t, []string{"text:before dispatch", "text:owned answer", "text:after response"}, assembledTexts(t, messages))
	var taggedTexts []string
	var outcomes []leapmuxv1.AgentInputTurnOutcome
	for _, message := range messages {
		if message.NativeTurnID != turnID {
			continue
		}
		taggedTexts = append(taggedTexts, assembledTexts(t, []agenttest.Message{message})...)
		if message.TurnEnd {
			outcomes = append(outcomes, message.InputTurnOutcome)
		}
	}
	assert.Equal(t, []string{"text:owned answer"}, taggedTexts)
	assert.Equal(t, []leapmuxv1.AgentInputTurnOutcome{leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED}, outcomes)
	assert.Empty(t, sink.retired)
}

func TestACPInputReplyCannotCompleteFromAnUnrecognizedResponse(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)

	require.True(t, b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.retired[receipt])
	for _, message := range sink.Messages() {
		assert.Zero(t, message.InputTurnOutcome)
	}
}

func TestACPPromptSteerSharesOnlyOutputAfterItsEcho(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		name := "completes"
		if failed {
			name = "fails"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPInputReplyBase(t)
			lead, err := b.SendInput("ordinary prompt", nil)
			require.NoError(t, err)
			b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "before the echo"))
			b.FlushBufferedOutput(agent.MessageCompletionComplete)
			resolve := b.CaptureRootSteer(lead.SessionID, "native-steer", agent.SteerResolution{
				InputID: "queued-steer", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed,
			})
			require.NoError(t, resolve())
			b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "after the echo"))
			response := promptResponse(1)
			if failed {
				response = providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"execution failed"}}`))
			}
			require.True(t, b.HandleJSONRPCResponseForTest(response))
			testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

			steer := agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: "native-steer", TurnID: lead.TurnID}
			require.Len(t, sink.SteerResolutions(), 1)
			assert.Equal(t, steer, sink.SteerResolutions()[0].Receipt)
			var texts []agenttest.Message
			for _, message := range sink.Messages() {
				if len(assembledTexts(t, []agenttest.Message{message})) != 0 {
					texts = append(texts, message)
				}
			}
			require.Len(t, texts, 2)
			assert.Equal(t, lead.InputID, texts[0].NativeInputID)
			assert.Equal(t, lead.TurnID, texts[0].NativeTurnID)
			assert.Empty(t, texts[1].NativeInputID)
			assert.Equal(t, lead.TurnID, texts[1].NativeTurnID)

			sink.mu.Lock()
			defer sink.mu.Unlock()
			if failed {
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink.retired[lead])
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink.retired[steer])
			} else {
				messages := sink.Messages()
				require.Len(t, messages, 3)
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, messages[2].InputTurnOutcome)
				assert.Equal(t, lead.TurnID, messages[2].NativeTurnID)
				assert.Empty(t, messages[2].NativeInputID)
				assert.Empty(t, sink.retired)
			}
		})
	}
}

func TestACPInputReplyRecognizesAnExplicitCancellation(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "partial answer"))
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":{"role":"result","content":{"stopReason":"cancelled"}}}`)
	require.True(t, b.HandleJSONRPCResponseForTest(providerkit.ParseLine(response)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"partial answer","completion":"interrupted"}`, string(messages[0].Content))
	assert.Equal(t, receipt.TurnID, messages[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, messages[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, messages[1].InputTurnOutcome)
}

func TestACPAgentTurnKeepsItsReplyOutcomeUntilTheEarlierPromptEnds(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	lead, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "ordinary reply"))
	require.True(t, b.BeginAgentTurnWithReply("native-agent-turn"))
	require.False(t, b.AgentTurnActive(), "the agent turn is queued behind the earlier response")
	resolve := b.CaptureRootSteer(lead.SessionID, "native-steer", agent.SteerResolution{
		InputID: "steer", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed,
	})
	require.NoError(t, resolve())
	b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "agent turn reply"))
	b.EndAgentTurnWithReply("native-agent-turn", []byte(`{"kind":"native-end"}`), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
	for _, message := range sink.Messages() {
		assert.False(t, message.TurnEnd, "the queued turn cannot publish its end yet")
	}
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	messages := sink.Messages()
	assert.Equal(t, []string{"text:ordinary reply", "text:agent turn reply"}, assembledTexts(t, messages))
	var ends []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			ends = append(ends, message)
		}
	}
	require.Len(t, ends, 2)
	assert.Equal(t, lead.TurnID, ends[0].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, ends[0].InputTurnOutcome)
	assert.Equal(t, "native-agent-turn", ends[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, ends[1].InputTurnOutcome)
}

func TestACPInputReplyRetiresOnNativeRejection(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	response := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"provider refused to run the prompt"}}`)
	require.True(t, b.HandleJSONRPCResponseForTest(providerkit.ParseLine(response)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink.retired[receipt])
}

func TestACPInputReplyLeavesSessionLevelControlsUnbound(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	b.hooks.AnswerlessControlsOutliveTurns = true
	_, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	line := providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":"session-trust","method":"vendor/trust","params":{"sessionId":"session-1"}}`))
	b.PublishSessionControlRequest(line, nil)
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.controls, 1)
	assert.Equal(t, "session-1", sink.controls[0].AgentSessionID)
	assert.Empty(t, sink.controls[0].NativeTurnID)
}

func TestACPInputReplyKeepsOutputOfAgentWorkWithoutADividerSeparate(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	sessionID := b.CurrentSessionID()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "first owned answer"))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"work-tool","title":"Read","kind":"read","status":"in_progress"}`))
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "separate work"))
	b.EndAgentTurnWithoutRow()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "second owned answer"))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"work-permission","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"work-tool"},"options":[{"optionId":"yes","name":"Allow","kind":"allow_once"}]}}`))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.controls, 1)
	assert.Empty(t, sink.controls[0].NativeTurnID)
	messages := sink.Messages()
	assert.Equal(t, []string{"text:first owned answer", "text:separate work", "text:second owned answer"}, assembledTexts(t, messages))
	var taggedTexts []string
	for _, message := range messages {
		if message.NativeTurnID == receipt.TurnID {
			taggedTexts = append(taggedTexts, assembledTexts(t, []agenttest.Message{message})...)
		}
	}
	assert.Equal(t, []string{"text:first owned answer", "text:second owned answer"}, taggedTexts)
}

func TestACPSendInputForSessionReturnsReceiptAndTracksReply(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	sessionID := b.CurrentSessionID()

	for _, bad := range []string{"", "wrong-session"} {
		receipt, err := b.SendInputForSession(bad, "rejected", nil)
		require.ErrorIs(t, err, agent.ErrInputSessionChanged)
		assert.Empty(t, receipt)
	}

	receipt, err := b.SendInputForSession(sessionID, "session-pinned prompt", nil)
	require.NoError(t, err)
	require.Equal(t, sessionID, receipt.SessionID)
	require.NotEmpty(t, receipt.InputID)
	require.NotEmpty(t, receipt.TurnID)

	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "session reply"))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })

	var taggedTexts []string
	var outcomes []leapmuxv1.AgentInputTurnOutcome
	for _, message := range sink.Messages() {
		if message.NativeTurnID != receipt.TurnID {
			continue
		}
		taggedTexts = append(taggedTexts, assembledTexts(t, []agenttest.Message{message})...)
		if message.TurnEnd {
			outcomes = append(outcomes, message.InputTurnOutcome)
		}
	}
	assert.Equal(t, []string{"text:session reply"}, taggedTexts)
	assert.Equal(t, []leapmuxv1.AgentInputTurnOutcome{leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED}, outcomes)
}
