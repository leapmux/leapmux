package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

func TestItemsTheReaderDoesNotSeeAreDropped(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	// LeapMux already stored the reader's own message.
	a.HandleOutput(itemEvent(1, "item.completed", "item_1", contracts.CodewhaleItemKindUserMessage, "Say hello.", nil))
	// The runtime's own loop.
	for i, summary := range []string{"Continuing — tool results", "Executing tools sequentially (writes)", "Loaded deferred tool 'apply_patch'.", "Policy: Full Access / ACT", "Permissions: Ask · Work"} {
		a.HandleOutput(itemEvent(uint64(2+i), "item.completed", "item_2", contracts.CodewhaleItemKindStatus, summary, nil))
	}
	a.HandleOutput(itemEvent(10, "item.completed", "item_3", contracts.CodewhaleItemKindStatus, "An internal note", map[string]any{"visibility": "internal"}))
	// A delta of another kind, and an item of no kind this build knows.
	a.HandleOutput(runtimeEvent(11, "item.delta", testTurnID, "item_4", map[string]any{"delta": "x", "kind": "tool_call"}))
	a.HandleOutput(itemEvent(12, "item.completed", "item_5", "a_later_kind", "x", nil))

	assert.Empty(t, sink.Messages())
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
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, len(events))
	for i, raw := range events {
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
