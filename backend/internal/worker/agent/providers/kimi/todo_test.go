package kimi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeTodoValidatesWholeSnapshot(t *testing.T) {
	existing := []todoevents.Item{{Content: "Existing", Status: todoevents.StatusPending}}
	for _, tc := range []struct {
		name, fields string
		accepted     bool
		items        []todoevents.Item
	}{
		{name: "all native statuses", fields: `"items":[{"title":"Pending","status":"pending"},{"title":"Active","status":"in_progress"},{"title":"Finished","status":"done"}]`, accepted: true, items: []todoevents.Item{{Content: "Pending", Status: todoevents.StatusPending}, {Content: "Active", Status: todoevents.StatusInProgress}, {Content: "Finished", Status: todoevents.StatusCompleted}}},
		{name: "empty snapshot", fields: `"items":[]`, accepted: true},
		{name: "zero text", fields: `"items":[{"title":"0","status":"pending"}]`, accepted: true, items: []todoevents.Item{{Content: "0", Status: todoevents.StatusPending}}},
		{name: "missing items", fields: `"unused":true`},
		{name: "null items", fields: `"items":null`},
		{name: "object items", fields: `"items":{}`},
		{name: "scalar items", fields: `"items":0`},
		{name: "null member", fields: `"items":[null]`},
		{name: "scalar member", fields: `"items":[7]`},
		{name: "missing title", fields: `"items":[{"status":"pending"}]`},
		{name: "empty title", fields: `"items":[{"title":"","status":"pending"}]`},
		{name: "blank title", fields: `"items":[{"title":" \n ","status":"pending"}]`},
		{name: "numeric title", fields: `"items":[{"title":0,"status":"pending"}]`},
		{name: "null title", fields: `"items":[{"title":null,"status":"pending"}]`},
		{name: "missing status", fields: `"items":[{"title":"A"}]`},
		{name: "null status", fields: `"items":[{"title":"A","status":null}]`},
		{name: "empty status", fields: `"items":[{"title":"A","status":""}]`},
		{name: "unknown status", fields: `"items":[{"title":"A","status":"completed"}]`},
		{name: "numeric zero status", fields: `"items":[{"title":"A","status":0}]`},
		{name: "negative status", fields: `"items":[{"title":"A","status":-1}]`},
		{name: "large status", fields: `"items":[{"title":"A","status":9223372036854775807}]`},
		{name: "invalid after valid prefix", fields: `"items":[{"title":"Valid","status":"done"},{"title":"Invalid","status":"unknown"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID(rig.sessionID())
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"todo","session_id":%q,"agent_id":"main","items":[{"title":"Existing","status":"pending"}]}`, rig.sessionID())))
			require.Equal(t, existing, rig.sink.TodoSnapshot())
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"todo","session_id":%q,"agent_id":"main",%s}`, rig.sessionID(), tc.fields)))
			if tc.accepted {
				if len(tc.items) == 0 {
					assert.Empty(t, rig.sink.TodoSnapshot())
				} else {
					assert.Equal(t, tc.items, rig.sink.TodoSnapshot())
				}
			} else {
				assert.Equal(t, existing, rig.sink.TodoSnapshot())
			}
		})
	}
}

func TestKimiNativeTodoRejectsForeignOrMissingDestination(t *testing.T) {
	for _, raw := range []string{
		`{"type":"todo","agent_id":"main","items":[]}`,
		`{"type":"todo","session_id":"foreign","agent_id":"main","items":[]}`,
		`{"type":"todo","session_id":"session_1","items":[]}`,
		`{"type":"todo","session_id":"session_1","agent_id":"","items":[]}`,
		`{"type":"todo","session_id":"session_1","agent_id":"agent-99","items":[]}`,
		`{"type":"todo","session_id":"session_1","payload":{"agent_id":"main","items":[]}}`,
		`not JSON`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID(rig.sessionID())
			rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[{"title":"Existing","status":"pending"}]}`))
			rig.agent.HandleOutput([]byte(raw))
			assert.Equal(t, []todoevents.Item{{Content: "Existing", Status: todoevents.StatusPending}}, rig.sink.TodoSnapshot())
		})
	}
}

func TestKimiNativeTodoCapsOnlyValidatedSnapshots(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID(rig.sessionID())
	var raw []string
	for i := range 1000 {
		raw = append(raw, fmt.Sprintf(`{"title":"Task %d","status":"pending"}`, i))
	}
	frame := func() []byte {
		return []byte(fmt.Sprintf(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[%s]}`, strings.Join(raw, ",")))
	}
	rig.agent.HandleOutput(frame())
	items := rig.sink.TodoSnapshot()
	require.Len(t, items, todoevents.MaxTodos)
	assert.Equal(t, "Task 0", items[0].Content)
	assert.Equal(t, fmt.Sprintf("Task %d", todoevents.MaxTodos-1), items[len(items)-1].Content)
	raw[len(raw)-1] = `{"title":"Invalid last item","status":"unknown"}`
	rig.agent.HandleOutput(frame())
	assert.Equal(t, items, rig.sink.TodoSnapshot(), "an invalid item beyond the retained prefix invalidates the whole snapshot")
}

func TestKimiHistoricalTodoToolNeverMutatesCurrentState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"done", "error", "running"} {
		raw := fmt.Sprintf(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":3,"ops":[{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"todos","toolCallId":"todos","name":"TodoList","state":%q,"input":{"todos":[{"title":"Historical","status":"pending"}]}}}]}}`, state)
		_, present := kimiProvider{}.ExtractTodoEvent(contracts.KimiToolTodoList, []byte(raw), nil)
		assert.False(t, present)
	}
}
