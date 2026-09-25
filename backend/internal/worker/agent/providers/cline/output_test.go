package cline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// feedTurn starts a turn by hand, with no hub command, so a test drives every
// event through HandleOutput.
func (r *rig) feedTurn(t *testing.T) {
	t.Helper()
	r.agent.armTurn()
}

func TestReasoningReachesTheTranscriptBeforeTheText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Think ", "redacted": false})
	r.feed(t, eventReasoningDelta, map[string]any{"text": "first.", "redacted": false})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Hel"})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "lo."})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Hello."})
	// Cline sends the reasoning's end after the text's.
	r.feed(t, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": "Think first."})

	messages := r.sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, []string{contracts.ClineEventReasoningFinished, contracts.ClineEventAssistantFinished}, rowEvents(t, &r.sink.Sink))
	assert.Equal(t, "Think first.", payloadOf(t, messages[0])["reasoning"])
	assert.Equal(t, "Hello.", payloadOf(t, messages[1])["text"])
}

func TestReasoningWithNoTextKeepsClinesOwnRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Only thinking."})
	r.feed(t, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": "Only thinking."})
	require.Len(t, r.sink.Messages(), 1)
	assert.Equal(t, []string{contracts.ClineEventReasoningFinished}, rowEvents(t, &r.sink.Sink))
}

func TestReasoningThatNoDeltaStreamedStillReachesTheTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Answer."})
	r.feed(t, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": "Reasoning with no stream."})
	assert.Equal(t, []string{contracts.ClineEventAssistantFinished, contracts.ClineEventReasoningFinished}, rowEvents(t, &r.sink.Sink))
}

func TestARedactedReasoningStaysOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "", "redacted": true})
	r.feed(t, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": ""})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "  "})
	assert.Zero(t, r.sink.MessageCount())
}

func TestTheNextMessagesReasoningStartsFresh(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "One."})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "First."})
	// The reasoning's end of the first message never arrives.
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Two."})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Second."})
	messages := r.sink.Messages()
	require.Len(t, messages, 4)
	assert.Equal(t, "Two.", payloadOf(t, messages[2])["reasoning"])
}

func TestAToolCallOpensAndClosesItsSpan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Run it."})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": contracts.ClineToolRunCommands, "input": map[string]any{"commands": []string{"echo hi"}}})
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_1", "toolName": contracts.ClineToolRunCommands, "update": map[string]any{"stream": "stdout", "chunk": "hi\n"}})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "call_1", "toolName": contracts.ClineToolRunCommands, "output": []any{map[string]any{"query": "echo hi", "result": "hi\n", "success": true}}})

	messages := r.sink.Messages()
	require.Len(t, messages, 3)
	assert.Equal(t, []string{contracts.ClineEventReasoningFinished, contracts.ClineEventToolStarted, contracts.ClineEventToolFinished}, rowEvents(t, &r.sink.Sink))
	assert.Equal(t, "call_1", messages[1].SpanID)
	assert.Equal(t, contracts.ClineToolRunCommands, messages[1].SpanType)
	assert.Equal(t, "call_1", messages[2].SpanID)
	assert.True(t, messages[2].Closing)
	assert.Contains(t, r.sink.ClosedSpans(), "call_1")
	progress := r.sink.ProgressSnapshot()
	assert.Zero(t, progress.OutputBytes, "the call's end completes its output counter")
}

func TestAHooksCopyOfAToolCallIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolName": "read_files"})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolName": "read_files"})
	assert.Zero(t, r.sink.MessageCount())
}

func TestARepeatedToolStartOpensNoSecondRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	start := map[string]any{"toolCallId": "call_1", "toolName": "read_files", "input": map[string]any{}}
	r.feed(t, contracts.ClineEventToolStarted, start)
	r.feed(t, contracts.ClineEventToolStarted, start)
	assert.Equal(t, 1, r.sink.MessageCount())
}

func TestTheRunsEndWritesATrimmedTurnEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.emit(contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": "read_files", "input": map[string]any{}})
	r.emit(contracts.ClineEventToolFinished, map[string]any{"toolCallId": "call_1", "toolName": "read_files", "output": []any{}})
	r.emit(contracts.ClineEventRunCompleted, map[string]any{
		"reason": "completed",
		"result": map[string]any{
			"text": "Done.", "iterations": 2, "finishReason": "completed", "durationMs": 12,
			"usage":     map[string]any{"inputTokens": 10, "outputTokens": 2},
			"messages":  []any{map[string]any{"role": "user"}},
			"toolCalls": []any{map[string]any{"name": "read_files"}},
			"model":     map[string]any{"id": "gpt-5.6", "provider": "openai-native", "info": map[string]any{"contextWindow": 1}},
		},
		"snapshot": map[string]any{"status": "idle"},
	})
	waitFor(t, func() bool { return !r.turnActive() }, "the run's end ends the turn")
	r.hub.reply(requestID, fakeReply{})

	messages := r.sink.Messages()
	last := messages[len(messages)-1]
	require.True(t, last.TurnEnd)
	assert.Equal(t, agent.MessageCompletionComplete, last.Completion)
	payload := payloadOf(t, last)
	assert.NotContains(t, payload, "snapshot")
	result := payload["result"].(map[string]any)
	assert.NotContains(t, result, "messages")
	assert.NotContains(t, result, "toolCalls")
	assert.Equal(t, "Done.", result["text"])
	assert.Equal(t, map[string]any{"id": "gpt-5.6", "provider": "openai-native"}, result["model"])
	metadata := decode(t, last.Metadata)
	assert.EqualValues(t, 1, metadata[contracts.MessageMetadataFieldToolUses], "the turn made one tool call")
	assert.Contains(t, metadata, contracts.MessageMetadataFieldDurationMs)
}

func TestTheRunsEndClosesWhatTheTurnLeftOpen(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	require.NoError(t, r.agent.Interrupt())
	abort, ok := r.hub.waitCommand(commandRunAbort)
	require.True(t, ok)
	assert.Equal(t, r.sessionID(), abort.str("sessionId"))
	r.emit(eventAssistantDelta, map[string]any{"text": "Partial"})
	r.emit(contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": "run_commands", "input": map[string]any{"commands": []string{"sleep 9"}}})
	r.endRun(t, requestID, contracts.ClineRunReasonAborted)

	messages := r.sink.Messages()
	events := rowEvents(t, &r.sink.Sink)
	assert.Equal(t, []string{
		contracts.ClineEventToolStarted,
		contracts.ClineEventAssistantFinished,
		contracts.ClineEventToolStarted,
		contracts.ClineEventRunAborted,
	}, events, "the cut text, the open call's close and the end")
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[1].Completion)
	assert.Equal(t, "Partial", payloadOf(t, messages[1])["text"])
	assert.True(t, messages[2].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[2].Completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[3].Completion)
}

func TestAFailedRunEndsAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.endRun(t, requestID, contracts.ClineRunReasonError)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionError, messages[len(messages)-1].Completion)
}

func TestCompletionOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason      string
		interrupted bool
		want        agent.MessageCompletion
	}{
		{contracts.ClineRunReasonCompleted, false, agent.MessageCompletionComplete},
		{contracts.ClineRunReasonMaxIterations, false, agent.MessageCompletionComplete},
		{contracts.ClineRunReasonAborted, false, agent.MessageCompletionInterrupted},
		{contracts.ClineRunReasonError, false, agent.MessageCompletionError},
		{contracts.ClineRunReasonMistakeLimit, false, agent.MessageCompletionError},
		{"", false, agent.MessageCompletionError},
		{contracts.ClineRunReasonError, true, agent.MessageCompletionInterrupted},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, completionOf(tc.reason, tc.interrupted), "%q interrupted=%v", tc.reason, tc.interrupted)
	}
}

func TestTrimRunEndKeepsAnUnreadableRow(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []byte(`not json`), trimRunEnd([]byte(`not json`)))
	assert.Equal(t, []byte(`{"payload":"text"}`), trimRunEnd([]byte(`{"payload":"text"}`)))
}

func TestRunEndRow(t *testing.T) {
	t.Parallel()
	failed := decode(t, runEndRow("s", runReasonError, "boom"))
	assert.Equal(t, contracts.ClineEventRunFailed, failed["event"])
	assert.Equal(t, map[string]any{"reason": runReasonError, "error": "boom"}, failed["payload"])
	aborted := decode(t, runEndRow("s", runReasonAborted, ""))
	assert.Equal(t, contracts.ClineEventRunAborted, aborted["event"])
	assert.Equal(t, map[string]any{"reason": runReasonAborted}, aborted["payload"])
}

func TestUsageOfTheLeadReachesTheContextReadout(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventUsageUpdated, map[string]any{
		"delta": map[string]any{"inputTokens": 1000, "outputTokens": 50, "cacheReadTokens": 600, "cacheWriteTokens": 100},
		"agent": map[string]any{"kind": "lead"},
	})
	value, ok := r.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage := value.(map[string]any)
	assert.EqualValues(t, 1000, usage[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 1050000, usage[contracts.ContextUsageFieldContextWindow], "the catalog states the model's window")

	count := r.sink.SessionInfoCount()
	r.feed(t, eventUsageUpdated, map[string]any{"delta": map[string]any{"inputTokens": 5}, "agent": map[string]any{"kind": "teammate"}})
	assert.Equal(t, count, r.sink.SessionInfoCount(), "a teammate's usage is not the lead's context")
	r.feed(t, eventUsageUpdated, map[string]any{
		"delta": map[string]any{"inputTokens": 1000, "outputTokens": 50, "cacheReadTokens": 600, "cacheWriteTokens": 100},
		"agent": map[string]any{"kind": "lead"},
	})
	assert.Equal(t, count, r.sink.SessionInfoCount(), "an unchanged readout broadcasts nothing")
}

func TestUsageWhileASubagentRunsIsTheSubagents(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "spawn_1", "toolName": contracts.ClineToolSpawnAgent, "input": map[string]any{"task": "Look."}})
	r.feed(t, eventUsageUpdated, map[string]any{"delta": map[string]any{"inputTokens": 7}, "agent": map[string]any{"kind": "lead"}})
	assert.Zero(t, r.sink.SessionInfoCount())
}

func TestANoticeOfTheLeadIsPersisted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{
		"message": "auto-compacting", "noticeType": "status", "reason": "auto_compaction",
		"metadata": map[string]any{"kind": "auto_compaction", "phase": "started"}, "agent": map[string]any{"kind": "lead"},
	})
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{
		"message": "sub status", "noticeType": "status", "agent": map[string]any{"kind": "subagent"},
	})
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Contains(t, string(notifications[0].Content), "auto-compacting")
}

func TestAMediaRowFollowsItsReasoning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Draw."})
	r.feed(t, contracts.ClineEventAssistantMedia, map[string]any{"media": map[string]any{"type": "image", "mediaType": "image/png", "data": "AAAA"}})
	assert.Equal(t, []string{contracts.ClineEventReasoningFinished, contracts.ClineEventAssistantMedia}, rowEvents(t, &r.sink.Sink))
}

func TestLiveOutputKeepsTheTail(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": "run_commands", "input": map[string]any{}})
	big := strings.Repeat("x", liveOutputLimit)
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_1", "update": map[string]any{"chunk": big}})
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_1", "update": map[string]any{"chunk": "END"}})
	r.agent.Mu.Lock()
	tool := r.agent.out.lead.open["call_1"]
	r.agent.Mu.Unlock()
	require.NotNil(t, tool)
	assert.EqualValues(t, liveOutputLimit+3, tool.total)
	assert.True(t, strings.HasSuffix(tool.tail, "END"))
	assert.LessOrEqual(t, len(tool.tail), liveOutputLimit)
	assert.True(t, tool.tailLost)
}

func TestLastPlanTextIsTheLeadsLastAnswer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "  Plan:\n1. Do it.  "})
	assert.Equal(t, "Plan:\n1. Do it.", r.agent.lastPlanText())
}

func TestEventRowIsClinesEnvelope(t *testing.T) {
	t.Parallel()
	row := decode(t, eventRow("s-1", contracts.ClineEventAssistantFinished, map[string]any{"text": "x"}))
	assert.Equal(t, hubProtocolVersion, row["version"])
	assert.Equal(t, "s-1", row["sessionId"])
	assert.Equal(t, contracts.ClineEventAssistantFinished, row[contracts.ClineEventFieldEvent])
	raw, err := json.Marshal(row[contracts.ClineEventFieldPayload])
	require.NoError(t, err)
	assert.JSONEq(t, `{"text":"x"}`, string(raw))
}

// Cline's input count includes the cached tokens. A reading whose cached counts
// exceed it states no negative input, and a model that the catalog does not
// know states no window.
func TestUsageStatesNoNegativeInputAndNoUnknownWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.selection = providerSelection{Provider: "openrouter", Model: "vendor/private-model"}
	})
	r.feed(t, eventUsageUpdated, map[string]any{
		"delta": map[string]any{"inputTokens": 100, "outputTokens": 5, "cacheReadTokens": 150, "cacheWriteTokens": 20},
		"agent": map[string]any{"kind": "lead"},
	})
	value, ok := r.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage := value.(map[string]any)
	assert.EqualValues(t, 100, usage[contracts.ContextUsageFieldContextTokens])
	assert.NotContains(t, usage, contracts.ContextUsageFieldContextWindow, "the catalog states no window for the model")
	assert.EqualValues(t, 0, usage[contracts.ContextUsageFieldInputTokens], "no negative uncached input")
	assert.EqualValues(t, 150, usage[contracts.ContextUsageFieldCacheReadInputTokens])
	assert.EqualValues(t, 20, usage[contracts.ContextUsageFieldCacheCreationInputTokens])
	assert.EqualValues(t, 5, usage[contracts.ContextUsageFieldOutputTokens])
}

func TestTurnDurationMs(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	duration := turnDurationMs(start, start.Add(1500*time.Millisecond))
	require.NotNil(t, duration)
	assert.EqualValues(t, 1500, *duration)
	zero := turnDurationMs(start, start)
	require.NotNil(t, zero)
	assert.Zero(t, *zero)
	assert.Nil(t, turnDurationMs(time.Time{}, start), "a turn with no start")
	assert.Nil(t, turnDurationMs(start, start.Add(-time.Second)), "a clock that moved backwards")
}

func TestRowMetadata(t *testing.T) {
	t.Parallel()
	assert.Nil(t, rowMetadata(nil, nil), "nothing to state")
	ms := int64(12)
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldDurationMs+`":12}`, string(rowMetadata(map[string]any{}, &ms)))
	assert.JSONEq(t, `{"`+contracts.SessionInfoKeyContextUsage+`":{"a":1}}`, string(rowMetadata(map[string]any{"a": 1}, nil)))
}

// A result that is not an object, and a model with no catalog details, stay as
// Cline sent them.
func TestTrimRunEndKeepsWhatItCannotTrim(t *testing.T) {
	t.Parallel()
	row := decode(t, trimRunEnd([]byte(`{"event":"run.completed","payload":{"reason":"completed","result":"plain","snapshot":{"status":"idle"}}}`)))
	assert.Equal(t, map[string]any{"reason": "completed", "result": "plain"}, row["payload"], "the snapshot goes; a result that is not an object stays")
	row = decode(t, trimRunEnd([]byte(`{"event":"run.completed","payload":{"result":{"text":"t","model":"gpt-5.6"}}}`)))
	assert.Equal(t, map[string]any{"text": "t", "model": "gpt-5.6"}, row["payload"].(map[string]any)["result"])
}

// A notice with no text shows nothing, and a notice that states no agent is the
// lead's.
func TestANoticeNeedsItsText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{"message": "", "agent": map[string]any{"kind": "lead"}})
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{"message": "Retrying."})
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Contains(t, string(notifications[0].Content), "Retrying.")
}

// A run that completed around a call that it still held open did not finish the
// call, so the call closes as an error, not as a success.
func TestACompletedRunClosesAnOpenCallAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.emit(contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": "run_commands", "input": map[string]any{}})
	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	messages := r.sink.Messages()
	require.Len(t, messages, 3)
	assert.True(t, messages[1].Closing)
	assert.Equal(t, agent.MessageCompletionError, messages[1].Completion)
	assert.Equal(t, agent.MessageCompletionComplete, messages[2].Completion)
	assert.EqualValues(t, 0, decode(t, messages[2].Metadata)[contracts.MessageMetadataFieldToolUses], "an open call is no finished tool use")
}

// Output of a call that is not open, and output with no text, report nothing.
func TestToolOutputNeedsAnOpenCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	before := r.sink.ProgressSnapshot()
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "unknown", "update": map[string]any{"chunk": "x"}})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_1", "toolName": "run_commands", "input": map[string]any{}})
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_1", "update": map[string]any{"chunk": ""}})
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_1", "update": "not an update"})
	assert.Equal(t, before.OutputBytes, r.sink.ProgressSnapshot().OutputBytes)
	r.agent.Mu.Lock()
	tool := r.agent.out.lead.open["call_1"]
	r.agent.Mu.Unlock()
	require.NotNil(t, tool)
	assert.Zero(t, tool.total)
	assert.Empty(t, tool.tail)
}
