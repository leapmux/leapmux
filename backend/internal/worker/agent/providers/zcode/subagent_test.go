package zcode

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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
func spawnAZCodeSubagent(t *testing.T) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))
	return a, sink
}

func TestZCodeSubagentAutomaticDenialPersistsInTheChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"automatic","toolCallId":"sub-1","toolName":"Bash","decision":"deny","reason":"Plan mode"}`))
	assert.Zero(t, sink.NotificationCount())
	child := sink.Child(sink.ChildAgentIDs()[0])
	require.Len(t, child.PersistedNotifications(), 1)
	assert.Contains(t, string(child.PersistedNotifications()[0].Content), "Plan mode")
}

func TestZCodeSubagentDenialAfterTheCallEndedUsesTheChildSession(t *testing.T) {
	t.Parallel()
	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"automatic","toolCallId":"sub-1","childSessionId":"sess_subagent_agent-1","toolName":"Bash","decision":"deny","reason":"Plan mode"}`))
	assert.Zero(t, sink.NotificationCount())
	child := sink.Child(sink.ChildAgentIDs()[0])
	require.Len(t, child.PersistedNotifications(), 1)
}

func TestZCodeDenialOfAnUnknownChildStaysInTheRoot(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"automatic","toolCallId":"unknown-call","childSessionId":"unknown-session","toolName":"Bash","decision":"deny","reason":"Plan mode"}`))
	assert.Len(t, sink.PersistedNotifications(), 1)
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestZCodeBackgroundChildDenialUsesTheChildSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"spawn-1","taskKind":"subagent","childSessionId":"child-session","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"automatic","childSessionId":"child-session","toolName":"Bash","decision":"deny","reason":"Plan mode"}`))
	assert.Zero(t, sink.NotificationCount())
	require.Len(t, sink.ChildAgentIDs(), 1)
	assert.Len(t, sink.Child(sink.ChildAgentIDs()[0]).PersistedNotifications(), 1)
}

func TestZCodeChildSessionRoutesEndWithTheirSpawn(t *testing.T) {
	t.Parallel()
	var index zcodeChildIndex
	index.rememberChild("first", "call-1", "child-1")
	index.rememberSession("first", "session-1")
	index.rememberChild("second", "call-2", "child-2")
	index.rememberSession("second", "session-2")
	index.rememberSession("second", "session-1")
	index.takeChild("first")
	child, ok := index.sessionChild("session-1")
	assert.True(t, ok)
	assert.Equal(t, "child-2", child, "the new owner keeps a reused session route")
	index.takeChild("second")
	_, ok = index.sessionChild("session-1")
	assert.False(t, ok)
	assert.Empty(t, index.sessionSpawns)
	index.rememberChild("third", "call-3", "child-3")
	index.rememberSession("third", "session-3")
	index.clear()
	_, ok = index.sessionChild("session-3")
	assert.False(t, ok)
	assert.Empty(t, index.sessionSpawns)
	index.rememberSession("", "session-4")
	index.rememberSession("fourth", "")
	assert.Empty(t, index.sessionSpawns)
}

func TestZCodeSubagent_ToolCallsLandInTheChildTranscript(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)

	// The parent holds the spawn card only.
	parentMessages := sink.Messages()
	require.Len(t, parentMessages, 1)
	assert.Equal(t, zcodeTestSpanID(t, sink, "spawn-1"), parentMessages[0].SpanID)
	assert.Equal(t, contracts.ZCodeToolNameAgent, parentMessages[0].SpanType)
	assert.True(t, parentMessages[0].NoSpan, "a spawn owns no rail: its work is in the child transcript")

	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	child := sink.Child(childIDs[0])

	childMessages := child.Messages()
	require.Len(t, childMessages, 2)
	// The spawn prompt opens the transcript, as the user's instruction to the child.
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.Contains(t, string(childMessages[0].Content), "count the files")
	assert.Equal(t, zcodeTestSpanID(t, sink, "sub-1"), childMessages[1].SpanID)
	assert.Equal(t, contracts.ZCodeToolNameBash, childMessages[1].SpanType)

	// The registry row uses the native child session key and retains the child
	// linkage that opens its transcript.
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "sess_subagent_agent-1", rows[0].RowKey)
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

	child := sink.Child(sink.ChildAgentIDs()[0])
	assert.Equal(t, []string{zcodeTestSpanID(t, sink, "sub-1")}, child.ClosedSpans())
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

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, 1, a.TurnToolUses)
}

func TestZCodeSubagent_SpawnResultClosesTheRegistryRowAndTheChild(t *testing.T) {
	t.Parallel()

	a, sink := spawnAZCodeSubagent(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	child := sink.Child(rows[0].ChildAgentID)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "1 file", reports[0]["text"])

	_, stillOpen := a.children.child(zcodeTestSpanID(t, sink, "spawn-1"))
	assert.False(t, stillOpen, "the child index is dropped with the spawn it belonged to")
}

func TestZCodeSubagent_BackgroundLaunchIsNotAChildReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent-1"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status,
		"the Agent result acknowledges launch; the background lifecycle closes the row later")
	_, stillOpen := a.children.child(zcodeTestSpanID(t, sink, "spawn-1"))
	assert.True(t, stillOpen)
	child := sink.Child(rows[0].ChildAgentID)
	assert.Empty(t, child.LeapMuxNotifications())
}

func TestZCodeSubagent_MessageEventRoutesToTheChild(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"running","description":"Inspect the parser","prompt":"Find the parser."}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","summaryText":"Parser report","message":"The parser is in parser.go."}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Find the parser."}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The parser is in parser.go.", reports[0]["text"])
	assert.Empty(t, sink.LeapMuxNotifications(), "the foreground Agent result owns the parent handback")
}

func TestZCodeSubagent_ReplayedLifecycleEventKeepsOneReportIdentity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.handleZCodeSubagentLifecycle(zcodeEventEnvelope{
		EventID: "report-event-1",
		Payload: json.RawMessage(`{"agentId":"agent-1","parentToolCallId":"spawn-1","prompt":"Inspect.","message":"Draft report"}`),
	})
	a.handleZCodeSubagentLifecycle(zcodeEventEnvelope{
		EventID: "report-event-1",
		Payload: json.RawMessage(`{"agentId":"agent-1","parentToolCallId":"spawn-1","message":"Corrected report"}`),
	})

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	assert.Len(t, child.LeapMuxNotifications(), 1, "a replayed event must not create a second report row")
}

func TestZCodeSubagent_DistinctLifecycleEventsKeepIdenticalReports(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	for _, eventID := range []string{"report-event-1", "report-event-2"} {
		a.handleZCodeSubagentLifecycle(zcodeEventEnvelope{
			EventID: eventID,
			Payload: json.RawMessage(`{"agentId":"agent-1","parentToolCallId":"spawn-1","prompt":"Inspect.","message":"Same report"}`),
		})
	}

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	assert.Len(t, child.LeapMuxNotifications(), 2, "distinct provider events must not collapse because their text matches")
}

func TestDecodeZCodeSubagentLifecycleTransition(t *testing.T) {
	t.Parallel()

	event := zcodeEventEnvelope{Payload: json.RawMessage(`{
      "parentToolCallId":"spawn-1",
      "description":"  Inspect the parser  ",
      "agentType":"Explore",
      "prompt":"Inspect.",
      "status":"completed",
      "summaryText":"Summary report",
      "text":"Text report",
      "message":"  Final report  ",
      "error":"  model request failed  "
    }`)}

	transition, ok := decodeZCodeSubagentLifecycleTransition(event)
	require.True(t, ok)
	assert.Equal(t, "spawn-1", transition.rowKey)
	assert.Equal(t, "Inspect the parser", transition.title)
	assert.Equal(t, "Inspect.", transition.prompt)
	assert.Equal(t, bgtask.StatusSucceeded, transition.status)
	assert.True(t, transition.final)
	assert.Equal(t, "Final report", transition.report.Text)
	assert.Equal(t, zcodeFinalReportID("spawn-1"), transition.reportID)
	assert.Equal(t, "model request failed", transition.failure)
}

func TestZCodeSubagent_LifecycleAndAgentResultPersistOneReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1","status":"completed","message":"1 file"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1, "the child keeps only its prompt as a plain message")
	require.Len(t, child.LeapMuxNotifications(), 1,
		"the Agent result must not copy a lifecycle report into the child a second time")
	assert.Empty(t, sink.LeapMuxNotifications(), "the foreground Agent result owns the parent handback")
}

func TestZCodeSubagent_FinalLifecycleClosesWithoutAnAgentResult(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent-1"}}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","parentToolCallId":"spawn-1","status":"completed","message":"Done."}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	require.Len(t, sink.LeapMuxNotifications(), 1, "a background run has no later Agent result for the parent")
}

func TestZCodeSubagent_FailedLifecycleNotificationStaysInTheChild(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"running","prompt":"Inspect."}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"child-session","parentToolCallId":"spawn-1","status":"failed","error":"model request failed"}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 2)
	var assembled map[string]string
	require.NoError(t, json.Unmarshal(child.Messages()[1].Content, &assembled))
	assert.Equal(t, "model request failed", assembled[contracts.AssembledMessageFieldText])
	assert.Equal(t, string(agent.MessageCompletionError), assembled[contracts.AssembledMessageFieldCompletion])
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

// Without a parent invocation or native child identity, the update cannot
// identify a transcript. Its row stays in the parent.
func TestZCodeSubagent_AnUnattachableUpdateStaysInTheParent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"orphan-1","toolName":"Bash","source":"subagent","input":{"command":"ls"}}`))

	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, zcodeTestSpanID(t, sink, "orphan-1"), sink.Messages()[0].SpanID)
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

	child := sink.Child(sink.ChildAgentIDs()[0])
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
	child := sink.Child(sink.ChildAgentIDs()[0])
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

	child := sink.Child(sink.ChildAgentIDs()[0])
	assert.Equal(t, []string{zcodeTestSpanID(t, sink, "sub-1")}, child.ClosedSpans())
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sub-1","toolName":"Bash","source":"subagent",
		  "parentToolCallId":"spawn-1","description":"List files in target directory",
		  "input":{"command":"ls -la"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "file census", rows[0].Title)
}

// After a worker restart the resumed session replays no history, so `a.toolInvocations` is
// empty and a spawn's own `result` carries no `toolName`. The row's CHILD linkage is
// what says this tool call owns a subagent -- a tool-name check would leave the
// registry row Running for good and never clean up the child transcript.
func TestZCodeSubagent_AResumedSpawnResultStillClosesTheRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	// The state a resume leaves behind: the durable row exists, the in-memory caches do
	// not.
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "stored-spawn-span", ProviderChildKey: "native-child", Title: "file census"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "native-child", Kind: bgtask.KindSubagent, Title: "file census",
		Status: bgtask.StatusRunning, ChildAgentID: childID,
	}))
	a.HandleOutput(zcodeEventLine(t, 0, contracts.ZCodeEventSessionUpdated, `{"agentId":"native-agent","parentToolCallId":"spawn-1","childSessionId":"native-child","status":"running"}`))

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status,
		"a row left Running pins the thinking indicator for the rest of the session")
}

// A background SHELL task is keyed by the same tool-call id and carries no child, so
// the child linkage still has to tell the two apart: closing a shell task the moment
// its launch tool call returns would mark a still-running command finished.
func TestZCodeSubagent_AShellTasksRowSurvivesItsLaunchToolCall(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	require.Len(t, sink.ChildAgentIDs(), 1)

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))
	assert.Empty(t, a.children.title("spawn-1"), "the label is dropped when the spawn ends")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)

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

// A background task without a tool-call ID records its registry row but does not
// create a transcript.
func TestZCodeSubagent_ABackgroundTaskWithNoToolCallIDCreatesNoTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

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
	a.persistIncompleteZCodeTools(agent.MessageCompletionInterrupted)

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

// --- subagent interrupt ---

// zcodeBackgroundSubagentTask is the background-task update of the spawn's
// subagent, as ZCode 3.16.3 states it: the task id is the subagent's agent id.
func zcodeBackgroundSubagentTask(status string) string {
	return `{"taskId":"agent_1","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"` + status + `"}`
}

// newZCodeBackgroundChild starts the spawn's subagent in the background.
func newZCodeBackgroundChild(t *testing.T) (*Agent, *agenttest.ControlSink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("running")))
	return a, sink, stdin
}

func TestZCodeChildCapabilitiesAcceptAnInterrupt(t *testing.T) {
	t.Parallel()
	capabilities := zcodeProvider{}.ChildCapabilities(nil)
	assert.True(t, capabilities.AcceptsInterrupt)
	assert.False(t, capabilities.AcceptsMessages)
}

// The Interrupt of a background subagent's tab cancels its task alone, and the
// cancellation closes the row as interrupted.
func TestZCodeInterruptChildCancelsTheBackgroundTask(t *testing.T) {
	t.Parallel()
	for name, closing := range map[string]func(a *Agent){
		"by the task": func(a *Agent) {
			a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("cancelled")))
		},
		"by the subagent": func(a *Agent) {
			a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
				`{"agentId":"agent_1","agentType":"general-purpose","parentToolCallId":"spawn-1","status":"cancelled"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeBackgroundChild(t)
			go func() {
				req := waitZCodeRequest(t, stdin, MethodSessionCancelBackgroundTask)
				// ZCode reports the cancellation before it answers the request.
				closing(a)
				a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(
					`{"cancelled":true,"snapshot":{"taskId":"agent_1","toolCallId":"spawn-1","taskKind":"subagent","status":"cancelled"}}`)))
			}()
			require.NoError(t, a.InterruptChild(zcodeTestSubagentKey(t, a, "spawn-1"), agent.StopContext{}))
			req := waitZCodeRequest(t, stdin, MethodSessionCancelBackgroundTask)
			assert.JSONEq(t, `{"sessionId":"sess-1","taskId":"agent_1"}`, string(req.Params))
			row, ok := sink.BackgroundTask("sess_subagent_agent_1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusInterrupted, row.Status)
		})
	}
}

// A task that the model stopped is stopped, not interrupted.
func TestZCodeCancelledSubagentWithoutAnInterruptIsStopped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newZCodeBackgroundChild(t)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("cancelled")))
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

// A cancel that finds the task ended stops nothing and succeeds. Any other
// refusal fails, and a later cancellation is no interrupt of LeapMux's.
func TestZCodeInterruptChildRefusals(t *testing.T) {
	t.Parallel()
	t.Run("an ended task", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newZCodeBackgroundChild(t)
		answerZCodeRequest(t, a, stdin, MethodSessionCancelBackgroundTask, `{"cancelled":false,"reason":"background_task_not_running","status":"completed","taskId":"agent_1"}`)
		require.NoError(t, a.InterruptChild(zcodeTestSubagentKey(t, a, "spawn-1"), agent.StopContext{}))
	})
	for name, answer := range map[string]func(a *Agent, stdin *zcodeRecordedStdin){
		"a refused cancel": func(a *Agent, stdin *zcodeRecordedStdin) {
			answerZCodeRequest(t, a, stdin, MethodSessionCancelBackgroundTask, `{"cancelled":false,"reason":"background_task_not_cancellable","status":"running"}`)
		},
		"an app-server error": func(a *Agent, stdin *zcodeRecordedStdin) {
			refuseZCodeRequest(t, a, stdin, MethodSessionCancelBackgroundTask, ErrRuntimeUnavailable, "Background task cancellation is not supported by this session runtime")
		},
		"an unreadable answer": func(a *Agent, stdin *zcodeRecordedStdin) {
			answerZCodeRequest(t, a, stdin, MethodSessionCancelBackgroundTask, `"cancelled"`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeBackgroundChild(t)
			answer(a, stdin)
			require.Error(t, a.InterruptChild(zcodeTestSubagentKey(t, a, "spawn-1"), agent.StopContext{}))
			a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("cancelled")))
			row, ok := sink.BackgroundTask("sess_subagent_agent_1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusStopped, row.Status, "the refused interrupt leaves no mark")
		})
	}
}

// A foreground subagent runs inside its parent's turn, so its tab has no stop of
// its own; an unknown row identifies no subagent.
func TestZCodeInterruptChildWithoutABackgroundTask(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))
	require.ErrorIs(t, a.InterruptChild(zcodeTestSubagentKey(t, a, "spawn-1"), agent.StopContext{}), agent.ErrChildOperationUnsupported)
	err := a.InterruptChild("spawn-unknown", agent.StopContext{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrChildOperationUnsupported)
	assert.Empty(t, stdin.Requests(t), "no cancel reaches ZCode")
}

// --- the end of a background subagent ---

// zcodeChildLifecycleCalls records the transcript creations and cleanups that the
// agent asks the sink for, in order.
type zcodeChildLifecycleCalls struct {
	agent.ProviderServices
	mu    sync.Mutex
	calls []string
}

func (c *zcodeChildLifecycleCalls) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	c.mu.Lock()
	c.calls = append(c.calls, "ensure")
	c.mu.Unlock()
	return c.ProviderServices.EnsureChildAgent(spec)
}

func (c *zcodeChildLifecycleCalls) CleanupChildAgent(childAgentID string) {
	c.mu.Lock()
	c.calls = append(c.calls, "cleanup")
	c.mu.Unlock()
	c.ProviderServices.CleanupChildAgent(childAgentID)
}

func (c *zcodeChildLifecycleCalls) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// zcodeBackgroundLifecycleStart is the lifecycle event that ZCode 3.16.3 sends when
// the spawn starts a background subagent.
const zcodeBackgroundLifecycleStart = `{"agentId":"agent_1","agentType":"general-purpose","background":true,"childSessionId":"sess_subagent_agent_1","description":"file census","prompt":"Count the files.","parentToolCallId":"spawn-1"}`

// newZCodeLifecycleChild starts a background subagent that reports lifecycle
// events, in the order ZCode 3.16.3 sends them.
func newZCodeLifecycleChild(t *testing.T) (*Agent, *agenttest.ControlSink, *zcodeChildLifecycleCalls, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	calls := &zcodeChildLifecycleCalls{ProviderServices: agent.NewProviderServices(sink)}
	a := newZCodeTestAgentWithStdin(t, calls, stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleStart))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent_1"}}`))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("running")))
	return a, sink, calls, stdin
}

// requireOneEndWithoutRebirth requires that the transcript ended once, and that
// nothing created it again after its end.
func requireOneEndWithoutRebirth(t *testing.T, calls []string) {
	t.Helper()
	require.Contains(t, calls, "cleanup")
	end := slices.Index(calls, "cleanup")
	assert.Equal(t, []string{"cleanup"}, calls[end:], "the transcript ends once, and nothing creates it again: %v", calls)
}

// zcodeBackgroundLifecycleEnd is the lifecycle event that ends the subagent.
const zcodeBackgroundLifecycleEnd = `{"agentId":"agent_1","agentType":"general-purpose","background":true,"parentToolCallId":"spawn-1","status":"completed","message":"There are 42 files."}`

// ZCode sends the task's final status before the subagent's final lifecycle
// event, which carries the report. The report lands in the transcript, and the
// transcript ends once, after it.
func TestZCodeBackgroundSubagentEndsAfterItsReport(t *testing.T) {
	t.Parallel()
	a, sink, calls, _ := newZCodeLifecycleChild(t)
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	assert.NotContains(t, calls.snapshot(), "cleanup", "the task's final status leaves the transcript to the lifecycle event")
	a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))

	requireOneEndWithoutRebirth(t, calls.snapshot())
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Len(t, sink.ChildAgentIDs(), 1, "one subagent has one transcript")
	require.Len(t, sink.LeapMuxNotifications(), 1, "the parent reads the report of its background run")
}

// The end in the other order ends the transcript once too.
func TestZCodeBackgroundSubagentEndsOnceWhenTheLifecycleComesFirst(t *testing.T) {
	t.Parallel()
	a, sink, calls, _ := newZCodeLifecycleChild(t)
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))
	a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	requireOneEndWithoutRebirth(t, calls.snapshot())
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

// A subagent that sends no lifecycle event (an earlier build) ends with its task,
// and a late update of the ended subagent creates no transcript again.
func TestZCodeBackgroundSubagentWithoutLifecycleEndsWithItsTask(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	calls := &zcodeChildLifecycleCalls{ProviderServices: agent.NewProviderServices(sink)}
	a := newZCodeTestAgentWithStdin(t, calls, &zcodeRecordedStdin{})
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("running")))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	requireOneEndWithoutRebirth(t, calls.snapshot())
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))
	requireOneEndWithoutRebirth(t, calls.snapshot())
}

// An interrupt that LeapMux sent ends the subagent as interrupted, and its stop
// message reads as an interruption, not an error.
func TestZCodeInterruptedSubagentEndsAsInterrupted(t *testing.T) {
	t.Parallel()
	a, sink, calls, stdin := newZCodeLifecycleChild(t)
	go func() {
		req := waitZCodeRequest(t, stdin, MethodSessionCancelBackgroundTask)
		a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("cancelled")))
		a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated,
			`{"agentId":"agent_1","agentType":"general-purpose","background":true,"parentToolCallId":"spawn-1","status":"stopped","error":"Background agent task stopped."}`))
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(`{"cancelled":true}`)))
	}()
	require.NoError(t, a.InterruptChild(zcodeTestSubagentKey(t, a, "spawn-1"), agent.StopContext{}))

	requireOneEndWithoutRebirth(t, calls.snapshot())
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusInterrupted, row.Status)
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	messages := sink.Child(childIDs[0]).Messages()
	require.NotEmpty(t, messages)
	var last map[string]string
	require.NoError(t, json.Unmarshal(messages[len(messages)-1].Content, &last))
	assert.Equal(t, "Background agent task stopped.", last[contracts.AssembledMessageFieldText])
	assert.Equal(t, string(agent.MessageCompletionInterrupted), last[contracts.AssembledMessageFieldCompletion])
}

func TestZCodeSubagents_RepeatedNativeSpawnIDsKeepSeparateChildren(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	sequence := int64(0)
	for index, turnID := range []string{"foreground", "background"} {
		source := ""
		if index > 0 {
			source = "background_task"
		}
		sequence++
		a.HandleOutput(zcodeTurnEventLine(t, sequence, turnID, contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"turnNumber":%d,"inputSource":%q}`, index+1, source)))
		sequence++
		a.HandleOutput(zcodeTurnEventLine(t, sequence, turnID, contracts.ZCodeEventToolUpdated, fmt.Sprintf(`{"kind":"scheduled","toolCallId":"spawn","assistantMessageId":"message-%d","toolName":"Agent","input":{"prompt":"Inspect."}}`, index)))
		sequence++
		a.HandleOutput(zcodeTurnEventLine(t, sequence, turnID, contracts.ZCodeEventSessionUpdated, fmt.Sprintf(`{"agentId":"agent-%d","parentToolCallId":"spawn","childSessionId":"child-%d","status":"running"}`, index, index)))
	}
	require.Len(t, sink.ChildAgentIDs(), 2)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 2)
	rootRows := sink.Messages()
	require.Len(t, rootRows, 2)
	assert.NotEqual(t, rootRows[0].SpanID, rootRows[1].SpanID)
	for _, row := range rows {
		assert.Contains(t, []string{"child-0", "child-1"}, row.RowKey)
		span, err := sink.ChildSpawnSpan(row.ChildAgentID)
		require.NoError(t, err)
		assert.Contains(t, []string{rootRows[0].SpanID, rootRows[1].SpanID}, span)
	}
}

func TestZCodeSubagents_RestartRecoversTheOriginalSpawnSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","toolCallId":"spawn","assistantMessageId":"native-message","toolName":"Agent","input":{"prompt":"Inspect."}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventSessionUpdated, `{"agentId":"child-agent","parentToolCallId":"spawn","childSessionId":"child-session","status":"running"}`))
	originalSpan := sink.Messages()[0].SpanID
	originalChild := sink.ChildAgentIDs()[0]
	replacement := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	replacement.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	replacement.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventSessionUpdated, `{"agentId":"child-agent","parentToolCallId":"spawn","childSessionId":"child-session","status":"running"}`))
	replacement.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"spawn","result":{"content":"Done."}}`))
	assert.Equal(t, []string{originalChild}, sink.ChildAgentIDs())
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, originalSpan, rows[1].SpanID, "a restarted owner must close the stored opening span")
}

func TestZCodeSubagents_BackgroundToolsOutliveTheirParentTurn(t *testing.T) {
	t.Parallel()
	for _, nativeResult := range []bool{true, false} {
		t.Run(fmt.Sprintf("native result %t", nativeResult), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "parent", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
			a.HandleOutput(zcodeTurnEventLine(t, 2, "parent", contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
			a.HandleOutput(zcodeTurnEventLine(t, 3, "parent", contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled))
			child := sink.Child(sink.ChildAgentIDs()[0])
			childSpan := zcodeTestSpanID(t, sink, "sub-1")
			a.HandleOutput(zcodeTurnEventLine(t, 4, "parent", contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully.\nagentId: agent-1"}}`))
			a.HandleOutput(zcodeTurnEventLine(t, 5, "parent", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
			assert.Empty(t, child.ClosedSpans(), "the parent end must leave its background child's tool open")
			assert.Equal(t, "Bash", child.GetSpanType(childSpan))
			a.HandleOutput(zcodeTurnEventLine(t, 6, "parent", contracts.ZCodeEventToolUpdated, `{"kind":"progress","toolCallId":"sub-1","outputBytes":17,"stdoutTail":"still running"}`))
			updates := child.ProgressUpdates()
			require.NotEmpty(t, updates)
			assert.Contains(t, updates, agent.OutputExactTotalProgress(childSpan, 17))
			sequence := int64(7)
			if nativeResult {
				a.HandleOutput(zcodeTurnEventLine(t, sequence, "parent", contracts.ZCodeEventToolUpdated, zcodeSubagentResult))
				sequence++
			}
			a.HandleOutput(zcodeTurnEventLine(t, sequence, "parent", contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","toolCallId":"tool_subagent_agent-1_next","toolName":"Read","source":"subagent","agentId":"agent-1","childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1","input":{"path":"README.md"}}`))
			nextSpan := zcodeTestSpanID(t, sink, "tool_subagent_agent-1_next")
			assert.Equal(t, "Read", child.GetSpanType(nextSpan), "new child work still routes through the finalized parent owner")
			a.HandleOutput(zcodeTurnEventLine(t, sequence+1, "parent", contracts.ZCodeEventSessionUpdated, `{"agentId":"agent-1","parentToolCallId":"spawn-1","childSessionId":"sess_subagent_agent-1","status":"completed","message":"Done."}`))
			assert.ElementsMatch(t, []string{childSpan, nextSpan}, child.ClosedSpans())
			a.Mu.Lock()
			assert.Empty(t, a.turns)
			assert.Empty(t, a.toolInvocations)
			a.Mu.Unlock()
		})
	}
}

func TestZCodeSubagent_ReportFreeLifecycleWaitsForTheForegroundResult(t *testing.T) {
	t.Parallel()
	a, sink := spawnAZCodeSubagent(t)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	childID := rows[0].ChildAgentID
	spanID := zcodeTestSpanID(t, &sink.Sink, "spawn-1")
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, zcodeSubagentResult))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","agentType":"Explore","childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1","status":"completed","totalToolUseCount":1,"totalTokens":3}`))
	assert.False(t, a.children.hasEnded(spanID), "the foreground Agent call still owns its child report")
	routed, present := a.children.child(spanID)
	assert.True(t, present)
	assert.Equal(t, childID, routed)
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventToolUpdated, zcodeSpawnResult))
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "sess_subagent_agent-1", rows[0].RowKey)
	assert.Equal(t, childID, rows[0].ChildAgentID)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	child := sink.Child(childID)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "1 file", reports[0][contracts.NotificationFieldText])
	assert.True(t, a.children.hasEnded(spanID))
	assert.Len(t, sink.ChildAgentIDs(), 1, "completion must not create a replacement child")
}
