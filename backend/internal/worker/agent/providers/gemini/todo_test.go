package gemini

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// The worker never hands ExtractTodoEvent a frame that carries the record
// inline. It resolves the stored row -- the agent's own frame and the supplement
// that the tool transcript stored beside it -- and extracts from that
// (worker/service: persistAndBroadcast and applyTodoEventForEnrichment). The
// frame and the record below are the bytes that the installed Gemini CLI 0.62.0
// wrote for one write_todos call: the frame states no list, so only the record
// that the transcript supplies can.
func TestGeminiTodoSnapshotReadsTheRecordThatTheTranscriptStored(t *testing.T) {
	t.Parallel()
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__probe-todo-1","status":"completed","title":"Set 5 todo(s)","content":[],"locations":[],"kind":"other"}`)
	record := []byte(`{"id":"write_todos__probe-todo-1","name":"write_todos","args":{"todos":[{"description":"PROBE_TASK_pending","status":"pending"},{"description":"PROBE_TASK_in_progress","status":"in_progress"},{"description":"PROBE_TASK_completed","status":"completed"},{"description":"PROBE_TASK_cancelled","status":"cancelled"},{"description":"PROBE_TASK_blocked","status":"blocked"}]},"result":[{"functionResponse":{"id":"write_todos__probe-todo-1","name":"write_todos","response":{"output":"Successfully updated the todo list. The current list is now:\n1. [pending] PROBE_TASK_pending\n2. [in_progress] PROBE_TASK_in_progress\n3. [completed] PROBE_TASK_completed\n4. [cancelled] PROBE_TASK_cancelled\n5. [blocked] PROBE_TASK_blocked"}}}],"status":"success","timestamp":"2026-10-04T16:41:29.474Z","resultDisplay":{"todos":[{"description":"PROBE_TASK_pending","status":"pending"},{"description":"PROBE_TASK_in_progress","status":"in_progress"},{"description":"PROBE_TASK_completed","status":"completed"},{"description":"PROBE_TASK_cancelled","status":"cancelled"},{"description":"PROBE_TASK_blocked","status":"blocked"}]},"description":"Set 5 todo(s)","displayName":"WriteTodos","renderOutputAsMarkdown":true}`)
	supplemental, err := geminiToolSupplement(original, record)
	require.NoError(t, err)
	provider := Registration().Plugin
	resolved := agent.ResolveMessageContent(provider, agent.MessageContent{Original: original, Supplemental: supplemental})

	event, present := provider.ExtractTodoEvent("other", resolved, nil)

	require.True(t, present, "the stored record states the list: %s", resolved)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{Content: "PROBE_TASK_pending", Status: todoevents.StatusPending},
		{Content: "PROBE_TASK_in_progress", Status: todoevents.StatusInProgress},
		{Content: "PROBE_TASK_completed", Status: todoevents.StatusCompleted},
		{Content: "PROBE_TASK_cancelled", Status: todoevents.StatusDeleted},
		{Content: "PROBE_TASK_blocked", Status: todoevents.StatusBlocked},
	}, event.Snapshot)
	_, present = provider.ExtractTodoEvent("other", agent.ResolveMessageContent(provider, agent.MessageContent{Original: original}), nil)
	assert.False(t, present, "the frame alone states no list")
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
