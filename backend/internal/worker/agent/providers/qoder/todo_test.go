package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestQoderWriteTodosReplacesTheWholeList(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","session_id":"s","message":{"content":[` +
		`{"type":"text","text":"tracking"},` +
		`{"type":"tool_use","id":"call-1","name":"WriteTodos","input":{"todos":[` +
		`{"description":"ship it","status":"in_progress"},` +
		`{"description":"write tests","status":"pending"},` +
		`{"description":"drop this","status":"cancelled"}]}}]}}`)

	event, ok := qoderProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 3)
	assert.Equal(t, todoevents.Item{Content: "ship it", Status: todoevents.StatusInProgress}, event.Snapshot[0])
	assert.Equal(t, todoevents.Item{Content: "write tests", Status: todoevents.StatusPending}, event.Snapshot[1])
	assert.Equal(t, todoevents.Item{Content: "drop this", Status: todoevents.StatusDeleted}, event.Snapshot[2])
}

// A model that spells the description `content` reaches the same parser: the
// CLI maps that alias before the tool runs.
func TestQoderWriteTodosAcceptsTheContentFieldSpelling(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"call-1","name":"WriteTodos","input":{"todos":[` +
		`{"content":"ship it","status":"pending"}]}}]}}`)

	event, ok := qoderProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, 1)
	assert.Equal(t, "ship it", event.Snapshot[0].Content)
}

// The tool's own contract is to clear the list by sending an empty one, so an
// empty snapshot is a real state rather than "no event".
func TestQoderWriteTodosAnEmptyListClears(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"call-1","name":"WriteTodos","input":{"todos":[]}}]}}`)

	event, ok := qoderProvider{}.ExtractTodoEvent("", content, nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
}

// The result half of a WriteTodos call carries no list. It must not read as an
// empty snapshot that would wipe the row the use half just wrote.
func TestQoderWriteTodosIgnoresTheResultHalf(t *testing.T) {
	t.Parallel()
	content := []byte(`{"type":"user","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"call-1","content":"Successfully updated the todo list."}]}}`)

	_, ok := qoderProvider{}.ExtractTodoEvent("", content, nil)
	assert.False(t, ok)
}

func TestQoderWriteTodosIgnoresAPayloadOutsideTheFamily(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"type":"system","message":{"content":[]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"WriteTodos","input":"not an object"}]}}`,
	} {
		_, ok := qoderProvider{}.ExtractTodoEvent("", []byte(content), nil)
		assert.False(t, ok, content)
	}
}

func TestQoderTodoStatusWordsMapOntoTheNeutralVocabulary(t *testing.T) {
	t.Parallel()
	assert.Equal(t, todoevents.StatusPending, todoevents.StatusFromProviderWord("pending"))
	assert.Equal(t, todoevents.StatusInProgress, todoevents.StatusFromProviderWord("in_progress"))
	assert.Equal(t, todoevents.StatusCompleted, todoevents.StatusFromProviderWord("completed"))
	// A cancelled task is the tombstone, the same end state every provider's
	// cancel word maps to.
	assert.Equal(t, todoevents.StatusDeleted, todoevents.StatusFromProviderWord("cancelled"))
	// The neutral vocabulary has no blocked state, and an unrecognized word
	// claims the least about the row.
	assert.Equal(t, todoevents.StatusPending, todoevents.StatusFromProviderWord("blocked"))
}
