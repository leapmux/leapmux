package mimo

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func taskEvent(t *testing.T, status string, input map[string]any, metadata map[string]any) []byte {
	t.Helper()
	return toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: status, Input: input, Metadata: metadata})
}

func taskOperation(action string, fields map[string]any) map[string]any {
	operation := map[string]any{"action": action}
	for key, value := range fields {
		operation[key] = value
	}
	return map[string]any{"operation": operation}
}

func TestExtractTodoEvent(t *testing.T) {
	t.Parallel()
	detail := func(item todoevents.Item) todoevents.Event {
		return todoevents.Event{Kind: todoevents.KindDetail, Item: item}
	}

	for _, tc := range []struct {
		name  string
		event []byte
		want  todoevents.Event
	}{
		{name: "a create adds a row",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionCreate, map[string]any{"summary": " Write the parser "}),
				map[string]any{"id": "T1", "status": contracts.MiMoTaskStatusOpen}),
			want: todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{ID: "T1", Content: "Write the parser", Status: todoevents.StatusPending}}},
		{name: "a start marks the row in progress",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionStart, map[string]any{"id": "T1"}),
				map[string]any{"id": "T1", "status": contracts.MiMoTaskStatusInProgress}),
			want: detail(todoevents.Item{ID: "T1", Status: todoevents.StatusInProgress})},
		{name: "a block keeps the row pending",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionBlock, map[string]any{"id": "T1", "event_summary": "waiting"}),
				map[string]any{"id": "T1", "status": contracts.MiMoTaskStatusBlocked}),
			want: detail(todoevents.Item{ID: "T1", Status: todoevents.StatusPending})},
		{name: "a done completes the row",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionDone, map[string]any{"id": "T1.2", "event_summary": "shipped"}),
				map[string]any{"id": "T1.2", "status": contracts.MiMoTaskStatusDone}),
			want: detail(todoevents.Item{ID: "T1.2", Status: todoevents.StatusCompleted})},
		{name: "an abandon deletes the row",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionAbandon, map[string]any{"id": "T1"}),
				map[string]any{"id": "T1", "status": contracts.MiMoTaskStatusAbandoned}),
			want: detail(todoevents.Item{ID: "T1", Status: todoevents.StatusDeleted})},
		{name: "a rename changes the text and restates the status",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionRename, map[string]any{"id": "T1", "summary": "Write the lexer"}),
				map[string]any{"id": "T1", "status": contracts.MiMoTaskStatusInProgress}),
			want: detail(todoevents.Item{ID: "T1", Content: "Write the lexer", Status: todoevents.StatusInProgress})},
		{name: "an id from the input serves when the result states none",
			event: taskEvent(t, contracts.MiMoToolStatusCompleted,
				taskOperation(contracts.MiMoTaskActionUnblock, map[string]any{"id": "T3"}),
				map[string]any{"status": contracts.MiMoTaskStatusOpen}),
			want: detail(todoevents.Item{ID: "T3", Status: todoevents.StatusPending})},
		{name: "a list restates the rows it lists and leaves the rest",
			event: toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: contracts.MiMoToolStatusCompleted,
				Input:    taskOperation(contracts.MiMoTaskActionList, nil),
				Output:   "T1 in_progress — Write the parser\nT1.1 open — Tokenize — carefully\nnot a task line",
				Metadata: map[string]any{"count": 2, "ids": []string{"T1", "T1.1"}}}),
			want: todoevents.Event{Kind: todoevents.KindMerge, Items: []todoevents.Item{
				{ID: "T1", Status: todoevents.StatusInProgress, Content: "Write the parser"},
				{ID: "T1.1", Status: todoevents.StatusPending, Content: "Tokenize — carefully"},
			}}},
		{name: "a get restates one row",
			event: toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: contracts.MiMoToolStatusCompleted,
				Input:    taskOperation(contracts.MiMoTaskActionGet, map[string]any{"id": "T2"}),
				Output:   `{"id":"T2","session_id":"ses_test","status":"done","summary":"Ship it","created_at":1,"last_event_at":2}`,
				Metadata: map[string]any{"id": "T2", "status": "done"}}),
			want: detail(todoevents.Item{ID: "T2", Content: "Ship it", Status: todoevents.StatusCompleted})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := mimoProvider{}.ExtractTodoEvent(contracts.MiMoToolTask, tc.event, nil)
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractTodoEventChangesNothing(t *testing.T) {
	t.Parallel()
	completed := contracts.MiMoToolStatusCompleted

	for _, tc := range []struct {
		name     string
		spanType string
		event    []byte
	}{
		{name: "an empty list", spanType: contracts.MiMoToolTask,
			event: toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: completed,
				Input: taskOperation(contracts.MiMoTaskActionList, nil), Output: "No tasks.", Metadata: map[string]any{"count": 0, "ids": []string{}}})},
		{name: "a get of an item that does not exist", spanType: contracts.MiMoToolTask,
			event: toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: completed,
				Input: taskOperation(contracts.MiMoTaskActionGet, map[string]any{"id": "T9"}), Output: "No task T9. Use `task list` to see valid task IDs."})},
		{name: "a failed call changed no row", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, contracts.MiMoToolStatusError, taskOperation(contracts.MiMoTaskActionCreate, map[string]any{"summary": "x"}), nil)},
		{name: "a running call has no result", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, contracts.MiMoToolStatusRunning, taskOperation(contracts.MiMoTaskActionCreate, map[string]any{"summary": "x"}), nil)},
		{name: "a status change with no status", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, completed, taskOperation(contracts.MiMoTaskActionDone, map[string]any{"id": "T1"}), map[string]any{"id": "T1"})},
		{name: "a call with no id", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, completed, taskOperation(contracts.MiMoTaskActionCreate, map[string]any{"summary": "x"}), map[string]any{"status": "open"})},
		{name: "another tool", spanType: contracts.MiMoToolBash,
			event: toolPartEvent(t, "prt_b", "msg_1", contracts.MiMoToolBash, "call-b", toolState{Status: completed, Input: map[string]any{"command": "ls"}})},
		{name: "a task span with another tool's row", spanType: contracts.MiMoToolTask,
			event: toolPartEvent(t, "prt_b", "msg_1", contracts.MiMoToolBash, "call-b", toolState{Status: completed, Input: map[string]any{"command": "ls"}})},
		{name: "an input that is not an operation", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, completed, map[string]any{"operation": "create T1"}, map[string]any{"id": "T1", "status": "open"})},
		{name: "an action that this build does not know", spanType: contracts.MiMoToolTask,
			event: taskEvent(t, completed, taskOperation("archive", map[string]any{"id": "T1"}), map[string]any{"id": "T1", "status": "open"})},
		{name: "a result that is not an object", spanType: contracts.MiMoToolTask,
			event: eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
				"id": "prt_t", "messageID": "msg_1", "sessionID": testSessionID, "type": contracts.MiMoPartTypeTool,
				"tool": contracts.MiMoToolTask, "callID": "call-t", "state": map[string]any{
					"status": completed, "input": taskOperation(contracts.MiMoTaskActionDone, map[string]any{"id": "T1"}), "metadata": "T1 done",
				},
			}})},
		{name: "a get of an item that states a blank id", spanType: contracts.MiMoToolTask,
			event: toolPartEvent(t, "prt_t", "msg_1", contracts.MiMoToolTask, "call-t", toolState{Status: completed,
				Input: taskOperation(contracts.MiMoTaskActionGet, map[string]any{"id": "T2"}), Output: `{"id":"  ","status":"done","summary":"Ship it"}`})},
		{name: "a row that is not an event", spanType: contracts.MiMoToolTask, event: []byte(`{"content":"hello"}`)},
		{name: "another event", spanType: contracts.MiMoToolTask, event: statusEvent(t, contracts.MiMoStatusTypeIdle)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := mimoProvider{}.ExtractTodoEvent(tc.spanType, tc.event, nil)
			assert.False(t, ok)
		})
	}
}

func TestMiMoTaskStatus(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]todoevents.Status{
		contracts.MiMoTaskStatusOpen:       todoevents.StatusPending,
		contracts.MiMoTaskStatusInProgress: todoevents.StatusInProgress,
		contracts.MiMoTaskStatusBlocked:    todoevents.StatusPending,
		contracts.MiMoTaskStatusDone:       todoevents.StatusCompleted,
		contracts.MiMoTaskStatusAbandoned:  todoevents.StatusDeleted,
		"":                                 todoevents.StatusUnspecified,
		"archived":                         todoevents.StatusPending,
	} {
		assert.Equal(t, want, mimoTaskStatus(status), "status %q", status)
	}
}
