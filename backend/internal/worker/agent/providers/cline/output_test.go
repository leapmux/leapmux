package cline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
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
			r.agent.endTurn(completion, runEndRow(r.sessionID(), runReasonError, "The turn ended."), providerkit.TurnFailure{})
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
	r.agent.endTurn(agent.MessageCompletionInterrupted, runEndRow(r.sessionID(), runReasonAborted, ""), providerkit.TurnFailure{})
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
	r.agent.endTurn(agent.MessageCompletionError, runEndRow(r.sessionID(), runReasonError, "The model failed."), providerkit.TurnFailure{})
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
				r.agent.endTurn(agent.MessageCompletionInterrupted, runEndRow(r.sessionID(), runReasonAborted, ""), providerkit.TurnFailure{})
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

// --- Automatic retry ---

// probeRunEnd returns the captured run-end frame with eventID from
// testdata/probe_run_ends.jsonl: the end event of every run of Cline 3.0.73
// in .tmp/probe/cline/runs. The capture's local directories read /probe.
func probeRunEnd(t *testing.T, eventID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "probe_run_ends.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			EventID string `json:"eventId"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event.EventID == eventID {
			return line
		}
	}
	t.Fatalf("no captured run end has the event id %s", eventID)
	return ""
}

// feedRunEnd hands one captured run-end frame to the agent, moved into the
// rig's session. A timestamp that is not zero replaces the event's own, and a
// negative one removes it.
func (r *rig) feedRunEnd(t *testing.T, line string, timestamp int64) {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &event))
	event["sessionId"] = r.sessionID()
	switch {
	case timestamp < 0:
		delete(event, "timestamp")
	case timestamp > 0:
		event["timestamp"] = timestamp
	}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	r.agent.HandleOutput(data)
}

// shiftedZAITimestamp moves the Z.AI 1308 capture into its window. The capture
// failed at 01:12:30Z, more than five hours before its stamp, 15:00:00 at
// UTC+8, which a real 1308 never states. 03:00:00Z lies inside the window.
var shiftedZAITimestamp = time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC).UnixMilli()

// The classifier reads the research frames of Cline 3.0.73
// (.tmp/retry-research/cline-dsh.md), copied verbatim. The Z.AI frame's
// timestamp moves into its window (shiftedZAITimestamp).
func TestRunEndFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		line        string
		timestamp   int64
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{
			name:      "a Z.AI usage limit schedules at its stamp",
			line:      `{"version":"v1","event":"run.failed","eventId":"hevt_1793149950647_zt72i","sessionId":"1793149888146_4cdoc","timestamp":1793149950647,"payload":{"reason":"error","error":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00 (Request ID: req-probe-1793149950605)","result":{"text":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00 (Request ID: req-probe-1793149950605)","finishReason":"error","iterations":1}},"sequence":13}`,
			timestamp: shiftedZAITimestamp,
			want:      providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 10, 28, 7, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			// At its own timestamp, the stamp lies more than five hours ahead,
			// which the vendor rule rejects. The window of the text decides.
			name: "a Z.AI usage limit with an implausible stamp falls back to its window",
			line: `{"version":"v1","event":"run.failed","eventId":"hevt_1793149950647_zt72i","sessionId":"1793149888146_4cdoc","timestamp":1793149950647,"payload":{"reason":"error","error":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00 (Request ID: req-probe-1793149950605)","result":{"text":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00 (Request ID: req-probe-1793149950605)","finishReason":"error","iterations":1}},"sequence":13}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Z.AI reported a usage limit with an implausible reset stamp"},
		},
		{
			name: "a ClinePass limit lifts at the event timestamp plus its delay",
			line: `{"version":"v1","event":"run.failed","eventId":"hevt_1793150051960_mjlgp","sessionId":"1793150051604_b875q","timestamp":1793150051960,"payload":{"reason":"error","error":"You have reached your 5-hour Clinepass limit. The limit resets in 5h, please try again later.","result":{"text":"You have reached your 5-hour Clinepass limit. The limit resets in 5h, please try again later.","finishReason":"error","iterations":1}},"sequence":11}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150051960).UTC().Add(5 * time.Hour), Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported its ClinePass limit (the provider states when to retry)"},
		},
		{
			name: "an overload is transient",
			line: `{"version":"v1","event":"run.failed","eventId":"hevt_1793149950642_s9w4g","sessionId":"1793149888084_v8mbd","timestamp":1793149950642,"payload":{"reason":"error","error":"Overloaded (Request ID: req-probe-1793149950598)","result":{"text":"Overloaded (Request ID: req-probe-1793149950598)","finishReason":"error","iterations":1}},"sequence":13}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Cline reported a transient failure"},
		},
		{
			name:        "an interrupted overload is not retryable",
			line:        `{"version":"v1","event":"run.failed","eventId":"hevt_1793149950642_s9w4g","sessionId":"1793149888084_v8mbd","timestamp":1793149950642,"payload":{"reason":"error","error":"Overloaded (Request ID: req-probe-1793149950598)","result":{"text":"Overloaded (Request ID: req-probe-1793149950598)","finishReason":"error","iterations":1}},"sequence":13}`,
			interrupted: true,
			want:        providerkit.TurnFailure{},
		},
		{
			name: "a rejected key is not retryable",
			line: `{"version":"v1","event":"run.failed","eventId":"hevt_1793150297680_zl9gi","sessionId":"1793150297365_47kg2","timestamp":1793150297680,"payload":{"reason":"error","error":"Authentication Fails, Your api key is invalid (Request ID: req-probe-1793150297659)","result":{"text":"Authentication Fails, Your api key is invalid (Request ID: req-probe-1793150297659)","finishReason":"error","iterations":1}},"sequence":11}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "a completed run is not retryable",
			line: `{"version":"v1","event":"run.completed","eventId":"hevt_1793149873095_ixmed","sessionId":"1793149872764_qv231","timestamp":1793149873095,"payload":{"reason":"completed","result":{"text":"Hello from the probe.","finishReason":"completed","iterations":1}},"sequence":16}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an overload that ends the run at its mistake limit is not retryable (synthetic)",
			line: `{"event":"run.failed","timestamp":1793149950642,"payload":{"reason":"mistake_limit","error":"Overloaded"}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an error with no reason is not retryable (synthetic)",
			line: `{"event":"run.failed","timestamp":1793149950642,"payload":{"error":"Overloaded"}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an empty error is not retryable (synthetic)",
			line: `{"event":"run.failed","timestamp":1793149950642,"payload":{"reason":"error"}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "a request ID inside the text is not removed (synthetic)",
			line: `{"event":"run.failed","timestamp":1793149950642,"payload":{"reason":"error","error":"Gateway (Request ID: r-1) said: Overloaded"}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "a free model delay with seconds lifts at the timestamp plus the whole delay (synthetic)",
			line: `{"event":"run.failed","timestamp":1793150051960,"payload":{"reason":"error","error":"Error 429: Daily free limit reached on model m. Try again in 1h 2m 3s"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150051960).UTC().Add(time.Hour + 2*time.Minute + 3*time.Second), Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported the free limit of the model (the provider states when to retry)"},
		},
		{
			name: "a free model delay that repeats a unit falls back to the window (synthetic)",
			line: `{"event":"run.failed","timestamp":1793150051960,"payload":{"reason":"error","error":"Error 429: Daily free limit reached on model m. Try again in 1h 2h"}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported the free limit of the model"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var event hubEvent
			require.NoError(t, json.Unmarshal([]byte(tc.line), &event))
			if tc.timestamp != 0 {
				event.Timestamp = tc.timestamp
			}
			var end runEnd
			require.NoError(t, json.Unmarshal(event.Payload, &end))
			fallback := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
			assert.Equal(t, tc.want, runEndFailure(end, tc.interrupted, event.publishedAt(fallback)))
		})
	}
}

func TestHubEventPublishedAt(t *testing.T) {
	t.Parallel()
	fallback := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, time.UnixMilli(1793150051960).UTC(), hubEvent{Timestamp: 1793150051960}.publishedAt(fallback))
	assert.Equal(t, fallback, hubEvent{}.publishedAt(fallback), "an absent timestamp reads the fallback")
	assert.Equal(t, fallback, hubEvent{Timestamp: -5}.publishedAt(fallback), "a negative timestamp reads the fallback")
}

// The captured end of each run reaches the real handler, and its verdict
// follows the turn-end row.
func TestClineRunEndsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Cline reported a transient failure"}
	for _, tc := range []struct {
		name      string
		eventID   string
		timestamp int64
		want      providerkit.TurnFailure
	}{
		{
			name: "a Z.AI usage limit schedules at its stamp", eventID: "hevt_1793149950647_zt72i", timestamp: shiftedZAITimestamp,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 10, 28, 7, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			name: "a Z.AI weekly limit schedules at its stamp", eventID: "hevt_1793149950688_fuo6o",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 11, 3, 2, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			name: "a ClinePass limit schedules at the event timestamp plus its delay", eventID: "hevt_1793150051960_mjlgp",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150051960).UTC().Add(5 * time.Hour), Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported its ClinePass limit (the provider states when to retry)"},
		},
		{
			name: "a free model limit schedules at the event timestamp plus its delay", eventID: "hevt_1793150051960_atqa3",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150051960).UTC().Add(23*time.Hour + 59*time.Minute), Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported the free limit of the model (the provider states when to retry)"},
		},
		{
			name: "no ClinePass subscription is a billing block", eventID: "hevt_1793150051960_bo7ah",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Cline reported that the account has no ClinePass access"},
		},
		{
			name: "a Z.AI balance block is a billing block", eventID: "hevt_1793149950642_75pou",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{
			name: "an expired Z.AI package is a billing block", eventID: "hevt_1793149950671_gs6pu",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{name: "an overload is transient", eventID: "hevt_1793149950642_s9w4g", want: transient},
		{name: "a 502 page is transient", eventID: "hevt_1793150176243_6fwgg", want: transient},
		{name: "a refused connection is transient", eventID: "hevt_1793150176243_uige4", want: transient},
		{name: "a stream that ended early is transient", eventID: "hevt_1793149889511_oip01", want: transient},
		{
			name: "a Z.AI rate limit is rate limited", eventID: "hevt_1793149898680_ohi9z",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{name: "an unknown provider message is not retryable", eventID: "hevt_1793149950564_5qiqp", want: providerkit.TurnFailure{}},
		{name: "a rejected key is not retryable", eventID: "hevt_1793150297680_zl9gi", want: providerkit.TurnFailure{}},
		{name: "a full context is not retryable", eventID: "hevt_1793149888636_uapeu", want: providerkit.TurnFailure{}},
		{name: "a completed run is not retryable", eventID: "hevt_1793149873095_ixmed", want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.feedTurn(t)
			r.feedRunEnd(t, probeRunEnd(t, tc.eventID), tc.timestamp)
			assert.False(t, r.turnActive())
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
		})
	}
}

// A run that the user interrupted never retries, even when it failed.
func TestClineInterruptedRunDoesNotRetry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.agent.Mu.Lock()
	r.agent.turn.interruptRequested = true
	r.agent.Mu.Unlock()
	r.feedRunEnd(t, probeRunEnd(t, "hevt_1793149950642_s9w4g"), 0)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
}

// A run end that states no timestamp reads the agent's clock instead.
func TestClineRunEndWithNoTimestampReadsTheClock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	before := time.Now()
	r.feedRunEnd(t, probeRunEnd(t, "hevt_1793150051960_mjlgp"), -1)
	after := time.Now()
	timeline := providerkittest.TurnEndAndRetryOps(r.sink.Timeline())
	require.Len(t, timeline, 3)
	assert.Equal(t, "turn_end", timeline[0].Op)
	assert.Equal(t, "schedule_usage_limit", timeline[1].Op)
	assert.False(t, timeline[1].ResetAt.Before(before.Add(5*time.Hour)))
	assert.False(t, timeline[1].ResetAt.After(after.Add(5*time.Hour)))
	assert.Equal(t, "cancel_server_error", timeline[2].Op)
}

// A ClinePass delay that is zero or more than 31 days never schedules a
// usage-limit retry: zero would fire at once and repeat for ever. The window of
// the limit decides instead. The texts are the captured ClinePass text with
// another delay.
func TestClinePassDelayOutsideTheLimitsFallsBackToTheWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		error string
		want  providerkit.TurnFailure
	}{
		{
			name:  "a zero delay",
			error: "You have reached your 5-hour Clinepass limit. The limit resets in 0h, please try again later.",
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Cline reported its ClinePass limit"},
		},
		{
			name:  "a delay of more than 31 days",
			error: "You have reached your 40-day Clinepass limit. The limit resets in 40d, please try again later.",
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Cline reported its ClinePass limit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, contracts.ClineEventRunFailed, map[string]any{"reason": contracts.ClineRunReasonError, "error": tc.error})
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
		})
	}
}

// A turn end that the worker writes states the worker's own error, not
// Cline's, so it never retries. A failed reply after the run started is one:
// its message reads like a 503, and still nothing schedules.
func TestClineWorkerWrittenTurnEndDoesNotRetry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.hub.reply(requestID, fakeReply{Code: "runtime_error", Message: "Service Unavailable"})
	waitFor(t, func() bool { return !r.turnActive() }, "the failed reply ends the turn")
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
}

// A run end with no turn running ends nothing, so it applies no verdict.
func TestClineRunEndWithNoTurnAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedRunEnd(t, probeRunEnd(t, "hevt_1793149950642_s9w4g"), 0)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
}

// A discarded turn writes no row, so it applies no verdict.
func TestClineDiscardedRunEndAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.agent.DiscardOutput()
	r.feedRunEnd(t, probeRunEnd(t, "hevt_1793149950642_s9w4g"), 0)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
}
