package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
