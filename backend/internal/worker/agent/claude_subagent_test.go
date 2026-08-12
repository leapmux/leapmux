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
	// The spawn's tool_result does call CloseSpan -- that is how the recorded
	// span type is freed -- but with no span of its own to remove it cannot
	// disturb the Read's column.
	assert.Equal(t, []string{"tu-spawn"}, sink.ClosedSpans())
	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Empty(t, sink.DiscardedSpans())
}

// A Workflow run spawns a fleet of agents and blocks until the last one ends,
// so it is a spawn too. Its tool_use already opened a span -- the CLI keeps the
// Workflow tool behind a feature flag, so its name is not matched -- and
// task_started is the first authoritative word. Take the span back there.
func TestClaude_WorkflowTaskStartedDiscardsTheSpan(t *testing.T) {
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

	assert.Equal(t, []string{"tu-wf"}, sink.DiscardedSpans())
	assert.Empty(t, sink.ClosedSpans(), "discarded, not closed -- the run is still going")
	assert.Equal(t, "Workflow", sink.GetSpanType("tu-wf"),
		"a discard keeps the type the tool_result reads back")

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

// A backgrounded shell is an ordinary Bash span whose tool_result returns at
// once. It reports task_started too, and it must KEEP its rail.
func TestClaude_BackgroundShellTaskStartedKeepsTheSpan(t *testing.T) {
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

	assert.Empty(t, sink.DiscardedSpans(), "a background shell keeps its span")
	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-bash", open[0].SpanID)
}

// A local_agent Task never opened a span, so its task_started has nothing to
// discard.
func TestClaude_AgentTaskStartedDiscardsNothing(t *testing.T) {
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
	assert.Empty(t, sink.DiscardedSpans())
}

// A workflow event without a tool_use_id has no span to name, so it discards
// nothing rather than discarding the empty id.
func TestClaude_WorkflowTaskStartedWithoutToolUseIDDiscardsNothing(t *testing.T) {
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

	assert.Empty(t, sink.DiscardedSpans())
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
