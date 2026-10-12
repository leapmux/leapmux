package letta

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func lettaTaskRequest(name, callID string) func() []byte {
	return func() []byte {
		return []byte(fmt.Sprintf(`{"message_type":"tool_call_message","tool_call":{"name":%q,"tool_call_id":%q,"arguments":"{}"}}`, name, callID))
	}
}

func lettaTaskRequestFromSeveralCalls(name, callID string) func() []byte {
	return func() []byte {
		return []byte(fmt.Sprintf(`{"message_type":"tool_call_message","tool_calls":[{"name":%q,"tool_call_id":%q,"arguments":"{}"}]}`, name, callID))
	}
}

func lettaClientToolStart(name, callID string) func() []byte {
	return func() []byte {
		return []byte(fmt.Sprintf(`{"message_type":"client_tool_start","tool_name":%q,"tool_call_id":%q}`, name, callID))
	}
}

func TestLettaTaskResultsBecomeTodoEvents(t *testing.T) {
	t.Parallel()

	create := `{"message_type":"tool_return_message","tool_call_id":"create-1","status":"success","tool_return":"{\"taskId\":\"task_1\",\"subject\":\"Inspect the repository\",\"description\":\"Read the files.\",\"activeForm\":\"Inspecting the repository\",\"status\":\"pending\"}"}`
	event, ok := lettaProvider{}.ExtractTodoEvent("", []byte(create), lettaClientToolStart("TaskCreate", "create-1"))
	require.True(t, ok)
	assert.Equal(t, todoevents.KindCreate, event.Kind)
	assert.Equal(t, todoevents.Item{
		ID:          "task_1",
		Content:     "Inspect the repository",
		Description: "Read the files.",
		ActiveForm:  "Inspecting the repository",
		Status:      todoevents.StatusPending,
	}, event.Item)
	_, ok = lettaProvider{}.ExtractTodoEvent("", []byte(create), lettaClientToolStart("TaskCreate", "another-call"))
	assert.False(t, ok, "a different native call must not own this result")

	update := `{"message_type":"tool_return_message","tool_call_id":"update-1","status":"success","tool_return":"{\"taskId\":\"task_1\",\"subject\":\"Inspect the repository\",\"description\":\"Read the files.\",\"status\":\"completed\"}"}`
	event, ok = lettaProvider{}.ExtractTodoEvent("", []byte(update), lettaTaskRequestFromSeveralCalls("TaskUpdate", "update-1"))
	require.True(t, ok)
	assert.Equal(t, todoevents.KindCreate, event.Kind)
	assert.Equal(t, "task_1", event.Item.ID)
	assert.Equal(t, todoevents.StatusCompleted, event.Item.Status)
}

func TestLettaTaskListResultReplacesAndClearsTodos(t *testing.T) {
	t.Parallel()

	list := `{"message_type":"tool_return_message","tool_call_id":"list-1","status":"success","tool_return":"{\"tasks\":[{\"taskId\":\"task_1\",\"subject\":\"Inspect the repository\",\"description\":\"Read the files.\",\"status\":\"in_progress\"}]}"}`
	event, ok := lettaProvider{}.ExtractTodoEvent("", []byte(list), lettaTaskRequest("TaskList", "list-1"))
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{{
		ID:          "task_1",
		Content:     "Inspect the repository",
		Description: "Read the files.",
		Status:      todoevents.StatusInProgress,
	}}, event.Snapshot)

	clear := `{"message_type":"tool_return_message","tool_call_id":"list-2","status":"success","tool_return":"{\"tasks\":[]}"}`
	event, ok = lettaProvider{}.ExtractTodoEvent("", []byte(clear), lettaTaskRequest("TaskList", "list-2"))
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestLettaTodoEventRejectsUnrelatedAndFailedFrames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		spanType    string
		content     string
		requestName string
		requestID   string
	}{
		{"unrelated tool", "", `{"message_type":"tool_return_message","tool_call_id":"read-1","status":"success","tool_return":"{\"taskId\":\"task_1\",\"subject\":\"Read\",\"status\":\"pending\"}"}`, "Read", "read-1"},
		{"request frame", "", `{"message_type":"tool_call_message","tool_call":{"name":"TaskCreate"}}`, "TaskCreate", "create-1"},
		{"failed call", "", `{"message_type":"tool_return_message","tool_call_id":"create-1","status":"error","tool_return":"{\"taskId\":\"task_1\",\"subject\":\"Read\",\"status\":\"pending\"}"}`, "TaskCreate", "create-1"},
		{"wrong request ID", "", `{"message_type":"tool_return_message","tool_call_id":"create-1","status":"success","tool_return":"{\"taskId\":\"task_1\",\"subject\":\"Read\",\"status\":\"pending\"}"}`, "TaskCreate", "other"},
		{"missing ID", "", `{"message_type":"tool_return_message","tool_call_id":"create-1","status":"success","tool_return":"{\"subject\":\"Read\",\"status\":\"pending\"}"}`, "TaskCreate", "create-1"},
		{"malformed result", "", `{"message_type":"tool_return_message","tool_call_id":"list-1","status":"success","tool_return":"not JSON"}`, "TaskList", "list-1"},
		{"absent list", "", `{"message_type":"tool_return_message","tool_call_id":"list-1","status":"success","tool_return":"{}"}`, "TaskList", "list-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := lettaProvider{}.ExtractTodoEvent(tc.spanType, []byte(tc.content), lettaTaskRequest(tc.requestName, tc.requestID))
			assert.False(t, ok)
		})
	}
}
