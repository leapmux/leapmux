package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// todoUpdate is a `todo_list` result as the probe recorded it.
func todoUpdate(t *testing.T, status string, tasks []any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"sessionUpdate": contracts.ACPUpdateToolCallUpdate, "toolCallId": "t_todo2", "status": status,
		"title":     contracts.KiroToolTitleTaskList,
		"rawInput":  map[string]any{"command": "complete", "completed_task_ids": map[string]any{"0": "1"}},
		"rawOutput": map[string]any{"tasks": tasks, "description": "V3 list", "context": []any{"did one"}},
	})
	require.NoError(t, err)
	return data
}

func TestKiroTodoListCallIsASnapshot(t *testing.T) {
	t.Parallel()
	content := todoUpdate(t, "completed", []any{
		map[string]any{"id": "1", "task_description": "one", "details": " first ", "completed": true},
		map[string]any{"id": "2", "task_description": "two", "completed": false},
		map[string]any{"id": "3", "task_description": "  ", "completed": false},
	})

	event, ok := kiroProvider{}.ExtractTodoEvent("", content, nil)

	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{ID: "1", Content: "one", Description: "first", Status: todoevents.StatusCompleted},
		{ID: "2", Content: "two", Status: todoevents.StatusPending},
	}, event.Snapshot, "a task without text is no item")
}

func TestKiroEmptyTodoListClearsTheList(t *testing.T) {
	t.Parallel()
	event, ok := kiroProvider{}.ExtractTodoEvent("", todoUpdate(t, "completed", []any{}), nil)

	require.True(t, ok, "a list that the model emptied is a snapshot too")
	assert.Empty(t, event.Snapshot)
}

func TestKiroTodoExtractionIgnoresEveryOtherMessage(t *testing.T) {
	t.Parallel()
	for name, content := range map[string][]byte{
		"a running call": todoUpdate(t, "in_progress", []any{map[string]any{"id": "1", "task_description": "one"}}),
		"a failed call":  todoUpdate(t, "failed", []any{map[string]any{"id": "1", "task_description": "one"}}),
		"another tool": []byte(`{"sessionUpdate":"tool_call_update","status":"completed","title":"Read File",` +
			`"rawOutput":{"tasks":[{"id":"1","task_description":"Task List"}]}}`),
		"the opening call": []byte(`{"sessionUpdate":"tool_call","status":"pending","title":"Task List","rawInput":{"command":"create"}}`),
		"a message that never states the list": []byte(`{"sessionUpdate":"tool_call_update","status":"completed","title":"Read File",` +
			`"rawOutput":{"tasks":[{"id":"1","task_description":"one"}]}}`),
		"no task list": []byte(`{"sessionUpdate":"tool_call_update","status":"completed","title":"Task List","rawOutput":{"message":"x"}}`),
		"not json":     []byte(`Task List`),
		"a text chunk": []byte(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Task List"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := kiroProvider{}.ExtractTodoEvent("", content, nil)
			assert.False(t, ok)
		})
	}
}

func TestKiroTodoExtractionReadsTheSharedPlan(t *testing.T) {
	t.Parallel()
	event, ok := kiroProvider{}.ExtractTodoEvent("", []byte(`{"sessionUpdate":"plan","entries":[{"content":"one","status":"in_progress"}]}`), nil)

	require.True(t, ok)
	require.Len(t, event.Snapshot, 1)
	assert.Equal(t, "one", event.Snapshot[0].Content)
}
