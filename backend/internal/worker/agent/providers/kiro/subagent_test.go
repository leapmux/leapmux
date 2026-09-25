package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The ids of the probe's subagent run.
const (
	kiroSpawnCallID = "invoke_subagent_t_sub"
	kiroSubtaskID   = "d5789deb-18e5-4770-a9fb-3e17bf10e526"
)

// spawnInput is the arguments of the probe's spawn.
var spawnInput = map[string]any{
	"name": "context-gatherer", "prompt": "CHILD3-TASK find hello", "explanation": "delegate", "contextFiles": []any{},
}

// spawnMeta is the `_meta` of each frame of the spawn itself.
func spawnMeta() map[string]any {
	return map[string]any{"kiro": map[string]any{"kind": contracts.KiroKindAgentSubtask, "agentSubtaskId": kiroSubtaskID}}
}

// childMeta is the `_meta` of each update of the subagent's own work.
func childMeta() map[string]any {
	return map[string]any{"kiro": map[string]any{"agentSubtaskId": kiroSubtaskID}}
}

// spawnFrame is one frame of the spawn, as the probe recorded it.
func spawnFrame(t *testing.T, updateType, status string, extra map[string]any) []byte {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": updateType, "toolCallId": kiroSpawnCallID, "status": status,
		"title": "Sub-agent: context-gatherer", "kind": "other",
		"rawInput": spawnInput, "_meta": spawnMeta(),
	}
	for key, value := range extra {
		update[key] = value
	}
	return sessionUpdate(t, kiroTestSession, update)
}

// childFrame is one update of the subagent's own work.
func childFrame(t *testing.T, update map[string]any) []byte {
	t.Helper()
	update["_meta"] = childMeta()
	return sessionUpdate(t, kiroTestSession, update)
}

// childTexts reads the assembled text rows of one child transcript.
func childTexts(t *testing.T, child *agenttest.Sink) []string {
	t.Helper()
	var texts []string
	for _, message := range child.Messages() {
		var envelope map[string]any
		if json.Unmarshal(message.Content, &envelope) != nil || envelope["type"] != "assembled_message" {
			continue
		}
		if text, ok := envelope["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// userContents reads the user rows of one child transcript.
func userContents(t *testing.T, child *agenttest.Sink) []string {
	t.Helper()
	var contents []string
	for _, message := range child.Messages() {
		var envelope map[string]any
		if json.Unmarshal(message.Content, &envelope) != nil {
			continue
		}
		if content, ok := envelope["content"].(string); ok && envelope["type"] == nil {
			contents = append(contents, content)
		}
	}
	return contents
}

// reportTexts reads the subagent reports of one transcript.
func reportTexts(sink *agenttest.Sink) []string {
	var texts []string
	for _, notification := range sink.LeapMuxNotifications() {
		if notification[contracts.NotificationFieldType] != contracts.NotificationTypeSubagentReport {
			continue
		}
		if text, ok := notification[contracts.NotificationFieldText].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestKiroSubagentLifecycle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(messageChunk(t, "Delegating.", nil))
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))
	task, ok := sink.BackgroundTask(kiroSpawnCallID)
	require.True(t, ok, "the spawn opens a row at once")
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	assert.Equal(t, "context-gatherer", task.Title)
	require.NotEmpty(t, task.ChildAgentID)
	child := sink.Child(task.ChildAgentID)
	assert.Equal(t, []string{"CHILD3-TASK find hello"}, userContents(t, child), "the child tab opens on the prompt")

	a.HandleOutput(spawnFrame(t, "tool_call_update", "in_progress", nil))
	a.HandleOutput(childFrame(t, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "child3 reading"},
	}))
	a.HandleOutput(childFrame(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "c_read", "title": "Read File", "kind": "read", "status": "pending",
		"rawInput": map[string]any{"path": "/w/hello.txt"},
	}))
	a.HandleOutput(childFrame(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "c_read", "title": "Read File", "status": "completed",
		"rawOutput": map[string]any{"message": "hello world"},
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "hello world"}}},
	}))
	a.HandleOutput(childFrame(t, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "CHILD3 RESULT: hello world found"},
	}))
	a.HandleOutput(spawnFrame(t, "tool_call_update", "completed", map[string]any{"rawOutput": "CHILD3 RESULT: hello world found"}))
	a.HandleOutput(messageChunk(t, "Parent got the child result.", nil))

	task, _ = sink.BackgroundTask(kiroSpawnCallID)
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
	assert.Equal(t, []string{"child3 reading", "CHILD3 RESULT: hello world found"}, childTexts(t, child))
	var childTool bool
	for _, message := range child.Messages() {
		if message.SpanID == "c_read" {
			childTool = true
		}
	}
	assert.True(t, childTool, "the child's tool call reaches the child transcript")
	assert.Equal(t, []string{"CHILD3 RESULT: hello world found"}, reportTexts(child))
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "child3 reading", "the child's text stays out of the parent")
		assert.NotEqual(t, "c_read", message.SpanID, "the child's tool call stays out of the parent")
	}
}

func TestKiroSubagentWithoutANameTakesTheDefaultTitle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "invoke_subagent_x", "status": "pending", "title": "Sub-agent",
		"rawInput": map[string]any{"prompt": "go"}, "_meta": spawnMeta(),
	}))

	task, ok := sink.BackgroundTask("invoke_subagent_x")
	require.True(t, ok)
	assert.Equal(t, kiroSubagentTitle, task.Title)
}

func TestKiroFailedSubagentReportsTheToolText(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))

	a.HandleOutput(spawnFrame(t, "tool_call_update", "failed", map[string]any{
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "The subagent hit its limit."}}},
	}))

	task, _ := sink.BackgroundTask(kiroSpawnCallID)
	assert.Equal(t, bgtask.StatusFailed, task.Status)
	assert.Equal(t, []string{"The subagent hit its limit."}, reportTexts(sink.Child(task.ChildAgentID)))
}

// A later frame of the spawn completes the row only when it states the prompt.
// A frame with no arguments, with arguments that LeapMux cannot read, or with
// no prompt changes nothing: the row keeps its title, and the child tab keeps
// the one prompt that opened it.
func TestKiroSpawnFrameWithoutAPromptChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))

	for _, rawInput := range []any{nil, "unreadable", map[string]any{"name": "renamed"}} {
		update := map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": kiroSpawnCallID, "status": "in_progress", "_meta": spawnMeta(),
		}
		if rawInput != nil {
			update["rawInput"] = rawInput
		}
		a.HandleOutput(sessionUpdate(t, kiroTestSession, update))
	}

	require.Len(t, sink.BackgroundTasks(), 1)
	task, _ := sink.BackgroundTask(kiroSpawnCallID)
	assert.Equal(t, "context-gatherer", task.Title)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	assert.Equal(t, []string{"CHILD3-TASK find hello"}, userContents(t, sink.Child(task.ChildAgentID)))
}

// A spawn that states no subtask opens its row, and no update of the parent
// routes to it.
func TestKiroSpawnWithoutASubtaskRoutesNoUpdate(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "invoke_subagent_y", "status": "pending",
		"rawInput": map[string]any{"prompt": "go"},
		"_meta":    map[string]any{"kiro": map[string]any{"kind": contracts.KiroKindAgentSubtask}},
	}))

	_, ok := sink.BackgroundTask("invoke_subagent_y")
	require.True(t, ok)
	encoded, err := json.Marshal(map[string]any{"agentSubtaskId": ""})
	require.NoError(t, err)
	assert.Empty(t, a.childUpdateRoute("agent_message_chunk", map[string]json.RawMessage{contracts.KiroMetaNamespace: encoded}))
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.children.rowBySubtask, "an empty subtask id keys no route")
}

func TestKiroChildUpdateRoute(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))
	meta := func(kiro map[string]any) map[string]json.RawMessage {
		encoded, err := json.Marshal(kiro)
		require.NoError(t, err)
		return map[string]json.RawMessage{contracts.KiroMetaNamespace: encoded}
	}

	assert.Equal(t, kiroSpawnCallID, a.childUpdateRoute("agent_message_chunk", meta(map[string]any{"agentSubtaskId": kiroSubtaskID})))
	assert.Empty(t, a.childUpdateRoute("tool_call_update", meta(map[string]any{"agentSubtaskId": kiroSubtaskID, "kind": contracts.KiroKindAgentSubtask})),
		"the spawn's own frames belong to the parent")
	assert.Empty(t, a.childUpdateRoute("agent_message_chunk", meta(map[string]any{"agentSubtaskId": "unknown"})))
	assert.Empty(t, a.childUpdateRoute("agent_message_chunk", meta(map[string]any{})))
	assert.Empty(t, a.childUpdateRoute("agent_message_chunk", nil))
}

func TestKiroSubagentRouteEndsWithTheSpawn(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))
	a.HandleOutput(spawnFrame(t, "tool_call_update", "completed", map[string]any{"rawOutput": "done"}))
	encoded, err := json.Marshal(map[string]any{"agentSubtaskId": kiroSubtaskID})
	require.NoError(t, err)

	assert.Empty(t, a.childUpdateRoute("agent_message_chunk", map[string]json.RawMessage{contracts.KiroMetaNamespace: encoded}),
		"a late update of an ended subtask reaches no transcript")
}

func TestKiroToolCallThatIsNoSpawnClaimsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))

	assert.Empty(t, sink.BackgroundTasks())
}
