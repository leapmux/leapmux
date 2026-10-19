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
	r.agent.beginInputTurn()
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
	require.NoError(t, r.agent.Interrupt(agent.StopContext{}))
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
		"message": "The context exceeded the window.", "noticeType": "status", "reason": "context_overflow_recovery",
		"metadata": map[string]any{"kind": "context_overflow_recovery", "phase": "started"}, "agent": map[string]any{"kind": "lead"},
	})
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{
		"message": "sub status", "noticeType": "status", "agent": map[string]any{"kind": "subagent"},
	})
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Contains(t, string(notifications[0].Content), "The context exceeded the window.")
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

func TestClineSteerCutAnswerIsInterrupted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	// Trimmed from .tmp/probe/cline/round2/logs/hub-frames.jsonl, frames 16-26.
	r.feed(t, eventAssistantDelta, map[string]any{"text": "CUT-1 CUT-2 CUT-3 CUT-4 "})
	pending := map[string]any{"prompts": []any{map[string]any{"id": "steer-1", "delivery": "steer", "prompt": "Say hi instead."}}}
	r.feed(t, "session.pending_prompts", pending)
	r.feed(t, "session.pending_prompts", pending)
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "CUT-1 CUT-2 CUT-3 CUT-4 "})
	r.feed(t, eventIterationFinished, map[string]any{})
	r.feed(t, "session.pending_prompts", map[string]any{"prompts": []any{}})
	r.feed(t, eventPendingPromptSubmitted, map[string]any{"prompt": map[string]any{"id": "steer-1", "delivery": "steer"}})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "AFTER-STEER-ANSWER"})
	rows := r.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, noCompletion, rows[1].Completion)
}

func TestClineSteerReplyRecordsAnUnlistedSteer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	require.NoError(t, r.agent.SteerInput("Use the second answer.", nil))
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The cut answer."})
	rows := r.sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
}

func TestClineSteerReplyAfterSubmissionDoesNotInterruptTheNextAnswer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.hub.handle(commandSessionSendInput, func(command fakeCommand) fakeReply {
		r.feed(t, "session.pending_prompts", map[string]any{"prompts": []any{map[string]any{"delivery": "steer"}}})
		r.feed(t, eventPendingPromptSubmitted, map[string]any{"prompt": map[string]any{"delivery": "steer"}})
		return fakeReply{Payload: map[string]any{"queued": true}}
	})
	require.NoError(t, r.agent.SteerInput("Use the second answer.", nil))
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The next answer."})
	rows := r.sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, noCompletion, rows[0].Completion)
}

func TestClineReasoningDiscardAtIterationOrCompaction(t *testing.T) {
	t.Parallel()
	for _, compaction := range []bool{false, true} {
		name := "iteration"
		if compaction {
			name = "compaction"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			// Trimmed from .tmp/probe/cline/round2/logs/hub-frames.jsonl, frames 43-56.
			r.feed(t, eventReasoningDelta, map[string]any{"text": "THINK-1 "})
			if compaction {
				r.feed(t, contracts.ClineEventSessionNotice, map[string]any{"message": "Recover from the output limit.", "metadata": map[string]any{"kind": "max_tokens_compaction", "phase": "started"}})
			} else {
				r.feed(t, eventIterationFinished, map[string]any{})
			}
			r.feed(t, eventReasoningDelta, map[string]any{"text": "NEW-THOUGHT"})
			r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "NEW-ANSWER"})
			rows := r.sink.Messages()
			require.Len(t, rows, 2)
			assert.Equal(t, "NEW-THOUGHT", payloadOf(t, rows[0])["reasoning"])
			assert.Contains(t, r.sink.ProgressUpdates(), agent.ResetModelScopeProgress(r.agent.out.lead.progressScope("reasoning")))
		})
	}
}

func TestClineSteerQueueReportsKeepTheNativeCount(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		submitted int
		want      agent.MessageCompletion
	}{
		{"one remains", 1, agent.MessageCompletionInterrupted},
		{"all submitted", 2, noCompletion},
		{"extra submission", 3, noCompletion},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			prompts := map[string]any{"prompts": []any{map[string]any{"delivery": "steer"}, map[string]any{"delivery": "queued"}, map[string]any{"delivery": "steer"}}}
			r.feed(t, eventPendingPrompts, prompts)
			r.feed(t, eventPendingPrompts, prompts)
			r.feed(t, eventPendingPrompts, map[string]any{"prompts": "malformed"})
			r.feed(t, eventPendingPromptSubmitted, map[string]any{"prompt": map[string]any{"delivery": "queued"}})
			for i := 0; i < test.submitted; i++ {
				r.feed(t, eventPendingPromptSubmitted, map[string]any{"prompt": map[string]any{"delivery": "steer"}})
			}
			r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The answer."})
			rows := r.sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, test.want, rows[0].Completion)
		})
	}
}

func TestClineSteerReplyCannotChangeTheReplacementTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.hub.handle(commandSessionSendInput, func(command fakeCommand) fakeReply {
		r.agent.releaseInputTurn()
		r.agent.beginInputTurn()
		return fakeReply{Payload: map[string]any{"queued": true}}
	})
	require.NoError(t, r.agent.SteerInput("The old steer.", nil))
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "The replacement answer."})
	rows := r.sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, noCompletion, rows[0].Completion)
}

func TestClineIterationEndStartsNoTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventIterationFinished, map[string]any{})
	assert.False(t, r.turnActive())
	assert.Empty(t, r.sink.Messages())
}

func compactionNotice(phase string) map[string]any {
	return map[string]any{"message": "The native compaction notice.", "metadata": map[string]any{"kind": contracts.ClineNoticeKindAutoCompaction, "phase": phase}}
}

func TestClineCompactionStartIsLiveThenOnlyTheEndPersists(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{contracts.ClineNoticePhaseCompleted, contracts.ClineNoticePhaseSkipped, contracts.ClineNoticePhaseFailed} {
		t.Run(phase, func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, contracts.ClineEventSessionNotice, compactionNotice(contracts.ClineNoticePhaseStarted))
			assert.Zero(t, r.sink.NotificationCount())
			value, exists := r.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, exists)
			assert.Equal(t, map[string]any{}, value)
			r.feed(t, contracts.ClineEventSessionNotice, compactionNotice(phase))
			value, exists = r.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, exists)
			assert.Nil(t, value)
			require.Len(t, r.sink.PersistedNotifications(), 1)
			assert.Equal(t, agent.CompactionBoundaryNotification(), (clineProvider{}).Classify(r.sink.LastNotification().Content))
		})
	}
}

func TestClineRunEndWithALiveCompactionWritesAFailedEnd(t *testing.T) {
	t.Parallel()
	for _, completion := range []agent.MessageCompletion{agent.MessageCompletionComplete, agent.MessageCompletionError, agent.MessageCompletionInterrupted} {
		t.Run(string(completion), func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, contracts.ClineEventSessionNotice, compactionNotice(contracts.ClineNoticePhaseStarted))
			r.agent.dispatchMu.Lock()
			r.agent.endTurn(completion, runEndRow(r.sessionID(), runReasonError, "The turn ended."))
			r.agent.dispatchMu.Unlock()
			value, exists := r.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, exists)
			assert.Nil(t, value)
			notices := r.sink.Notifications()
			require.Len(t, notices, 1)
			assert.Equal(t, contracts.NotificationTypeCompactionEnded, notices[0][contracts.NotificationFieldType])
			outcome := contracts.CompactionEndOutcomeFailed
			if completion == agent.MessageCompletionInterrupted {
				outcome = contracts.CompactionEndOutcomeAborted
			}
			assert.Equal(t, outcome, notices[0][contracts.NotificationFieldOutcome])
			assert.NotContains(t, notices[0], contracts.NotificationFieldError)
		})
	}
}

func TestClineCompactionDiscardClearsLiveStateWithoutARow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventSessionNotice, compactionNotice(contracts.ClineNoticePhaseStarted))
	r.agent.DiscardOutput()
	r.agent.dispatchMu.Lock()
	r.agent.endTurn(agent.MessageCompletionInterrupted, runEndRow(r.sessionID(), runReasonAborted, ""))
	r.agent.dispatchMu.Unlock()
	assert.Empty(t, r.sink.Notifications())
	assert.Empty(t, r.sink.Messages())
	value, exists := r.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, exists)
	assert.Nil(t, value)
}

func TestClineKnownHubEventsStoreNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, event := range []string{eventRunHeartbeat, eventAgentDone, eventUINotify, eventSessionAttached, eventSessionDetached, eventIterationFinished, eventPendingPrompts, eventPendingPromptSubmitted} {
		r.feed(t, event, map[string]any{})
	}
	assert.Empty(t, r.sink.Messages())
	assert.Empty(t, r.sink.PersistedNotifications())
	assert.False(t, r.turnActive())
}

func TestClineRetryResolvesAtTheFirstModelOutput(t *testing.T) {
	t.Parallel()
	for _, event := range []string{eventAssistantDelta, eventReasoningDelta, contracts.ClineEventToolStarted, contracts.ClineEventAssistantFinished, contracts.ClineEventReasoningFinished} {
		t.Run(event, func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, contracts.ClineEventSessionNotice, map[string]any{"message": "Retry the model.", "metadata": map[string]any{"kind": contracts.ClineNoticeKindProviderErrorRetry, "phase": contracts.ClineNoticePhaseStarted}})
			assert.Empty(t, r.sink.Notifications())
			payload := map[string]any{"text": "A fresh attempt."}
			if event == contracts.ClineEventToolStarted {
				payload = map[string]any{"toolCallId": "retry-tool", "toolName": "read_files", "input": map[string]any{"paths": []string{"file.go"}}}
			}
			if event == contracts.ClineEventReasoningFinished {
				payload = map[string]any{"reasoning": "A fresh thought."}
			}
			r.feed(t, event, payload)
			require.Len(t, r.sink.Notifications(), 1, "the first output resolves before another frame")
			r.feed(t, eventAssistantDelta, map[string]any{"text": " More text."})
			notices := r.sink.Notifications()
			require.Len(t, notices, 1)
			assert.Equal(t, contracts.NotificationTypeApiRetryResolved, notices[0][contracts.NotificationFieldType])
		})
	}
}

func TestClineRetryEndsWithoutResolvingOnTheNextTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventSessionNotice, map[string]any{"message": "Retry the model.", "metadata": map[string]any{"kind": contracts.ClineNoticeKindProviderErrorRetry, "phase": contracts.ClineNoticePhaseStarted}})
	r.agent.dispatchMu.Lock()
	r.agent.endTurn(agent.MessageCompletionError, runEndRow(r.sessionID(), runReasonError, "The model failed."))
	r.agent.dispatchMu.Unlock()
	r.feedTurn(t)
	r.feed(t, eventAssistantDelta, map[string]any{"text": "The next turn."})
	assert.Empty(t, r.sink.Notifications())
}

func TestClineMalformedFinalFrameKeepsItsBufferedOutput(t *testing.T) {
	t.Parallel()
	for _, event := range []string{contracts.ClineEventAssistantFinished, contracts.ClineEventReasoningFinished} {
		for _, invalid := range []struct {
			name    string
			payload any
		}{{"string", "malformed"}, {"null", nil}, {"array", []any{}}} {
			t.Run(event+"/"+invalid.name, func(t *testing.T) {
				r := newRig(t)
				r.feedTurn(t)
				delta, key := eventAssistantDelta, "text"
				if event == contracts.ClineEventReasoningFinished {
					delta, key = eventReasoningDelta, "reasoning"
				}
				r.feed(t, delta, map[string]any{"text": "The buffered output."})
				r.feed(t, event, invalid.payload)
				r.agent.dispatchMu.Lock()
				r.agent.endTurn(agent.MessageCompletionInterrupted, runEndRow(r.sessionID(), runReasonAborted, ""))
				r.agent.dispatchMu.Unlock()
				rows := r.sink.Messages()
				require.Len(t, rows, 2)
				assert.Equal(t, "The buffered output.", payloadOf(t, rows[0])[key])
				assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
				assert.True(t, rows[1].TurnEnd)
			})
		}
	}
}

func TestClineRetryIgnoresAbsentOutputAndAStoppedRetry(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		name := "retry failed"
		if stopped {
			name = "discarded output"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.feedTurn(t)
			retry := map[string]any{"message": "Retry the model.", "metadata": map[string]any{"kind": contracts.ClineNoticeKindProviderErrorRetry, "phase": contracts.ClineNoticePhaseStarted}}
			r.feed(t, contracts.ClineEventSessionNotice, retry)
			for _, event := range []string{eventAssistantDelta, eventReasoningDelta, contracts.ClineEventAssistantFinished, contracts.ClineEventReasoningFinished} {
				r.feed(t, event, map[string]any{})
			}
			assert.Empty(t, r.sink.Notifications())
			if stopped {
				r.agent.DiscardOutput()
			} else {
				retry["metadata"] = map[string]any{"kind": contracts.ClineNoticeKindProviderErrorRetry, "phase": contracts.ClineNoticePhaseFailed}
				r.feed(t, contracts.ClineEventSessionNotice, retry)
			}
			r.feed(t, eventAssistantDelta, map[string]any{"text": "A later output."})
			assert.Empty(t, r.sink.Notifications())
		})
	}
}

func TestClineMalformedRunStartDoesNotStartATurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventRunStarted, "malformed")
	assert.False(t, r.turnActive())
	r.feed(t, eventRunStarted, nil)
	assert.False(t, r.turnActive())
	assert.Empty(t, r.sink.Messages())
}
