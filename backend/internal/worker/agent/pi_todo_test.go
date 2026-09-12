package agent

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiTodoSnapshot(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"tool_execution_end","toolCallId":"todo-call","toolName":"todo","isError":false,"result":{"content":[],"details":{"action":"list","tasks":[{"id":1,"subject":"Inspect sample","description":"Read the sample.","activeForm":"Inspecting sample","status":"in_progress"},{"id":2,"subject":"Old task","status":"deleted"}]}}}`)
	event, ok := (piProvider{}).ExtractTodoEvent("todo", raw, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{ID: "1", Content: "Inspect sample", Description: "Read the sample.", ActiveForm: "Inspecting sample", Status: todoevents.StatusInProgress},
		{ID: "2", Content: "Old task", Status: todoevents.StatusDeleted},
	}, event.Snapshot)
}

func TestPiTodoClear(t *testing.T) {
	t.Parallel()
	event, ok := (piProvider{}).ExtractTodoEvent("todo", []byte(`{"type":"tool_execution_end","toolName":"todo","result":{"details":{"action":"clear","tasks":[],"nextId":1}}}`), nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
}

func TestPiTodoInvalidSnapshot(t *testing.T) {
	t.Parallel()
	for _, tasks := range []string{`null`, `{}`, `[null]`, `[{"id":0,"subject":"Invalid","status":"pending"}]`, `[{"id":1.5,"subject":"Invalid","status":"pending"}]`, `[{"id":9007199254740992,"subject":"Invalid","status":"pending"}]`, `[{"id":1,"subject":"  ","status":"pending"}]`, `[{"id":1,"subject":"Invalid","status":"unknown"}]`, `[{"id":1,"subject":"First","status":"pending"},{"id":1,"subject":"Duplicate","status":"pending"}]`} {
		t.Run(tasks, func(t *testing.T) {
			_, ok := (piProvider{}).ExtractTodoEvent("todo", []byte(`{"type":"tool_execution_end","toolName":"todo","result":{"details":{"action":"list","tasks":`+tasks+`}}}`), nil)
			assert.False(t, ok)
		})
	}
}

func TestPiTodoSnapshotFromFailedOperation(t *testing.T) {
	t.Parallel()
	event, ok := (piProvider{}).ExtractTodoEvent("todo", []byte(`{"type":"tool_execution_end","toolName":"todo","result":{"details":{"action":"update","error":"#99 not found","tasks":[{"id":1,"subject":"Existing task","status":"pending"}]}}}`), nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, 1)
	assert.Equal(t, "Existing task", event.Snapshot[0].Content)
}

func TestPiTodoOnlyProjectsCompletedTodoCalls(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"tool_execution_start","toolName":"todo","result":{"details":{"tasks":[]}}}`,
		`{"type":"tool_execution_end","toolName":"other","result":{"details":{"tasks":[]}}}`,
		`{"type":"tool_execution_end","toolName":"todo","result":{"details":{}}}`,
		`{"type":"message_end","message":{"role":"toolResult","toolName":"todo","details":{"tasks":[]}}}`,
		`{"sessionUpdate":"plan","entries":[{"content":"one"}]}`,
		`{"method":"turn/plan/updated","params":{"plan":[{"step":"x"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`,
	} {
		_, ok := (piProvider{}).ExtractTodoEvent("todo", []byte(raw), nil)
		assert.False(t, ok)
	}
}
