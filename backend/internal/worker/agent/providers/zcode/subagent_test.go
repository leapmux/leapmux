package zcode

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
	zcodeSubagentSessionID = "sess_subagent_agent-1"
	zcodeSpawnScheduled    = `{"kind":"scheduled","toolCallId":"spawn-1","toolName":"Agent",
	  "input":{"prompt":"count the files","description":"file census"}}`
	zcodeSubagentScheduled = `{"kind":"scheduled","toolCallId":"sub-1","toolName":"Bash",
	  "source":"subagent","agentId":"agent-1","agentType":"Explore",
	  "childSessionId":"` + zcodeSubagentSessionID + `","parentToolCallId":"spawn-1",
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
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, including the agentId below, into a user-facing reply.)\nagentId: agent-1 (internal ID - do not mention to user. Use SendMessage with to: 'agent-1', summary: '<5-10 word recap>' to continue this agent.)\nThe agent is working in the background. You will be notified automatically when it completes. You know nothing about its results until that notification arrives — do not report, assume, or predict them; continue other work or respond to the user in the meantime.\nIn your own words, briefly tell the user what you launched — do not echo this tool result. Agent results will arrive in a subsequent message. If the user asks for progress, say the agent is still running."}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status,
		"the Agent result acknowledges launch; the background lifecycle closes the row later")
	_, stillOpen := a.children.child(zcodeTestSpanID(t, sink, "spawn-1"))
	assert.True(t, stillOpen)
	child := sink.Child(rows[0].ChildAgentID)
	assert.Empty(t, child.LeapMuxNotifications())
}

func TestZCodeBackgroundLaunchRequiresACompleteSuccessfulReceipt(t *testing.T) {
	t.Parallel()
	identity := "agent-child"
	content := strings.Join([]string{
		contracts.ZCodeAgentLaunchHeader,
		contracts.ZCodeAgentLaunchIdentityPrefix + identity + contracts.ZCodeAgentLaunchIdentityMiddle + identity + contracts.ZCodeAgentLaunchIdentitySuffix,
		contracts.ZCodeAgentLaunchBackground,
		contracts.ZCodeAgentLaunchWait,
	}, "\n")
	for _, tc := range []struct {
		name    string
		content string
		success bool
		want    bool
	}{
		{"current", content, true, true},
		{"failed", content, false, false},
		{"empty", "", true, false},
		{"partial", strings.Join(strings.Split(content, "\n")[:3], "\n"), true, false},
		{"foreign identity", strings.Replace(content, "to: 'agent-child'", "to: 'other'", 1), true, false},
		{"empty identity", strings.Replace(content, "agentId: agent-child", "agentId: ", 1), true, false},
		{"unknown suffix", content + "\nUnknown", true, false},
		{"quoted receipt", "Report:\n" + content, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(map[string]any{"success": tc.success, "content": tc.content})
			require.NoError(t, err)
			assert.Equal(t, tc.want, zcodeSubagentLaunchedInBackground(zcodeToolUpdated{ToolName: "Agent", Result: result}))
		})
	}
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
	assert.Equal(t, "zcode-final:spawn-1", zcodeFinalReportID("spawn-1"))
	assert.Equal(t, "zcode-final:spawn-1#2", agent.ScopeSubagentReportID(zcodeFinalReportID("spawn-1"), 1))
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
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, including the agentId below, into a user-facing reply.)\nagentId: agent-1 (internal ID - do not mention to user. Use SendMessage with to: 'agent-1', summary: '<5-10 word recap>' to continue this agent.)\nThe agent is working in the background. You will be notified automatically when it completes. You know nothing about its results until that notification arrives — do not report, assume, or predict them; continue other work or respond to the user in the meantime.\nIn your own words, briefly tell the user what you launched — do not echo this tool result. Agent results will arrive in a subsequent message. If the user asks for progress, say the agent is still running."}}`))
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
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "stored-spawn-span", ProviderChildKey: "native-child", Title: "file census"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask,

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

	for _, order := range []string{"background event first", "child tool first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))

			updates := [2]struct{ eventType, payload string }{
				{contracts.ZCodeEventSessionUpdated, `{"taskId":"task-1","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent",
				  "childSessionId":"` + zcodeSubagentSessionID + `","status":"running"}`},
				{contracts.ZCodeEventToolUpdated, zcodeSubagentScheduled},
			}
			if order == "child tool first" {
				updates[0], updates[1] = updates[1], updates[0]
			}
			a.HandleOutput(zcodeEventLine(t, 2, updates[0].eventType, updates[0].payload))
			a.HandleOutput(zcodeEventLine(t, 3, updates[1].eventType, updates[1].payload))

			children := sink.ChildAgentIDs()
			require.Len(t, children, 1)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, zcodeSubagentSessionID, rows[0].RowKey)
			assert.Equal(t, "file census", rows[0].Title, "the spawn supplies the child title")
			assert.Equal(t, children[0], rows[0].ChildAgentID)
		})
	}
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
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, including the agentId below, into a user-facing reply.)\nagentId: agent_1 (internal ID - do not mention to user. Use SendMessage with to: 'agent_1', summary: '<5-10 word recap>' to continue this agent.)\nThe agent is working in the background. You will be notified automatically when it completes. You know nothing about its results until that notification arrives — do not report, assume, or predict them; continue other work or respond to the user in the meantime.\nIn your own words, briefly tell the user what you launched — do not echo this tool result. Agent results will arrive in a subsequent message. If the user asks for progress, say the agent is still running."}}`))
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
			a.HandleOutput(zcodeTurnEventLine(t, 4, "parent", contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, including the agentId below, into a user-facing reply.)\nagentId: agent-1 (internal ID - do not mention to user. Use SendMessage with to: 'agent-1', summary: '<5-10 word recap>' to continue this agent.)\nThe agent is working in the background. You will be notified automatically when it completes. You know nothing about its results until that notification arrives — do not report, assume, or predict them; continue other work or respond to the user in the meantime.\nIn your own words, briefly tell the user what you launched — do not echo this tool result. Agent results will arrive in a subsequent message. If the user asks for progress, say the agent is still running."}}`))
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

type zcodeCleanupObservationSink struct {
	*agenttest.ControlSink
	statusesAtCleanup []bgtask.Status
}

func (sink *zcodeCleanupObservationSink) CleanupChildAgent(childID string) {
	for _, row := range sink.BackgroundTasks() {
		if row.ChildAgentID == childID && row.Role.IsCanonicalChildActivity() {
			sink.statusesAtCleanup = append(sink.statusesAtCleanup, row.Status)
		}
	}
	sink.ControlSink.CleanupChildAgent(childID)
}
func TestZCodeBackgroundTaskPublishesItsFinalStateBeforeRetiringTheChild(t *testing.T) {
	t.Parallel()
	sink := &zcodeCleanupObservationSink{ControlSink: &agenttest.ControlSink{}}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("running")))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("cancelled")))
	assert.Equal(t, []bgtask.Status{bgtask.StatusStopped}, sink.statusesAtCleanup,
		"retiring a still-working activity entry before the final registry update leaves its published spinner unchanged")
}

func TestZCodeTaskToolSpawnsSubagent(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"task-spawn-1","toolName":"Task","input":{"description":"audit files","prompt":"Check all files."}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1, "Task tool must open a subagent registry row")
	assert.Equal(t, bgtask.KindSubagent, rows[0].Kind)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
	assert.Equal(t, "audit files", rows[0].Title)
	require.NotEmpty(t, rows[0].ChildAgentID)

	spanID := zcodeTestSpanID(t, &sink.Sink, "task-spawn-1")
	assert.True(t, a.zcodeForegroundSpawnResultPending(spanID), "Task tool must count as a pending foreground spawn")

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"task-spawn-1","result":{"success":true,"content":"All checked."}}`))
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
}

func TestZCodeBackgroundLaunchRecognizesAllFourUpstreamFormats(t *testing.T) {
	t.Parallel()
	current4 := strings.Join([]string{
		contracts.ZCodeAgentLaunchHeader,
		contracts.ZCodeAgentLaunchIdentityPrefix + "agent-1" + contracts.ZCodeAgentLaunchIdentityMiddle + "agent-1" + contracts.ZCodeAgentLaunchIdentitySuffix,
		contracts.ZCodeAgentLaunchBackground,
		contracts.ZCodeAgentLaunchWait,
	}, "\n")
	previous := "Agent Explore started in background.\nagentId: agent-2\noutputFile: /tmp/out.log\nYou will be notified when the Agent completes."
	legacy := "backgroundTaskId: bg-3\nRuntime will wait for this background Agent to finish."
	jsonBackgrounded := `{"status":"backgrounded","backgroundTaskId":"bg-4"}`
	jsonAsyncLaunched := `{"status":"async_launched","agentId":"agent-5","outputFile":"/tmp/a5.log"}`

	for _, toolName := range []string{contracts.ZCodeToolNameAgent, contracts.ZCodeToolNameTask} {
		for _, tc := range []struct {
			name    string
			content string
			want    bool
		}{
			{"current", current4, true},
			{"previous", previous, true},
			{"legacy", legacy, true},
			{"json backgrounded", jsonBackgrounded, true},
			{"json async_launched", jsonAsyncLaunched, true},
			{"plain report", "I found 3 files.", false},
			{"json other status", `{"status":"completed","agentId":"agent-5"}`, false},
		} {
			t.Run(toolName+"/"+tc.name, func(t *testing.T) {
				result, err := json.Marshal(map[string]any{"success": true, "content": tc.content})
				require.NoError(t, err)
				assert.Equal(t, tc.want, zcodeSubagentLaunchedInBackground(zcodeToolUpdated{ToolName: toolName, Result: result}))
			})
		}
	}
}

func TestZCodeSendMessageRevivesFinishedSubagent(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"sess_subagent_agent_1", "agent_1"} {
		t.Run("target="+target, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _ := newZCodeLifecycleChild(t)
			spanID := zcodeTestSpanID(t, &sink.Sink, "spawn-1")

			// Finish Job 1.
			a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
			a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))
			require.True(t, a.children.hasEnded(spanID))
			row, ok := sink.BackgroundTask("sess_subagent_agent_1")
			require.True(t, ok)
			require.Equal(t, bgtask.StatusSucceeded, row.Status)

			// SendMessage targeting the finished subagent must clear ended and revive the row.
			a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
				fmt.Sprintf(`{"kind":"scheduled","toolCallId":"sm-1","toolName":"SendMessage","input":{"to":%q,"message":"Run job 2"}}`, target)))
			a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventToolUpdated,
				`{"kind":"result","toolCallId":"sm-1","result":{"success":true,"content":"Message delivered."}}`))

			assert.False(t, a.children.hasEnded(spanID), "SendMessage must clear ended[spanID]")
			assert.Equal(t, []string{"sess_subagent_agent_1"}, sink.RevivedTasks())
			row, ok = sink.BackgroundTask("sess_subagent_agent_1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, row.Status)

			// Job 2 runs and finishes cleanly under the same row and child transcript.
			a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventSessionUpdated,
				`{"taskId":"agent_1_job2","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"running"}`))
			a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventSessionUpdated,
				`{"taskId":"agent_1_job2","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"completed"}`))
			row, ok = sink.BackgroundTask("sess_subagent_agent_1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.Len(t, sink.ChildAgentIDs(), 1, "reused subagent keeps one child transcript")
		})
	}
}

func TestZCodeSendMessageDoesNotReviveBeforeSuccessfulResult(t *testing.T) {
	t.Parallel()
	a, sink, _, _ := newZCodeLifecycleChild(t)
	spanID := zcodeTestSpanID(t, &sink.Sink, "spawn-1")

	// Finish Job 1.
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))
	require.True(t, a.children.hasEnded(spanID))
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Scheduled-only SendMessage must NOT revive before completion.
	a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-err","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Run job 2"}}`))
	assert.True(t, a.children.hasEnded(spanID), "scheduled SendMessage must not clear ended[spanID]")
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Failed SendMessage must keep the child ended.
	a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventToolUpdated,
		`{"kind":"error","toolCallId":"sm-err","error":{"message":"permission denied"}}`))
	assert.True(t, a.children.hasEnded(spanID))
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// SendMessage result with success:false must keep the child ended.
	a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-false","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Run job 2"}}`))
	a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"sm-false","result":{"success":false,"content":"rejected"}}`))
	assert.True(t, a.children.hasEnded(spanID))
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Upstream ZCode structured failure: success:true with display.status:"failed" must keep the child ended.
	a.HandleOutput(zcodeEventLine(t, 11, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-display-fail","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Run job 2"}}`))
	a.HandleOutput(zcodeEventLine(t, 12, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"sm-display-fail","result":{"success":true,"content":"Cannot resume subagent","display":{"kind":"local_agent_message","status":"failed","error":"target missing"}}}`))
	assert.True(t, a.children.hasEnded(spanID), "display.status=failed must not clear ended[spanID]")
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Successful SendMessage result without repeated input still resolves target from scheduled input and revives once.
	a.HandleOutput(zcodeEventLine(t, 13, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-ok","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Run job 2"}}`))
	assert.True(t, a.children.hasEnded(spanID))
	a.HandleOutput(zcodeEventLine(t, 14, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"sm-ok","result":{"success":true,"content":"Message delivered."}}`))
	assert.False(t, a.children.hasEnded(spanID))
	assert.Equal(t, []string{"sess_subagent_agent_1"}, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestZCodeSendMessageFastJob2CompletesBeforeSendMessageResult(t *testing.T) {
	t.Parallel()
	a, sink, _, _ := newZCodeLifecycleChild(t)
	spanID := zcodeTestSpanID(t, &sink.Sink, "spawn-1")
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	childID := childIDs[0]

	// Finish Job 1.
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated, zcodeBackgroundLifecycleEnd))
	require.True(t, a.children.hasEnded(spanID))
	row, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)
	parentMessagesBefore := len(sink.Messages())

	// SendMessage is scheduled; before its result arrives, Job 2 emits resumed lifecycle,
	// a child tool call, and completes cleanly.
	a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-fast","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Fast job 2"}}`))
	assert.True(t, a.children.hasEnded(spanID), "scheduled SendMessage alone does not revive")

	// Resumed lifecycle event arrives while SendMessage is still in flight.
	a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent_1","agentType":"Explore","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","description":"file census","status":"running","resumed":true}`))
	assert.False(t, a.children.hasEnded(spanID), "resumed lifecycle during pending SendMessage must revive child")
	assert.Equal(t, []string{"sess_subagent_agent_1"}, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// Child tool call emitted before SendMessage result must route to child transcript, not parent.
	parentMessagesAfterScheduled := len(sink.Messages())
	require.Greater(t, parentMessagesAfterScheduled, parentMessagesBefore)
	childMessagesBefore := len(sink.Child(childID).Messages())
	a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"child-tool-job2","toolName":"Read","source":"subagent","agentId":"agent_1","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","input":{"path":"a.go"}}`))
	a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"child-tool-job2","source":"subagent","agentId":"agent_1","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","result":{"success":true,"content":"ok"}}`))
	assert.Equal(t, parentMessagesAfterScheduled, len(sink.Messages()), "child tool call must not leak into parent transcript")
	assert.Greater(t, len(sink.Child(childID).Messages()), childMessagesBefore, "child tool call must route into child transcript")

	// Job 2 finishes before SendMessage result arrives.
	a.HandleOutput(zcodeEventLine(t, 11, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"agent_1_job2","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"completed"}`))
	a.HandleOutput(zcodeEventLine(t, 12, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent_1","agentType":"Explore","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","description":"file census","status":"completed","summaryText":"Job 2 done."}`))
	require.True(t, a.children.hasEnded(spanID))
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	require.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Late SendMessage result must NOT re-revive the already-finished Job 2 row.
	a.HandleOutput(zcodeEventLine(t, 13, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"sm-fast","result":{"success":true,"content":"Message delivered."}}`))
	assert.True(t, a.children.hasEnded(spanID), "late SendMessage result must not revive finished Job 2")
	assert.Equal(t, []string{"sess_subagent_agent_1"}, sink.RevivedTasks(), "must not revive a second time")
	row, _ = sink.BackgroundTask("sess_subagent_agent_1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestZCodeSendMessageJob2ReportPersistsInChildAndParent(t *testing.T) {
	t.Parallel()
	a, sink, _, _ := newZCodeLifecycleChild(t)
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	child := sink.Child(childIDs[0])

	// Finish Job 1 with a report.
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("completed")))
	a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent_1","agentType":"Explore","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","description":"file census","status":"completed","summaryText":"Job 1 report."}`))

	// Revive for Job 2 via SendMessage and complete Job 2 with a distinct report.
	a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sm-job2","toolName":"SendMessage","input":{"to":"sess_subagent_agent_1","message":"Run job 2"}}`))
	a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"sm-job2","result":{"success":true,"content":"Message delivered."}}`))
	a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent_1","agentType":"Explore","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","description":"file census","status":"running","resumed":true}`))
	a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"agent_1_job2","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 11, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"agent_1_job2","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","childSessionId":"sess_subagent_agent_1","description":"file census","status":"completed"}`))
	a.HandleOutput(zcodeEventLine(t, 12, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent_1","agentType":"Explore","childSessionId":"sess_subagent_agent_1","parentToolCallId":"spawn-1","description":"file census","status":"completed","summaryText":"Job 2 report."}`))

	collectReports := func(s *agenttest.Sink) []string {
		var texts []string
		for _, n := range s.LeapMuxNotifications() {
			if n[contracts.NotificationFieldType] == contracts.NotificationTypeSubagentReport {
				texts = append(texts, fmt.Sprint(n[contracts.NotificationFieldText]))
			}
		}
		return texts
	}
	assert.Equal(t, []string{"Job 1 report.", "Job 2 report."}, collectReports(child),
		"Job 2 report must not be deduplicated against Job 1 in the child transcript")
	assert.Equal(t, []string{"Job 1 report.", "Job 2 report."}, collectReports(&sink.Sink),
		"Job 2 report must not be deduplicated against Job 1 in the parent transcript")
}

func TestZCodeSendMessageWakeAuthorizationIsolationAndDeterminism(t *testing.T) {
	t.Parallel()

	spawnAndFinishPair := func(t *testing.T, nameA, nameB string) (*Agent, *agenttest.ControlSink, string, string) {
		t.Helper()
		sink := &agenttest.ControlSink{}
		a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
		zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
			fmt.Sprintf(`{"kind":"scheduled","toolCallId":"spawn-a","toolName":"Agent","input":{"name":%q,"description":"Child A","prompt":"Work A"}}`, nameA)))
		a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-a-1","toolCallId":"spawn-a","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-a","description":"Child A","status":"running"}`))
		a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-a-1","toolCallId":"spawn-a","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-a","description":"Child A","status":"completed"}`))

		a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventToolUpdated,
			fmt.Sprintf(`{"kind":"scheduled","toolCallId":"spawn-b","toolName":"Agent","input":{"name":%q,"description":"Child B","prompt":"Work B"}}`, nameB)))
		a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-b-1","toolCallId":"spawn-b","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-b","description":"Child B","status":"running"}`))
		a.HandleOutput(zcodeEventLine(t, 6, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-b-1","toolCallId":"spawn-b","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-b","description":"Child B","status":"completed"}`))

		spanA := zcodeTestSpanID(t, &sink.Sink, "spawn-a")
		spanB := zcodeTestSpanID(t, &sink.Sink, "spawn-b")
		require.True(t, a.children.hasEnded(spanA))
		require.True(t, a.children.hasEnded(spanB))
		return a, sink, spanA, spanB
	}

	t.Run("SendMessage for Child A never unlocks unrelated ended Child B", func(t *testing.T) {
		t.Parallel()
		a, sink, spanA, spanB := spawnAndFinishPair(t, "alpha", "beta")

		a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
			`{"kind":"scheduled","toolCallId":"sm-a","toolName":"SendMessage","input":{"to":"sess-a","message":"Wake A"}}`))
		a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventToolUpdated,
			`{"kind":"result","toolCallId":"sm-a","result":{"success":true,"content":"Message delivered."}}`))
		require.False(t, a.children.hasEnded(spanA))
		require.True(t, a.children.hasEnded(spanB))

		// An unrelated new-task event for ended Child B must NOT revive Child B.
		a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-b-stale-2","toolCallId":"spawn-b","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-b","description":"Child B","status":"running"}`))
		assert.True(t, a.children.hasEnded(spanB), "SendMessage targeting Child A must never unlock revival of unrelated ended Child B")
		rowB, ok := sink.BackgroundTask("sess-b")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, rowB.Status)
		assert.Equal(t, []string{"sess-a"}, sink.RevivedTasks())
	})

	t.Run("empty-input scheduled SendMessage does not wildcard-revive unrelated Child B", func(t *testing.T) {
		t.Parallel()
		a, sink, spanA, spanB := spawnAndFinishPair(t, "alpha", "beta")

		// Scheduled SendMessage arrives without input (e.g. inputOmitted).
		a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
			`{"kind":"scheduled","toolCallId":"sm-empty","toolName":"SendMessage","input":{}}`))

		// While sm-empty is in flight, events for unrelated Child B must NOT wildcard-revive Child B.
		a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventSessionUpdated,
			`{"agentId":"agent-b","agentType":"Explore","childSessionId":"sess-b","parentToolCallId":"spawn-b","description":"Child B","status":"running","resumed":true}`))
		a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventSessionUpdated,
			`{"taskId":"task-b-2","toolCallId":"spawn-b","toolName":"Agent","taskKind":"subagent","childSessionId":"sess-b","description":"Child B","status":"running"}`))
		assert.True(t, a.children.hasEnded(spanB), "empty-input scheduled SendMessage must not wildcard-revive unrelated Child B")
		assert.Empty(t, sink.RevivedTasks())

		// When sm-empty completes carrying Child A's identity in result, only Child A revives.
		a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventToolUpdated,
			`{"kind":"result","toolCallId":"sm-empty","result":{"success":true,"childSessionId":"sess-a","content":"Message delivered."}}`))
		assert.False(t, a.children.hasEnded(spanA), "completed SendMessage result must revive its resolved target Child A")
		assert.True(t, a.children.hasEnded(spanB), "Child B must stay ended")
		assert.Equal(t, []string{"sess-a"}, sink.RevivedTasks())
	})

	t.Run("deterministic selection when two ended children share a name", func(t *testing.T) {
		t.Parallel()
		for i := range 20 {
			a, sink, spanA, spanB := spawnAndFinishPair(t, "worker", "worker")
			a.HandleOutput(zcodeEventLine(t, 7, contracts.ZCodeEventToolUpdated,
				`{"kind":"scheduled","toolCallId":"sm-dup","toolName":"SendMessage","input":{"to":"worker","message":"Wake"}}`))
			a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventToolUpdated,
				`{"kind":"result","toolCallId":"sm-dup","result":{"success":true,"content":"Message delivered."}}`))
			wantFirst := "sess-a"
			wantRevivedSpan, wantEndedSpan := spanA, spanB
			if spanB < spanA {
				wantFirst = "sess-b"
				wantRevivedSpan, wantEndedSpan = spanB, spanA
			}
			assert.Equal(t, []string{wantFirst}, sink.RevivedTasks(), "iteration %d must pick lexicographically smallest spanID", i)
			assert.False(t, a.children.hasEnded(wantRevivedSpan), "iteration %d", i)
			assert.True(t, a.children.hasEnded(wantEndedSpan), "iteration %d", i)
		}
	})

	t.Run("secondary ended lookup maps stay in sync across end, revive, and clear", func(t *testing.T) {
		t.Parallel()
		var idx zcodeChildIndex
		idx.rememberChild("spawn-1", "", "child-1")
		idx.rememberSession("spawn-1", "sess-1")
		idx.rememberTask("spawn-1", "task-1")
		idx.rememberAgent("spawn-1", "agent-1")

		idx.takeChild("spawn-1")
		span, active, ended := idx.lookupExistingSpawn("sess-1", "")
		assert.Equal(t, "spawn-1", span)
		assert.False(t, active)
		assert.True(t, ended)
		span, active, ended = idx.lookupExistingSpawn("", "task-1")
		assert.Equal(t, "spawn-1", span)
		assert.False(t, active)
		assert.True(t, ended)
		span, active, ended = idx.lookupExistingSpawn("", "agent-1")
		assert.Equal(t, "spawn-1", span)
		assert.False(t, active)
		assert.True(t, ended)

		_, revived := idx.completeSendMessage("sm-1", true, []string{"agent-1"})
		require.True(t, revived)
		assert.Empty(t, idx.endedBySession)
		assert.Empty(t, idx.endedByTask)
		assert.Empty(t, idx.endedByAgent)

		idx.takeChild("spawn-1")
		require.NotEmpty(t, idx.endedBySession)
		idx.clear()
		assert.Empty(t, idx.endedBySession)
		assert.Empty(t, idx.endedByTask)
		assert.Empty(t, idx.endedByAgent)
	})
}

func TestZCodeChildIndexSortedIndicesAndReviveForNewTask(t *testing.T) {
	t.Parallel()

	t.Run("overwriting agentID and name updates sorted indices and SendMessage targeting", func(t *testing.T) {
		t.Parallel()
		var idx zcodeChildIndex
		idx.rememberChild("spawn-1", "", "child-1")
		idx.rememberAgent("spawn-1", "agent-old")
		idx.rememberAgent("spawn-1", "agent-new")
		assert.NotContains(t, idx.agentSpawns, "agent-old")
		assert.Equal(t, []string{"spawn-1"}, idx.agentSpawns["agent-new"])

		idx.rememberName("spawn-1", "name-old")
		idx.rememberName("spawn-1", "name-new")
		assert.NotContains(t, idx.nameSpawns, "name-old")
		assert.Equal(t, []string{"spawn-1"}, idx.nameSpawns["name-new"])

		idx.completeSendMessage("sm-old-agent", true, []string{"agent-old"})
		assert.False(t, idx.spawns["spawn-1"].authorizedWake, "old agent ID must not authorize wake")
		idx.completeSendMessage("sm-old-name", true, []string{"name-old"})
		assert.False(t, idx.spawns["spawn-1"].authorizedWake, "old name must not authorize wake")

		idx.completeSendMessage("sm-new-agent", true, []string{"agent-new"})
		assert.True(t, idx.spawns["spawn-1"].authorizedWake, "new agent ID must authorize wake")

		idx.spawns["spawn-1"].authorizedWake = false
		idx.completeSendMessage("sm-new-name", true, []string{"name-new"})
		assert.True(t, idx.spawns["spawn-1"].authorizedWake, "new name must authorize wake")
	})

	t.Run("duplicate active spawn names deterministically authorize lowest spanID", func(t *testing.T) {
		t.Parallel()
		var idx zcodeChildIndex
		idx.rememberChild("spawn-b", "", "child-b")
		idx.rememberName("spawn-b", "worker")
		idx.rememberChild("spawn-a", "", "child-a")
		idx.rememberName("spawn-a", "worker")

		assert.Equal(t, []string{"spawn-a", "spawn-b"}, idx.nameSpawns["worker"])
		idx.completeSendMessage("sm-dup-name", true, []string{"worker"})
		assert.True(t, idx.spawns["spawn-a"].authorizedWake, "lowest spanID must receive wake authorization")
		assert.False(t, idx.spawns["spawn-b"].authorizedWake, "higher spanID must not receive wake authorization")
	})

	t.Run("agentSpawns, nameSpawns, and endedByName move and clear in lockstep", func(t *testing.T) {
		t.Parallel()
		var idx zcodeChildIndex
		idx.rememberChild("spawn-1", "", "child-1")
		idx.rememberSession("spawn-1", "sess-1")
		idx.rememberAgent("spawn-1", "agent-1")
		idx.rememberName("spawn-1", "worker")
		assert.Equal(t, map[string][]string{"agent-1": {"spawn-1"}}, idx.agentSpawns)
		assert.Equal(t, map[string][]string{"worker": {"spawn-1"}}, idx.nameSpawns)
		assert.Empty(t, idx.endedByName)

		idx.takeChild("spawn-1")
		assert.Empty(t, idx.agentSpawns)
		assert.Empty(t, idx.nameSpawns)
		assert.Equal(t, map[string][]string{"worker": {"spawn-1"}}, idx.endedByName)

		idx.mu.Lock()
		regKey, ok := idx.reviveLocked("spawn-1")
		idx.mu.Unlock()
		require.True(t, ok)
		assert.Equal(t, "sess-1", regKey)
		assert.Empty(t, idx.endedByName)
		assert.Equal(t, map[string][]string{"agent-1": {"spawn-1"}}, idx.agentSpawns)
		assert.Equal(t, map[string][]string{"worker": {"spawn-1"}}, idx.nameSpawns)

		idx.takeChild("spawn-1")
		assert.Empty(t, idx.agentSpawns)
		assert.Empty(t, idx.nameSpawns)
		assert.Equal(t, map[string][]string{"worker": {"spawn-1"}}, idx.endedByName)

		idx.clear()
		assert.Empty(t, idx.spawns)
		assert.Empty(t, idx.ended)
		assert.Empty(t, idx.agentSpawns)
		assert.Empty(t, idx.nameSpawns)
		assert.Empty(t, idx.endedByName)
	})

	t.Run("reviveForNewTask rejects empty and previous task IDs even when authorized", func(t *testing.T) {
		t.Parallel()
		var idx zcodeChildIndex
		idx.rememberChild("spawn-1", "", "child-1")
		idx.rememberSession("spawn-1", "sess-1")
		idx.rememberTask("spawn-1", "prev-task")
		idx.rememberName("spawn-1", "worker")

		idx.completeSendMessage("sm-auth", true, []string{"worker"})
		require.True(t, idx.spawns["spawn-1"].authorizedWake)
		idx.takeChild("spawn-1")
		require.True(t, idx.ended["spawn-1"].authorizedWake)

		regKey, ok := idx.reviveForNewTask("spawn-1", "")
		assert.Empty(t, regKey)
		assert.False(t, ok)
		assert.True(t, idx.hasEnded("spawn-1"))

		regKey, ok = idx.reviveForNewTask("spawn-1", "prev-task")
		assert.Empty(t, regKey)
		assert.False(t, ok)
		assert.True(t, idx.hasEnded("spawn-1"))

		regKey, ok = idx.reviveForNewTask("spawn-1", "new-task")
		assert.Equal(t, "sess-1", regKey)
		assert.True(t, ok)
		assert.False(t, idx.hasEnded("spawn-1"))
	})
}
