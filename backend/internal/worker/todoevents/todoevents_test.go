package todoevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// --- ApplyPatch / MergeDetail ----------------------------------------

func TestApplyPatch_OverlaysProvidedFields(t *testing.T) {
	base := Item{ID: "1", Content: "Run tests", ActiveForm: "Running tests", Status: StatusPending}
	got := ApplyPatch(base, Patch{Status: ptrconv.Ptr(StatusInProgress)})
	assert.Equal(t, StatusInProgress, got.Status)
	// Untouched fields preserved.
	assert.Equal(t, "Run tests", got.Content)
	assert.Equal(t, "Running tests", got.ActiveForm)
}

func TestApplyPatch_NilFieldsPreserveBase(t *testing.T) {
	base := Item{ID: "1", Content: "x", ActiveForm: "Doing x"}
	got := ApplyPatch(base, Patch{Status: ptrconv.Ptr(StatusCompleted)})
	assert.Equal(t, "Doing x", got.ActiveForm)
	assert.Equal(t, "x", got.Content)
}

func TestMergeDetail_OverlaysNonZeroFields(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusPending}
	got := MergeDetail(base, Item{
		ID: "1", Content: "new", Description: "details", Status: StatusInProgress,
	})
	assert.Equal(t, "new", got.Content)
	assert.Equal(t, "details", got.Description)
	assert.Equal(t, StatusInProgress, got.Status)
}

// Regression: a KindDetail event whose Item carries the zero Status
// (StatusPending) must not silently downgrade the existing row's
// status. TaskGet's response may omit a populated status, which maps
// to StatusPending via StatusFromWire; preserving the existing status
// matches the wire's "field absent" intent.
func TestMergeDetail_PreservesStatusOnZeroValue(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusInProgress}
	got := MergeDetail(base, Item{ID: "1", Content: "new", Status: StatusPending})
	assert.Equal(t, "new", got.Content)
	assert.Equal(t, StatusInProgress, got.Status)
}

// --- Extractor -------------------------------------------------------

func TestExtract_TodoWriteSnapshot(t *testing.T) {
	body := `{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TodoWrite",
			"input": {"todos": [
				{"content": "Run tests", "status": "in_progress", "activeForm": "Running tests"},
				{"content": "Lint", "status": "pending", "activeForm": "Linting"}
			]}
		}]}
	}`
	ev, ok := Extract("TodoWrite", []byte(body), nil)
	require.True(t, ok)
	require.Equal(t, KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "Run tests", ev.Snapshot[0].Content)
	assert.Equal(t, StatusInProgress, ev.Snapshot[0].Status)
	assert.Equal(t, "Running tests", ev.Snapshot[0].ActiveForm)
}

func TestExtract_TodoWriteUserSideEnvelopeIgnored(t *testing.T) {
	// USER-side echoes are not assistant messages; should not produce events.
	body := `{"type": "user", "message": {"content": [{"type": "tool_result"}]}}`
	_, ok := Extract("TodoWrite", []byte(body), nil)
	assert.False(t, ok)
}

func TestExtract_TaskCreate(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": [{"type": "tool_result", "tool_use_id": "x", "content": "Task #1 created successfully: Add proto"}]},
		"tool_use_result": {"task": {"id": "1", "subject": "Add proto"}}
	}`
	use := `{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TaskCreate",
			"input": {"subject": "Add proto", "description": "Edit proto/agent.proto", "activeForm": "Adding proto"}
		}]}
	}`
	ev, ok := Extract("TaskCreate", []byte(result), []byte(use))
	require.True(t, ok)
	require.Equal(t, KindCreate, ev.Kind)
	assert.Equal(t, "1", ev.Item.ID)
	assert.Equal(t, "Add proto", ev.Item.Content)
	assert.Equal(t, "Adding proto", ev.Item.ActiveForm)
	assert.Equal(t, "Edit proto/agent.proto", ev.Item.Description)
	assert.Equal(t, StatusPending, ev.Item.Status)
}

func TestExtract_TaskCreateWithoutPairedToolUse(t *testing.T) {
	// Race: tool_use not yet visible. We still emit a create with
	// just the subject from the result.
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"task": {"id": "1", "subject": "fallback subject"}}
	}`
	ev, ok := Extract("TaskCreate", []byte(result), nil)
	require.True(t, ok)
	assert.Equal(t, "1", ev.Item.ID)
	assert.Equal(t, "fallback subject", ev.Item.Content)
	assert.Empty(t, ev.Item.Description)
}

func TestExtract_TaskCreateOnUseSideNoEvent(t *testing.T) {
	// The tool_use side has no `task.id` yet — return false.
	use := `{
		"type": "assistant",
		"message": {"content": [{"type": "tool_use", "name": "TaskCreate", "input": {"subject": "x"}}]}
	}`
	_, ok := Extract("TaskCreate", []byte(use), nil)
	assert.False(t, ok)
}

func TestExtract_TaskUpdate(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": true, "taskId": "1", "updatedFields": ["status"], "statusChange": {"from": "pending", "to": "in_progress"}}
	}`
	use := `{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"name": "TaskUpdate",
			"input": {"taskId": "1", "status": "in_progress", "activeForm": "Running tests"}
		}]}
	}`
	ev, ok := Extract("TaskUpdate", []byte(result), []byte(use))
	require.True(t, ok)
	require.Equal(t, KindUpdate, ev.Kind)
	assert.Equal(t, "1", ev.ID)
	require.NotNil(t, ev.Patch.Status)
	assert.Equal(t, StatusInProgress, *ev.Patch.Status)
	require.NotNil(t, ev.Patch.ActiveForm)
	assert.Equal(t, "Running tests", *ev.Patch.ActiveForm)
}

func TestExtract_TaskUpdateDeleted(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": true, "taskId": "5", "updatedFields": ["status"], "statusChange": {"from": "completed", "to": "deleted"}}
	}`
	ev, ok := Extract("TaskUpdate", []byte(result), nil)
	require.True(t, ok)
	require.Equal(t, KindDelete, ev.Kind)
	assert.Equal(t, "5", ev.ID)
}

func TestExtract_TaskUpdateFailureNoEvent(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"success": false, "taskId": "1", "updatedFields": [], "error": "not found"}
	}`
	_, ok := Extract("TaskUpdate", []byte(result), nil)
	assert.False(t, ok)
}

func TestExtract_TaskList(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"tasks": [
			{"id": "1", "subject": "A", "status": "completed"},
			{"id": "2", "subject": "B", "status": "in_progress"}
		]}
	}`
	ev, ok := Extract("TaskList", []byte(result), nil)
	require.True(t, ok)
	require.Equal(t, KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "1", ev.Snapshot[0].ID)
	assert.Equal(t, StatusCompleted, ev.Snapshot[0].Status)
	assert.Equal(t, "2", ev.Snapshot[1].ID)
	assert.Equal(t, StatusInProgress, ev.Snapshot[1].Status)
}

func TestExtract_TaskListEmpty(t *testing.T) {
	result := `{"type": "user", "message": {"content": []}, "tool_use_result": {"tasks": []}}`
	ev, ok := Extract("TaskList", []byte(result), nil)
	require.True(t, ok)
	assert.Empty(t, ev.Snapshot)
}

func TestExtract_TaskGet(t *testing.T) {
	result := `{
		"type": "user",
		"message": {"content": []},
		"tool_use_result": {"task": {"id": "3", "subject": "T3", "description": "details", "status": "pending"}}
	}`
	ev, ok := Extract("TaskGet", []byte(result), nil)
	require.True(t, ok)
	require.Equal(t, KindDetail, ev.Kind)
	assert.Equal(t, "3", ev.Item.ID)
	assert.Equal(t, "T3", ev.Item.Content)
	assert.Equal(t, "details", ev.Item.Description)
}

func TestExtract_TaskGetNullTask(t *testing.T) {
	result := `{"type": "user", "message": {"content": []}, "tool_use_result": {"task": null}}`
	_, ok := Extract("TaskGet", []byte(result), nil)
	assert.False(t, ok)
}

func TestExtract_CodexPlan(t *testing.T) {
	body := `{
		"method": "turn/plan/updated",
		"params": {"plan": [
			{"step": "Investigate", "status": "in_progress"},
			{"step": "Write tests", "status": "pending"}
		]}
	}`
	ev, ok := Extract("", []byte(body), nil)
	require.True(t, ok)
	require.Equal(t, KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "Investigate", ev.Snapshot[0].Content)
	assert.Equal(t, StatusInProgress, ev.Snapshot[0].Status)
}

func TestExtract_CodexPlanMalformedDropped(t *testing.T) {
	body := `{
		"method": "turn/plan/updated",
		"params": {"plan": [
			{"step": "", "status": "in_progress"},
			{"step": "real", "status": "pending"}
		]}
	}`
	ev, ok := Extract("", []byte(body), nil)
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, "real", ev.Snapshot[0].Content)
}

func TestExtract_AcpPlan(t *testing.T) {
	body := `{
		"sessionUpdate": "plan",
		"entries": [
			{"content": "one", "status": "pending"},
			{"content": "two", "status": "completed"}
		]
	}`
	ev, ok := Extract("", []byte(body), nil)
	require.True(t, ok)
	require.Equal(t, KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "one", ev.Snapshot[0].Content)
	assert.Equal(t, StatusCompleted, ev.Snapshot[1].Status)
}

func TestExtract_AcpPlanEmpty(t *testing.T) {
	body := `{"sessionUpdate": "plan", "entries": []}`
	ev, ok := Extract("", []byte(body), nil)
	require.True(t, ok)
	assert.Empty(t, ev.Snapshot)
}

func TestExtract_UnknownSpanTypeNoEvent(t *testing.T) {
	body := `{"type": "assistant", "message": {"content": [{"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}]}}`
	_, ok := Extract("Bash", []byte(body), nil)
	assert.False(t, ok)
}

func TestExtract_EmptyContentJSON(t *testing.T) {
	_, ok := Extract("TaskCreate", nil, nil)
	assert.False(t, ok)
}
