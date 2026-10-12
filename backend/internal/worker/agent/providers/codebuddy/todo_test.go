package codebuddy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestCodebuddyTodoWriteReplacesTheWholeList(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","session_id":"s","message":{"content":[` +
		`{"type":"text","text":"tracking"},` +
		`{"type":"tool_use","id":"call-1","name":"TodoWrite","input":{` +
		`"oldTodos":[{"content":"a","activeForm":"A","status":"completed"}],` +
		`"newTodos":[` +
		`{"content":"ship it","activeForm":"Shipping","status":"in_progress"},` +
		`{"content":"write tests","activeForm":"Writing tests","status":"pending"}]}}]}}`)

	event, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 2)
	assert.Equal(t, todoevents.Item{Content: "ship it", ActiveForm: "Shipping", Status: todoevents.StatusInProgress}, event.Snapshot[0])
	assert.Equal(t, todoevents.Item{Content: "write tests", ActiveForm: "Writing tests", Status: todoevents.StatusPending}, event.Snapshot[1])
}

// The tool_use input states the list as `newTodos`. A model that spells the
// field `todos` reaches the same parser: the CLI's own converter accepts both.
func TestCodebuddyTodoWriteAcceptsTheTodosFieldSpelling(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"call-1","name":"TodoWrite","input":{` +
		`"todos":[{"content":"ship it","activeForm":"Shipping","status":"pending"}]}}]}}`)

	event, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, 1)
	assert.Equal(t, "ship it", event.Snapshot[0].Content)
}

// An empty new list is a real state: the tool's own contract is to clear the
// list that way. It must not be read as "no event".
func TestCodebuddyTodoWriteAnEmptyListClears(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"call-1","name":"TodoWrite","input":{"oldTodos":[],"newTodos":[]}}]}}`)

	event, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

// The user half of a TodoWrite call carries the tool_result and no list. It
// must not read as an empty snapshot that would wipe the row the use half just
// wrote.
func TestCodebuddyTodoWriteIgnoresTheResultHalf(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"user","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"call-1","content":"Todo list updated successfully"}]}}`)

	_, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	assert.False(t, ok)
}

// Every Task* result returns the whole list under `_meta.rawResponse.todos`, so
// each one is a snapshot rather than a one-row mutation.
func TestCodebuddyTaskResultIsASnapshotOfTheWholeList(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-2","content":"ok","_meta":{"rawResponse":{` +
		`"task":{"id":"7","subject":"ship it","status":"in_progress"},` +
		`"todos":[` +
		`{"id":"7","content":"ship it","activeForm":"Shipping","status":"in_progress"},` +
		`{"id":"8","content":"write tests","activeForm":"Writing tests","status":"completed"}]}}}]}}`)

	event, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 2)
	assert.Equal(t, todoevents.Item{ID: "7", Content: "ship it", ActiveForm: "Shipping", Status: todoevents.StatusInProgress}, event.Snapshot[0])
	assert.Equal(t, todoevents.Item{ID: "8", Content: "write tests", ActiveForm: "Writing tests", Status: todoevents.StatusCompleted}, event.Snapshot[1])
}

// A TaskGet result returns the single task it read, in the storage shape: the
// subject is `subject`, not `content`. That is a one-row detail.
func TestCodebuddyTaskGetResultIsOneRowDetail(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-3","content":"ok","_meta":{"rawResponse":{` +
		`"id":"7","subject":"ship it","description":"cut the release","activeForm":"Shipping","status":"pending"}}}]}}`)

	event, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindDetail, event.Kind)
	assert.Equal(t, todoevents.Item{
		ID: "7", Content: "ship it", Description: "cut the release",
		ActiveForm: "Shipping", Status: todoevents.StatusPending,
	}, event.Item)
}

// A failed call returns an error payload with no list. It must leave the list
// alone rather than wipe it.
func TestCodebuddyFailedTaskResultChangesNothing(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","is_error":true,"_meta":{"rawResponse":{"todos":[{"id":"7","content":"x","status":"pending"}]}}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","content":"Error: Task not found","_meta":{"rawResponse":{"taskId":"7","error":"Task not found"}}}]}}`,
	} {
		_, ok := codebuddyProvider{}.ExtractTodoEvent("", []byte(content), nil)
		assert.False(t, ok, content)
	}
}

// A structured result of another tool must never become a row of the list.
func TestCodebuddyUnknownResultShapeChangesNothing(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"type":"system","message":{"content":[]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","_meta":{"rawResponse":{"is_error":true,"error":"boom"}}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","_meta":{"rawResponse":{"id":"","subject":"","status":""}}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"Bash","input":{"command":"ls"}}]}}`,
	} {
		_, ok := codebuddyProvider{}.ExtractTodoEvent("", []byte(content), nil)
		assert.False(t, ok, content)
	}
}

// An unparseable TodoWrite input reports no event rather than an empty snapshot
// that would wipe the list.
func TestCodebuddyUnreadableTodoWriteInputChangesNothing(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"call-1","name":"TodoWrite","input":"not an object"}]}}`)

	_, ok := codebuddyProvider{}.ExtractTodoEvent("", content, nil)
	assert.False(t, ok)
}

func TestCodebuddyTodoStatusWordsMapOntoTheNeutralVocabulary(t *testing.T) {
	t.Parallel()
	assert.Equal(t, todoevents.StatusPending, todoevents.StatusFromProviderWord("pending"))
	assert.Equal(t, todoevents.StatusInProgress, todoevents.StatusFromProviderWord("in_progress"))
	assert.Equal(t, todoevents.StatusCompleted, todoevents.StatusFromProviderWord("completed"))
	assert.Equal(t, todoevents.StatusDeleted, todoevents.StatusFromProviderWord("deleted"))
}
