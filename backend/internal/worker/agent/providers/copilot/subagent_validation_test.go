package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCopilotMalformedCompletionKeepsTheChildActive(t *testing.T) {
	t.Parallel()
	for _, outcome := range []struct {
		name   string
		event  string
		status bgtask.Status
	}{
		{"completed", contracts.CopilotEventSubagentCompleted, bgtask.StatusSucceeded},
		{"failed", contracts.CopilotEventSubagentFailed, bgtask.StatusFailed},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			t.Parallel()
			for _, malformed := range []struct {
				name string
				data any
			}{
				{"string", "not an object"},
				{"array", []any{}},
				{"boolean", false},
				{"invalid tool identity", map[string]any{"toolCallId": 0}},
				{"invalid cancellation flag", map[string]any{"toolCallId": "task-1", "cancelled": "false"}},
			} {
				t.Run(malformed.name, func(t *testing.T) {
					t.Parallel()
					a, sink := newNativeCopilotForEvents(t)
					a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
						"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
						"arguments": map[string]any{"name": "Worker", "prompt": "Check the diff."},
					}))
					a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
						"toolCallId": "task-1", "agentName": "worker",
					}))
					row, found := copilotBackgroundRow(t, sink, "agent-1")
					require.True(t, found)
					child := sink.Child(row.ChildAgentID)
					nativeChild := a.children["agent-1"]
					require.NotNil(t, nativeChild)
					a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventToolStarted, map[string]any{
						"toolCallId": "child-tool", "toolName": contracts.CopilotToolView,
						"arguments": map[string]any{"path": "main.go"},
					}))
					before := len(child.Messages())
					parentBefore := len(sink.Messages())
					resets := child.ResetSpanCount()
					raw := nativeCopilotEvent(t, "agent-1", outcome.event, malformed.data)
					a.HandleOutput(raw)
					row, found = copilotBackgroundRow(t, sink, "agent-1")
					require.True(t, found)
					assert.Equal(t, bgtask.StatusRunning, row.Status)
					assert.Same(t, nativeChild, a.children["agent-1"])
					assert.Contains(t, a.openTools, "child-tool")
					assert.Equal(t, resets, child.ResetSpanCount())
					assert.Len(t, sink.Messages(), parentBefore)
					require.Len(t, child.Messages(), before+1)
					assert.Equal(t, raw, child.Messages()[before].Content)
					a.HandleOutput(nativeCopilotEvent(t, "agent-1", outcome.event, map[string]any{"toolCallId": "task-1"}))
					row, found = copilotBackgroundRow(t, sink, "agent-1")
					require.True(t, found)
					assert.Equal(t, outcome.status, row.Status)
					assert.NotContains(t, a.children, "agent-1")
					assert.NotContains(t, a.openTools, "child-tool")
					assert.Greater(t, child.ResetSpanCount(), resets)
				})
			}
		})
	}
}

func TestNativeCopilotMalformedCompletionUsesItsKnownSpawnOwner(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-1", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Worker", "prompt": "Check the diff."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-1", "agentName": "worker",
	}))
	row, found := copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, found)
	child := sink.Child(row.ChildAgentID)
	before := len(child.Messages())
	parentBefore := len(sink.Messages())
	raw := nativeCopilotEvent(t, "", contracts.CopilotEventSubagentCompleted, map[string]any{
		"toolCallId": "task-1", "cancelled": "invalid",
	})
	a.HandleOutput(raw)
	row, found = copilotBackgroundRow(t, sink, "agent-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Contains(t, a.children, "agent-1")
	assert.Len(t, sink.Messages(), parentBefore)
	require.Len(t, child.Messages(), before+1)
	assert.Equal(t, raw, child.Messages()[before].Content)
}
