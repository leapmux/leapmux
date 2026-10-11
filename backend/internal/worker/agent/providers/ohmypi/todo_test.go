package ohmypi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frameTodoInit is omp 18.2.11's `todo init` end frame from probe s2.
const frameTodoInit = `{"type":"tool_execution_end","toolCallId":"call_6","toolName":"todo","result":{"content":[{"type":"text","text":"Remaining items (2)"}],"details":{"op":"init","phases":[{"name":"Build","tasks":[{"content":"Write code","status":"in_progress"},{"content":"Test it","status":"pending"}]}],"storage":"session"}},"isError":false}`

func TestExtractTodoEventReadsTheSnapshot(t *testing.T) {
	t.Parallel()
	event, ok := ompProvider{}.ExtractTodoEvent("todo", []byte(frameTodoInit), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{ID: "Build/Write code", Content: "Write code", Description: "Build", Status: todoevents.StatusInProgress},
		{ID: "Build/Test it", Content: "Test it", Description: "Build", Status: todoevents.StatusPending},
	}, event.Snapshot)
}

func TestExtractTodoEventKeepsEveryPhaseAndStatus(t *testing.T) {
	t.Parallel()
	frame := `{"type":"tool_execution_end","toolCallId":"c","toolName":"todo","result":{"details":{"phases":[` +
		`{"name":"Plan","tasks":[{"content":"Read","status":"completed"},{"content":"Old idea","status":"abandoned"}]},` +
		`{"name":"Build","tasks":[{"content":"Deploy","status":"blocked","blocker":"waiting for the key"},{"content":"  ","status":"pending"},{"content":"Deploy","status":"pending"},{"content":"Odd","status":"hibernating"}]}` +
		`]}},"isError":false}`
	event, ok := ompProvider{}.ExtractTodoEvent("todo", []byte(frame), nil)
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{
		{ID: "Plan/Read", Content: "Read", Description: "Plan", Status: todoevents.StatusCompleted},
		{ID: "Plan/Old idea", Content: "Old idea", Description: "Plan", Status: todoevents.StatusDeleted},
		{ID: "Build/Deploy", Content: "Deploy", Description: "Build\nBlocked: waiting for the key", Status: todoevents.StatusPending},
		{ID: "Build/Deploy#2", Content: "Deploy", Description: "Build", Status: todoevents.StatusPending},
		{ID: "Build/Odd", Content: "Odd", Description: "Build", Status: todoevents.StatusPending},
	}, event.Snapshot, "a blank task is skipped, and a repeated text keeps a row of its own")
}

// A phase with no name still keeps a task's blocker, with no blank first line.
func TestExtractTodoEventKeepsTheBlockerOfAnUnnamedPhase(t *testing.T) {
	t.Parallel()
	frame := `{"type":"tool_execution_end","toolCallId":"c","toolName":"todo","result":{"details":{"phases":[` +
		`{"name":"","tasks":[{"content":" Deploy ","status":"blocked","blocker":"  waiting for the key "},{"content":"Test","status":"pending","blocker":"   "}]}` +
		`]}},"isError":false}`
	event, ok := ompProvider{}.ExtractTodoEvent("todo", []byte(frame), nil)
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{
		{ID: "/Deploy", Content: "Deploy", Description: "Blocked: waiting for the key", Status: todoevents.StatusPending},
		{ID: "/Test", Content: "Test", Description: "", Status: todoevents.StatusPending},
	}, event.Snapshot)
}

func TestExtractTodoEventClearsAnEmptyList(t *testing.T) {
	t.Parallel()
	event, ok := ompProvider{}.ExtractTodoEvent("todo", []byte(`{"type":"tool_execution_end","toolName":"todo","result":{"details":{"op":"clear","phases":[]}},"isError":false}`), nil)
	require.True(t, ok, "an empty list is a snapshot too: it clears the panel")
	assert.Empty(t, event.Snapshot)
}

func TestExtractTodoEventRefuses(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		spanType string
		frame    string
	}{
		"another tool's span":  {spanType: "bash", frame: frameTodoInit},
		"a start frame":        {spanType: "todo", frame: `{"type":"tool_execution_start","toolCallId":"c","toolName":"todo","args":{}}`},
		"another tool's frame": {spanType: "todo", frame: `{"type":"tool_execution_end","toolName":"bash","result":{"details":{"phases":[]}},"isError":false}`},
		"a failed call":        {spanType: "todo", frame: `{"type":"tool_execution_end","toolName":"todo","result":{"details":{"phases":[]}},"isError":true}`},
		"no phases":            {spanType: "todo", frame: `{"type":"tool_execution_end","toolName":"todo","result":{"details":{}},"isError":false}`},
		"malformed JSON":       {spanType: "todo", frame: `{`},
	}
	for name, tc := range cases {
		_, ok := ompProvider{}.ExtractTodoEvent(tc.spanType, []byte(tc.frame), nil)
		assert.False(t, ok, name)
	}
}
