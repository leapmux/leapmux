package agent

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pairedToolUse wraps a message body as the lazy reader ExtractTodoEvent takes.
func pairedToolUse(body string) func() []byte {
	return func() []byte { return []byte(body) }
}

func claudeExtract(t *testing.T, spanType, content string, paired func() []byte) (todoevents.Event, bool) {
	t.Helper()
	return claudeProvider{}.ExtractTodoEvent(spanType, []byte(content), paired)
}

func TestClaudeExtractTodoEvent_TodoWriteSnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TodoWrite", `{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TodoWrite",
			"input": {"todos": [
				{"content": "Run tests", "status": "in_progress", "activeForm": "Running tests"},
				{"content": "Lint", "status": "pending", "activeForm": "Linting"}
			]}
		}]}
	}`, nil)
	require.True(t, ok)
	require.Equal(t, todoevents.KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "Run tests", ev.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusInProgress, ev.Snapshot[0].Status)
	assert.Equal(t, "Running tests", ev.Snapshot[0].ActiveForm)
}

// The USER-side echo of the same span carries the tool_result and no input.
// Reading it would report an empty list and wipe what the use half just wrote.
func TestClaudeExtractTodoEvent_TodoWriteUserSideEnvelopeIgnored(t *testing.T) {
	t.Parallel()

	_, ok := claudeExtract(t, "TodoWrite", `{"type": "user", "message": {"content": [{"type": "tool_result"}]}}`, nil)
	assert.False(t, ok)
}

func TestClaudeExtractTodoEvent_TaskCreate(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskCreate", `{
		"type": "user",
		"message": {"content": [{"type": "tool_result", "tool_use_id": "x", "content": "Task #1 created successfully: Add proto"}]},
		"tool_use_result": {"task": {"id": "1", "subject": "Add proto"}}
	}`, pairedToolUse(`{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TaskCreate",
			"input": {"subject": "Add proto", "description": "Edit proto/agent.proto", "activeForm": "Adding proto"}
		}]}
	}`))
	require.True(t, ok)
	require.Equal(t, todoevents.KindCreate, ev.Kind)
	assert.Equal(t, "1", ev.Item.ID)
	assert.Equal(t, "Add proto", ev.Item.Content)
	assert.Equal(t, "Adding proto", ev.Item.ActiveForm)
	assert.Equal(t, "Edit proto/agent.proto", ev.Item.Description)
	assert.Equal(t, todoevents.StatusPending, ev.Item.Status)
}

// A race in which the tool_use is not visible yet still yields a row, with the
// subject the result states and no description.
func TestClaudeExtractTodoEvent_TaskCreateWithoutPairedToolUse(t *testing.T) {
	t.Parallel()

	const result = `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"task": {"id": "1", "subject": "fallback subject"}}
	}`
	for name, paired := range map[string]func() []byte{
		"no reader":      nil,
		"reader says no": func() []byte { return nil },
		"unreadable":     pairedToolUse(`not json`),
		"another tool":   pairedToolUse(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ev, ok := claudeExtract(t, "TaskCreate", result, paired)
			require.True(t, ok)
			assert.Equal(t, "1", ev.Item.ID)
			assert.Equal(t, "fallback subject", ev.Item.Content)
			assert.Empty(t, ev.Item.Description)
		})
	}
}

// The paired reader costs a database read, so a parser that cannot use its answer
// must never call it.
func TestClaudeExtractTodoEvent_ReadsThePairedUseOnlyWhereItHelps(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spanType string
		content  string
		wantRead bool
	}{
		"TaskCreate needs the description": {"TaskCreate",
			`{"tool_use_result":{"task":{"id":"1","subject":"s"}}}`, true},
		"TaskUpdate needs the text fields": {"TaskUpdate",
			`{"tool_use_result":{"success":true,"taskId":"1"}}`, true},
		"TodoWrite states the whole list itself": {"TodoWrite",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`, false},
		"TaskList states every row itself": {"TaskList",
			`{"tool_use_result":{"tasks":[]}}`, false},
		"TaskGet states the whole row itself": {"TaskGet",
			`{"tool_use_result":{"task":{"id":"3","subject":"T3"}}}`, false},
		"an unrelated tool exits on the switch": {"Bash", `{"type":"assistant"}`, false},
		// A create that carries no task id has no row to enrich.
		"a refused create never reaches the lookup": {"TaskCreate", `{"tool_use_result":{}}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			read := false
			claudeExtract(t, tc.spanType, tc.content, func() []byte {
				read = true
				return nil
			})
			assert.Equal(t, tc.wantRead, read)
		})
	}
}

func TestClaudeExtractTodoEvent_TaskUpdate(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskUpdate", `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": true, "taskId": "1", "updatedFields": ["status"], "statusChange": {"from": "pending", "to": "in_progress"}}
	}`, pairedToolUse(`{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TaskUpdate",
			"input": {"taskId": "1", "status": "in_progress", "activeForm": "Running tests"}
		}]}
	}`))
	require.True(t, ok)
	require.Equal(t, todoevents.KindUpdate, ev.Kind)
	assert.Equal(t, "1", ev.ID)
	require.NotNil(t, ev.Patch.Status)
	assert.Equal(t, todoevents.StatusInProgress, *ev.Patch.Status)
	require.NotNil(t, ev.Patch.ActiveForm)
	assert.Equal(t, "Running tests", *ev.Patch.ActiveForm)
	assert.Nil(t, ev.Patch.Content, "a field the input does not state stays unset")
	assert.Nil(t, ev.Patch.Description)
}

// An input field set to the EMPTY string is a change, not an absence. The patch
// carries pointers so the two cannot be confused.
func TestClaudeExtractTodoEvent_TaskUpdateClearsAField(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskUpdate",
		`{"tool_use_result": {"success": true, "taskId": "1"}}`,
		pairedToolUse(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate",
		  "input":{"taskId":"1","description":""}}]}}`))
	require.True(t, ok)
	require.NotNil(t, ev.Patch.Description)
	assert.Empty(t, *ev.Patch.Description)
	assert.Nil(t, ev.Patch.Status, "no status change was reported")
}

func TestClaudeExtractTodoEvent_TaskUpdateDeleted(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskUpdate", `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": true, "taskId": "5", "updatedFields": ["status"], "statusChange": {"from": "completed", "to": "deleted"}}
	}`, nil)
	require.True(t, ok)
	require.Equal(t, todoevents.KindDelete, ev.Kind)
	assert.Equal(t, "5", ev.ID)
}

func TestClaudeExtractTodoEvent_TaskUpdateFailureNoEvent(t *testing.T) {
	t.Parallel()

	_, ok := claudeExtract(t, "TaskUpdate", `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": false, "taskId": "1", "updatedFields": [], "error": "not found"}
	}`, nil)
	assert.False(t, ok)
}

func TestClaudeExtractTodoEvent_TaskList(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskList", `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"tasks": [
			{"id": "1", "subject": "A", "status": "completed"},
			{"id": "2", "subject": "B", "status": "in_progress"}
		]}
	}`, nil)
	require.True(t, ok)
	require.Equal(t, todoevents.KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "1", ev.Snapshot[0].ID)
	assert.Equal(t, todoevents.StatusCompleted, ev.Snapshot[0].Status)
	assert.Equal(t, "2", ev.Snapshot[1].ID)
	assert.Equal(t, todoevents.StatusInProgress, ev.Snapshot[1].Status)
}

func TestClaudeExtractTodoEvent_TaskListEmpty(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskList",
		`{"type": "user", "message": {"content": []}, "tool_use_result": {"tasks": []}}`, nil)
	require.True(t, ok)
	assert.Empty(t, ev.Snapshot)
}

func TestClaudeExtractTodoEvent_TaskGet(t *testing.T) {
	t.Parallel()

	ev, ok := claudeExtract(t, "TaskGet", `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"task": {"id": "3", "subject": "T3", "description": "details", "status": "pending"}}
	}`, nil)
	require.True(t, ok)
	require.Equal(t, todoevents.KindDetail, ev.Kind)
	assert.Equal(t, "3", ev.Item.ID)
	assert.Equal(t, "T3", ev.Item.Content)
	assert.Equal(t, "details", ev.Item.Description)
}

func TestClaudeExtractTodoEvent_TaskGetNullTask(t *testing.T) {
	t.Parallel()

	_, ok := claudeExtract(t, "TaskGet",
		`{"type": "user", "message": {"content": []}, "tool_use_result": {"task": null}}`, nil)
	assert.False(t, ok)
}

func TestClaudeExtractTodoEvent_IgnoresEverythingElse(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ spanType, content string }{
		"another tool": {"Bash",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`},
		"no span type": {"",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`},
		"empty content": {"TaskCreate", ""},
		"not json":      {"TaskCreate", `not json`},
		"another tool's use block under a todo span": {"TodoWrite",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := claudeExtract(t, tc.spanType, tc.content, nil)
			assert.False(t, ok)
		})
	}
}

// Claude reads only its OWN shapes. Another provider's plan reaching this
// extractor is a routing bug, and answering it would hide one.
func TestClaudeExtractTodoEvent_IgnoresAnotherProvidersPlan(t *testing.T) {
	t.Parallel()

	_, ok := claudeExtract(t, "", `{"method":"turn/plan/updated","params":{"plan":[{"step":"x"}]}}`, nil)
	assert.False(t, ok)
	_, ok = claudeExtract(t, "", `{"sessionUpdate":"plan","entries":[{"content":"x"}]}`, nil)
	assert.False(t, ok)
}
