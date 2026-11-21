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

func newACPInputReplyBase(t *testing.T) (*Base, *acpInputReplySink, agent.InputReceiptSender) {
	t.Helper()
	b, baseSink := newACPTurnBase(t, &agenttest.Stdin{})
	sink := &acpInputReplySink{Sink: baseSink, retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
	b.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	sender, ok := any(b).(agent.InputReceiptSender)
	require.True(t, ok, "the Worker needs an ACP receipt to follow the accepted input")
	return b, sink, sender
}

func TestACPInputReplyUsesTheDispatchedPrompt(t *testing.T) {
	t.Parallel()
	b, sink, sender := newACPInputReplyBase(t)
	waitForCompletion, resumeCompletion := pausePromptCompletion(t, b)
	sessionID := b.CurrentSessionID()
	b.hooks.PromptPrepared = func() {
		b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "before dispatch"))
	}

	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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
	b, sink, sender := newACPInputReplyBase(t)
	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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

func TestACPInputReplyRecognizesAnExplicitCancellation(t *testing.T) {
	t.Parallel()
	b, sink, sender := newACPInputReplyBase(t)
	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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

func TestACPInputReplyRetiresOnNativeRejection(t *testing.T) {
	t.Parallel()
	b, sink, sender := newACPInputReplyBase(t)
	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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
	b, sink, sender := newACPInputReplyBase(t)
	b.hooks.AnswerlessControlsOutliveTurns = true
	_, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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
	b, sink, sender := newACPInputReplyBase(t)
	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
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
