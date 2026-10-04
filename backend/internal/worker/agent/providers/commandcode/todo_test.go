package commandcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeTasksCreateAndUpdateSidebarItems(t *testing.T) {
	created, ok := todoEvent("task_create", json.RawMessage(`{"subject":"Native task","description":"Exact task details","activeForm":"Read the native file"}`), "Task #17 created: Native task")
	require.True(t, ok)
	assert.Equal(t, todoevents.KindCreate, created.Kind)
	assert.Equal(t, "17", created.Item.ID)
	assert.Equal(t, "Native task", created.Item.Content)
	assert.Equal(t, todoevents.StatusPending, created.Item.Status)
	updated, ok := todoEvent("task_update", json.RawMessage(`{"taskId":"17","status":"completed","subject":""}`), "Updated task #17: status\nStatus: pending -> completed")
	require.True(t, ok)
	require.NotNil(t, updated.Patch.Status)
	assert.Equal(t, todoevents.StatusCompleted, *updated.Patch.Status)
	require.NotNil(t, updated.Patch.Content)
	assert.Empty(t, *updated.Patch.Content)
}

func TestNativeTaskChangesRequireAMatchingSuccessfulResult(t *testing.T) {
	for _, input := range []struct{ tool, args, result string }{
		{"task_create", `{}`, "Task #17 created: Native task"},
		{"task_create", `{"subject":"Native task"}`, "Native task creation failed"},
		{"task_update", `{"taskId":"17","status":"completed"}`, "Updated task #18: status"},
		{"task_update", `{"taskId":"17","status":"unknown"}`, "Updated task #17: status"},
		{"unknown", `{}`, "Task #17 created: Native task"},
	} {
		_, ok := todoEvent(input.tool, json.RawMessage(input.args), input.result)
		assert.False(t, ok, input)
	}
}
