package gemini

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiTodoSnapshotReadsCompletedNativeState(t *testing.T) {
	t.Parallel()
	frame := map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "write_todos__native", "status": "completed",
		"rawOutput": map[string]any{contracts.GeminiSupplementStoredToolRecord: map[string]any{
			"id": "write_todos__native", "name": "write_todos", "status": "success",
			"resultDisplay": map[string]any{"todos": []map[string]string{
				{"description": "native pending", "status": "pending"},
				{"description": "native active", "status": "in_progress"},
				{"description": "native complete", "status": "completed"},
				{"description": "native cancelled", "status": "cancelled"},
			}},
		}},
	}
	encoded, err := json.Marshal(frame)
	require.NoError(t, err)
	event, present := (geminiProvider{}).ExtractTodoEvent("tool_call", encoded, nil)
	require.True(t, present)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 4)
	assert.Equal(t, "native active", event.Snapshot[1].Content)
	assert.Equal(t, []todoevents.Status{todoevents.StatusPending, todoevents.StatusInProgress, todoevents.StatusCompleted, todoevents.StatusDeleted}, []todoevents.Status{event.Snapshot[0].Status, event.Snapshot[1].Status, event.Snapshot[2].Status, event.Snapshot[3].Status})
}

func TestGeminiTodoSnapshotPreservesEmptyAndRejectsUnrelatedData(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		id     string
		status string
		todos  string
		want   bool
	}{
		{name: "empty list clears", id: "write_todos__native", status: "success", todos: `[]`, want: true},
		{name: "null is absent", id: "write_todos__native", status: "success", todos: `null`},
		{name: "failed write changes nothing", id: "write_todos__native", status: "error", todos: `[]`},
		{name: "foreign record changes nothing", id: "write_todos__foreign", status: "success", todos: `[]`},
		{name: "malformed list changes nothing", id: "write_todos__native", status: "success", todos: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := `{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__native","status":"completed","rawOutput":{"` + contracts.GeminiSupplementStoredToolRecord + `":{"id":"` + test.id + `","name":"write_todos","status":"` + test.status + `","resultDisplay":{"todos":` + test.todos + `}}}}`
			event, present := (geminiProvider{}).ExtractTodoEvent("tool_call", []byte(frame), nil)
			assert.Equal(t, test.want, present)
			if test.want {
				assert.Empty(t, event.Snapshot)
			}
		})
	}
}
