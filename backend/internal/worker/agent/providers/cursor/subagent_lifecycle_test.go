//go:build unix

package cursor

import (
	"encoding/json"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cursorNativeSessionFrame(sessionID string, update any) []byte {
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
	if err != nil {
		panic(err)
	}
	return frame
}

func cursorNativeLifecycleFrame(kind, sessionID, taskID, state string) map[string]any {
	frame := map[string]any{"sessionUpdate": kind, "subagentSessionId": sessionID, "name": "explore", "task": "The actual native child task.", "capabilities": map[string]any{}, "_meta": map[string]any{"cursor": map[string]any{"toolCallId": taskID, "agentId": "actual-native-child", "model": "default"}}}
	if state != "" {
		frame["state"] = state
	}
	return frame
}

func cursorNativeTaskStart(a *Agent) {
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "native-task", "title": "Task: Preserve the actual description", "kind": "other", "status": "pending", "rawInput": map[string]any{"_toolName": "task", "description": "Preserve the actual description", "prompt": "The actual native child task."}}))
}

func TestCursorNativeSubagentRoutesProgressAndFinalContent(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	childID := tasks[0].ChildAgentID
	require.NotEmpty(t, childID)
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ACTUAL_CHILD_NATIVE_DELTA"}}))
	child := sink.Child(childID)
	assert.Contains(t, child.ProgressUpdates(), agent.ModelTextProgress("acp:agent_message_chunk", "ACTUAL_CHILD_NATIVE_DELTA"))
	assert.NotContains(t, sink.ProgressUpdates(), agent.ModelTextProgress("acp:agent_message_chunk", "ACTUAL_CHILD_NATIVE_DELTA"))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "native-task", "completed")))
	var content strings.Builder
	for _, message := range child.Messages() {
		content.Write(message.Content)
	}
	assert.Contains(t, content.String(), "ACTUAL_CHILD_NATIVE_DELTA")
	tasks = sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
	assert.Equal(t, "Preserve the actual description", tasks[0].Title)
}

func TestCursorNativeSubagentStateUsesTheActualFinishedOutcome(t *testing.T) {
	for _, scenario := range []struct {
		native string
		want   bgtask.Status
	}{
		{"completed", bgtask.StatusSucceeded}, {"failed", bgtask.StatusFailed}, {"cancelled", bgtask.StatusStopped}, {"disconnected", bgtask.StatusStopped},
	} {
		t.Run(scenario.native, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := startCursorNativeLifecycleTestAgent(t, sink)
			cursorNativeTaskStart(a)
			a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")))
			a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "native-task", scenario.native)))
			tasks := sink.BackgroundTasks()
			require.Len(t, tasks, 1)
			assert.Equal(t, scenario.want, tasks[0].Status)
		})
	}
}

func TestCursorNativeSubagentRejectsAnEmptySessionIdentity(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("unrelated-child", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "UNRELATED_CHILD_MUST_NOT_ROUTE"}}))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Empty(t, sink.Child(tasks[0].ChildAgentID).ProgressUpdates())
}

func cursorNativeTaskChildID(t *testing.T, sink *agenttest.Sink, rowKey string) string {
	t.Helper()
	for _, task := range sink.BackgroundTasks() {
		if task.RowKey == rowKey {
			require.NotEmpty(t, task.ChildAgentID)
			return task.ChildAgentID
		}
	}
	t.Fatalf("The native task %q has no child row.", rowKey)
	return ""
}

func TestCursorNativeSubagentRoutesGrandchildLifecycle(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")))
	childID := cursorNativeTaskChildID(t, sink, "native-task")
	child := sink.Child(childID)
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "native-grandchild-task", "title": "Task: Actual grandchild",
		"status": "pending", "rawInput": map[string]any{"_toolName": "task", "prompt": "The actual grandchild task."},
	}))
	grandchildID := cursorNativeTaskChildID(t, child, "native-grandchild-task")
	for _, task := range child.BackgroundTasks() {
		if task.RowKey == "native-grandchild-task" {
			assert.Equal(t, childID, task.ParentAgentID, "The native grandchild belongs to its immediate parent.")
		}
	}
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", cursorNativeLifecycleFrame("subagent_spawned", "native-grandchild-session", "native-grandchild-task", "")))
	progress := agent.ModelTextProgress("acp:agent_message_chunk", "ACTUAL_GRANDCHILD_DELTA")
	a.HandleOutput(cursorNativeSessionFrame("native-grandchild-session", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": progress.Text},
	}))
	assert.Contains(t, child.Child(grandchildID).ProgressUpdates(), progress)
	assert.NotContains(t, child.ProgressUpdates(), progress)
	assert.NotContains(t, sink.ProgressUpdates(), progress)
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", cursorNativeLifecycleFrame("subagent_state_update", "native-grandchild-session", "native-grandchild-task", "completed")))
	var text strings.Builder
	for _, message := range child.Child(grandchildID).Messages() {
		text.Write(message.Content)
	}
	assert.Contains(t, text.String(), progress.Text)
}

func TestCursorNativeSubagentIgnoresLifecycleFromUnknownParentSession(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	a.HandleOutput(cursorNativeSessionFrame("unknown-parent", cursorNativeLifecycleFrame("subagent_spawned", "unrelated-native-session", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("unrelated-native-session", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "UNKNOWN_PARENT_MUST_NOT_ROUTE"},
	}))
	assert.Empty(t, sink.Child(cursorNativeTaskChildID(t, sink, "native-task")).ProgressUpdates())
}

func TestCursorNativeSubagentSpawnBeforeTaskKeepsOneRowAndPrompt(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	spawn := cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", ""))
	a.HandleOutput(spawn)
	childID := cursorNativeTaskChildID(t, sink, "native-task")
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "EARLY_NATIVE_CHILD_DELTA"},
	}))
	cursorNativeTaskStart(a)
	a.HandleOutput(spawn)
	assert.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, childID, cursorNativeTaskChildID(t, sink, "native-task"))
	assert.Equal(t, "Preserve the actual description", sink.BackgroundTasks()[0].Title)
	child := sink.Child(childID)
	assert.Contains(t, child.ProgressUpdates(), agent.ModelTextProgress("acp:agent_message_chunk", "EARLY_NATIVE_CHILD_DELTA"))
	var prompts []string
	for _, message := range child.Messages() {
		if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			prompts = append(prompts, string(message.Content))
		}
	}
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "The actual native child task.")
}

func TestCursorNativeSubagentFailurePrecedesCompletedTaskMirror(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "native-task", "failed")))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "native-task", "status": "completed", "rawOutput": map[string]any{"isBackground": false},
	}))
	require.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, bgtask.StatusFailed, sink.BackgroundTasks()[0].Status)
}

func TestCursorNativeSubagentLaterSessionRejectsOldUpdatesAndFinalState(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	first := cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", ""))
	a.HandleOutput(first)
	a.HandleOutput(first)
	childID := cursorNativeTaskChildID(t, sink, "native-task")
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session.2", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "STALE_NATIVE_CHILD_DELTA"},
	}))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "native-task", "failed")))
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)
	currentProgress := agent.ModelTextProgress("acp:agent_message_chunk", "CURRENT_NATIVE_CHILD_DELTA")
	a.HandleOutput(cursorNativeSessionFrame("native-child-session.2", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": currentProgress.Text},
	}))
	child := sink.Child(childID)
	assert.Contains(t, child.ProgressUpdates(), currentProgress)
	assert.NotContains(t, child.ProgressUpdates(), agent.ModelTextProgress("acp:agent_message_chunk", "STALE_NATIVE_CHILD_DELTA"))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session.2", "native-task", "completed")))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session.2", "native-task", "failed")))
	assert.Equal(t, bgtask.StatusSucceeded, sink.BackgroundTasks()[0].Status)
	assert.Len(t, sink.BackgroundTasks(), 1)
}

func TestCursorNativeSubagentRejectsMalformedAndUnknownLifecycleIdentity(t *testing.T) {
	for _, scenario := range []struct {
		label  string
		parent string
		update map[string]any
	}{
		{"absent parent", "", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")},
		{"unknown finished task", "cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "unknown-task", "completed")},
		{"empty task", "cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "", "")},
		{"invalid session type", "cursor-new", map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": 0, "_meta": map[string]any{"cursor": map[string]any{"toolCallId": "native-task", "agentId": "native-child"}}}},
		{"missing identity", "cursor-new", map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": "native-child-session"}},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := startCursorNativeLifecycleTestAgent(t, sink)
			cursorNativeTaskStart(a)
			a.HandleOutput(cursorNativeSessionFrame(scenario.parent, scenario.update))
			assert.Len(t, sink.BackgroundTasks(), 1)
			assert.Empty(t, sink.Child(cursorNativeTaskChildID(t, sink, "native-task")).ProgressUpdates())
		})
	}
}

func TestCursorNativeSubagentStoreMirrorDoesNotDuplicateLiveContent(t *testing.T) {
	sink := &agenttest.Sink{}
	a := startCursorNativeLifecycleTestAgent(t, sink)
	cursorNativeTaskStart(a)
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_spawned", "native-child-session", "native-task", "")))
	a.HandleOutput(cursorNativeSessionFrame("native-child-session", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ONE_NATIVE_CHILD_REPORT"},
	}))
	a.HandleOutput(cursorNativeSessionFrame("cursor-new", cursorNativeLifecycleFrame("subagent_state_update", "native-child-session", "native-task", "completed")))
	child := sink.Child(cursorNativeTaskChildID(t, sink, "native-task"))
	before := child.Messages()
	a.noteCursorTaskExtension("native-task")
	a.observeCursorTaskRecord("native-task", cursorToolRecord{content: json.RawMessage(`{"toolName":"task","result":"ONE_NATIVE_CHILD_REPORT"}`)})
	assert.Equal(t, before, child.Messages())
}
