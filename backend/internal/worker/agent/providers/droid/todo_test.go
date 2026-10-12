package droid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestDroidTodoWriteReplacesTheWholeList(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"tool_call","toolUse":{"type":"tool_use","id":"todo-1","name":"TodoWrite","input":{"todos":[` +
		`{"content":"Inspect the repository","status":"completed"},` +
		`{"content":"List three checks","status":"in_progress"},` +
		`{"content":"Report their purpose","status":"pending"}]}}}`)

	event, ok := droidProvider{}.ExtractTodoEvent("TodoWrite", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 3)
	assert.Equal(t, todoevents.Item{Content: "Inspect the repository", Status: todoevents.StatusCompleted}, event.Snapshot[0])
	assert.Equal(t, todoevents.Item{Content: "List three checks", Status: todoevents.StatusInProgress}, event.Snapshot[1])
	assert.Equal(t, todoevents.Item{Content: "Report their purpose", Status: todoevents.StatusPending}, event.Snapshot[2])
}

func TestDroidTodoWriteAnEmptyListClears(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"tool_call","toolUse":{"type":"tool_use","id":"todo-1","name":"TodoWrite","input":{"todos":[]}}}`)
	event, ok := droidProvider{}.ExtractTodoEvent("TodoWrite", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestDroidTodoWriteIgnoresOtherFrames(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"type":"tool_result","toolUseId":"todo-1","content":"Saved"}`,
		`{"type":"tool_call","toolUse":{"id":"call-1","name":"Read","input":{"file_path":"/repo/a.txt"}}}`,
		`{"type":"tool_call","toolUse":{"id":"call-1","name":"TodoWrite","input":"bad input"}}`,
	} {
		_, ok := droidProvider{}.ExtractTodoEvent("TodoWrite", []byte(content), nil)
		assert.False(t, ok, content)
	}
}
