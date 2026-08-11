package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// TestClaude_PendingTaskEndRecordsAndConsumes verifies the pending-end map
// that closes a Task subagent row whose terminal RESULT arrived before its
// task_started (a forward reorder). recordPendingTaskEnd stores the terminal
// status keyed by the spawn tool_use id; handleClaudeTaskStarted consumes it
// inline under a.mu. This test exercises the store+take mechanics directly so
// the reorder close cannot silently regress to a leaked Running row.
func TestClaude_PendingTaskEndRecordsAndConsumes(t *testing.T) {
	t.Parallel()
	a := &ClaudeCodeAgent{}

	// A terminal result for spawn span "tu-1" arrives before task_started.
	a.recordPendingTaskEnd("tu-1", bgtask.StatusCompleted)
	assert.Len(t, a.pendingTaskEnd, 1)
	assert.Equal(t, bgtask.StatusCompleted, a.pendingTaskEnd["tu-1"])

	// handleClaudeTaskStarted takes the entry inline. Simulate the take.
	a.mu.Lock()
	var got bgtask.Status
	var ok bool
	if s, present := a.pendingTaskEnd["tu-1"]; present {
		got, ok = s, true
		delete(a.pendingTaskEnd, "tu-1")
	}
	a.mu.Unlock()
	assert.True(t, ok, "pending end taken on the late task_started")
	assert.Equal(t, bgtask.StatusCompleted, got)
	assert.Empty(t, a.pendingTaskEnd, "entry consumed so it cannot fire twice")
}

// TestClaude_PendingTaskEndIgnoresEmptySpan verifies a result with no
// parent_tool_use_id (a non-forwarded envelope) does not seed a pending end
// that could never be matched by a task_started.
func TestClaude_PendingTaskEndIgnoresEmptySpan(t *testing.T) {
	t.Parallel()
	a := &ClaudeCodeAgent{}
	a.recordPendingTaskEnd("", bgtask.StatusCompleted)
	assert.Nil(t, a.pendingTaskEnd, "no entry recorded for an empty spawn span")
}

// A subagent tab must open on the instruction the subagent was given, not on
// its first reply. task_started is the only Claude event carrying the spawn
// prompt, and it lands before any forwarded envelope, so the prompt becomes the
// child transcript's first message.
func TestClaude_TaskStartedPersistsThePromptAsTheChildsFirstMessage(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle",
		"prompt": "Review the diff and **report** every finding."
	}`))

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1, "the prompt is the child transcript's only message so far")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source,
		"a USER envelope, so it renders as markdown in a user bubble like a typed message")
	assert.JSONEq(t, `{"content":"Review the diff and **report** every finding."}`, string(msgs[0].Content))
	assert.Empty(t, msgs[0].SpanID, "the prompt belongs to no tool span")

	// The subagent's own output still lands after it.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "On it."}]}
	}`))
	assert.Len(t, child.Messages(), 2)
}

// A background Task takes the same path: the prompt is persisted at spawn, not
// when (or whether) the user later opens the tab.
func TestClaude_TaskStartedPersistsThePromptWithNoDescription(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "First line\nSecond line"
	}`))

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"First line\nSecond line"}`, string(child.Messages()[0].Content))
}

// A task_started with no prompt must leave the transcript empty rather than
// persist a blank bubble.
func TestClaude_TaskStartedWithoutAPromptPersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle"
	}`))

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	assert.Empty(t, child.Messages())
}

// A shell task (local_bash) has no transcript at all, so nothing is written.
func TestClaude_TaskStartedForAShellPersistsNoPrompt(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_bash",
		"prompt": "npm test"
	}`))

	// Assert on the REGISTRY ROW, not on ChildSink's messages. ChildSink creates
	// its recording sink on demand, so `sink.ChildSink(...)` always succeeds and
	// an empty message list would still pass if the code wrongly created a child
	// agent for the shell. The row's ChildAgentID is what actually observes it.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
	assert.Empty(t, tasks[0].ChildAgentID, "a shell task never gets a child transcript")
}

// A task_notification carries an output_file for a Task SUBAGENT too, not only
// for a background shell -- so the upsert that records it must not decide the
// kind on its own. Hardcoding KindShell there rewrote every subagent's row into
// a shell one, which cost the row its clickable transcript: the sidebar showed
// a shell entry for a subagent that had a child tab waiting behind it.
func TestClaude_TaskNotificationWithOutputFileKeepsTheSubagentKind(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "Write two sentences about the ocean",
		"prompt": "Write two sentences about the ocean."
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)
	require.Equal(t, bgtask.KindSubagent, sink.BackgroundTasks()[0].Kind)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"status": "completed",
		"output_file": "/tmp/task-1.log"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind, "the notification must not rewrite the kind")
	assert.Equal(t, "/tmp/task-1.log", tasks[0].Description)
}

// The same upsert on a real shell keeps ITS kind, so a row this call resurrects
// after an eviction still lands in the shell cap pool rather than the subagent
// one it would get from an unspecified kind.
func TestClaude_TaskNotificationWithOutputFileKeepsTheShellKind(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "npm test"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-shell",
		"status": "completed",
		"output_file": "/tmp/shell.log"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
}

// A replayed task_started (a re-attach after a worker restart) must not stack a
// second copy of the prompt on top of the transcript it already introduced.
func TestClaude_ReplayedTaskStartedDoesNotDuplicateThePrompt(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	started := []byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "Do the thing."
	}`)
	a.HandleOutput(started)
	a.HandleOutput(started)

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	assert.Len(t, child.Messages(), 1)
}
