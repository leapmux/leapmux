package commandcode

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventVerdicts(t *testing.T) {
	for _, kind := range []string{"skill_loaded", "mod_error", "continuation_recovery", "tool_hooks"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := testAgent(t)
			feedEvent(t, a, map[string]any{"type": kind, "message": "native notice"})
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.Empty(t, sink.Messages())
		})
	}
	for _, kind := range []string{"session_titled", "model_trace", "tool_input_coerced", "tool_input_repaired", "message_start", "message_update", "turn_start", "turn_end", "model_request_start", "tool_running", "interrupted", "thinking_start"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := testAgent(t)
			feedEvent(t, a, map[string]any{"type": kind})
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestUnknownFramesPersist(t *testing.T) {
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "event", Frame: json.RawMessage(`{"type":"event","event":{"type":"future_event","detail":"new"}}`)},
		{Name: "event with a different field shape", Frame: json.RawMessage(`{"type":"event","event":{"type":"future_event","usage":"new shape"}}`)},
		{Name: "method", Frame: json.RawMessage(`{"jsonrpc":"2.0","method":"future/method","params":{"detail":"new"}}`)},
		{Name: "method with a different field shape", Frame: json.RawMessage(`{"jsonrpc":"2.0","method":"future/method","params":{"count":"new shape"}}`)},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink := testAgent(t)
		a.HandleOutput(tc.Frame)
		return sink
	})
}

func TestTurnEndPrecedesTheClear_CommandCode(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "message_end", "content": []map[string]string{{"type": "text", "text": "Native answer."}}})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
	assert.False(t, a.PublishTurnActive().Active)
}

func TestRetryAttemptKeepsOnlyTheRetriedText(t *testing.T) {
	// .tmp/probe/commandcode/hosted1/logs/frames.jsonl, frames 4, 9-15, 18, 20, and 25-31.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "the interrupted retry discards its first attempt", FramesPath: "testdata/stream_restart.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a, _ := testAgent(t)
			a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
			return a.HandleOutput
		},
		TextOf: func(row agenttest.Message) (agenttest.RetryAttemptText, bool) {
			if event, err := eventFromFrame(row.Content); err == nil && event.Type == contracts.CommandCodeEventMessageEnd {
				return agenttest.RetryAttemptText{Text: nativeText(event.Content), Completion: agent.MessageCompletionComplete}, true
			}
			kind, text, completion, ok := agenttest.DecodeAssembledMessage(row.Content)
			return agenttest.RetryAttemptText{Text: text, Completion: completion}, ok && kind == agent.AssembledMessageKindText
		},
		Want:      []agenttest.RetryAttemptText{{Text: "FULL-HOSTED", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"PARTIAL-HOSTED", "PARTIAL-TWO"},
	})
}

func TestStreamRestartResolvesAtTheFirstDelta(t *testing.T) {
	for _, output := range []map[string]any{
		{"type": "text_delta", "delta": "new answer"},
		{"type": "thinking_delta", "delta": "new reasoning"},
		{"type": "tool_queued", "toolCallId": "call", "toolName": "shell_command"},
	} {
		t.Run(output["type"].(string), func(t *testing.T) {
			a, sink := testAgent(t)
			a.startTurn("turn_1")
			feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "discarded answer"})
			feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "discarded reasoning"})
			feedEvent(t, a, map[string]any{"type": "stream_restart", "attempt": 1})
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.Zero(t, sink.ProgressSnapshot().OutputBytes)
			feedEvent(t, a, map[string]any{"type": "text_delta", "delta": ""})
			feedEvent(t, a, map[string]any{"type": "tool_queued"})
			assert.Empty(t, sink.LeapMuxNotifications())
			feedEvent(t, a, output)
			require.Len(t, sink.LeapMuxNotifications(), 1)
			assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
			feedEvent(t, a, output)
			assert.Len(t, sink.LeapMuxNotifications(), 1)
		})
	}
}

func TestStreamRestartResolutionEndsWithTheTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "api_retry", "attempt": 1})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error"})
	a.startTurn("turn_2")
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "another turn"})
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestAPIRetryResolvesAtTheFirstOutput(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "api_retry", "attempt": 1})
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "new reasoning"})
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
}

func TestRunErrorThatTheTurnEndRepeatsIsDropped(t *testing.T) {
	// .tmp/probe/commandcode/run1b/logs/frames.jsonl, frames 62 and 64 repeat this error.
	a, sink := testAgent(t)
	a.startTurn("turn_4")
	feedEvent(t, a, map[string]any{"type": "run_error", "error": map[string]any{"message": "PROBE-BAD-REQUEST", "status": 400}})
	assert.Empty(t, sink.PersistedNotifications())
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_4", "stopReason": "run_error", "error": map[string]any{"message": "PROBE-BAD-REQUEST", "status": 400}})
	assert.Empty(t, sink.PersistedNotifications())
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
}

func TestRunErrorWithADifferentTurnErrorPersists(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	first := feedEvent(t, a, map[string]any{"type": "run_error", "error": map[string]any{"message": "first error"}})
	assert.Empty(t, sink.PersistedNotifications())
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error", "error": map[string]any{"message": "different error"}})
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, string(first), string(sink.PersistedNotifications()[0].Content))
}

func TestRunErrorWithNoMessageIsNotAProvenDuplicate(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "run_error", "error": nil})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error"})
	require.Len(t, sink.PersistedNotifications(), 1)
}

func TestRunErrorKeepsItsBytesUntilTheBoundary(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	raw := feedEvent(t, a, map[string]any{"type": "run_error", "error": "first"})
	assert.Empty(t, sink.PersistedNotifications())
	index := bytes.Index(raw, []byte("first"))
	require.NotEqual(t, -1, index)
	copy(raw[index:], "other")
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error", "error": map[string]any{"message": "first"}})
	assert.Empty(t, sink.PersistedNotifications())
}

func TestRunErrorFlushesThePriorErrorBeforeItHoldsAnother(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "first"})
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "second"})
	require.Len(t, sink.PersistedNotifications(), 1)
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error", "error": map[string]any{"message": "second"}})
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "first")
}

func TestRunErrorPersistsAtProcessExitWithoutAnActiveTurn(t *testing.T) {
	a, sink := testAgent(t)
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "startup error"})
	assert.Empty(t, sink.PersistedNotifications())
	a.finishStream()
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "startup error")
}

type runErrorFailureSink struct {
	*agenttest.Sink
	notificationError error
	noticesAtMessage  int
	noticesAtWorker   int
}

func (sink *runErrorFailureSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	sink.noticesAtWorker = len(sink.PersistedNotifications())
	return sink.Sink.PersistWorkerNotification(content)
}

func (sink *runErrorFailureSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if sink.notificationError != nil {
		return false, sink.notificationError
	}
	return sink.Sink.PersistNotification(source, content)
}

func (sink *runErrorFailureSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	sink.noticesAtMessage = len(sink.PersistedNotifications())
	return sink.Sink.PersistMessage(source, content, span)
}

func TestRunErrorWaitsAheadOfTheNextRowAfterAFailedWrite(t *testing.T) {
	a, sink := testAgent(t)
	failure := &runErrorFailureSink{Sink: sink, notificationError: assert.AnError}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failure))
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "retained"})
	feedEvent(t, a, map[string]any{"type": "message_end", "content": []map[string]string{{"type": "text", "text": "later row"}}})
	assert.Empty(t, sink.Messages())
	failure.notificationError = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, 1, failure.noticesAtMessage)
}

func TestRunErrorPrecedesRetryResolution(t *testing.T) {
	a, sink := testAgent(t)
	observer := &runErrorFailureSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(observer))
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "api_retry", "attempt": 1})
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "held error"})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "new output"})
	assert.Equal(t, 2, observer.noticesAtWorker, "the held native error precedes the worker resolution")
}

func TestRunErrorPrecedesTheSteerFlush(t *testing.T) {
	a, sink := commandCodeSteerAgent(t, nil)
	observer := &runErrorFailureSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(observer))
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "buffered output"})
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "held error"})
	require.NoError(t, a.SteerInput("guidance", nil))
	feedMethod(t, a, "turn/steers_delivered", map[string]any{"count": 1})
	assert.Equal(t, 1, observer.noticesAtMessage)
	require.Len(t, sink.SteerEchoReports(), 1)
}

func TestRunErrorStaysHeldAcrossLiveProgressWithoutARetry(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "run_error", "error": "held error"})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "buffered output"})
	assert.Empty(t, sink.PersistedNotifications())
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "run_error", "error": map[string]any{"message": "held error"}})
	assert.Empty(t, sink.PersistedNotifications())
	require.Len(t, sink.Messages(), 2)
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
	assert.Equal(t, agent.NotificationKindAPIRetry, p.Classify([]byte(`{"type":"event","event":{"type":"api_retry"}}`)).Kind())
	assert.Equal(t, agent.NotificationKindCompactionBoundary, p.Classify([]byte(`{"type":"event","event":{"type":"compaction_outcome","outcome":"summarized"}}`)).Kind())
	assert.Empty(t, p.Classify([]byte(`{"event":{"type":"api_retry"}}`)).Kind())
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

func TestNativeTurnEndSuppliesItsToolCount(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "end_turn"})
	a.startTurn("turn_2")
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "call", "toolName": "shell_command", "input": map[string]string{"command": "native command"}})
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "call", "toolName": "shell_command", "result": []map[string]string{{"type": "text", "text": "native output"}}})
	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_2", "stopReason": "end_turn"})

	var counts []int32
	for _, message := range sink.Messages() {
		if !message.TurnEnd {
			continue
		}
		content := agent.MessageContent{Original: message.Content, Supplemental: message.SupplementalContent, Metadata: message.Metadata}
		count, ok := commandcodeProvider{}.TurnEndToolUses(agent.ResolveMessageContent(commandcodeProvider{}, content))
		require.True(t, ok, "a native turn end must state its tool count, including zero")
		counts = append(counts, count)
	}
	assert.Equal(t, []int32{0, 1}, counts)
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
