package acp

import (
	"encoding/json"
	"errors"
	"strings"
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
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

func TestACPCancelledPromptResponseDrainsOnlyPromptToolsWithTargets(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	var capturedTools int
	var mu sync.Mutex
	b.hooks.OpenRootTranscript = func(_ RootTranscriptStart, services agent.ProviderServices) RootTranscript {
		return &recordingRootTranscript{
			sink: services,
			onWrite: func() {
				mu.Lock()
				capturedTools++
				mu.Unlock()
			},
		}
	}

	_, err := b.SendInput("work", nil)
	require.NoError(t, err)
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Read","kind":"read","status":"in_progress"}`))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"queued-tool","title":"Write","kind":"edit","status":"in_progress"}`))

	response := []byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled"}}`)
	require.True(t, b.HandleJSONRPCResponseForTest(providerkit.ParseLine(response)))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })

	// Only prompt-tool should be drained and force-closed; queued-tool must remain open.
	b.turnMu.Lock()
	_, promptStillOpen := b.toolUpdateState["prompt-tool"]
	_, queuedStillOpen := b.toolUpdateState["queued-tool"]
	b.turnMu.Unlock()
	assert.False(t, promptStillOpen, "cancelled prompt tool must be drained")
	assert.True(t, queuedStillOpen, "queued agent turn tool must not be force-closed by cancelled prompt")

	var promptToolRows, queuedToolRows int
	for _, msg := range sink.Messages() {
		switch msg.SpanID {
		case "prompt-tool":
			promptToolRows++
		case "queued-tool":
			queuedToolRows++
		}
	}
	assert.Equal(t, 2, promptToolRows, "cancelled prompt tool must have its opening and interrupted closing rows")
	assert.Equal(t, 1, queuedToolRows, "queued agent turn tool must only have its opening row and not be force-closed")
	mu.Lock()
	gotCaptured := capturedTools
	mu.Unlock()
	assert.GreaterOrEqual(t, gotCaptured, 3, "cancelled prompt tool close must reserve through RootTranscript")
}

type recordingRootTranscript struct {
	sink    agent.ProviderServices
	onWrite func()
}

func (r *recordingRootTranscript) Prepare() error                       { return nil }
func (r *recordingRootTranscript) PrepareInput([]json.RawMessage) error { return nil }
func (r *recordingRootTranscript) Reserve() RootTranscriptCapture {
	if r.onWrite != nil {
		r.onWrite()
	}
	return recordingRootCapture{writer: r.sink}
}
func (r *recordingRootTranscript) Finish(RootTranscriptCompletion) error { return nil }

type recordingRootCapture struct {
	writer agent.TranscriptServices
}

func (c recordingRootCapture) Writer() agent.TranscriptServices { return c.writer }
func (c recordingRootCapture) Complete()                        {}

func TestACPDiscardModeSuppressesProgressOnPromptResponseAndDroppedQueuedTurn(t *testing.T) {
	t.Parallel()
	t.Run("prompt response while discarding", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		_, err := b.SendInput("hi", nil)
		require.NoError(t, err)
		b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "buffered"))
		beforeProgress := len(sink.ProgressUpdates())
		b.DiscardOutput()
		require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
		testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

		for _, update := range sink.ProgressUpdates()[beforeProgress:] {
			assert.NotEqual(t, agent.CompleteModelProgress("acp:agent_message_chunk"), update, "discarding agent must not emit CompleteModelProgress on prompt response")
		}
	})

	t.Run("dropped queued agent turn while discarding", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		_, err := b.SendInput("hi", nil)
		require.NoError(t, err)
		require.True(t, b.BeginAgentTurn())
		b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "queued text"))
		beforeMessages := sink.MessageCount()
		beforeProgress := len(sink.ProgressUpdates())
		b.DiscardOutput()
		b.EndAgentTurnWithoutRow()

		assert.Equal(t, beforeMessages, sink.MessageCount(), "discarding agent must not persist queued text")
		for _, update := range sink.ProgressUpdates()[beforeProgress:] {
			assert.NotEqual(t, agent.CompleteModelProgress("acp:agent_message_chunk"), update, "discarding agent must not emit CompleteModelProgress on EndAgentTurnWithoutRow")
		}
	})

	t.Run("dropped queued agent turn when interrupted marks text interrupted", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		_, err := b.SendInput("hi", nil)
		require.NoError(t, err)
		require.True(t, b.BeginAgentTurn())
		b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "interrupted work"))
		b.Mu.Lock()
		b.interruptRequested = true
		b.Mu.Unlock()
		b.EndAgentTurnWithoutRow()

		messages := sink.Messages()
		require.NotEmpty(t, messages)
		_, _, completion, ok := agenttest.DecodeAssembledMessage(messages[len(messages)-1].Content)
		require.True(t, ok)
		assert.Equal(t, agent.MessageCompletionInterrupted, completion)
	})
}

func TestACPEndAgentTurnPreservesEmptyFrameWhileQueued(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	_, err := b.SendInput("hi", nil)
	require.NoError(t, err)
	require.True(t, b.BeginAgentTurn())
	b.EndAgentTurn(nil)

	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() && len(sink.TurnActives()) == 2 })
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestACPEndAgentTurnWithReplyInterruptedAndDiscarding(t *testing.T) {
	t.Parallel()
	t.Run("interrupt overrides COMPLETED outcome on turn end", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		require.True(t, b.BeginAgentTurnWithReply("agent-turn-1"))
		b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "partial"))
		b.Mu.Lock()
		b.interruptRequested = true
		b.Mu.Unlock()
		b.EndAgentTurnWithReply("agent-turn-1", []byte(`{"stopReason":"end_turn"}`), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)

		var turnEnd *agenttest.Message
		for _, m := range sink.Messages() {
			if m.TurnEnd {
				copy := m
				turnEnd = &copy
			}
		}
		require.NotNil(t, turnEnd)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, turnEnd.InputTurnOutcome)
	})

	t.Run("discarding agent still retires shared steers", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		sessionID := b.CurrentSessionID()
		require.True(t, b.BeginAgentTurnWithReply("agent-turn-2"))
		resolve := b.CaptureRootSteer(sessionID, "steer-1", agent.SteerResolution{
			InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed,
		})
		require.NoError(t, resolve())
		b.DiscardOutput()
		b.EndAgentTurnWithReply("agent-turn-2", []byte(`{"stopReason":"end_turn"}`), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)

		want := agent.NativeInputReceipt{SessionID: sessionID, InputID: "steer-1", TurnID: "agent-turn-2"}
		sink.mu.Lock()
		got := sink.retired[want]
		sink.mu.Unlock()
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, got)
	})
}

func TestACPPublishSessionControlRequestRoutesNonPermissionChildControlToChild(t *testing.T) {
	t.Parallel()
	b, rootSink := newACPInputReplyBase(t)
	childSink := &acpInputReplySink{Sink: &agenttest.Sink{}, retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
	childServices := agent.NewProviderServices(childSink)

	b.ApplySubagentObservation(&SubagentObservation{RowKey: "row-1", ChildAgentKey: "child-1", Title: "child", Status: bgtask.StatusRunning, Spawns: true})
	require.True(t, b.AttachChildSession(ChildSessionTarget{RootSessionID: b.CurrentSessionID(), SessionID: "child-1", RowKey: "row-1"}))
	var seenOwnerSession string
	b.hooks.ControlOwner = func(sessionID string, _ *providerkit.ParsedLine, needsTurn bool) (ControlOwnerResolution, error) {
		seenOwnerSession = sessionID
		if sessionID == "child-1" {
			res := ControlOwnerResolution{Sink: childServices}
			if needsTurn {
				res.TurnID = "child-turn-1"
				res.InputIDs = []string{"child-input-1"}
			}
			return res, nil
		}
		return ControlOwnerResolution{Refuse: true}, nil
	}

	line := providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":"elicit-1","method":"elicitation/create","params":{"sessionId":"child-1","message":"Confirm"}}`))
	b.PublishSessionControlRequest(line, map[string]any{"action": "cancel"})

	assert.Equal(t, "child-1", seenOwnerSession)
	rootSink.mu.Lock()
	rootCount := len(rootSink.controls)
	rootSink.mu.Unlock()
	assert.Zero(t, rootCount, "child elicitation must not publish onto root sink")

	childSink.mu.Lock()
	defer childSink.mu.Unlock()
	require.Len(t, childSink.controls, 1)
	assert.Equal(t, "child-1", childSink.controls[0].AgentSessionID)
	assert.Equal(t, "child-turn-1", childSink.controls[0].NativeTurnID)
	assert.Equal(t, []string{"child-input-1"}, childSink.controls[0].NativeInputIDs)
}

type failingFirstRetireSink struct {
	*acpInputReplySink
	failInputID string
}

func (s *failingFirstRetireSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	if receipt.InputID == s.failInputID {
		return errors.New("per-receipt failure")
	}
	return s.acpInputReplySink.RetireNativeInputTurn(receipt, outcome)
}

func TestACPRetireSharedInputsContinuesAfterFirstError(t *testing.T) {
	t.Parallel()
	b, baseSink := newACPInputReplyBase(t)
	sink := &failingFirstRetireSink{acpInputReplySink: baseSink, failInputID: "steer-1"}
	b.sink = agent.NewProviderServices(sink)
	sessionID := b.CurrentSessionID()
	lead, err := b.SendInput("work", nil)
	require.NoError(t, err)
	require.NoError(t, b.CaptureRootSteer(sessionID, "steer-1", agent.SteerResolution{InputID: "q1", AttemptID: "a1", Outcome: agent.SteerOutcomeEchoed})())
	require.NoError(t, b.CaptureRootSteer(sessionID, "steer-2", agent.SteerResolution{InputID: "q2", AttemptID: "a2", Outcome: agent.SteerOutcomeEchoed})())

	require.True(t, b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"failed"}}`))))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	wantSecond := agent.NativeInputReceipt{SessionID: sessionID, InputID: "steer-2", TurnID: lead.TurnID}
	baseSink.mu.Lock()
	got := baseSink.retired[wantSecond]
	baseSink.mu.Unlock()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, got)
}

func TestACPInputReplyInterruptOverridesEndTurnOutcome(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	receipt, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "partial answer"))
	require.NoError(t, b.Interrupt(agent.StopContext{}))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"partial answer","completion":"interrupted"}`, string(messages[0].Content))
	assert.Equal(t, receipt.TurnID, messages[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, messages[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, messages[1].InputTurnOutcome)
	assert.Empty(t, sink.retired)
}

func TestACPCaptureRootSnapshotAllocatesAssistantTextKeyBeforeReconciliation(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	var order []string
	var capturedKey string
	b.hooks.AssistantTextKey = func(messageID string) string {
		order = append(order, "key:"+messageID)
		capturedKey = "reply:" + messageID
		return capturedKey
	}
	b.hooks.AssistantTextPrepared = func(messageID, key, text string) {
		order = append(order, "prepared:"+key+":"+text)
	}
	b.hooks.CaptureReplyReconciliation = func() func(agent.MessageCompletion, bool) error {
		order = append(order, "capture:"+capturedKey)
		return func(agent.MessageCompletion, bool) error {
			order = append(order, "reconcile")
			return nil
		}
	}
	_, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "final answer", "msg-1"))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	assert.Equal(t, []string{
		"key:msg-1",
		"capture:reply:msg-1",
		"prepared:reply:msg-1:final answer",
		"reconcile",
	}, order)
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, "reply:msg-1", messages[0].IdempotencyKey)
}

func TestACPCaptureRootSnapshotCallsAssistantTextKeyOnceWhenEmpty(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	calls := 0
	b.hooks.AssistantTextKey = func(string) string {
		calls++
		return ""
	}
	_, err := b.SendInput("ordinary prompt", nil)
	require.NoError(t, err)
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "final answer", "msg-1"))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	assert.Equal(t, 1, calls, "persistBufferedText must not re-invoke AssistantTextKey after captureRootSnapshotLocked resolved an empty key")
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Empty(t, messages[0].IdempotencyKey)
}

func TestACPControlOwnerHookHandlesRefuseErrorAndNeedsTurn(t *testing.T) {
	t.Parallel()

	t.Run("refuse and error send JSON-RPC refusals without publishing", func(t *testing.T) {
		t.Parallel()
		stdin := &agenttest.Stdin{}
		b, baseSink := newACPTurnBase(t, stdin)
		sink := &acpInputReplySink{Sink: baseSink, retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
		b.sink = agent.NewProviderServices(sink)

		b.hooks.ControlOwner = func(_ string, line *providerkit.ParsedLine, _ bool) (ControlOwnerResolution, error) {
			if line.Method == "vendor/err" {
				return ControlOwnerResolution{}, errors.New("ambiguous tool owner")
			}
			return ControlOwnerResolution{Refuse: true}, nil
		}

		b.PublishSessionControlRequest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"vendor/refuse","params":{"sessionId":"session-1"}}`)), nil)
		b.PublishSessionControlRequest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"method":"vendor/err","params":{"sessionId":"session-1"}}`)), nil)

		sink.mu.Lock()
		assert.Empty(t, sink.controls)
		sink.mu.Unlock()
		testutil.RequireEventually(t, func() bool { return strings.Count(stdin.String(), "\n") == 2 })
		written := stdin.String()
		assert.Contains(t, written, `"id":1`)
		assert.Contains(t, written, `"id":2`)
		assert.Contains(t, written, "ambiguous tool owner")
	})

	t.Run("answerless controls pass needsTurn false and clear turn attribution", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		b.hooks.AnswerlessControlsOutliveTurns = true
		var gotNeedsTurn []bool
		b.hooks.ControlOwner = func(_ string, _ *providerkit.ParsedLine, needsTurn bool) (ControlOwnerResolution, error) {
			gotNeedsTurn = append(gotNeedsTurn, needsTurn)
			return ControlOwnerResolution{TurnID: "should-ignore-when-false", InputIDs: []string{"i1"}}, nil
		}

		b.PublishSessionControlRequest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":"trust-1","method":"vendor/trust","params":{"sessionId":"session-1"}}`)), nil)
		b.PublishSessionControlRequest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":"ask-1","method":"vendor/ask","params":{"sessionId":"session-1"}}`)), map[string]any{"outcome": "cancelled"})

		assert.Equal(t, []bool{false, true}, gotNeedsTurn)
		sink.mu.Lock()
		defer sink.mu.Unlock()
		require.Len(t, sink.controls, 2)
		assert.Empty(t, sink.controls[0].NativeTurnID)
		assert.Empty(t, sink.controls[0].NativeInputIDs)
		assert.Equal(t, "should-ignore-when-false", sink.controls[1].NativeTurnID)
		assert.Equal(t, []string{"i1"}, sink.controls[1].NativeInputIDs)
	})
}
