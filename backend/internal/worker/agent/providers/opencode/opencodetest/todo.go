package opencodetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertReadsTheFamilyTodoResult pins that plugin reads the todo list that an
// OpenCode-family daemon reports in a completed todo tool's raw output.
func AssertReadsTheFamilyTodoResult(t *testing.T, plugin agent.Provider) {
	t.Helper()
	event, ok := plugin.ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[{"content":"Inspect sample","status":"in_progress"},{"content":"Report findings","status":"completed"}]}}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 2)
	assert.Equal(t, "Inspect sample", event.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusInProgress, event.Snapshot[0].Status)
	assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[1].Status)
}
