package commandcode

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnEndPrecedesTheClear_CommandCode(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "message_end", "content": []map[string]string{{"type": "text", "text": "Native answer."}}})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
	assert.False(t, a.PublishTurnActive().Active)
}

func TestLateNativeStartReplyCannotReopenACompletedTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	a.startTurn("turn_1")
	assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)
}

func TestNativeCompactionDoesNotAdvertiseSteering(t *testing.T) {
	a, _ := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "compaction_start", "trigger": "manual"})
	state := a.PublishTurnActive()
	assert.True(t, state.Active)
	assert.False(t, state.Steerable)
	feedEvent(t, a, map[string]any{"type": "compaction_done", "trigger": "manual", "tokensSaved": 0})
	assert.False(t, a.PublishTurnActive().Active)
}

func TestNativeStopRetainsAnInterruptedExitBoundary(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	a.NoteIntentionalStop()
	a.finishStream()
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
}

func TestNativePartialTextSurvivesAnUnexpectedExit(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "partial native answer"})
	a.finishStream()
	messages := sink.Messages()
	require.GreaterOrEqual(t, len(messages), 2)
	assert.Contains(t, string(messages[0].Content), "partial native answer")
	assert.Equal(t, agent.MessageCompletionError, messages[0].Completion)
}

func TestNativeToolProgressCountsDeltas(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "call", "toolName": "shell_command", "input": map[string]string{"command": "native command"}})
	for _, chunk := range []string{"first", "second", "third"} {
		feedEvent(t, a, map[string]any{"type": "tool_update", "toolCallId": "call", "toolName": "shell_command", "partial": []map[string]string{{"type": "text", "text": chunk}}})
	}
	assert.Equal(t, int64(len("firstsecondthird")), sink.ProgressSnapshot().OutputBytes)
}

func TestNativeToolProgressRejectsAnUnopenedCall(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "tool_update", "toolCallId": "unopened-call", "toolName": "shell_command", "partial": []map[string]string{{"type": "text", "text": "Unowned output."}}})
	assert.Zero(t, sink.ProgressSnapshot().OutputBytes)
}

func TestNativeThinkingAndTextDoNotDuplicate(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "Native reasoning."})
	feedEvent(t, a, map[string]any{"type": "thinking_end", "text": "Native reasoning."})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "Native answer."})
	feedEvent(t, a, map[string]any{"type": "message_end", "content": []map[string]string{{"type": "thinking", "thinking": "Native reasoning."}, {"type": "text", "text": "Native answer."}}})
	a.startTurn("turn_1")
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	messages := sink.Messages()
	require.Len(t, messages, 3)
	assert.Contains(t, string(messages[0].Content), "thinking_end")
	assert.Contains(t, string(messages[1].Content), "message_end")
	assert.NotContains(t, string(messages[2].Content), "Native answer")
}

func TestNativeRetryAndCompactionClassifications(t *testing.T) {
	p := commandcodeProvider{}
	assert.Equal(t, agent.NotificationKindAPIRetry, p.Classify([]byte(`{"type":"event","event":{"type":"api_retry"}}`)).Kind)
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_done","tokensSaved":101}}`)).Kind)
	assert.Empty(t, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_start"}}`)).Kind)
	assert.Empty(t, p.Classify([]byte(`{"event":{"type":"api_retry"}}`)).Kind)
}

func TestNativeUsageKeepsAnExplicitZero(t *testing.T) {
	a, sink := testAgent(t)
	a.recordUsage(&nativeUsage{})
	infos := sink.SessionInfos()
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0], contracts.SessionInfoKeyContextUsage)
	assert.NotContains(t, strings.Join(sink.TurnLifecycle(), ","), "turn_active")
}

func TestNativeToolSpanKeepsItsConnectorOrder(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "call", "toolName": "shell_command", "input": map[string]string{"command": "native command"}})
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "call", "toolName": "shell_command", "result": []map[string]string{{"type": "text", "text": "native output"}}})
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Empty(t, messages[0].SpansOpenAtPersist)
	require.Len(t, messages[1].SpansOpenAtPersist, 1)
	assert.Equal(t, "call", messages[1].SpansOpenAtPersist[0].SpanID)
}

func TestCommandCodeTurnFrames(t *testing.T) {
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "native turn starts", Line: `{"jsonrpc":"2.0","method":"turn/started","params":{"turnId":"native-turn"}}`, Moves: true},
		{Name: "native turn completes", Line: `{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"native-turn","stopReason":"end_turn"}}`, Moves: true},
		{Name: "native compaction starts", Line: `{"type":"event","event":{"type":"compaction_start","trigger":"manual"}}`, Moves: true},
		{Name: "native compaction completes", Line: `{"type":"event","event":{"type":"compaction_done","trigger":"manual"}}`, Moves: true},
		{Name: "native hello is inert", Line: `{"jsonrpc":"2.0","method":"hello","params":{"protocolVersion":1}}`},
		{Name: "native text is inert", Line: `{"type":"event","event":{"type":"text_delta","delta":"text"}}`},
		{Name: "native repeated run context is inert", Line: `{"type":"event","event":{"type":"run_end","result":{"nextState":[]}}}`},
		{Name: "unknown event is inert", Line: `{"type":"event","event":{"type":"new_native_event"}}`},
	}, func(t *testing.T, testCase agenttest.TurnFrameCase) []bool {
		a, sink := testAgent(t)
		if testCase.Name == "native turn completes" {
			a.turnID = "native-turn"
		}
		a.HandleOutput([]byte(testCase.Line))
		return sink.TurnActiveCalls
	})
}
