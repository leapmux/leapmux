package codewhale

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// messagesWithSpan returns the persisted rows of one span.
func messagesWithSpan(sink *agenttest.ControlSink, spanID string) []agenttest.Message {
	var rows []agenttest.Message
	for _, message := range sink.Messages() {
		if message.SpanID == spanID {
			rows = append(rows, message)
		}
	}
	return rows
}

func TestAMessageIsPersistedFromItsFinalEvent(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "item.delta", testTurnID, "item_2", map[string]any{"delta": "Hel", "kind": "agent_message"}))
	a.HandleOutput(runtimeEvent(2, "item.delta", testTurnID, "item_2", map[string]any{"delta": "lo.", "kind": "agent_message"}))
	final := itemEvent(3, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "Hello.", nil)
	a.HandleOutput(final)

	messages := sink.Messages()
	require.Len(t, messages, 1, "the deltas feed progress, and the final event is the row")
	assert.Equal(t, final, messages[0].Content, "the runtime's own event is stored byte for byte")
	assert.Equal(t, agent.MessageCompletionComplete, messages[0].Completion)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[0].Source)
	assert.NotEmpty(t, sink.ProgressUpdates())
}

func TestAMessageWithNoTextKeepsItsStreamedText(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "item.delta", testTurnID, "item_2", map[string]any{"delta": "Thinking", "kind": "agent_reasoning"}))
	a.HandleOutput(itemEvent(2, "item.interrupted", "item_2", contracts.CodewhaleItemKindAgentReasoning, "", nil))

	want, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindReasoning, "Thinking", agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, want, messages[0].Content)
}

func TestKnownInternalItemsAreDroppedAndUnknownItemsPersist(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	// LeapMux already stored the reader's own message.
	a.HandleOutput(itemEvent(1, "item.completed", "item_1", itemKindUserMessage, "Say hello.", nil))
	// The runtime's own loop.
	for i, summary := range []string{"Continuing — tool results", "Executing tools sequentially (writes)", "Loaded deferred tool 'apply_patch'.", "Policy: Full Access / ACT", "Permissions: Ask · Work"} {
		a.HandleOutput(itemEvent(uint64(2+i), "item.completed", "item_2", contracts.CodewhaleItemKindStatus, summary, nil))
	}
	a.HandleOutput(itemEvent(10, "item.completed", "item_3", contracts.CodewhaleItemKindStatus, "An internal note", map[string]any{"visibility": "internal"}))
	// A delta of another kind, and an item of no kind this build knows.
	a.HandleOutput(runtimeEvent(11, "item.delta", testTurnID, "item_4", map[string]any{"delta": "x", "kind": "tool_call"}))
	a.HandleOutput(itemEvent(12, "item.completed", "item_5", "a_later_kind", "x", nil))

	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "a_later_kind")
	assert.Empty(t, sink.PersistedNotifications())
}

func TestNoticesArePersistedAsNotifications(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	events := [][]byte{
		itemEvent(1, "item.completed", "item_1", contracts.CodewhaleItemKindStatus, "Checkpoint saved", nil),
		itemEvent(2, "item.completed", "item_2", contracts.CodewhaleItemKindContextCompaction, "Compaction complete", nil),
		itemEvent(3, "item.failed", "item_3", contracts.CodewhaleItemKindError, "Rate limited", nil),
		runtimeEvent(4, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "", map[string]any{"input": "more", "reason": "ended"}),
		runtimeEvent(5, contracts.CodewhaleEventApprovalTimeout, testTurnID, "", map[string]any{"approval_id": "ap1", "timeout_secs": 300}),
		runtimeEvent(6, contracts.CodewhaleEventSandboxDenied, testTurnID, "", map[string]any{"tool_name": "bash", "reason": "network"}),
		runtimeEvent(7, contracts.CodewhaleEventStoreFailure, "", "", map[string]any{"message": "disk full"}),
	}
	for _, raw := range events {
		a.HandleOutput(raw)
	}
	assert.Len(t, sink.PersistedNotifications(), len(events)-1, "the error waits for its turn")
	a.HandleOutput(turnCompletedEvent(8, testTurnID, contracts.CodewhaleTurnStatusFailed))
	notifications := sink.PersistedNotifications()
	want := [][]byte{events[0], events[1], events[3], events[4], events[5], events[6], events[2]}
	require.Len(t, notifications, len(want))
	for i, raw := range want {
		assert.Equal(t, raw, notifications[i].Content)
	}
}

func TestAToolCallOpensAndClosesItsSpan(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	start := toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "ls"})
	a.HandleOutput(start)
	end := toolEndEvent(3, "item.completed", "item_1", "call_1", contracts.CodewhaleToolBash, "a.ts", map[string]any{"command": "ls"}, map[string]any{"exit_code": 0})
	a.HandleOutput(end)

	rows := messagesWithSpan(sink, "call_1")
	require.Len(t, rows, 2)
	assert.Equal(t, start, rows[0].Content)
	assert.False(t, rows[0].Closing)
	assert.Equal(t, contracts.CodewhaleToolBash, rows[0].SpanType)
	assert.Equal(t, end, rows[1].Content)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, agent.MessageCompletionComplete, rows[1].Completion)
	assert.Contains(t, sink.ClosedSpans(), "call_1")
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestAToolCallStatesHowItEnded(t *testing.T) {
	t.Parallel()
	for event, want := range map[string]agent.MessageCompletion{
		"item.failed":      agent.MessageCompletionError,
		"item.interrupted": agent.MessageCompletionInterrupted,
		"item.canceled":    agent.MessageCompletionInterrupted,
	} {
		a, sink := newTestAgent(t, nil)
		a.HandleOutput(toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "ls"}))
		a.HandleOutput(toolEndEvent(2, event, "item_1", "call_1", contracts.CodewhaleToolBash, "x", nil, nil))
		rows := messagesWithSpan(sink, "call_1")
		require.Len(t, rows, 2, event)
		assert.Equal(t, want, rows[1].Completion, event)
	}
}

func TestAToolResultFindsItsSpanByItem(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolRequestUserInput, map[string]any{"questions": []any{}}))
	a.HandleOutput(userInputEvent(2, "call_1"))
	// The redacted question result names its call by tool_call_id alone.
	a.HandleOutput(redactedQuestionResult(3, "item_1", "call_1"))
	// A final event that states no tool finds the span by its item.
	a.HandleOutput(toolStartEvent(4, "item_2", "call_2", contracts.CodewhaleToolBash, map[string]any{"command": "ls"}))
	a.HandleOutput(runtimeEvent(5, "item.completed", testTurnID, "item_2", map[string]any{"item": map[string]any{"id": "item_2", "kind": "tool_call", "detail": "a.ts"}}))

	assert.Len(t, messagesWithSpan(sink, "call_1"), 2)
	rows := messagesWithSpan(sink, "call_2")
	require.Len(t, rows, 2)
	assert.Equal(t, contracts.CodewhaleToolBash, rows[1].SpanType, "the span keeps the name its start stated")
}

// redactedQuestionResult is the final event of a `request_user_input` call, as
// the runtime sends it: the answer, and anything else the call said, redacted.
func redactedQuestionResult(seq uint64, itemID, callID string) []byte {
	return runtimeEvent(seq, "item.completed", testTurnID, itemID, map[string]any{
		"item": map[string]any{
			"id": itemID, "kind": "tool_call", "status": "completed", "summary": "User input submitted", "detail": "User input submitted",
			"metadata": map[string]any{"tool_call_id": callID, "tool_name": contracts.CodewhaleToolRequestUserInput, "response_redacted": true},
		},
	})
}

var questionInput = map[string]any{"questions": testQuestions}

// The FIRST call of `request_user_input` loads its schema and asks nothing, and
// the runtime redacts its result, so only the missing question tells it apart.
func TestAQuestionCallThatAsksNothingLeavesNoRow(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_load", contracts.CodewhaleToolRequestUserInput, questionInput))
	assert.Empty(t, messagesWithSpan(sink, "call_load"), "the start row waits for the question")
	a.HandleOutput(redactedQuestionResult(3, "item_1", "call_load"))

	assert.Empty(t, messagesWithSpan(sink, "call_load"))
	assert.Empty(t, sink.OpenSpans(), "no span opened for a call that never ran")
	assert.Empty(t, sink.ClosedSpans())
	assert.Zero(t, a.TurnToolUses, "the runtime did not run the call")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.tools.bySpan, "the call is forgotten")
}

func TestAQuestionCallIsPersistedOnceItAsks(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	start := toolStartEvent(2, "item_1", "call_ask", contracts.CodewhaleToolRequestUserInput, questionInput)
	a.HandleOutput(start)
	a.HandleOutput(userInputEvent(3, "call_ask"))

	rows := messagesWithSpan(sink, "call_ask")
	require.Len(t, rows, 1, "the question proves that the call runs")
	assert.JSONEq(t, string(start), string(rows[0].Content), "the row is the call's own start event")
	assert.False(t, rows[0].Closing)
	require.Len(t, sink.OpenSpans(), 1)
	assert.Equal(t, "call_ask", sink.OpenSpans()[0].SpanID)
	assert.Equal(t, "user_input:call_ask", sink.LastPublishedControl().RequestID)

	// A repeated question publishes no second start row.
	a.HandleOutput(userInputEvent(4, "call_ask"))
	assert.Len(t, messagesWithSpan(sink, "call_ask"), 1)

	a.HandleOutput(redactedQuestionResult(5, "item_1", "call_ask"))
	rows = messagesWithSpan(sink, "call_ask")
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, contracts.CodewhaleToolRequestUserInput, rows[1].SpanType)
	assert.Equal(t, []string{"call_ask"}, sink.ClosedSpans())
	assert.Equal(t, 1, a.TurnToolUses)
}

// A call that fails before it asks still ran: its failure needs the call it
// belongs to.
func TestAQuestionCallThatFailsBeforeItAsksKeepsBothRows(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_bad", contracts.CodewhaleToolRequestUserInput, map[string]any{"questions": []any{}}))
	a.HandleOutput(toolEndEvent(3, "item.failed", "item_1", "call_bad", contracts.CodewhaleToolRequestUserInput, "questions must not be empty", nil, nil))

	rows := messagesWithSpan(sink, "call_bad")
	require.Len(t, rows, 2)
	assert.False(t, rows[0].Closing, "the start row goes in first")
	assert.True(t, rows[1].Closing)
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestAQuestionCallThatTheTurnCutOffKeepsBothRows(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_cut", contracts.CodewhaleToolRequestUserInput, questionInput))
	a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusInterrupted))

	rows := messagesWithSpan(sink, "call_cut")
	require.Len(t, rows, 2)
	assert.False(t, rows[0].Closing)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestADeferredFirstCallDoesNotCount(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolApplyPatch, map[string]any{"patch": "x"}))
	a.HandleOutput(toolEndEvent(3, "item.completed", "item_1", "call_1", contracts.CodewhaleToolApplyPatch, "Tool `apply_patch` was deferred", nil, map[string]any{"deferred_tool_loaded": true}))

	assert.Len(t, messagesWithSpan(sink, "call_1"), 2, "both rows persist; the browser hides the result row")
	assert.Zero(t, a.TurnToolUses, "the runtime did not run the call")
}

func TestAReplayedStartOpensNoSecondSpan(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	start := toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "ls"})
	a.HandleOutput(start)
	a.openToolCall(codewhaleEnvelope{raw: start}, "item_1", "call_1", contracts.CodewhaleToolBash, nil)
	assert.Len(t, messagesWithSpan(sink, "call_1"), 1)
}

func TestTurnEndPersistsTheTurnAndItsToolCount(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "ls"}))
	a.HandleOutput(toolEndEvent(3, "item.completed", "item_1", "call_1", contracts.CodewhaleToolBash, "a", nil, nil))
	a.HandleOutput(runtimeEvent(4, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 100, "output_tokens": 5}}))
	// A call the turn end cuts off.
	a.HandleOutput(toolStartEvent(5, "item_2", "call_2", contracts.CodewhaleToolBash, map[string]any{"command": "sleep 9"}))
	end := turnCompletedEvent(6, testTurnID, contracts.CodewhaleTurnStatusInterrupted)
	a.HandleOutput(end)

	var turnEnd *agenttest.Message
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			turnEnd = &message
		}
	}
	require.NotNil(t, turnEnd)
	assert.Equal(t, end, turnEnd.Content)
	metadata := decodeJSON(t, turnEnd.Metadata)
	assert.EqualValues(t, 1, metadata[contracts.MessageMetadataFieldToolUses])
	assert.Contains(t, metadata, contracts.SessionInfoKeyContextUsage)

	retained := messagesWithSpan(sink, "call_2")
	require.Len(t, retained, 2)
	assert.True(t, retained[1].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, retained[1].Completion)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestTurnCompletion(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.MessageCompletionComplete, turnCompletion(contracts.CodewhaleTurnStatusCompleted))
	assert.Equal(t, agent.MessageCompletionInterrupted, turnCompletion(contracts.CodewhaleTurnStatusInterrupted))
	assert.Equal(t, agent.MessageCompletionInterrupted, turnCompletion(contracts.CodewhaleTurnStatusCanceled))
	assert.Equal(t, agent.MessageCompletionError, turnCompletion(contracts.CodewhaleTurnStatusFailed))
	assert.Equal(t, agent.MessageCompletionError, turnCompletion("a_later_status"))
}

func TestDiscardedOutputPersistsNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.DiscardOutput()
	a.HandleOutput(itemEvent(1, "item.completed", "item_2", contracts.CodewhaleItemKindAgentMessage, "Hello.", nil))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "ls"}))
	a.HandleOutput(itemEvent(3, "item.completed", "item_3", contracts.CodewhaleItemKindStatus, "Checkpoint saved", nil))
	a.HandleOutput(approvalEvent(4, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(turnStartedEvent(5, testTurnID))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.OpenSpans())
	assert.Zero(t, sink.PublishedControlCount())
	assert.Empty(t, sink.TurnActives())
	assert.Zero(t, a.lastSeq, "a discarded event does not count as dispatched")
}

// A process that restarts after a discard leaves no streamed text behind: the
// text belongs to the output that the discard dropped.
func TestAnExitAfterADiscardPersistsNoStreamedText(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "sleep 9"}))
	a.HandleOutput(runtimeEvent(3, "item.delta", testTurnID, "item_2", map[string]any{"delta": "partial", "kind": "agent_message"}))
	require.Len(t, sink.Messages(), 1, "the call's start row")

	a.DiscardOutput()
	a.stopProcess()
	require.NoError(t, a.Wait())
	assert.Len(t, sink.Messages(), 1, "the sink stores neither the streamed text nor a closing row")
	assert.Contains(t, sink.ClosedSpans(), "call_1", "the span still closes")
}

// The agent reads a turn end whose payload states no turn from its envelope.
// Its status is unknown, so the agent closes what it cut off as an error.
func TestATurnEndWithNoTurnRecordUsesTheEnvelope(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]any{"an empty payload": map[string]any{}, "a payload of no shape": "x"} {
		a, sink := newTestAgent(t, nil)
		a.HandleOutput(turnStartedEvent(1, testTurnID))
		a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "sleep 9"}))
		end := runtimeEvent(3, contracts.CodewhaleEventTurnCompleted, testTurnID, "", payload)
		a.HandleOutput(end)

		assert.Equal(t, []bool{true, false}, sink.TurnActives(), name)
		rows := messagesWithSpan(sink, "call_1")
		require.Len(t, rows, 2, name)
		assert.Equal(t, agent.MessageCompletionError, rows[1].Completion, name)
		var turnEnds int
		for _, message := range sink.Messages() {
			if message.TurnEnd {
				turnEnds++
				assert.Equal(t, end, message.Content, name)
			}
		}
		assert.Equal(t, 1, turnEnds, name)
	}
}

// A tool item that states no call id has no span to open, and its end finds
// none to close.
func TestAToolItemWithNoCallIDPersistsNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	item := map[string]any{"id": "item_1", "kind": "tool_call", "metadata": map[string]any{"tool_name": contracts.CodewhaleToolBash}}
	a.HandleOutput(runtimeEvent(1, contracts.CodewhaleEventItemStarted, testTurnID, "item_1", map[string]any{"item": item}))
	a.HandleOutput(runtimeEvent(2, contracts.CodewhaleEventItemCompleted, testTurnID, "item_1", map[string]any{"item": item}))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.OpenSpans())
	assert.Zero(t, a.TurnToolUses)
}

func errorTurnEnd(seq uint64, turnID string, failure any) []byte {
	return runtimeEvent(seq, contracts.CodewhaleEventTurnCompleted, turnID, "", map[string]any{"turn": map[string]any{"id": turnID, "status": contracts.CodewhaleTurnStatusFailed, "error": failure}})
}

func TestCodewhaleErrorItemEqualToTheTurnErrorStoresNoRow(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	// Trimmed shape from .tmp/probe/codewhale/logs/runtime-events.jsonl, frames 56 and 58.
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemFailed, "error-item", contracts.CodewhaleItemKindError, "  Invalid request (400): probe invalid request ", nil))
	assert.Empty(t, sink.PersistedNotifications(), "the error waits for its turn outcome")
	a.HandleOutput(errorTurnEnd(2, testTurnID, "Invalid request (400): probe invalid request"))
	assert.Empty(t, sink.PersistedNotifications())
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
}

func TestCodewhaleErrorItemWithOtherTextKeepsItsRow(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	raw := itemEvent(1, contracts.CodewhaleEventItemFailed, "error-item", contracts.CodewhaleItemKindError, "The first failure.", nil)
	a.HandleOutput(raw)
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput(errorTurnEnd(2, testTurnID, "The final failure."))
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, raw, rows[0].Content)
}

func TestCodewhaleHeldErrorPersistsAtExit(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	raw := itemEvent(1, contracts.CodewhaleEventItemFailed, "error-item", contracts.CodewhaleItemKindError, "The process failed.", nil)
	a.HandleOutput(raw)
	assert.Empty(t, sink.PersistedNotifications())
	a.finishAfterExit(agent.MessageCompletionError)
	a.finishAfterExit(agent.MessageCompletionError)
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, raw, rows[0].Content)
}

func TestCodewhaleHeldErrorIsComparedOnlyWithItsOwnTurn(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	raw := itemEvent(1, contracts.CodewhaleEventItemFailed, "error-item", contracts.CodewhaleItemKindError, "The first failure.", nil)
	a.HandleOutput(raw)
	a.HandleOutput(errorTurnEnd(2, "another-turn", "The first failure."))
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput(errorTurnEnd(3, testTurnID, "Another failure."))
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, raw, rows[0].Content)
}

func TestCodewhaleErrorItemUsesItsSummaryWhenDetailIsBlank(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, contracts.CodewhaleEventItemFailed, testTurnID, "error-item", map[string]any{"item": map[string]any{"id": "error-item", "turn_id": testTurnID, "kind": contracts.CodewhaleItemKindError, "detail": " ", "summary": " The failure. "}}))
	a.HandleOutput(errorTurnEnd(2, testTurnID, "The failure."))
	assert.Empty(t, sink.PersistedNotifications())
}

func TestCodewhaleErrorWithoutATurnPersistsAtOnce(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	raw := runtimeEvent(1, contracts.CodewhaleEventItemFailed, "", "error-item", map[string]any{"item": map[string]any{"id": "error-item", "kind": contracts.CodewhaleItemKindError, "detail": "The unowned error."}})
	a.HandleOutput(raw)
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, raw, rows[0].Content)
}

func TestCodewhaleHeldErrorIsDroppedDuringDiscard(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemFailed, "error-item", contracts.CodewhaleItemKindError, "The stopped error.", nil))
	a.DiscardOutput()
	a.finishAfterExit(agent.MessageCompletionInterrupted)
	assert.Empty(t, sink.PersistedNotifications())
}

func TestCodewhaleStreamResumeDiscardsThePartial(t *testing.T) {
	t.Parallel()
	// Trimmed from .tmp/probe/codewhale/logs/runtime-events.jsonl, frames 7-19.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "stream resume", FramesPath: "testdata/stream_resume.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a, _ := newTestAgentWith(t, nil, testAgentOptions{services: func(agent.ProviderServices) agent.ProviderServices { return agent.NewProviderServices(sink) }})
			return a.HandleOutput
		},
		TextOf: func(row agenttest.Message) (agenttest.RetryAttemptText, bool) {
			if kind, text, completion, ok := agenttest.DecodeAssembledMessage(row.Content); ok && kind == agent.AssembledMessageKindText {
				return agenttest.RetryAttemptText{Text: text, Completion: completion}, true
			}
			env, ok := parseEnvelope(row.Content)
			if !ok {
				return agenttest.RetryAttemptText{}, false
			}
			payload, ok := parseItemPayload(env)
			if !ok || payload.Item.Kind != contracts.CodewhaleItemKindAgentMessage {
				return agenttest.RetryAttemptText{}, false
			}
			return agenttest.RetryAttemptText{Text: payload.Item.Detail, Completion: row.Completion}, true
		},
		Want: []agenttest.RetryAttemptText{{Text: "RETRIED-ANSWER", Completion: agent.MessageCompletionComplete}}, Discarded: []string{"DISCARDED-"},
	})
}

func TestCodewhaleStreamResumeDiscardsAPartialThought(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemStarted, "thought-one", contracts.CodewhaleItemKindAgentReasoning, "", nil))
	a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "thought-one", map[string]any{"kind": contracts.CodewhaleItemKindAgentReasoning, "delta": "DISCARDED-THOUGHT"}))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemStarted, "thought-two", contracts.CodewhaleItemKindAgentReasoning, "", nil))
	a.HandleOutput(itemEvent(4, contracts.CodewhaleEventItemCompleted, "thought-two", contracts.CodewhaleItemKindAgentReasoning, "The next thought.", nil))
	a.HandleOutput(turnCompletedEvent(5, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.NotContains(t, string(row.Content), "DISCARDED-")
	}
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelScopeProgress("thought-one"))
}

type blockedCodewhaleMessages struct {
	agent.ProviderServices
	blocked atomic.Bool
}

func (s *blockedCodewhaleMessages) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.blocked.Load() {
		return errors.New("the controlled message write failed")
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func TestCodewhaleStreamResumeKeepsPendingWritesAcrossTurns(t *testing.T) {
	t.Parallel()
	var blocked *blockedCodewhaleMessages
	a, sink := newTestAgentWith(t, nil, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
		blocked = &blockedCodewhaleMessages{ProviderServices: inner}
		return blocked
	}})
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "old-item", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "RETAINED-ANSWER"}))
	blocked.blocked.Store(true)
	a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Empty(t, sink.Messages())
	a.HandleOutput(turnStartedEvent(4, "next-turn"))
	blocked.blocked.Store(false)
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, _, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "RETAINED-ANSWER", text)
	assert.True(t, rows[1].TurnEnd)
}

func TestCodewhaleStreamResumeRepeatedStartKeepsTheOpenItem(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemStarted, "text", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "text", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The answer."}))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemStarted, "text", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(itemEvent(4, contracts.CodewhaleEventItemCompleted, "text", contracts.CodewhaleItemKindAgentMessage, "", nil))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	_, text, _, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "The answer.", text)
	completed := 0
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.CompleteModelProgress("text").Operation && update.ScopeID == "text" {
			completed++
		}
	}
	assert.Equal(t, 1, completed)
}

func TestCodewhaleStreamResumeKeepsTheOtherGeneratedKind(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemStarted, "text", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "text", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The answer."}))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemStarted, "thought", contracts.CodewhaleItemKindAgentReasoning, "", nil))
	a.HandleOutput(runtimeEvent(4, eventItemDelta, testTurnID, "thought", map[string]any{"kind": contracts.CodewhaleItemKindAgentReasoning, "delta": "The thought."}))
	a.HandleOutput(turnCompletedEvent(5, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	rows := sink.Messages()
	require.Len(t, rows, 3)
	for i, want := range []string{"The answer.", "The thought."} {
		_, text, _, ok := agenttest.DecodeAssembledMessage(rows[i].Content)
		require.True(t, ok)
		assert.Equal(t, want, text)
	}
}

func TestCodewhaleStreamResumeLateFinalKeepsTheCurrentItemIdentity(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemStarted, "old", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "second", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "second", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "DISCARDED-SECOND"}))
	a.HandleOutput(itemEvent(4, contracts.CodewhaleEventItemInterrupted, "old", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(itemEvent(5, contracts.CodewhaleEventItemStarted, "third", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(itemEvent(6, contracts.CodewhaleEventItemCompleted, "third", contracts.CodewhaleItemKindAgentMessage, "The third answer.", nil))
	a.HandleOutput(turnCompletedEvent(7, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.NotContains(t, string(row.Content), "DISCARDED-")
	}
}

func TestCodewhaleAutomaticCompactionStartIsLive(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, exists)
	assert.Equal(t, map[string]any{}, value)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestCodewhaleCompactionTurnWithoutANativeEndWritesAnEnd(t *testing.T) {
	t.Parallel()
	for _, status := range []string{contracts.CodewhaleTurnStatusCompleted, contracts.CodewhaleTurnStatusFailed, contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled} {
		t.Run(status, func(t *testing.T) {
			a, sink := newTestAgent(t, nil)
			a.HandleOutput(turnStartedEvent(1, testTurnID))
			a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))
			a.HandleOutput(turnCompletedEvent(3, testTurnID, status))
			value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, exists)
			assert.Nil(t, value)
			notices := sink.Notifications()
			require.Len(t, notices, 1)
			outcome := contracts.CompactionEndOutcomeFailed
			if status == contracts.CodewhaleTurnStatusInterrupted || status == contracts.CodewhaleTurnStatusCanceled {
				outcome = contracts.CompactionEndOutcomeAborted
			}
			assert.Equal(t, contracts.NotificationTypeCompactionEnded, notices[0][contracts.NotificationFieldType])
			assert.Equal(t, outcome, notices[0][contracts.NotificationFieldOutcome])
			assert.NotContains(t, notices[0], contracts.NotificationFieldError)
		})
	}
}

func TestCodewhaleStaleTurnEndKeepsCurrentCompactionAndText(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))
	a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "text", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The current text."}))
	a.HandleOutput(turnCompletedEvent(4, "old-turn", contracts.CodewhaleTurnStatusCompleted))
	_, text, present := a.generationBuffer.ScopeSnapshot("text")
	assert.True(t, present)
	assert.Equal(t, "The current text.", text)
	assert.Empty(t, sink.Messages())
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, exists)
	assert.Equal(t, map[string]any{}, value)
	assert.False(t, a.markTurnStarted("old-turn"))
}

func TestCodewhaleOrdinaryTurnCreatesNoCompactionState(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	_, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.False(t, exists)
}

func TestCodewhaleCompactionExitClearsTheLiveState(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))
	a.finishAfterExit(agent.MessageCompletionError)
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, exists)
	assert.Nil(t, value)
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Notifications())
}

func TestCodewhaleRepeatedTurnEndRetriesOnlyThePendingWrites(t *testing.T) {
	t.Parallel()
	var blocked *blockedCodewhaleMessages
	a, sink := newTestAgentWith(t, nil, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
		blocked = &blockedCodewhaleMessages{ProviderServices: inner}
		return blocked
	}})
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "text", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The retained answer."}))
	blocked.blocked.Store(true)
	a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Empty(t, sink.Messages())
	blocked.blocked.Store(false)
	a.HandleOutput(turnCompletedEvent(4, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, _, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "The retained answer.", text)
	assert.True(t, rows[1].TurnEnd)
	a.HandleOutput(turnCompletedEvent(5, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Len(t, sink.Messages(), 2)
}

func TestCodewhaleKnownToolKindsWithoutIdentityStayInternal(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{contracts.CodewhaleItemKindToolCall, contracts.CodewhaleItemKindFileChange, contracts.CodewhaleItemKindCommandExecution} {
		t.Run(kind, func(t *testing.T) {
			a, sink := newTestAgent(t, nil)
			a.HandleOutput(itemEvent(1, contracts.CodewhaleEventItemStarted, "incomplete", kind, "", nil))
			a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemCompleted, "incomplete", kind, "", nil))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.OpenSpans())
		})
	}
}

func TestCodewhaleInvalidRequestSnapshotKeepsOpenOutput(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []any{nil, map[string]any{}, map[string]any{"turn_id": map[string]any{"value": "engine", "truncated": false}, "step": -1}, map[string]any{"turn_id": map[string]any{"value": "engine", "truncated": true}, "step": 0}, map[string]any{"turn_id": map[string]any{"value": "engine"}, "step": 0}} {
		a, sink := newTestAgent(t, nil)
		a.HandleOutput(turnStartedEvent(1, testTurnID))
		a.HandleOutput(runtimeEvent(2, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The open answer."}))
		a.HandleOutput(runtimeEvent(3, eventModelToolsSnapshot, testTurnID, "", map[string]any{"snapshot": snapshot}))
		_, text, present := a.generationBuffer.ScopeSnapshot("answer")
		assert.True(t, present)
		assert.Equal(t, "The open answer.", text)
		assert.Empty(t, sink.Messages())
	}
}

// A turn that did not fail cancels the pending server-error retry once, after
// its turn-end row. A failed turn whose error states nothing retryable does too.
func TestATurnEndCancelsThePendingServerErrorRetry(t *testing.T) {
	t.Parallel()
	for _, status := range []string{
		contracts.CodewhaleTurnStatusCompleted, contracts.CodewhaleTurnStatusFailed,
		contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, sink := newTestAgent(t, nil)
			a.HandleOutput(turnStartedEvent(1, testTurnID))
			assert.Zero(t, sink.AutoCancelCount(), "a turn start cancels nothing")

			a.HandleOutput(turnCompletedEvent(2, testTurnID, status))

			require.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
		})
	}
}

// Captured turn.completed frames of Codewhale 0.10.1. The first four are the
// fixtures of .tmp/retry-research/partial-providers.md (section 5). The 529 and
// the 1308 come from .tmp/probe/codewhale/run2-m529.jsonl and
// run2-glm1308.jsonl, trimmed to the same fields as those fixtures.
const (
	codewhaleTurnFailed500          = `{"event":"turn.completed","turn_id":"turn_1ce987c70ec44e9fbbc63a1212534da9","payload":{"turn":{"id":"turn_1ce987c70ec44e9fbbc63a1212534da9","status":"failed","error":"Server error (500): Internal server error","modelRequestDiagnostics":{"modelRequestsStarted":1,"transportRetries":2,"transparentStreamRetries":0,"streamResumes":0,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
	codewhaleTurnFailedNetwork      = `{"event":"turn.completed","turn_id":"turn_c6e78e2e0d064f2f97f8d080a72f8559","payload":{"turn":{"id":"turn_c6e78e2e0d064f2f97f8d080a72f8559","status":"failed","error":"Network error: Cannot reach 127.0.0.1:18999 (connection refused): error sending request for url (http://127.0.0.1:18999/v1/chat/completions): client error (Connect): tcp connect error: Connection refused (os error 61)","modelRequestDiagnostics":{"modelRequestsStarted":4,"transportRetries":8,"transparentStreamRetries":0,"streamResumes":3,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
	codewhaleTurnFailedStreamClosed = `{"event":"turn.completed","turn_id":"turn_ced05a34dd16451b9275a9163032a808","payload":{"turn":{"id":"turn_ced05a34dd16451b9275a9163032a808","status":"failed","error":"Chat Completions stream closed before [DONE] or finish_reason","modelRequestDiagnostics":{"modelRequestsStarted":1,"transportRetries":0,"transparentStreamRetries":0,"streamResumes":0,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
	codewhaleTurnFailedRateLimit    = `{"event":"turn.completed","turn_id":"turn_658b36f448674ae4be9693f23d5a820a","payload":{"turn":{"id":"turn_658b36f448674ae4be9693f23d5a820a","status":"failed","error":"Rate limit exceeded: Rate limit reached for requests","modelRequestDiagnostics":{"modelRequestsStarted":1,"transportRetries":2,"transparentStreamRetries":0,"streamResumes":0,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
	codewhaleTurnFailed529          = `{"event":"turn.completed","turn_id":"turn_8abcfeae2e5740c5b3fd9139a1022501","payload":{"turn":{"id":"turn_8abcfeae2e5740c5b3fd9139a1022501","status":"failed","error":"Server error (529): Overloaded","modelRequestDiagnostics":{"modelRequestsStarted":1,"transportRetries":2,"transparentStreamRetries":0,"streamResumes":0,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
	codewhaleTurnFailedUsage1308    = `{"event":"turn.completed","turn_id":"turn_7c137a1405fc4127b1b557198ecb5445","payload":{"turn":{"id":"turn_7c137a1405fc4127b1b557198ecb5445","status":"failed","error":"Rate limit exceeded: Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00","modelRequestDiagnostics":{"modelRequestsStarted":1,"transportRetries":2,"transparentStreamRetries":0,"streamResumes":0,"reasoningOnlyReprompts":0,"emptyStopRetries":0}}}}`
)

// codewhaleInsideTheStampWindow is a clock inside the 5-hour window of the
// 1308 stamp, which reads 2026-10-28 12:06:00 UTC. The probe itself ran at
// 01:31 UTC against a fake server, more than five hours before its stamp.
var codewhaleInsideTheStampWindow = time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)

// codewhaleFrameTurnID reads the turn of a captured turn.completed frame.
func codewhaleFrameTurnID(t *testing.T, frame string) string {
	t.Helper()
	var env struct {
		TurnID string `json:"turn_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &env))
	return env.TurnID
}

// A failed turn applies the verdict of its error text once, after its turn-end
// row, and the retry carries the turn's own frame.
func TestAFailedTurnAppliesItsVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
		now   time.Time
		want  []string
	}{
		{"a 500 schedules a server-error retry", codewhaleTurnFailed500, codewhaleInsideTheStampWindow, []string{"turn_end", "schedule_server_error"}},
		{"a refused connection schedules a server-error retry", codewhaleTurnFailedNetwork, codewhaleInsideTheStampWindow, []string{"turn_end", "schedule_server_error"}},
		{"a closed stream schedules a server-error retry", codewhaleTurnFailedStreamClosed, codewhaleInsideTheStampWindow, []string{"turn_end", "schedule_server_error"}},
		{"a rate limit schedules a server-error retry", codewhaleTurnFailedRateLimit, codewhaleInsideTheStampWindow, []string{"turn_end", "schedule_server_error"}},
		{"a 529 schedules a server-error retry", codewhaleTurnFailed529, codewhaleInsideTheStampWindow, []string{"turn_end", "schedule_server_error"}},
		{
			"a dated 1308 schedules a usage-limit retry at its stamp", codewhaleTurnFailedUsage1308, codewhaleInsideTheStampWindow,
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T12:06:01Z", "cancel_server_error"},
		},
		{
			// The stamp lies outside its stated window, so the window wording
			// decides: a limit of a week or less.
			"a 1308 stamp outside its window schedules a server-error retry", codewhaleTurnFailedUsage1308,
			time.Date(2026, 10, 28, 1, 31, 40, 0, time.UTC), []string{"turn_end", "schedule_server_error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			clock.Set(tc.now).MustWait(testutil.DeadlineContext(t))
			a, sink := newTestAgentWithClock(t, nil, clock)
			a.HandleOutput(turnStartedEvent(1, codewhaleFrameTurnID(t, tc.frame)))

			a.HandleOutput([]byte(tc.frame))

			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			require.Positive(t, sink.AutoScheduleCount())
			assert.Equal(t, []byte(tc.frame), sink.LastAutoSchedule().SourcePayload, "the retry carries the turn's own frame")
		})
	}
}

// A failed turn that ends while the agent stops is not retried.
func TestAFailedTurnDuringAStopIsNotRetryable(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, codewhaleFrameTurnID(t, codewhaleTurnFailed500)))
	a.NoteIntentionalStop()

	a.HandleOutput([]byte(codewhaleTurnFailed500))

	require.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// codewhaleTurnFailure reads Codewhale's error text in the order of the
// classifier recipe.
func TestCodewhaleTurnFailure(t *testing.T) {
	t.Parallel()
	errorOf := func(t *testing.T, frame string) string {
		t.Helper()
		var env struct {
			Payload turnEventPayload `json:"payload"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame), &env))
		text, _ := providerkit.RawString(env.Payload.Turn.Error)
		return text
	}
	failed := contracts.CodewhaleTurnStatusFailed
	for _, tc := range []struct {
		name    string
		status  string
		failure string
		kind    providerkit.TurnFailureKind
		window  providerkit.UsageWindow
		resetAt time.Time
	}{
		{"a 500 is transient", failed, errorOf(t, codewhaleTurnFailed500), providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a 529 is transient", failed, errorOf(t, codewhaleTurnFailed529), providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a network failure is transient", failed, errorOf(t, codewhaleTurnFailedNetwork), providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a closed stream is transient", failed, errorOf(t, codewhaleTurnFailedStreamClosed), providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a rate limit is a rate limit", failed, errorOf(t, codewhaleTurnFailedRateLimit), providerkit.TurnFailureRateLimited, 0, time.Time{}},
		{
			"a dated 1308 lifts at its stamp", failed, errorOf(t, codewhaleTurnFailedUsage1308),
			providerkit.TurnFailureUsageLimited, 0, time.Date(2026, 10, 28, 12, 6, 1, 0, time.UTC),
		},
		// The texts below are constructed from LlmError's Display forms
		// (llm_client/mod.rs in Codewhale 0.10.1).
		{"a timeout is transient", failed, "Request timed out after 120s", providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a responses stream that closed is transient", failed, "Responses stream closed before a successful completion event", providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a stalled stream is transient", failed, "Stream stalled for 300 seconds", providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a rate limit with an unknown body is a rate limit", failed, "Rate limit exceeded: Too Many Requests", providerkit.TurnFailureRateLimited, 0, time.Time{}},
		{
			"a plan quota with an unknown body is a usage limit with no stated window", failed, "Provider plan quota exhausted: quota exceeded",
			providerkit.TurnFailureUsageLimited, providerkit.UsageWindowUnstated, time.Time{},
		},
		{
			"a plan quota with a billing body is a billing block", failed, `Provider plan quota exhausted: {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
			providerkit.TurnFailureBillingBlocked, 0, time.Time{},
		},
		{"a 4xx-class server error is not retryable", failed, "Server error (499): Client closed request", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"a prefix in another case is not retryable", failed, "server error (500): Internal server error", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"a prefix inside the text is not retryable", failed, "Model error: Network error: refused", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"an authentication failure is not retryable", failed, "Authentication failed: invalid API key", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"an exceeded context is not retryable", failed, "Context length exceeded: 300000 tokens", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"an empty error is not retryable", failed, "", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"a completed turn is not retryable", contracts.CodewhaleTurnStatusCompleted, "Server error (500): Internal server error", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"an interrupted turn is not retryable", contracts.CodewhaleTurnStatusInterrupted, "Network error: refused", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"a canceled turn is not retryable", contracts.CodewhaleTurnStatusCanceled, "Network error: refused", providerkit.TurnFailureNotRetryable, 0, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := codewhaleTurnFailure(tc.status, tc.failure, false, codewhaleInsideTheStampWindow)
			assert.Equal(t, tc.kind, failure.Kind)
			assert.Equal(t, tc.window, failure.Window)
			assert.True(t, tc.resetAt.Equal(failure.ResetAt), "reset %v, want %v", failure.ResetAt, tc.resetAt)
		})
	}
	t.Run("a stop is not retryable", func(t *testing.T) {
		t.Parallel()
		failure := codewhaleTurnFailure(failed, errorOf(t, codewhaleTurnFailed500), true, codewhaleInsideTheStampWindow)
		assert.Equal(t, providerkit.TurnFailureNotRetryable, failure.Kind)
	})
}

// --- The automatic-retry verdict of a compaction turn ---

// codewhaleCompactionEnd is the turn end of a compaction turn: its turn-end
// row, then the verdict that cancels the server-error retry.
var codewhaleCompactionEnd = []agenttest.TimelineEntry{
	{Op: "turn_end"},
	{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: providerkit.TurnEndedOnCompaction},
}

// manualCompactionStartedEvent is the turn.started of a manual compaction. The
// runtime marks it with `manual_compaction` (runtime_threads.rs
// emit_claimed_turn_started, from Codewhale 0.9.13).
func manualCompactionStartedEvent(seq uint64, turnID string) []byte {
	return runtimeEvent(seq, "turn.started", turnID, "", map[string]any{
		"turn":              map[string]any{"id": turnID, "status": "in_progress", "input_summary": "Manual context compaction"},
		"manual_compaction": true,
	})
}

// failedTurnCompletedEvent is the turn.completed of a turn that failed with the
// error text failure.
func failedTurnCompletedEvent(seq uint64, turnID, failure string) []byte {
	return runtimeEvent(seq, "turn.completed", turnID, "", map[string]any{
		"turn": map[string]any{"id": turnID, "status": contracts.CodewhaleTurnStatusFailed, "error": failure},
	})
}

// codewhaleServerError is a failure text that the classifier reads as
// transient.
const codewhaleServerError = "Server error (500): Internal server error"

// A manual compaction that fails on an error that the classifier reads as
// retryable schedules no retry. The reply of the compaction request states the
// turn, and the runtime marks the turn's start.
func TestCodewhaleCompactionAFailedManualCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"a 500", codewhaleTurnFailed500},
		{"a refused connection", codewhaleTurnFailedNetwork},
		{"a closed stream", codewhaleTurnFailedStreamClosed},
		{"a rate limit", codewhaleTurnFailedRateLimit},
		{"a 529", codewhaleTurnFailed529},
		{"a dated 1308 usage limit", codewhaleTurnFailedUsage1308},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			turnID := codewhaleFrameTurnID(t, tc.frame)
			rt := newFakeRuntime(t)
			rt.respondJSON(http.MethodPost, threadPath(testThreadID, threadRouteCompact), http.StatusOK, map[string]any{"turn": map[string]any{"id": turnID}})
			clock := testutil.NewQuartzMock(t)
			clock.Set(codewhaleInsideTheStampWindow).MustWait(testutil.DeadlineContext(t))
			a, sink := newTestAgentWithClock(t, rt, clock)
			require.NoError(t, a.CompactContext())
			a.HandleOutput(manualCompactionStartedEvent(1, turnID))

			a.HandleOutput([]byte(tc.frame))

			assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A compaction ends on the compaction verdict whatever its outcome.
func TestCodewhaleCompactionEveryOutcomeAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	for _, status := range []string{
		contracts.CodewhaleTurnStatusCompleted, contracts.CodewhaleTurnStatusFailed,
		contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, sink := newTestAgent(t, nil)
			a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))
			a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))

			a.HandleOutput(turnCompletedEvent(3, testTurnID, status))

			assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The runtime marks a compaction turn that another client of the runtime
// requested, so that turn is a compaction turn too.
func TestCodewhaleCompactionAMarkedTurnStartIsACompactionTurnWithoutARequest(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))

	a.HandleOutput(failedTurnCompletedEvent(2, testTurnID, codewhaleServerError))

	assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The reply of the compaction request states the turn by itself, so a turn
// start without the runtime's mark is still the compaction's.
func TestCodewhaleCompactionTheReplyMarksTheTurnThatItStates(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPost, threadPath(testThreadID, threadRouteCompact), http.StatusOK, map[string]any{"turn": map[string]any{"id": testTurnID}})
	a, sink := newTestAgent(t, rt)
	require.NoError(t, a.CompactContext())
	a.HandleOutput(turnStartedEvent(1, testTurnID))

	a.HandleOutput(failedTurnCompletedEvent(2, testTurnID, codewhaleServerError))

	assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The reply can arrive after the compaction's turn ended. The marked start
// already decided the verdict, and the late reply marks no later turn.
func TestCodewhaleCompactionALateReplyAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgent(t, rt)
	rt.handle(http.MethodPost, threadPath(testThreadID, threadRouteCompact), func(w http.ResponseWriter, _ *http.Request) {
		a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))
		a.HandleOutput(failedTurnCompletedEvent(2, testTurnID, codewhaleServerError))
		writeFakeJSON(w, http.StatusOK, map[string]any{"turn": map[string]any{"id": testTurnID}})
	})
	require.NoError(t, a.CompactContext())
	assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))

	a.HandleOutput(turnStartedEvent(3, "turn_next"))
	a.HandleOutput(failedTurnCompletedEvent(4, "turn_next", codewhaleServerError))

	assert.Equal(t, []string{"turn_end", "cancel_server_error", "turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
}

// The end of a compaction turn spends the mark, so the next conversation turn
// applies its own verdict.
func TestCodewhaleCompactionTheTurnAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))

	a.HandleOutput(turnStartedEvent(3, "turn_next"))
	a.HandleOutput(failedTurnCompletedEvent(4, "turn_next", codewhaleServerError))

	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Len(t, timeline, 4)
	assert.Equal(t, codewhaleCompactionEnd, timeline[:2])
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(timeline[2:]))
}

// An automatic compaction inside a conversation turn ends no turn. A failed
// conversation turn keeps its own verdict, also when the compaction failed it.
func TestCodewhaleAnAutomaticCompactionLeavesTheConversationTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "compact", contracts.CodewhaleItemKindContextCompaction, "", nil))

	a.HandleOutput(failedTurnCompletedEvent(3, testTurnID, codewhaleServerError))

	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
}

// The runtime can repeat a turn end, and the repeat applies no second verdict.
func TestCodewhaleCompactionARepeatedTurnEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))
	a.HandleOutput(failedTurnCompletedEvent(2, testTurnID, codewhaleServerError))

	a.HandleOutput(failedTurnCompletedEvent(3, testTurnID, codewhaleServerError))

	assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A steer call holds the compaction's turn end. The held end runs once, after
// the call, and it applies the compaction verdict once.
func TestCodewhaleCompactionAHeldTurnEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(manualCompactionStartedEvent(1, testTurnID))
	current := a.steers.begin(testTurnID, "Also this.", nil, false)
	a.steerCall = current
	a.HandleOutput(failedTurnCompletedEvent(2, testTurnID, codewhaleServerError))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the steer call holds the turn end")

	a.finishSteerCall(current)

	assert.Equal(t, codewhaleCompactionEnd, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
