package copilot

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNativeCopilotForEvents builds an agent that reads the event stream without a
// process. The connection is present but unstarted, so every field the dispatch reads
// resolves and no request can leave.
func newNativeCopilotForEvents(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "copilot-events", Ctx: t.Context(), Stdin: agenttest.FailingStdin{}}),
		}},
		sink:      agent.NewProviderServices(sink),
		sessionID: "session-1",
		options:   optionmap.Map{},
	}
	return a, sink
}

// nativeCopilotEvent frames one session event exactly as the runtime sends it, for the
// session newNativeCopilotForEvents opens.
func nativeCopilotEvent(t *testing.T, agentID, eventType string, data any) []byte {
	t.Helper()
	return nativeCopilotSessionEvent(t, "session-1", agentID, eventType, data)
}

// nativeCopilotSessionEvent frames one session event for a named session, which is what
// a test against a LIVE agent needs: that agent's identity is a generated one.
func nativeCopilotSessionEvent(t *testing.T, sessionID, agentID, eventType string, data any) []byte {
	t.Helper()
	event := map[string]any{"id": eventType + "-id", "type": eventType, "data": data}
	if agentID != "" {
		event["agentId"] = agentID
	}
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session.event",
		"params": map[string]any{"sessionId": sessionID, "event": event},
	})
	require.NoError(t, err)
	return raw
}

// copilotBackgroundRow reads one registry row by key.
func copilotBackgroundRow(t *testing.T, sink *agenttest.Sink, rowKey string) (bgtask.Item, bool) {
	t.Helper()
	for _, row := range sink.BackgroundTasks() {
		if row.RowKey == rowKey {
			return row, true
		}
	}
	return bgtask.Item{}, false
}

func TestNativeCopilotToolCallOpensAndClosesItsSpan(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolView,
		"arguments": map[string]any{"path": "/project/main.go"},
	}))
	require.Equal(t, []string{"tool-1"}, sink.ReservedColorSpans())
	require.Equal(t, contracts.CopilotToolView, sink.GetSpanType("tool-1"))

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolCompleted, map[string]any{
		"toolCallId": "tool-1", "success": true,
		"result": map[string]any{"content": "package main"},
	}))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, "tool-1", messages[0].SpanID)
	assert.False(t, messages[0].Closing)
	assert.Equal(t, "tool-1", messages[1].SpanID)
	assert.True(t, messages[1].Closing)
	assert.Equal(t, contracts.CopilotToolView, messages[1].SpanType)
	assert.Empty(t, messages[1].Completion, "a successful tool result is not an error")
	assert.Contains(t, sink.ClosedSpans(), "tool-1")
}

func TestNativeCopilotFailedToolResultCarriesTheErrorCompletion(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolBash, "arguments": map[string]any{"command": "false"},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolCompleted, map[string]any{
		"toolCallId": "tool-1", "success": false,
		"error": map[string]any{"message": "the command failed"},
	}))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, agent.MessageCompletionError, messages[1].Completion)
}

// A completion whose start this process never saw still reaches the transcript. A
// resumed session replays no earlier event, so the result is all there is.
func TestNativeCopilotUnmatchedToolResultStillReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolCompleted, map[string]any{
		"toolCallId": "tool-orphan", "success": true, "result": map[string]any{"content": "output"},
	}))
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, "tool-orphan", messages[0].SpanID)
	assert.True(t, messages[0].Closing)
	assert.Empty(t, messages[0].SpanType, "an unmatched result states no tool name it did not read")
}

func TestNativeCopilotSubagentOpensItsOwnTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Reviewer", "prompt": "Review the diff."},
	}))
	assert.Empty(t, sink.ReservedColorSpans(), "a subagent launch reserves no rail colour")

	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "reviewer", "agentDisplayName": "Reviewer",
	}))
	row, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Reviewer", row.Title)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)

	child := sink.Child(row.ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Review the diff."}`, string(child.Messages()[0].Content))

	// The child's own events land in the child transcript, never in the root.
	before := len(sink.Messages())
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventAssistantMessage, map[string]any{
		"messageId": "message-1", "content": "The diff looks correct.",
	}))
	assert.Len(t, sink.Messages(), before, "a subagent message never reaches the parent transcript")
	require.Len(t, child.Messages(), 2)
	assert.Contains(t, string(child.Messages()[1].Content), "The diff looks correct.")

	parentBeforeCompletion := len(sink.Messages())
	childBeforeCompletion := len(child.Messages())
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentCompleted, map[string]any{
		"toolCallId": "task-1", "agentName": "reviewer", "agentDisplayName": "Reviewer",
	}))
	assert.Len(t, sink.Messages(), parentBeforeCompletion,
		"a child completion notification must not leak into the parent transcript")
	require.Len(t, child.Messages(), childBeforeCompletion+1)
	assert.Contains(t, string(child.Messages()[childBeforeCompletion].Content), contracts.CopilotEventSubagentCompleted)
	row, ok = copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.NotEmpty(t, child.ResetSpanCount(), "a finished subagent releases its spans")
}

// A second start for a running subagent must not open a second transcript: the
// first one's registry row would then stay Running with nothing to close it.
func TestNativeCopilotRepeatedSubagentStartKeepsOneTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Worker", "prompt": "Work."},
	}))
	started := nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "worker", "agentDisplayName": "Worker",
	})
	a.HandleOutput(started)
	first, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	a.HandleOutput(started)
	assert.Len(t, sink.ChildAgentIDs(), 1)
	again, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	assert.Equal(t, first.ChildAgentID, again.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, again.Status)
}

func TestNativeCopilotSubagentOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		event string
		data  map[string]any
		want  bgtask.Status
	}{
		{"completed", contracts.CopilotEventSubagentCompleted, map[string]any{"toolCallId": "task-1"}, bgtask.StatusCompleted},
		{"cancelled", contracts.CopilotEventSubagentCompleted, map[string]any{"toolCallId": "task-1", "cancelled": true}, bgtask.StatusStopped},
		{"failed", contracts.CopilotEventSubagentFailed, map[string]any{"toolCallId": "task-1", "error": "it broke"}, bgtask.StatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newNativeCopilotForEvents(t)
			a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
				"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
				"arguments": map[string]any{"description": "Check it"},
			}))
			a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
				"toolCallId": "task-1", "agentName": "checker",
			}))
			row, ok := copilotBackgroundRow(t, sink, "agent-1")
			require.True(t, ok)
			child := sink.Child(row.ChildAgentID)
			parentBefore := len(sink.Messages())
			a.HandleOutput(nativeCopilotEvent(t, "agent-1", test.event, test.data))
			row, ok = copilotBackgroundRow(t, sink, "agent-1")
			require.True(t, ok)
			assert.Equal(t, test.want, row.Status)
			assert.Len(t, sink.Messages(), parentBefore)
			require.Len(t, child.Messages(), 1, "the final lifecycle notification stays in the child")
			assert.Contains(t, string(child.Messages()[0].Content), test.event)
		})
	}
}

// A subagent that spawns another one owns the child. The spawning tool call states
// the owner, so the grandchild reaches its parent's transcript rather than the root.
func TestNativeCopilotNestedSubagentBelongsToItsParent(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Parent", "prompt": "Delegate."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "parent", "agentDisplayName": "Parent",
	}))
	parentRow, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	parentSink := sink.Child(parentRow.ChildAgentID)

	// The nested task call is the PARENT subagent's own tool call.
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-2", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Child", "prompt": "Do the work."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "agent-2", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-2", "agentName": "child", "agentDisplayName": "Child",
	}))

	nestedRow, ok := copilotBackgroundRow(t, parentSink, "agent-2")
	require.True(t, ok, "the nested row belongs to the subagent that spawned it")
	assert.Equal(t, parentRow.ChildAgentID, nestedRow.ParentAgentID)
	_, rootHasNested := copilotBackgroundRow(t, sink, "agent-2")
	assert.False(t, rootHasNested, "the root never owns a grandchild row")
}

// A streaming delta is transient by the runtime's own mark, and the finished message
// arrives separately. Persisting one would repeat that text in the transcript.
func TestNativeCopilotEphemeralDeltasReportProgressWithoutPersisting(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session.event",
		"params": map[string]any{"sessionId": "session-1", "event": map[string]any{
			"id": "event-1", "type": contracts.CopilotEventAssistantMessageDelta, "ephemeral": true,
			"data": map[string]any{"messageId": "message-1", "delta": "partial "},
		}},
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
	assert.Empty(t, sink.Messages(), "an ephemeral event is never stored")
	require.NotEmpty(t, sink.ProgressUpdates())
	assert.Equal(t, "message-1", sink.ProgressUpdates()[0].ScopeID)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessage, map[string]any{
		"messageId": "message-1", "content": "partial and final",
	}))
	require.Len(t, sink.Messages(), 1, "the finished message is the one that is stored")
}

func TestNativeCopilotUsageInfoBroadcastsTheContextCounter(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionUsageInfo, map[string]any{
		"currentTokens": 1200, "tokenLimit": 128000, "messagesLength": 4,
	}))
	usage, ok := sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1200), usage[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, int64(128000), usage[contracts.ContextUsageFieldContextWindow])
}

// An event for a subagent this process never saw start still reaches a transcript.
// A visible row in the root is recoverable; a dropped one is not.
func TestNativeCopilotUnknownSubagentEventReachesTheRoot(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "agent-unknown", contracts.CopilotEventAssistantMessage, map[string]any{
		"messageId": "message-1", "content": "orphaned",
	}))
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "orphaned")
}

// Stopping the agent closes every open subagent row, so a transcript the process
// leaves behind does not stay Running for good.
func TestNativeCopilotClearingChildrenStopsTheirRows(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Worker", "prompt": "Work."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "worker",
	}))

	a.outputMu.Lock()
	a.clearNativeChildren()
	a.outputMu.Unlock()

	row, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Empty(t, a.children)
	assert.Empty(t, a.openTools)
}

// A foreign session's event changes nothing, including the turn flag.
func TestNativeCopilotIgnoresAnotherSession(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	raw := fmt.Appendf(nil, `{"method":"session.event","params":{"sessionId":%q,"event":{"type":%q,"data":{}}}}`,
		"other-session", contracts.CopilotEventAssistantTurnStart)
	a.HandleOutput(raw)
	assert.Empty(t, sink.Messages())
	assert.False(t, a.PublishTurnActive().Active)
}

// A turn that ends while a tool call runs must close that call's span. An open span
// leaves the tool card showing a running badge for the rest of the session.
func TestNativeCopilotTurnEndClosesAToolThatNeverCompleted(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	start := nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolBash,
		"arguments": map[string]any{"command": "bun test"},
	})
	a.HandleOutput(start)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{}))

	messages := sink.Messages()
	require.GreaterOrEqual(t, len(messages), 3)
	// The call closes BEFORE the turn-end divider, so the transcript reads in the
	// order the work happened.
	assert.True(t, messages[len(messages)-1].TurnEnd)
	closing := messages[len(messages)-2]
	assert.True(t, closing.Closing, "the turn end closes the call it interrupted")
	assert.Equal(t, "tool-1", closing.SpanID)
	assert.Equal(t, contracts.CopilotToolBash, closing.SpanType)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
	// The row is the agent's own start frame, byte for byte. Copilot sent no result,
	// so LeapMux states the outcome in its completion column and invents nothing.
	assert.JSONEq(t, string(start), string(closing.Content))
	assert.Contains(t, sink.ClosedSpans(), "tool-1")
}

// A stop reaches the same sweep, so a card does not survive the process that drew it.
func TestNativeCopilotStopClosesAToolThatNeverCompleted(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolBash,
		"arguments": map[string]any{"command": "bun test"},
	}))
	a.clearNativeChildren()

	closing := sink.Messages()[len(sink.Messages())-1]
	assert.True(t, closing.Closing)
	assert.Equal(t, "tool-1", closing.SpanID)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
	assert.Contains(t, sink.ClosedSpans(), "tool-1")
}

// The runtime reports its own session metadata and its feature flags on separate
// methods. Neither carries conversation, and a single trivial turn sends ten of the
// first, so persisting them fills the transcript with rows a reader cannot use.
func TestNativeCopilotDropsFramesThatCarryNoConversation(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session.lifecycle","params":{"type":"session.updated","sessionId":"session-1","metadata":{"modifiedTime":"2026-09-14T02:22:55.613Z"}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"host.event","params":{"scope":{"type":"session","sessionId":"session-1"},"kind":"feature_flag_changed","payload":{"revision":1,"flags":{"SANDBOX":true}}}}`))
	a.HandleOutput(nativeCopilotEvent(t, "", "session.usage_checkpoint", map[string]any{
		"totalNanoAiu": 12199400000, "totalPremiumRequests": 1,
	}))

	assert.Empty(t, sink.Messages(), "none of the three belongs in the transcript")
}

// The runtime's model-call trace and its hook plumbing describe the runtime rather
// than the work. One ordinary turn sends ten of these frames, and each one reached the
// reader as a raw-JSON row.
func TestNativeCopilotDropsItsOwnModelTraceAndHooks(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	for _, eventType := range []string{
		"model.turn_started",
		"model.model_call_started",
		"model.captured_assignment_context",
		"model.model_call_success",
		"model.message",
		"model.response",
		"model.turn_ended",
		"model.messages_snapshot",
		"hook.start",
		"hook.end",
	} {
		a.HandleOutput(nativeCopilotEvent(t, "", eventType, map[string]any{"kind": eventType}))
	}

	assert.Empty(t, sink.Messages(), "the runtime's own trace belongs in no transcript row")
}

// EVERY family the contract lists, not the two the worker used to test. The browser
// hides all six, so a stored row for any of them is a row nobody ever sees -- and it
// still spends a message seq and a slot in the chat-history page budget.
//
// The families come from the contract rather than from a list here, so a seventh one
// added later fails this test until the worker drops it too.
func TestNativeCopilotDropsEveryRuntimeTraceFamily(t *testing.T) {
	t.Parallel()
	// An empty table would pass every case below without running one.
	require.NotEmpty(t, contracts.CopilotEventPrefixKeys)

	for _, prefix := range contracts.CopilotEventPrefixKeys {
		// A synthetic member, because the four experimental families state no member
		// LeapMux draws a row for. The worker answers on the prefix alone, which is
		// the whole point of a family.
		eventType := prefix + "probe"
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()
			a, sink := newNativeCopilotForEvents(t)

			a.HandleOutput(nativeCopilotEvent(t, "", eventType, map[string]any{"kind": eventType}))

			assert.True(t, copilotEventIsRuntimeTrace(eventType), "the family is runtime trace")
			assert.Empty(t, sink.Messages(), "a member of this family belongs in no transcript row")
		})
	}
}

// The one model event LeapMux surfaces stays. A failed model call is a notification
// the reader acts on, so the family rule cannot take it.
func TestNativeCopilotKeepsTheModelCallFailure(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	raw := nativeCopilotEvent(t, "", contracts.CopilotEventModelCallFailure, map[string]any{
		"message": "the model refused the request",
	})
	a.HandleOutput(raw)

	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

// The runtime marks every streaming delta ephemeral and emits the assistant message
// only once that message is COMPLETE, so an interrupted turn stored nothing and the
// answer the reader had been watching vanished. Every other provider keeps what the
// model produced, through the same shared buffer.
func TestNativeCopilotKeepsTheAnswerAnInterruptedTurnStreamed(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	// The installed runtime spells BOTH streams' text `deltaContent`. A build that
	// fills `delta` instead is read the same way.
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantReasoningDelta, map[string]any{
		"reasoningId": "reasoning-1", "deltaContent": "Weighing ",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantReasoningDelta, map[string]any{
		"reasoningId": "reasoning-1", "deltaContent": "the options.",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessageDelta, map[string]any{
		"messageId": "message-1", "delta": "Chapter 1: ",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessageDelta, map[string]any{
		"messageId": "message-1", "deltaContent": "the abacus.",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{"aborted": true}))

	var assembled []string
	for _, message := range sink.Messages() {
		if kind, completion := agent.MessageMetadata(agent.MessageContent{Original: message.Content, Completion: message.Completion}); kind != leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED {
			assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED, completion,
				"the turn ended before the message did")
			assembled = append(assembled, string(message.Content))
		}
	}
	require.Len(t, assembled, 2, "one reasoning segment and one answer segment")
	assert.Contains(t, assembled[0], "Weighing the options.")
	assert.Contains(t, assembled[1], "Chapter 1: the abacus.")
}

// A turn that ends BY ITSELF with a segment still open produced all the text it was
// going to, so that segment reads as complete rather than as an interruption.
func TestNativeCopilotCompletesASegmentTheTurnEndFindsOpen(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessageDelta, map[string]any{
		"messageId": "message-1", "deltaContent": "All of it.",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{}))

	found := false
	for _, message := range sink.Messages() {
		kind, completion := agent.MessageMetadata(agent.MessageContent{Original: message.Content, Completion: message.Completion})
		if kind == leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED {
			continue
		}
		found = true
		assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE, completion)
	}
	assert.True(t, found, "the text the turn streamed is stored")
}

// The runtime's own complete message is the authoritative row, so the deltas it
// replaces must not become a second copy of the same text.
func TestNativeCopilotDropsTheDeltasItsFinishedMessageReplaces(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessageDelta, map[string]any{
		"messageId": "message-1", "deltaContent": "Chapter 1",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantMessage, map[string]any{
		"messageId": "message-1", "content": "Chapter 1, complete.",
	}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{}))

	for _, message := range sink.Messages() {
		kind, _ := agent.MessageMetadata(agent.MessageContent{Original: message.Content, Completion: message.Completion})
		assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, kind,
			"the finished message is the row; its deltas are not a second one")
	}
}

// Copilot 1.0.24 reports the answer's running SIZE on a stream of its own, which
// identifies no message and carries no text. Every report is a running total for the whole
// response, so they all land on one scope: a scope for each event would add the
// totals, and a three-byte answer would count as six.
func TestNativeCopilotCountsTheAnswerSizeOnOneScope(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	for _, size := range []int{1, 2, 3} {
		a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantStreamingDelta, map[string]any{
			"totalResponseSizeBytes": size,
		}))
	}

	var scopes []string
	var values []int64
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTotal {
			scopes = append(scopes, update.ScopeID)
			values = append(values, update.Value)
			assert.True(t, update.Exact, "the runtime states a byte count, not an estimate")
		}
	}
	assert.Equal(t, []int64{1, 2, 3}, values)
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		assert.Equal(t, scopes[0], scope, "one response, one counter")
	}
	for _, message := range sink.Messages() {
		kind, _ := agent.MessageMetadata(agent.MessageContent{Original: message.Content, Completion: message.Completion})
		assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, kind,
			"a stream that carried no text stores no text")
	}
}

// A method this build does not recognize still reaches the transcript: a frame that
// carries conversation is worse lost than shown as raw JSON.
func TestNativeCopilotKeepsAnUnrecognizedMethod(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	raw := []byte(`{"jsonrpc":"2.0","method":"session.aMethodALaterRuntimeAdds","params":{"sessionId":"session-1"}}`)
	a.HandleOutput(raw)

	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

// A subagent reports its OWN context, so its counter belongs to its own transcript. The
// root's counter describes the root's context, and a child's number written there would
// overwrite it with a figure for a different conversation.
func TestNativeCopilotSubagentUsageInfoReachesItsOwnTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Reviewer", "prompt": "Review the diff."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "reviewer", "agentDisplayName": "Reviewer",
	}))
	row, ok := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	require.Zero(t, sink.SessionInfoCount())

	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSessionUsageInfo, map[string]any{
		"currentTokens": 300, "tokenLimit": 64000,
	}))

	assert.Zero(t, sink.SessionInfoCount(), "a subagent's context counter never reaches the root")
	usage, ok := child.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(300), usage[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, int64(64000), usage[contracts.ContextUsageFieldContextWindow])
}

// The scope of an accumulating segment is the runtime's own message or reasoning
// identity. The FRAME id is not a substitute: it changes for every delta, so a build
// that omitted both identities would open one segment per delta and store each of them
// as its own assembled message.
func TestNativeCopilotDeltaWithNoMessageIdentityOpensNoSegment(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)

	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	for index, text := range []string{"Chapter ", "1."} {
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{"sessionId": "session-1", "event": map[string]any{
				"id": fmt.Sprintf("frame-%d", index), "type": contracts.CopilotEventAssistantMessageDelta,
				"ephemeral": true, "data": map[string]any{"deltaContent": text},
			}},
		})
		require.NoError(t, err)
		a.HandleOutput(raw)
	}
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventSessionIdle, map[string]any{}))

	for _, message := range sink.Messages() {
		kind, _ := agent.MessageMetadata(agent.MessageContent{Original: message.Content, Completion: message.Completion})
		assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, kind,
			"a delta that states no message identity stores no assembled row")
	}
	for _, update := range sink.ProgressUpdates() {
		assert.NotEqual(t, agent.ProgressModelText, update.Operation,
			"a delta with no message identity moves no text counter either")
	}
}

// The runtime sends a separate change event for each settings axis, and one move can
// raise all three. Each event needs a read, and three reads in flight publish a mixed
// snapshot: whichever one finishes last decides, and its view of the other two axes can
// be older than theirs. One read runs, and one more follows it.
func TestNativeCopilotSettingsChangeEventsShareOneRead(t *testing.T) {
	t.Parallel()
	a, _ := newNativeCopilotForEvents(t)
	// Hold every background run at its first step, so the table states what the dispatch
	// registered rather than what a race let through.
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	for _, eventType := range []string{
		contracts.CopilotEventSessionModeChanged,
		contracts.CopilotEventSessionPermissionsChanged,
		contracts.CopilotEventSessionModelChange,
	} {
		a.HandleOutput(nativeCopilotEvent(t, "", eventType, map[string]any{}))
	}

	assert.Equal(t, map[string]bool{copilotReadSettings: true}, a.backgroundReads.inFlight(),
		"three change events start one read, with one more queued behind it")
}

// The objective read takes the same route, and its key keeps it apart from the settings
// read.
func TestNativeCopilotObjectiveChangeEventsShareOneRead(t *testing.T) {
	t.Parallel()
	a, _ := newNativeCopilotForEvents(t)
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	for range 2 {
		a.HandleOutput(nativeCopilotEvent(t, "",
			contracts.CopilotEventSessionAutopilotObjectiveChanged, map[string]any{}))
	}

	assert.Equal(t, map[string]bool{copilotReadGoal: true}, a.backgroundReads.inFlight())
}

// The runtime marks both progress frames "do not store", and they carry the only
// text a running call reports. Dropping them unread left the row blank for the
// whole call.
func TestCopilotNativeEvents_ProgressFramesFeedTheRunningRow(t *testing.T) {
	t.Parallel()

	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolProgress,
		map[string]any{"toolCallId": "call-1", "progressMessage": "Searching the index"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolPartialResult,
		map[string]any{"toolCallId": "call-1", "partialOutput": "match one\nmatch two"}))

	updates := sink.ProgressUpdates()
	// A progress MESSAGE is a whole sentence the runtime wrote, so it states no
	// loss; a partial output is a window on more by definition.
	assert.Contains(t, updates, agent.OutputTailProgress("call-1", "Searching the index", false))
	assert.Contains(t, updates, agent.OutputTailProgress("call-1", "match one\nmatch two", true))
	// Both frames are ephemeral: neither reaches the transcript.
	assert.Equal(t, 0, sink.MessageCount())
}

// The runtime interleaves the two frames, and they say different things. Once a call
// produced output, a sentence about what it DOES must not replace it -- the
// reader watched the lines disappear and a status line take their place.
func TestCopilotNativeEvents_AProgressSentenceDoesNotReplaceOutput(t *testing.T) {
	t.Parallel()

	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted,
		map[string]any{"toolCallId": "call-1", "toolName": "bash"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolProgress,
		map[string]any{"toolCallId": "call-1", "progressMessage": "Starting the build"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolPartialResult,
		map[string]any{"toolCallId": "call-1", "partialOutput": "compiling main.go"}))
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolProgress,
		map[string]any{"toolCallId": "call-1", "progressMessage": "Still building"}))

	updates := sink.ProgressUpdates()
	// The FIRST sentence shows: the call had produced nothing, so it is all there is.
	assert.Contains(t, updates, agent.OutputTailProgress("call-1", "Starting the build", false))
	assert.Contains(t, updates, agent.OutputTailProgress("call-1", "compiling main.go", true))
	assert.NotContains(t, updates, agent.OutputTailProgress("call-1", "Still building", false))
}

// A call whose start this process never saw keeps its sentence: there is no record
// that says output arrived, and a blank row states less than the sentence does.
func TestCopilotNativeEvents_AnUnopenedCallKeepsItsProgressSentence(t *testing.T) {
	t.Parallel()

	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolProgress,
		map[string]any{"toolCallId": "call-9", "progressMessage": "Searching"}))

	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("call-9", "Searching", false))
}
