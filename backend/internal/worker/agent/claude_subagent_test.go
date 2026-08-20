package agent

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// TestClaude_PendingTaskEndRecordsAndConsumes verifies the pending-end map
// that closes a Task subagent row whose FINAL result arrived before its
// task_started (a forward reorder). recordPendingTaskEnd stores the final
// status keyed by the spawn tool_use id; handleClaudeTaskStarted consumes it
// inline under a.mu. This test exercises the store+take mechanics directly so
// the reorder close cannot silently regress to a leaked Running row.
func TestClaude_PendingTaskEndRecordsAndConsumes(t *testing.T) {
	t.Parallel()
	a := &ClaudeCodeAgent{}

	// A final result for spawn span "tu-1" arrives before task_started.
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

// background_tasks_changed is a LEVEL signal with replace semantics: it lists
// the tasks the CLI counts as live BACKGROUND work, and it drops a task whose
// isBackgrounded is false. A foreground shell -- which the CLI registers as a
// local_bash task once its command runs for 2 seconds -- is therefore absent
// from every payload, although its task_started already opened a row here.
//
// The registry keeps that row on purpose, so this event must stay a no-op.
// Applying the payload's replace semantics to the registry would delete a row
// the sidebar is showing, and a payload that a full event queue dropped (the
// CLI evicts a non-bookend event first) would delete a genuine background
// shell's row mid-run.
func TestClaude_BackgroundTasksChangedLeavesTheRegistryAlone(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "task test-e2e"
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)

	// The empty list is the payload a FOREGROUND shell produces: nothing is
	// backgrounded, so the CLI reports no live background task.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "background_tasks_changed",
		"tasks": []
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "the level signal must not evict the row")
	assert.Equal(t, "task-sh", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, "task test-e2e", tasks[0].Title)
	assertTaskEventConsumed(t, sink)
}

// task_updated repeats what the closing task_notification carries, plus the
// foreground -> background flip in patch.is_backgrounded. It is a no-op refresh:
// the notification is the authority on a final status, so a patch must not close
// a row on its own, and the flip changes nothing because the row already exists.
func TestClaude_TaskUpdatedDoesNotChangeTheRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "sleep 30"
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_updated",
		"task_id": "task-sh",
		"patch": {"status": "completed", "end_time": 1760000000000, "is_backgrounded": true}
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status,
		"only task_notification ends a row")
	assert.True(t, tasks[0].EndedAt.IsZero(), "the row is still active")
	assertTaskEventConsumed(t, sink)
}

// A task event for a row that no task_started opened creates nothing. The
// registry is opened by the bookends alone, so a task_updated or a
// background_tasks_changed that identifies an unknown task cannot invent a row.
func TestClaude_TaskUpdatedForAnUnknownTaskCreatesNoRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_updated",
		"task_id": "task-never-started",
		"patch": {"is_backgrounded": true}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "background_tasks_changed",
		"tasks": [{"task_id": "task-never-started", "task_type": "local_bash", "description": "sleep 30"}]
	}`))

	assert.Empty(t, sink.BackgroundTasks())
	assertTaskEventConsumed(t, sink)
}

// assertTaskEventConsumed checks that a task event reached NO persist path. A
// `system` line that claudeHandleTaskEvent declines falls through to one of two
// sinks -- PersistNotification when the classifier calls it consolidatable, and
// PersistMessage otherwise -- so an assertion on either one alone would pass
// vacuously when a regression routed the line to the other.
func assertTaskEventConsumed(t *testing.T, sink *testSink) {
	t.Helper()
	assert.Empty(t, sink.Messages(), "a consumed event never reaches the transcript")
	assert.Empty(t, sink.PersistedNotifications(), "nor the notification thread")
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

// --- A subagent spawn owns no span ---

func TestClaude_ToolSpawnsSubagent(t *testing.T) {
	t.Parallel()

	assert.True(t, claudeToolSpawnsSubagent(ToolNameClaudeAgent))
	assert.True(t, claudeToolSpawnsSubagent(ToolNameClaudeTask),
		"Task is the legacy wire name for the SAME Agent tool")

	// The to-do tools merely share the "Task" prefix; none of them spawns.
	for _, name := range []string{
		"Read", "Bash", "TaskCreate", "TaskUpdate", "TaskGet",
		"TaskList", "TaskOutput", "TaskStop", "AgentTool", "",
	} {
		assert.False(t, claudeToolSpawnsSubagent(name), "%q does not spawn", name)
	}
}

// The spawn's tool_use opens no span and reserves no color. It still carries
// the span id (the frontend pairs it with the tool_result by that id) and the
// span type (the tool_result reads it back through GetSpanType).
func TestClaude_AgentToolUseOpensNoSpan(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{ToolNameClaudeAgent, ToolNameClaudeTask} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a := newTestAgent(sink)
			a.HandleOutput([]byte(`{
				"type": "assistant",
				"message": {"role": "assistant", "content": [
					{"type": "tool_use", "id": "tu-spawn", "name": "` + toolName + `",
					 "input": {"description": "explore", "prompt": "look around"}}
				]}
			}`))

			assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
			assert.Empty(t, sink.ReservedColorSpans(),
				"a spawn reserves no color, so none is blocked while it runs")
			assert.Equal(t, toolName, sink.GetSpanType("tu-spawn"),
				"the span type is still recorded for the tool_result")

			msgs := sink.Messages()
			require.Len(t, msgs, 1)
			assert.Equal(t, "tu-spawn", msgs[0].SpanID, "the row still carries the span id")
			assert.Empty(t, msgs[0].SpansOpenAtPersist, "nothing else was open, so no rail")
		})
	}
}

// The guard is not too wide: an ordinary tool still opens a span and reserves
// a color.
func TestClaude_OrdinaryToolUseStillOpensASpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Equal(t, []string{"tu-read"}, sink.ReservedColorSpans())
}

// One assistant envelope can carry parallel tool calls. The spawn among them
// opens nothing while its siblings still open their spans.
func TestClaude_ParallelBlocksOpenOnlyTheNonSpawns(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}},
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1, "only the Read opens a span")
	assert.Equal(t, "tu-read", open[0].SpanID)
	// spanID/spanColor come from the FIRST tool_use block, which is the spawn,
	// so no color is reserved for this row at all.
	assert.Empty(t, sink.ReservedColorSpans())
}

// The spawn's tool_result closes nothing and draws no rail of its own.
func TestClaude_AgentToolResultDrawsNoRail(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "user",
		"message": {"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "tu-spawn", "content": "the finding"}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.True(t, msgs[1].Closing, "the tool_result is still a closer")
	assert.Equal(t, ToolNameClaudeAgent, msgs[1].SpanType,
		"span_type survives because the spawn never closed a span to forget it")
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "and it draws no rail")
	assert.Empty(t, sink.OpenSpans())
}

// The user's second example: a spawn that starts while a Read is running draws
// exactly the Read's rail -- one column, not two.
func TestClaude_SpawnInsideOpenReadDrawsOneColumn(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "user",
		"message": {"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "tu-spawn", "content": "the finding"}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 3)
	for i, msg := range msgs[1:] {
		require.Len(t, msg.SpansOpenAtPersist, 1, "spawn row %d draws exactly one column", i)
		assert.Equal(t, "tu-read", msg.SpansOpenAtPersist[0].SpanID)
	}
	// The Read span outlives both spawn rows and is still the only one open.
	// The spawn's tool_result does call CloseSpan, but with no span of its own
	// to remove it cannot disturb the Read's column.
	assert.Equal(t, []string{"tu-spawn"}, sink.ClosedSpans())
	open := sink.OpenSpans()
	require.Len(t, open, 1, "the Read is still the only span that ever opened")
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Equal(t, ToolNameClaudeAgent, sink.GetSpanType("tu-spawn"),
		"the close kept the recorded type, so the tool_result persisted the real name")
}

// A Workflow run spawns a fleet of agents and blocks until the last one ends,
// so it is a spawn too. Its tool_use already opened a span -- the CLI keeps the
// Workflow tool behind a feature flag, so its name is not matched -- and
// task_started is the first authoritative word. Discard the span there.
func TestClaude_WorkflowTaskStartedGivesTheSpanBack(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-wf", "name": "Workflow", "input": {"name": "review"}}
		]}
	}`))
	require.Len(t, sink.OpenSpans(), 1, "the tool_use opened one before we knew")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-wf",
		"tool_use_id": "tu-wf",
		"task_type": "local_workflow",
		"workflow_name": "review"
	}`))

	assert.Equal(t, []string{"tu-wf"}, sink.ClosedSpans(),
		"the span is given back although the workflow run keeps going")
	assert.Equal(t, "Workflow", sink.GetSpanType("tu-wf"),
		"and the recorded type survives, so the tool_result reads it back")

	// A tool row persisted after the discard draws no rail.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the workflow rail is gone")
}

// A shell is an ordinary Bash span whose own tool_result closes it. It reports
// task_started too, and it must KEEP its rail. The event is identical for a
// backgrounded shell and for a foreground one that passed the CLI's 2-second
// registration threshold, so this covers both.
func TestClaude_ShellTaskStartedKeepsTheSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-bash", "name": "Bash", "input": {"command": "sleep 100"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-bash",
		"task_type": "local_bash",
		"description": "sleep 100"
	}`))

	assert.Empty(t, sink.ClosedSpans(), "a background shell keeps its span")
	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-bash", open[0].SpanID)
}

// A local_agent Task never opened a span, because claudeToolSpawnsSubagent
// matched its tool_use by name. Its task_started still runs the discard -- the
// predicate is the task type, not the tool name -- and that discard changes
// nothing: no span was open, and no later row draws a rail.
func TestClaude_AgentTaskStartedLeavesNoSpanOpen(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "go"
	}`))

	assert.Empty(t, sink.OpenSpans())

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn left no rail behind")
}

// task_started is the authority, so a task type this code does not name is
// still a spawn and still gives its span back. Matching only the tool name left
// such a run's rail open for the whole child run -- the exact defect the change
// removes for the names it does know.
func TestClaude_UnknownTaskTypeGivesTheSpanBack(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	// A spawning tool whose wire name is in no list, so its tool_use opens a span.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-x", "name": "Delegate", "input": {"prompt": "go"}}
		]}
	}`))
	require.Len(t, sink.OpenSpans(), 1, "the unknown name opened one before we knew")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-x",
		"tool_use_id": "tu-x",
		"task_type": "local_fleet",
		"prompt": "go"
	}`))

	assert.Equal(t, []string{"tu-x"}, sink.ClosedSpans())
	assert.Equal(t, "Delegate", sink.GetSpanType("tu-x"),
		"the recorded type survives, so the tool_result reads it back")

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the unknown spawn's rail is gone")
}

// A workflow event without a tool_use_id has no span to name, so it discards
// nothing rather than discarding the empty id.
func TestClaude_WorkflowTaskStartedWithoutToolUseIDClosesNothing(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-wf",
		"task_type": "local_workflow",
		"workflow_name": "review"
	}`))

	assert.Empty(t, sink.ClosedSpans())
}

// The child transcript reserves its tool_use color under the SPAWN span, not at
// the root: the reservation's parent decides the column it is computed for, so
// a child tool would otherwise be coloured as if it sat in the parent's
// transcript. The shared claudeSpanInfoFor takes that parent from its caller,
// which is the one thing the two transcripts do differently.
func TestClaude_ChildTranscriptReservesUnderTheSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	assert.Equal(t, []testSinkSpanOpen{{SpanID: "tu-read", ParentSpanID: "tu-spawn"}},
		child.ReservedColors(), "the child reserves under the spawn span")
	assert.Empty(t, sink.ReservedColors(), "and nothing is reserved in the parent transcript")
}

// A spawn row reports span_color 0 as its ANSWER, so the persist path must not
// fill it from the connector. Claude resolves a top-level envelope's parent span
// from its own tool_use_id, so a spawn CAN carry a ParentSpanID naming a span
// that is still open -- and the connector-colour fallback would then tint the
// spawn card with a colour no rail anywhere draws.
func TestClaude_SpawnRowUnderAnOpenParentStillTakesTheNeutralBorder(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	// A top-level envelope carrying tool_use_id: the parent span resolves to it.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"tool_use_id": "tu-read",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, "tu-read", msgs[1].ParentSpanID, "the spawn row does sit under the open Read")
	assert.True(t, msgs[1].NoSpan,
		"and it is marked as owning no span, so nothing substitutes a colour for its 0")
	assert.Equal(t, []string{"tu-read"}, sink.ReservedColorSpans(),
		"only the Read reserved a colour; the spawn reserved none")
}

// A subagent can spawn a subagent of its own. That nested spawn owns no span in
// the CHILD transcript either, for the same reason: its output goes to a
// transcript of its own.
func TestClaude_NestedSpawnOpensNoSpanInTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	// A forwarded envelope carries the spawning tool_use id, which routes it to
	// the child transcript.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-nested", "name": "Agent", "input": {"prompt": "go deeper"}},
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	assert.Empty(t, sink.OpenSpans(), "the parent transcript is untouched")

	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	open := child.OpenSpans()
	require.Len(t, open, 1, "only the Read opens a span in the child transcript")
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Empty(t, child.ReservedColorSpans(),
		"the row's color comes from the first block, which is the nested spawn")
	assert.Equal(t, ToolNameClaudeAgent, child.GetSpanType("tu-nested"),
		"the nested spawn's type is still recorded for its tool_result")
}

// --- SendMessage revive ---
//
// Claude restarts a FINISHED subagent when the parent messages it, and says so by
// emitting task_started again for the same task_id. The event alone cannot drive
// the revive: a resumed session re-announces every task it once ran the same way,
// with all of them finished. So the SendMessage tool_use arms a recipient and the
// task_started fires it. These tests pin both halves and each impostor the pair
// excludes.

// spawnAndFinishSubagent replays a full first run -- spawn, one reply, final
// notification -- and returns the child sink the subagent wrote into. Starting
// from the real lifecycle rather than a seeded row is what makes the revive
// assertions meaningful: the transcript already holds its closing divider's
// registry state, and the row already carries the child linkage.
func spawnAndFinishSubagent(t *testing.T, a *ClaudeCodeAgent, sink *testSink) *testSink {
	t.Helper()
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "Explore the parser",
		"prompt": "Find every caller."
	}`))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Found three."}]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"status": "completed",
		"summary": "done"
	}`))
	child, ok := sink.ChildSink("child-of-tu-spawn").(*testSink)
	require.True(t, ok)
	_, status, found, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, found, "the first run left a registry row")
	require.True(t, status.IsFinished(), "the first run finished")
	return child
}

// spanIDs flattens the recorded opens to their span ids.
func spanIDs(opens []testSinkSpanOpen) []string {
	ids := make([]string, 0, len(opens))
	for _, o := range opens {
		ids = append(ids, o.SpanID)
	}
	return ids
}

// sendMessageTo is the parent's SendMessage tool_use, which arms the revive.
func sendMessageTo(a *ClaudeCodeAgent, toolUseID, recipient string) {
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"id": "` + toolUseID + `",
			"name": "SendMessage",
			"input": {"to": "` + recipient + `", "message": "keep going"}
		}]}
	}`))
}

// reviveTaskStarted is the event the CLI emits once the restart happened. Its
// tool_use_id is the SENDMESSAGE call, not the original spawn -- the CLI
// re-registers the task under the tool call that restarted it -- and its prompt
// is the text the subagent actually received.
func reviveTaskStarted(a *ClaudeCodeAgent, toolUseID, prompt string) {
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "` + toolUseID + `",
		"task_type": "local_agent",
		"description": "Explore the parser",
		"prompt": "` + prompt + `"
	}`))
}

func TestClaude_SendMessageRevivesAFinishedSubagent(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"the armed task_started reopens the registry row")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status, "the row is active again")

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the delivered message is appended to the SAME transcript")
	last := msgs[len(msgs)-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, last.Source)
	assert.JSONEq(t, `{"content":"Also check the tests."}`, string(last.Content),
		"the text recorded is what task_started says the subagent received")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType,
		"a mid-transcript message carries a scroll-rail mark; the opening prompt does not")
}

// The revive's task_started identifies the SendMessage call, which still runs in
// the parent transcript. Closing its span would free the rail mid-flight and
// leave its own tool_result drawing a connector_end with nothing above it.
func TestClaude_ReviveDoesNotCloseTheSendMessageSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	require.Contains(t, spanIDs(sink.OpenSpans()), "tu-send", "SendMessage owns an ordinary span")
	reviveTaskStarted(a, "tu-send", "more work")

	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"a re-registration's tool_use_id is not a spawn span")
}

// The narrowed CloseSpan must still fire for a genuine first spawn, which is the
// case it was written for: the spawn owns no rail because its output goes to a
// transcript of its own.
func TestClaude_FirstTaskStartedStillClosesTheSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_workflow",
		"description": "a workflow run"
	}`))

	assert.Contains(t, sink.ClosedSpans(), "tu-spawn")
}

// The resumed-session hydration burst: task_started for a finished row, with no
// SendMessage anywhere. Reviving here would resurrect every subagent the session
// ever ran, each with no close ever arriving.
func TestClaude_TaskStartedWithoutASendMessageDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	reviveTaskStarted(a, "tu-spawn", "(resumed agent)")

	assert.Empty(t, sink.RevivedTasks(), "an unarmed re-registration is not a revive")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status, "the row keeps its final status")
	assert.Len(t, child.Messages(), before, "nothing is appended to the transcript")
}

// An arm lives for one turn. A revive's task_started lands inside the turn that
// sent the message, so an arm still standing at the turn end addressed a live
// subagent, a foreign recipient, or a send the CLI refused.
func TestClaude_SendMessageArmExpiresAtTheTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	a.HandleOutput([]byte(`{"type": "result", "subtype": "success", "result": "ok"}`))
	reviveTaskStarted(a, "tu-send", "too late")

	assert.Empty(t, sink.RevivedTasks(), "the arm did not survive the turn")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status)
}

// `to` may be a display name, another session, or a uds:/bridge:/did: address.
// None of those identifies a row of ours, so the arm never matches anything.
func TestClaude_SendMessageToAnUnknownRecipientIsInert(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "bridge:some-other-machine")
	reviveTaskStarted(a, "tu-send", "not for us")

	assert.Empty(t, sink.RevivedTasks())
	assert.Len(t, child.Messages(), before)
}

// A SendMessage that addresses a task whose row is still RUNNING changes nothing:
// the CLI queues the message for the live subagent and emits no task_started.
func TestClaude_SendMessageToARunningSubagentDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "Find every caller."
	}`))

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "more work")

	assert.Empty(t, sink.RevivedTasks(), "an active row has nothing to revive")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A subagent can message another agent. Its tool_use blocks arrive through the
// child-transcript router, so the arm has to be recorded there too.
func TestClaude_SendMessageFromAChildTranscriptArms(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	// A DIFFERENT subagent sends the message, so the envelope is forwarded.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-other-spawn",
		"message": {"content": [{
			"type": "tool_use",
			"id": "tu-send",
			"name": "SendMessage",
			"input": {"to": "task-1", "message": "keep going"}
		}]}
	}`))
	reviveTaskStarted(a, "tu-send", "from a sibling")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"a subagent's SendMessage arms the same revive the parent's does")
}

// sendMessageFromChild is a subagent's SendMessage, forwarded under its spawn
// span. The arm it sets belongs to THAT transcript's turn, not to the root's.
func sendMessageFromChild(a *ClaudeCodeAgent, spawnSpanID, toolUseID, recipient string) {
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "` + spawnSpanID + `",
		"message": {"content": [{
			"type": "tool_use",
			"id": "` + toolUseID + `",
			"name": "SendMessage",
			"input": {"to": "` + recipient + `", "message": "keep going"}
		}]}
	}`))
}

// A subagent outlives the root turn that spawned it. A backgrounded one that
// messages a finished sibling AFTER the root's turn ended must keep its arm:
// clearing every arm on the root's result dropped it before the task_started
// could fire, which left the recipient finished and its transcript looking dead.
func TestClaude_AChildArmSurvivesTheRootTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageFromChild(a, "tu-other-spawn", "tu-send", "task-1")
	a.HandleOutput([]byte(`{"type": "result", "subtype": "success", "result": "ok"}`))
	reviveTaskStarted(a, "tu-send", "from a sibling")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"the root's turn end drops only the root's own arms")
}

// The child's own forwarded result IS the boundary for the arms it set, so one
// that never fired expires there rather than standing for the agent's life.
func TestClaude_AChildArmExpiresAtTheChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageFromChild(a, "tu-other-spawn", "tu-send", "task-1")
	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-other-spawn",
		"subtype": "success",
		"result": "ok"
	}`))
	reviveTaskStarted(a, "tu-send", "too late")

	assert.Empty(t, sink.RevivedTasks(), "the arm did not survive the sending transcript's turn")
}

// A child's turn end must not take the ROOT's arms with it. The two scopes are
// distinct keys, and a subagent finishing mid-turn is the common case.
func TestClaude_AChildTurnEndKeepsTheRootArms(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-other-spawn",
		"subtype": "success",
		"result": "ok"
	}`))
	reviveTaskStarted(a, "tu-send", "still armed")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"a subagent's turn end leaves the root's arms alone")
}

// Output forwarded AFTER the revive must land in the transcript the subagent
// already owns. The registry row key resolves it, so it does not matter whether
// the CLI tags the forwarded envelope with the original spawn span or with the
// tool_use id it re-registered under.
func TestClaude_RevivedSubagentOutputStaysInOneTranscript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		parentToolUseID string
	}{
		{"original spawn span", "tu-spawn"},
		{"the re-registered tool_use id", "tu-send"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a := newTestAgent(sink)
			child := spawnAndFinishSubagent(t, a, sink)
			before := len(child.Messages())

			sendMessageTo(a, "tu-send", "task-1")
			reviveTaskStarted(a, "tu-send", "Also check the tests.")
			a.HandleOutput([]byte(`{
				"type": "assistant",
				"parent_tool_use_id": "` + tc.parentToolUseID + `",
				"message": {"content": [{"type": "text", "text": "Checked them."}]}
			}`))

			msgs := child.Messages()
			require.Len(t, msgs, before+2, "the revive message and the new reply both land here")
			assert.Contains(t, string(msgs[len(msgs)-1].Content), "Checked them.")
		})
	}
}

// A revive with no prompt still reopens the row. The subagent runs again, so its
// thinking indicator is owed even when there is no text to show.
func TestClaude_ReviveWithoutAPromptStillReopensTheRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "   ")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks())
	assert.Len(t, child.Messages(), before, "a blank prompt persists no bubble")
}

// One SendMessage arms one revive. A second task_started for the same task must
// not reopen a row that closed again in between.
func TestClaude_OneSendMessageArmsOneRevive(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "first")
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-send",
		"status": "completed"
	}`))
	reviveTaskStarted(a, "tu-send", "second")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(), "the arm was consumed by the first")
}

// One assistant message can carry parallel tool calls, so a turn that messages
// two finished subagents at once must arm both.
func TestClaude_SendMessageArmsEveryRecipientInOneMessage(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	for _, spawn := range []string{"tu-spawn-a", "tu-spawn-b"} {
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_started",
			"task_id": "task-` + spawn + `", "tool_use_id": "` + spawn + `",
			"task_type": "local_agent", "prompt": "go"
		}`))
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_notification",
			"task_id": "task-` + spawn + `", "status": "completed"
		}`))
	}

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"content": [
			{"type": "tool_use", "id": "tu-send-a", "name": "SendMessage",
			 "input": {"to": "task-tu-spawn-a", "message": "one"}},
			{"type": "tool_use", "id": "tu-send-b", "name": "SendMessage",
			 "input": {"to": "task-tu-spawn-b", "message": "two"}}
		]}
	}`))
	for _, spawn := range []string{"tu-spawn-a", "tu-spawn-b"} {
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_started",
			"task_id": "task-` + spawn + `", "tool_use_id": "tu-send-` + spawn + `",
			"task_type": "local_agent", "prompt": "more"
		}`))
	}

	assert.ElementsMatch(t, []string{"task-tu-spawn-a", "task-tu-spawn-b"}, sink.RevivedTasks(),
		"a parallel pair of sends arms a revive for each recipient")
}

// A SendMessage whose input this code cannot read must not stop the turn or arm
// anything. `to` is absent for a malformed call and non-string for a structured
// one the schema allows to vary.
func TestClaude_SendMessageWithUnreadableInputArmsNothing(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"message": "no recipient"}`,
		`{"to": {"nested": "object"}, "message": "wrong type"}`,
		`"not an object at all"`,
		`{"to": "", "message": "blank recipient"}`,
	} {
		sink := &testSink{}
		a := newTestAgent(sink)
		spawnAndFinishSubagent(t, a, sink)

		a.HandleOutput([]byte(`{
			"type": "assistant",
			"message": {"content": [{
				"type": "tool_use", "id": "tu-send", "name": "SendMessage",
				"input": ` + input + `
			}]}
		}`))
		reviveTaskStarted(a, "tu-send", "should not land")

		assert.Empty(t, sink.RevivedTasks(), "input %s must arm nothing", input)
	}
}

// An unreadable registry is a THIRD answer, not a miss. Folding it into
// "no such row" made the first registry read of a process -- exactly what a
// worker restart leaves for a revive's task_started -- close the still-running
// SendMessage span and treat a finished subagent as brand new.
func TestClaude_AnUnreadableRegistryDoesNotFreeTheSendMessageSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{lookupErr: errors.New("database is locked")}
	a := newTestAgent(sink)

	sendMessageTo(a, "tu-send", "task-1")
	require.Contains(t, spanIDs(sink.OpenSpans()), "tu-send")
	reviveTaskStarted(a, "tu-send", "more work")

	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"a registry it could not read cannot prove this is a spawn")
	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"nor can it prove the id is a spawn span worth opening a transcript from")
}

// A row that names no transcript is the one state left in which a revive cannot
// resolve its child, and only a failed registry write produces it: cap eviction
// does not, because a linked row survives the display cap in the store. The
// event's tool_use id is the SendMessage call, so handing it to EnsureChildAgent
// would create a child keyed by a non-spawn span -- which the subagent's own
// forwarded envelopes, all stamped with the real spawn span, then duplicate.
func TestClaude_ReviveWithAnUnlinkedRowOpensNoSecondTranscript(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	// The child transcript exists; only the row's linkage to it is missing.
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"a SendMessage id must never open a transcript")
	assert.Len(t, child.Messages(), before, "nothing is written to a transcript the row does not name")

	// Refusing the SendMessage id costs no transcript: the subagent's own output
	// still resolves the real one through the spawn span.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Checked them."}]}
	}`))

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the reply lands in the subagent's own transcript")
	assert.Contains(t, string(msgs[before].Content), "Checked them.")
}

// The revive arm is spent only by a revive that resolved a transcript. A
// task_started that resolved none leaves it standing, so the retry a later
// task_started deserves is still armed -- the same rule a failed registry write
// follows.
func TestClaude_ATaskStartedThatResolvesNoChildKeepsTheArm(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Empty(t, sink.RevivedTasks(), "no transcript resolved, so no revive happened")
	assert.True(t, a.takeClaudeRevive("task-1"), "the arm is still standing for a retry")
}

// The CLI may stamp a revived run's forwarded result with the ORIGINAL spawn
// span. The first completion dropped that span from the tool_use index, and the
// revive re-registered the task under the SendMessage call -- so without a
// child-keyed fallback the result names no row and the row the revive just
// reopened stays Running for the agent's life.
func TestClaude_ARevivedResultUnderTheSpawnSpanClosesTheRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, status, "the revive reopened the row")

	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-spawn",
		"subtype": "success"
	}`))

	_, status, ok, _ = sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status,
		"the revived run's result closes the row it reopened")
}

// --- Wake revive ---
//
// A SendMessage is not the CLI's only restart. Captured against 2.1.233: when a
// subagent's own backgrounded shell finishes, the CLI re-registers that finished
// subagent with a <task-notification> block as the prompt and NO tool_use_id,
// and the subagent runs again. The row must reopen, and the block must NOT
// appear in the transcript -- it is harness plumbing addressed to the model.

// wakePrompt is the block the CLI hands a subagent when its shell completes.
func wakePrompt(shellTaskID string) string {
	return "<task-notification>\n<task-id>" + shellTaskID + "</task-id>\n" +
		"<tool-use-id>tu-bash</tool-use-id>\n<status>completed</status>\n</task-notification>"
}

// finishShellTask replays a backgrounded shell of the subagent, start to finish,
// so this process has seen the id the wake block names.
func finishShellTask(a *ClaudeCodeAgent, shellTaskID string) {
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "` + shellTaskID + `", "tool_use_id": "tu-bash", "task_type": "local_bash"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_notification",
		"task_id": "` + shellTaskID + `", "tool_use_id": "tu-bash", "status": "completed"
	}`))
}

func TestClaude_AShellWakeRevivesTheSubagentWithoutAMessage(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	finishShellTask(a, "shell-1")

	// The wake carries no tool_use_id at all, which is the shape the CLI emits.
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("shell-1")) + `
	}`))

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(), "the woken subagent's row reopens")
	assert.Len(t, child.Messages(), before,
		"a wake block is harness plumbing, not a message the user asked for")
}

// The case the discriminator exists to exclude. A resumed session re-announces
// every task it once ran, replaying prompts from a PREVIOUS process -- so the
// shell a replayed wake names is one this process never finished.
func TestClaude_AWakeNamingAnUnseenShellDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	spawnAndFinishSubagent(t, a, sink)

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("shell-from-a-previous-process")) + `
	}`))

	assert.Empty(t, sink.RevivedTasks(), "a wake this process cannot corroborate is not proof")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status)
}

// The tag must OPEN the first line or CLOSE the last one, so an ordinary spawn
// prompt that merely discusses it is not mistaken for a wake.
func TestClaude_WakeTaskIDRequiresTheTagAtAnEdge(t *testing.T) {
	t.Parallel()

	id, ok := claudeWakeTaskID(wakePrompt("shell-9"))
	assert.True(t, ok)
	assert.Equal(t, "shell-9", id)

	// Closing tag on the last line, with prose above it.
	_, ok = claudeWakeTaskID("Some preamble\n<task-id>shell-9</task-id>\n</task-notification>")
	assert.True(t, ok, "a closing tag on the last line is the other edge")

	for _, prompt := range []string{
		"Explain how <task-notification> blocks work.\nThe <task-id>shell-9</task-id> names the shell.\nThanks.",
		"",
		"<task-notification>",
		"<task-notification>\n<status>completed</status>\n</task-notification>",
	} {
		_, ok := claudeWakeTaskID(prompt)
		assert.False(t, ok, "prompt %q must not read as a wake", prompt)
	}
}

// A revive resolves the transcript from the REGISTRY ROW, never from the event's
// tool_use id. On a re-registration that id is the SendMessage call, so handing
// it to EnsureChildAgent walks past the row-key fast path, fails the
// spawn-span lookup, and creates a SECOND transcript keyed by the wrong span --
// then re-links the row to that orphan.
func TestClaude_ReviveResolvesTheChildFromTheRegistryRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"the SendMessage id must not open a transcript of its own")
	require.Len(t, child.Messages(), before+1, "the message lands in the transcript the ROW points at")
}

// The registry write is the half that can fail after the arm is spent. The
// delivered message must still reach the transcript, and the arm must come back
// -- the fallback path cannot record it, because PersistChildPrompt says nothing
// once a transcript has messages.
func TestClaude_AFailedReviveKeepsTheMessageAndRearms(t *testing.T) {
	t.Parallel()

	sink := &testSink{reviveErr: errors.New("database is locked")}
	a := newTestAgent(sink)
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	reviveTaskStarted(a, "tu-send", "Also check the tests.")

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the delivered message is recorded although the row write failed")
	assert.JSONEq(t, `{"content":"Also check the tests."}`, string(msgs[len(msgs)-1].Content))
	assert.True(t, a.takeClaudeRevive("task-1"), "the arm is back, so a later task_started can retry")
}
