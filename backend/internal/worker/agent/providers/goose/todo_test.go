package goose

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gooseTodoRequest = `{"sessionUpdate":"tool_call","toolCallId":"todo-1","status":"in_progress",` +
	`"rawInput":{"content":"- [x] Inspect the repository\n- [ ] Report the finding"},` +
	`"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`

const gooseTodoResult = `{"sessionUpdate":"tool_call_update","toolCallId":"todo-1",` +
	`"status":"completed","content":[{"type":"content","content":{"type":"text","text":"Updated (62 chars)"}}]}`

func TestGooseExtractTodoEventReadsCompletedNativeToolResult(t *testing.T) {
	t.Parallel()
	paired := func() []byte { return []byte(gooseTodoRequest) }
	event, ok := (gooseProvider{}).ExtractTodoEvent("todo_write", []byte(gooseTodoResult), paired)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{Content: "Inspect the repository", Status: todoevents.StatusCompleted},
		{Content: "Report the finding", Status: todoevents.StatusPending},
	}, event.Snapshot)
}

func TestGooseExtractTodoEventKeepsAnEmptyReplacement(t *testing.T) {
	t.Parallel()
	request := `{"sessionUpdate":"tool_call","toolCallId":"todo-1","rawInput":{"content":""},` +
		`"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`
	event, ok := (gooseProvider{}).ExtractTodoEvent("todo_write", []byte(gooseTodoResult), func() []byte { return []byte(request) })
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestGooseExtractTodoEventRefusesAnUnfinishedOrMismatchedCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		result  string
		request string
	}{
		{"failed", `{"sessionUpdate":"tool_call_update","toolCallId":"todo-1","status":"failed"}`, gooseTodoRequest},
		{"pending", `{"sessionUpdate":"tool_call_update","toolCallId":"todo-1","status":"in_progress"}`, gooseTodoRequest},
		{"wrong id", gooseTodoResult, `{"sessionUpdate":"tool_call","toolCallId":"other","rawInput":{"content":"- [x] Inspect"},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`},
		{"wrong extension", gooseTodoResult, `{"sessionUpdate":"tool_call","toolCallId":"todo-1","rawInput":{"content":"- [x] Inspect"},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"developer"}}}}`},
		{"wrong tool", gooseTodoResult, `{"sessionUpdate":"tool_call","toolCallId":"todo-1","rawInput":{"content":"- [x] Inspect"},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_read","extensionName":"todo"}}}}`},
		{"missing content", gooseTodoResult, `{"sessionUpdate":"tool_call","toolCallId":"todo-1","rawInput":{},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`},
		{"not a request", gooseTodoResult, `{"sessionUpdate":"tool_call_update","toolCallId":"todo-1","rawInput":{"content":"- [x] Inspect"},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`},
		{"bad checklist", gooseTodoResult, `{"sessionUpdate":"tool_call","toolCallId":"todo-1","rawInput":{"content":"- [x] Inspect\nnot a checklist"},"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := (gooseProvider{}).ExtractTodoEvent("todo_write", []byte(tc.result), func() []byte { return []byte(tc.request) })
			assert.False(t, ok)
			assert.Equal(t, todoevents.Event{}, event)
		})
	}
}

func TestGooseExtractTodoEventReadsAlternateMarkersAndCRLF(t *testing.T) {
	t.Parallel()
	request := `{"sessionUpdate":"tool_call","toolCallId":"todo-1",` +
		`"rawInput":{"content":"* [X] First task\r\n\r\n+ [ ] Second task\r\n"},` +
		`"_meta":{"goose":{"toolCall":{"toolName":"todo__todo_write","extensionName":"todo"}}}}`
	event, ok := (gooseProvider{}).ExtractTodoEvent("todo_write", []byte(gooseTodoResult), func() []byte { return []byte(request) })
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{
		{Content: "First task", Status: todoevents.StatusCompleted},
		{Content: "Second task", Status: todoevents.StatusPending},
	}, event.Snapshot)
}

func TestGooseExtractTodoEventRefusesAMissingPairedRequest(t *testing.T) {
	t.Parallel()
	_, ok := (gooseProvider{}).ExtractTodoEvent("todo_write", []byte(gooseTodoResult), nil)
	assert.False(t, ok)
}

func TestGooseExtractTodoEventStillReadsACPPlans(t *testing.T) {
	t.Parallel()
	event, ok := (gooseProvider{}).ExtractTodoEvent("", []byte(`{"sessionUpdate":"plan","entries":[{"content":"Keep planning","status":"pending"}]}`), nil)
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{{Content: "Keep planning", Status: todoevents.StatusPending}}, event.Snapshot)
}
