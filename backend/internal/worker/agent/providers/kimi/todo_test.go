package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestKimiExtractTodoEvent(t *testing.T) {
	t.Parallel()

	const call = `{"type":"tool.call.started","agentId":"main","turnId":0,"toolCallId":"c1","name":"TodoList","args":{"todos":[` +
		`{"title":"Read the code","status":"done"},{"title":"Write the fix","status":"in_progress"},{"title":"Run the tests","status":"pending"}]}}`
	event, ok := kimiProvider{}.ExtractTodoEvent(contracts.KimiToolTodoList, []byte(call), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{Content: "Read the code", Status: todoevents.StatusCompleted},
		{Content: "Write the fix", Status: todoevents.StatusInProgress},
		{Content: "Run the tests", Status: todoevents.StatusPending},
	}, event.Snapshot)

	cleared, ok := kimiProvider{}.ExtractTodoEvent(contracts.KimiToolTodoList,
		[]byte(`{"type":"tool.call.started","name":"TodoList","args":{"todos":[]}}`), nil)
	require.True(t, ok, "an empty list clears the list")
	assert.Empty(t, cleared.Snapshot)

	for name, tc := range map[string]struct {
		spanType string
		content  string
	}{
		"a call that only reads the list": {contracts.KimiToolTodoList, `{"type":"tool.call.started","name":"TodoList","args":{}}`},
		"the call's result":               {contracts.KimiToolTodoList, `{"type":"tool.result","name":"TodoList","output":"ok"}`},
		"another tool's span":             {contracts.KimiToolBash, call},
		"another tool on the span":        {contracts.KimiToolTodoList, `{"type":"tool.call.started","name":"Bash","args":{"todos":[]}}`},
		"bytes that are not JSON":         {contracts.KimiToolTodoList, `not json`},
	} {
		_, ok := kimiProvider{}.ExtractTodoEvent(tc.spanType, []byte(tc.content), nil)
		assert.False(t, ok, name)
	}
}

func TestKimiTodoStatus(t *testing.T) {
	t.Parallel()

	assert.Equal(t, todoevents.StatusPending, kimiTodoStatus("pending"))
	assert.Equal(t, todoevents.StatusInProgress, kimiTodoStatus("in_progress"))
	assert.Equal(t, todoevents.StatusCompleted, kimiTodoStatus("done"))
	assert.Equal(t, todoevents.StatusFromProviderWord("completed"), kimiTodoStatus("completed"), "another word takes the shared reading")
}
