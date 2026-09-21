package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The spawn, then one tool call the subagent made. The subagent-marking fields are
// exactly what the app-server sends: the update identifies the tool call that started it.
const (
	zcodeSpawnScheduled = `{"kind":"scheduled","toolCallId":"spawn-1","toolName":"Agent",
	  "input":{"prompt":"count the files","description":"file census"}}`
	zcodeSubagentScheduled = `{"kind":"scheduled","toolCallId":"sub-1","toolName":"Bash",
	  "source":"subagent","agentId":"agent-1","agentType":"Explore",
	  "childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1",
	  "input":{"command":"ls -la"}}`
	zcodeSubagentResult = `{"kind":"result","toolCallId":"sub-1","source":"subagent",
	  "agentId":"agent-1","parentToolCallId":"spawn-1","result":{"success":true,"content":"one file"}}`
	zcodeSpawnResult = `{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"1 file"}}`
)

// spawnAZCodeSubagent runs the spawn plus one of the subagent's own tool calls, and
// returns the agent and its parent sink.
func spawnAZCodeSubagent(t *testing.T) (*zcodeAgent, *recordingControlSink) {
	t.Helper()
	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))
	return a, sink
}

func TestZCodeSubagent_ToolCallsLandInTheChildTranscript(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)

	// The parent holds the spawn card only.
	parentMessages := sink.Messages()
	require.Len(t, parentMessages, 1)
	assert.Equal(t, "spawn-1", parentMessages[0].SpanID)
	assert.Equal(t, contracts.ZCodeToolNameAgent, parentMessages[0].SpanType)
	assert.True(t, parentMessages[0].NoSpan, "a spawn owns no rail: its work is in the child transcript")

	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	child, ok := sink.ChildSink(childIDs[0]).(*testSink)
	require.True(t, ok)

	childMessages := child.Messages()
	require.Len(t, childMessages, 2)
	// The spawn prompt opens the transcript, as the user's instruction to the child.
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.Contains(t, string(childMessages[0].Content), "count the files")
	assert.Equal(t, "sub-1", childMessages[1].SpanID)
	assert.Equal(t, contracts.ZCodeToolNameBash, childMessages[1].SpanType)

	// The registry row is keyed by the SPAWN's tool call, which is the span the
	// parent's card owns, and it carries the child linkage that makes the row open a tab.
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "spawn-1", rows[0].RowKey)
	assert.Equal(t, bgtask.KindSubagent, rows[0].Kind)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
	assert.Equal(t, "file census", rows[0].Title)
	assert.Equal(t, childIDs[0], rows[0].ChildAgentID)

	_ = a
}

func TestZCodeSubagent_ResultClosesTheChildSpanInTheChildTranscript(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))

	child, ok := sink.ChildSink(sink.ChildAgentIDs()[0]).(*testSink)
	require.True(t, ok)
	assert.Equal(t, []string{"sub-1"}, child.ClosedSpans())
	assert.Empty(t, sink.ClosedSpans(), "the parent never opened that span")
	require.Len(t, child.Messages(), 3)
	assert.True(t, child.Messages()[2].Closing)
}

// A subagent's calls are part of the work the turn did, so they count toward the
// tool-use total the turn-end row reports.
func TestZCodeSubagent_CallsCountTowardTheTurn(t *testing.T) {
	t.Parallel()

	a, _ := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, 1, a.turnToolUses)
}

func TestZCodeSubagent_SpawnResultClosesTheRegistryRowAndTheChild(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "1 file", reports[0]["text"])

	_, stillOpen := a.children.child("spawn-1")
	assert.False(t, stillOpen, "the child index is dropped with the spawn it belonged to")
}

func TestZCodeSubagent_BackgroundLaunchIsNotAChildReport(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent-1"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status,
		"the Agent result acknowledges launch; the background lifecycle closes the row later")
	_, stillOpen := a.children.child("spawn-1")
	assert.True(t, stillOpen)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	assert.Empty(t, child.LeapMuxNotifications())
}

func TestZCodeSubagent_MessageEventRoutesToTheChild(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"running","description":"Inspect the parser","prompt":"Find the parser."}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","summaryText":"Parser report","message":"The parser is in parser.go."}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Find the parser."}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The parser is in parser.go.", reports[0]["text"])
	assert.Empty(t, sink.LeapMuxNotifications(), "the foreground Agent result owns the parent handback")
}

func TestZCodeSubagent_LifecycleAndAgentResultPersistOneReport(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","parentToolCallId":"spawn-1","message":"1 file"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	require.Len(t, child.Messages(), 1, "the child keeps only its prompt as a plain message")
	require.Len(t, child.LeapMuxNotifications(), 1,
		"the Agent result must not copy a lifecycle report into the child a second time")
	assert.Empty(t, sink.LeapMuxNotifications(), "the foreground Agent result owns the parent handback")
}

func TestZCodeSubagent_FinalLifecycleClosesWithoutAnAgentResult(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent-1"}}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","parentToolCallId":"spawn-1","status":"completed","message":"Done."}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
	require.Len(t, sink.LeapMuxNotifications(), 1, "a background run has no later Agent result for the parent")
}

func TestZCodeSubagent_FailedLifecycleNotificationStaysInTheChild(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"running","prompt":"Inspect."}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"failed","error":"model request failed"}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	require.Len(t, child.Messages(), 2)
	var assembled map[string]string
	require.NoError(t, json.Unmarshal(child.Messages()[1].Content, &assembled))
	assert.Equal(t, "model request failed", assembled[contracts.AssembledMessageFieldText])
	assert.Equal(t, string(MessageCompletionError), assembled[contracts.AssembledMessageFieldCompletion])
	assert.Empty(t, sink.Messages(), "the child failure notification must not leak into the parent transcript")
}

// A failed spawn closes its row as failed, so the sidebar does not keep a finished
// subagent Running.
func TestZCodeSubagent_AFailedSpawnClosesTheRowAsFailed(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"error","toolCallId":"spawn-1","error":{"message":"the subagent failed"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusFailed, rows[0].Status)
}

// Without a parent tool call there is nothing to attach a transcript to. The row
// stays in the parent rather than being dropped, because a lost tool call is worse
// than a misplaced one.
func TestZCodeSubagent_AnUnattachableUpdateStaysInTheParent(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"orphan-1","toolName":"Bash","source":"subagent","input":{"command":"ls"}}`))

	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, "orphan-1", sink.Messages()[0].SpanID)
	assert.Empty(t, sink.ChildAgentIDs())
}

// `started` and `progress` carry no row: they stream against the tool-call id, which
// the child transcript's own rows already carry.
func TestZCodeSubagent_ProgressPersistsNoRow(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	before := len(sink.ChildAgentIDs())
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"sub-1","source":"subagent","parentToolCallId":"spawn-1","stdoutTail":"one"}`))

	child, ok := sink.ChildSink(sink.ChildAgentIDs()[0]).(*testSink)
	require.True(t, ok)
	assert.Len(t, child.Messages(), 2, "a progress update adds no row")
	assert.Len(t, sink.ChildAgentIDs(), before)
}

// A second subagent under the same spawn reuses the one transcript, rather than
// opening a tab for every tool call it makes.
func TestZCodeSubagent_SubsequentCallsReuseTheSameChild(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sub-2","toolName":"Read","source":"subagent",
		  "agentId":"agent-1","parentToolCallId":"spawn-1","input":{"file_path":"/tmp/x"}}`))

	require.Len(t, sink.ChildAgentIDs(), 1)
	child, ok := sink.ChildSink(sink.ChildAgentIDs()[0]).(*testSink)
	require.True(t, ok)
	assert.Len(t, child.Messages(), 3)
}

func TestZCodeChildIndex_ZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	var index zcodeChildIndex
	_, ok := index.child("nothing")
	assert.False(t, ok)
	_, ok = index.toolChild("nothing")
	assert.False(t, ok)
	_, ok = index.takeChild("nothing")
	assert.False(t, ok)
	assert.Empty(t, index.title("nothing"))
	index.forgetTool("nothing")
	index.clear()

	index.rememberChild("spawn-1", "tool-1", "child-1")
	got, ok := index.child("spawn-1")
	require.True(t, ok)
	assert.Equal(t, "child-1", got)
	got, ok = index.toolChild("tool-1")
	require.True(t, ok)
	assert.Equal(t, "child-1", got)

	// A finished tool call is dropped; the subagent's transcript stays, because its
	// next call belongs to the same one.
	index.forgetTool("tool-1")
	_, ok = index.toolChild("tool-1")
	assert.False(t, ok)
	_, ok = index.child("spawn-1")
	assert.True(t, ok)

	got, ok = index.takeChild("spawn-1")
	require.True(t, ok)
	assert.Equal(t, "child-1", got)
	_, ok = index.child("spawn-1")
	assert.False(t, ok, "takeChild removes the entry")
}

// A batch summary carries a list of tool-call ids and nothing else, so the closing
// row can only find its transcript through the id. Without that lookup it would
// close a span in the parent that only the child ever opened.
func TestZCodeSubagent_ABatchSummaryClosesInTheChildTranscript(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"batch","toolCallIds":["sub-1"],"successCount":1}`))

	child, ok := sink.ChildSink(sink.ChildAgentIDs()[0]).(*testSink)
	require.True(t, ok)
	assert.Equal(t, []string{"sub-1"}, child.ClosedSpans())
	assert.Empty(t, sink.ClosedSpans(), "the parent never opened that span")
}

// The progress stream belongs to the transcript that holds the row it updates.

// The spawn's own description gives the TASK its title. The subagent's updates describe each
// command it runs, so a row labelled from one of those reads "ls -la" where the
// user expects "file census".
func TestZCodeSpawnTitle(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "file census",
		zcodeSpawnTitle([]byte(`{"description":"file census","prompt":"count them"}`), zcodeToolUpdated{}))
	assert.Equal(t, "first line",
		zcodeSpawnTitle([]byte(`{"description":"first line\nsecond"}`), zcodeToolUpdated{}))
	assert.Equal(t, "Explore",
		zcodeSpawnTitle([]byte(`{"agentType":"Explore"}`), zcodeToolUpdated{}))
	assert.Equal(t, "reviewer",
		zcodeSpawnTitle([]byte(`{"subagent_type":"reviewer"}`), zcodeToolUpdated{}))
	assert.Equal(t, "Agent",
		zcodeSpawnTitle(nil, zcodeToolUpdated{ToolName: contracts.ZCodeToolNameAgent}))
	assert.Equal(t, "Agent",
		zcodeSpawnTitle([]byte(`not json`), zcodeToolUpdated{ToolName: contracts.ZCodeToolNameAgent}))
}

// The row is labelled from the SPAWN even though the subagent's first update carries
// a description of its own.
func TestZCodeSubagent_RowIsTitledFromTheSpawnNotTheFirstCommand(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sub-1","toolName":"Bash","source":"subagent",
		  "parentToolCallId":"spawn-1","description":"List files in target directory",
		  "input":{"command":"ls -la"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "file census", rows[0].Title)
}

// After a worker restart the resumed session replays no history, so `a.toolCalls` is
// empty and a spawn's own `result` carries no `toolName`. The row's CHILD linkage is
// what says this tool call owns a subagent -- a tool-name check would leave the
// registry row Running for good and never clean up the child transcript.
func TestZCodeSubagent_AResumedSpawnResultStillClosesTheRow(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	// The state a resume leaves behind: the durable row exists, the in-memory caches do
	// not.
	childID, err := sink.EnsureChildAgent("spawn-1", "spawn-1", "file census")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "spawn-1", Kind: bgtask.KindSubagent, Title: "file census",
		Status: bgtask.StatusRunning, ChildAgentID: childID,
	}))
	a.children.rememberChild("spawn-1", "", childID)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status,
		"a row left Running pins the thinking indicator for the rest of the session")
}

// A background SHELL task is keyed by the same tool-call id and carries no child, so
// the child linkage still has to tell the two apart: closing a shell task the moment
// its launch tool call returns would mark a still-running command finished.
func TestZCodeSubagent_AShellTasksRowSurvivesItsLaunchToolCall(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "call-1", Kind: bgtask.KindShell, Title: "sleep 60", Status: bgtask.StatusRunning,
	}))

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"call-1","toolName":"Bash","result":{"success":true}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status,
		"the command is still running; only its LAUNCH returned")
}

// A subagent that answers with text alone still gets a transcript with its prompt and
// report. The tab must not depend on the child making a tool call first.
func TestZCodeSubagent_ASpawnWithNoToolCallsPersistsItsPromptAndReport(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	require.Len(t, sink.ChildAgentIDs(), 1)

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))
	assert.Empty(t, a.children.title("spawn-1"), "the label is dropped when the spawn ends")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child, ok := sink.ChildSink(rows[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"count the files"}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "1 file", reports[0]["text"])
}

// One subagent, one transcript and one row, whichever event creates it first. Two
// creators produced two of each, and the background path's copy was invisible to
// zcodeSinkForToolCall, takeChild and the spawn's own teardown.
func TestZCodeSubagent_TheBackgroundTaskAndToolPathsShareOneTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	// The background-task event arrives first, keyed on the SAME spawn tool call.
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent",
		  "childSessionId":"sess_sub","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))

	assert.Len(t, sink.ChildAgentIDs(), 1, "one subagent gets one transcript")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1, "and one registry row")
	assert.Equal(t, "file census", rows[0].Title,
		"the SPAWN's label wins over the background event's bare tool name")
	assert.Equal(t, sink.ChildAgentIDs()[0], rows[0].ChildAgentID)
}

// A background subagent task with no tool-call id keys its row on the child session,
// which the tool.updated path never sees. Creating a transcript there would mint a
// second one that nothing can reach or tear down.
func TestZCodeSubagent_ABackgroundTaskWithNoToolCallIDCreatesNoTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolName":"Agent","taskKind":"subagent",
		  "childSessionId":"sess_sub","status":"running"}`))

	assert.Empty(t, sink.ChildAgentIDs(),
		"the tool.updated path owns the transcript; a second one here is unreachable")
	assert.Len(t, sink.BackgroundTasks(), 1, "the row itself is still worth recording")
}

func TestZCodeSpawnPrompt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "do it", zcodeSpawnPrompt(json.RawMessage(`{"prompt":"  do it  "}`)))
	assert.Equal(t, "fallback", zcodeSpawnPrompt(json.RawMessage(`{"description":"fallback"}`)),
		"the description stands in when no prompt is declared")
	assert.Equal(t, "", zcodeSpawnPrompt(nil))
	assert.Equal(t, "", zcodeSpawnPrompt(json.RawMessage(`not json`)))
	assert.Equal(t, "", zcodeSpawnPrompt(json.RawMessage(`{"prompt":"   "}`)))
}

func TestZCodeSubagentRowKey_PrefersTheIdentifierBothPathsCarry(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "call-1", zcodeSubagentRowKey("call-1", "child-1", "task-1"))
	assert.Equal(t, "child-1", zcodeSubagentRowKey("", "child-1", "task-1"))
	assert.Equal(t, "task-1", zcodeSubagentRowKey("", "", "task-1"))
	assert.Equal(t, "", zcodeSubagentRowKey("", "", ""))
}

func TestZCodeSubagentTitle(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "first line", zcodeSubagentTitle(zcodeToolUpdated{Description: "first line\nsecond"}))
	assert.Equal(t, "reviewer", zcodeSubagentTitle(zcodeToolUpdated{AgentType: "reviewer"}))
	assert.Equal(t, "Agent", zcodeSubagentTitle(zcodeToolUpdated{ToolName: "Agent"}))
}

// A turn that ends while the spawn runs closes the row with the TURN's outcome. An
// earlier build read Completed out of the missing result, which claimed work the
// subagent never finished.
func TestZCodeSubagent_AnInterruptedTurnStopsTheRowRatherThanCompletingIt(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.persistIncompleteZCodeTools(MessageCompletionInterrupted)

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusStopped, rows[0].Status)
}

// A batch summary that counted an error cannot say WHICH call failed, so a spawn it
// recovers stops rather than being reported as either outcome.
func TestZCodeSubagent_ABatchErrorStopsTheRowRatherThanClaimingAnOutcome(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"batch","toolCallIds":["spawn-1"],"successCount":0,"errorCount":1}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusStopped, rows[0].Status)
}
