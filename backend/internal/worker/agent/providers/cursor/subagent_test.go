package cursor

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACP_CursorSubagentFromToolCall_TaskToolNameDetectsSpawn(t *testing.T) {
	tc := acp.ToolCallEnvelope{
		ToolCallID: "call-abc-0",
		Title:      "Task: build the feature",
		RawInput:   json.RawMessage(`{"_toolName":"task","prompt":"do it"}`),
	}
	obs := cursorSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "call-abc-0", obs.RowKey)
		assert.Equal(t, "build the feature", obs.Title, "Task: prefix stripped")
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.False(t, obs.CloseRow)
	}
}

func TestACP_CursorSubagentFromToolCall_NonTaskReturnsNil(t *testing.T) {
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-c1",
		Title:      "Task: x",
		RawInput:   json.RawMessage(`{"_toolName":"read"}`),
	}
	assert.Nil(t, cursorSubagentFromToolCall(tc))
}

func TestACP_CursorSubagentFromToolCallUpdate_FinalCloses(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-abc-0",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"durationMs":1200,"isBackground":false}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, false)
	if assert.NotNil(t, obs) {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusCompleted, obs.Status)
		assert.Empty(t, obs.Activity, "isBackground false -> no activity note")
	}
}

// A backgrounded call that is NOT the task tool is a shell. The neutral layer
// defaults a blank kind to Subagent, so leaving it blank put a shell in the
// sidebar under a Bot icon, in the subagent filter tab, labelled with its raw
// toolCallId.
func TestACP_CursorSubagentFromToolCallUpdate_BackgroundShellIsAShellRow(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-bg-0",
		Title:      "npm run dev",
		Status:     "completed",
		RawInput:   json.RawMessage(`{"_toolName":"shell","command":"npm run dev"}`),
		RawOutput:  json.RawMessage(`{"durationMs":5000,"isBackground":true}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, false)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "background task", obs.Activity)
		assert.Equal(t, bgtask.KindShell, obs.Kind, "a backgrounded shell is not a subagent")
		assert.Equal(t, "npm run dev", obs.Title,
			"this update is the row's only event, so it must carry the label")
		assert.Equal(t, acp.ModeUpsert, obs.Mode)
		assert.True(t, obs.CloseRow)
	}
}

// Cursor does not always echo the input on an update. With nothing to say it
// was the task tool, a backgrounded call is still a shell -- that is the case
// this bug was reported for.
func TestACP_CursorSubagentFromToolCallUpdate_BackgroundWithoutInputIsAShellRow(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-bg-1",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"isBackground":true}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, false)
	if assert.NotNil(t, obs) {
		assert.Equal(t, bgtask.KindShell, obs.Kind)
	}
}

// A backgrounded TASK tool is still a subagent. Its kind and title stay blank
// so Item.PreservingBlanksFrom keeps what the spawn observation already wrote:
// setting them here would flip the row to a shell and overwrite its trimmed
// title with the raw "Task: ..." string.
func TestACP_CursorSubagentFromToolCallUpdate_BackgroundTaskStaysASubagent(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-abc-0",
		Title:      "Task: build the feature",
		Status:     "completed",
		RawInput:   json.RawMessage(`{"_toolName":"task","prompt":"do it"}`),
		RawOutput:  json.RawMessage(`{"isBackground":true}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, true)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "background task", obs.Activity)
		assert.Equal(t, bgtask.KindUnspecified, obs.Kind, "blank keeps the spawn row's kind")
		assert.Empty(t, obs.Title, "blank keeps the spawn row's trimmed title")
	}
}

// A finished FOREGROUND tool creates no row at all, so it must not carry a kind
// that a stray upsert could write.
func TestACP_CursorSubagentFromToolCallUpdate_ForegroundCarriesNoKind(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-fg-0",
		Title:      "Read file",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"isBackground":false}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, false)
	if assert.NotNil(t, obs) {
		assert.Equal(t, acp.ModeCloseOnly, obs.Mode)
		assert.Equal(t, bgtask.KindUnspecified, obs.Kind)
		assert.Empty(t, obs.Title)
	}
}

func TestACP_CursorToolCallIsTaskTool(t *testing.T) {
	t.Parallel()

	assert.True(t, cursorToolCallIsTaskTool(json.RawMessage(`{"_toolName":"task"}`)))
	assert.False(t, cursorToolCallIsTaskTool(json.RawMessage(`{"_toolName":"shell"}`)))
	assert.False(t, cursorToolCallIsTaskTool(json.RawMessage(`{}`)))
	assert.False(t, cursorToolCallIsTaskTool(json.RawMessage(`not json`)))
	assert.False(t, cursorToolCallIsTaskTool(nil), "an absent input is not known to be the task tool")
}

func TestACP_CursorToolCallRanInBackground(t *testing.T) {
	t.Parallel()

	assert.True(t, cursorToolCallRanInBackground(json.RawMessage(`{"isBackground":true}`)))
	assert.False(t, cursorToolCallRanInBackground(json.RawMessage(`{"isBackground":false}`)))
	assert.False(t, cursorToolCallRanInBackground(json.RawMessage(`{}`)))
	assert.False(t, cursorToolCallRanInBackground(json.RawMessage(`not json`)))
	assert.False(t, cursorToolCallRanInBackground(nil))
}

func TestACP_CursorSubagentFromToolCallUpdate_InProgressReturnsNil(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{ToolCallID: "tc-c2", Status: "in_progress"}
	assert.Nil(t, cursorSubagentFromToolCallUpdate(tcu, false))
}

func TestACP_WireDecode_CursorTaskToolName(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"call-abc-0","title":"Task: build the feature","status":"in_progress","rawInput":{"_toolName":"task","prompt":"do it"}}`
	var tc acp.ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tc))
	assert.NotEmpty(t, tc.RawInput)
	obs := cursorSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Cursor detector fires on decoded wire payload") {
		assert.Equal(t, "call-abc-0", obs.RowKey)
		assert.Equal(t, "build the feature", obs.Title, "Task: prefix stripped")
	}
}

// newCursorTestAgent wires an Agent exactly as Start does, so
// the two hooks share the per-agent note about which tool calls are the `task`
// tool. Building a bare Base from the package-level functions instead would
// drop that note and test a wiring production never uses.
func newCursorTestAgent(sink agent.ProviderServices) *Agent {
	a := &Agent{}
	a.SetSinkForTest(sink)
	a.HooksForTest().SubagentFromToolCall = a.spawnObservation
	a.HooksForTest().SubagentFromToolCallUpdate = a.finishedObservation
	a.HooksForTest().ClearProviderState = a.clearTaskToolCalls
	return a
}

// Cursor's closing hook fires for EVERY finished tool call. Its close-only
// observation must not be read as a spawn, or an ordinary tool would lose the
// span it is about to close.
func TestACP_CursorClosingUpdateDoesNotDiscardAPlainToolSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &newCursorTestAgent(agent.NewProviderServices(sink)).Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-read","kind":"read","title":"Read","rawInput":{"_toolName":"read"}}`))
	require.Len(t, sink.OpenSpans(), 1)

	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-read","status":"completed"}`))

	// Closed exactly ONCE, by the closing branch. A close-only observation read as
	// a spawn would take the span EARLY, before that arm persists the row, and
	// the result row would lose its connector_end.
	assert.Equal(t, []string{"call-read"}, sink.ClosedSpans(), "the span closes once, normally")
	require.Len(t, sink.Messages(), 2)
	assert.True(t, sink.Messages()[1].Closing)
	require.Len(t, sink.Messages()[1].SpansOpenAtPersist, 1,
		"the closing row persists while its own span is still open, so it can draw connector_end")
}

// End to end through the neutral layer: a Cursor background shell lands in the
// registry as a SHELL row with a readable title, not as a subagent.
func TestACP_CursorBackgroundShellLandsAsAShellRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &newCursorTestAgent(agent.NewProviderServices(sink)).Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"npm run dev","rawInput":{"_toolName":"shell","command":"npm run dev"}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-sh","status":"completed","title":"npm run dev","rawInput":{"_toolName":"shell"},"rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "call-sh", tasks[0].RowKey)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind,
		"a backgrounded shell must not render under the Bot icon in the subagent tab")
	assert.Equal(t, "npm run dev", tasks[0].Title,
		"without a title the sidebar falls back to the raw toolCallId")
	assert.False(t, tasks[0].TitleIsCommand,
		"Cursor's title is a label, not a verbatim command, so it stays in the normal face")
	assert.Empty(t, tasks[0].ChildAgentID, "a shell has no transcript to open")

	// A shell is an ordinary tool span: it opens one and closes it normally.
	// One rule keeps it that way -- neither Cursor hook ever claims a spawn for
	// a backgrounded shell. TestACP_OnlyTheSpawnDetectorsClaimASpawn isolates it.
	assert.Equal(t, []string{"call-sh"}, sink.ClosedSpans(),
		"a shell is not a spawn, so it closes once on its own closing update")
}

// The same row, when the closing update omits rawInput. Cursor does not always
// echo the input on an update, and a backgrounded task then looks exactly like
// a backgrounded shell on the wire. The spawn's note is what tells them apart.
//
// Reading the identity off the update alone took the shell arm here, and a
// non-blank Kind and Title win in Item.PreservingBlanksFrom -- so the live
// subagent row turned into a shell row under the Bot icon's replacement, and
// its trimmed title was overwritten with the raw envelope title.
func TestACP_CursorBackgroundTaskWithoutInputStaysASubagent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &newCursorTestAgent(agent.NewProviderServices(sink)).Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: build the feature","rawInput":{"_toolName":"task","prompt":"do it"}}`))
	// No rawInput on the close, and isBackground true.
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"Task: build the feature","rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind,
		"the spawn's note outranks an absent rawInput")
	assert.Equal(t, "build the feature", tasks[0].Title,
		"the closing update must not overwrite the spawn's trimmed title")
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

// A backgrounded SHELL with no rawInput is still a shell: the spawn hook left
// no note for it, which is the reported bug's case.
func TestACP_CursorBackgroundShellWithoutInputIsStillAShell(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &newCursorTestAgent(agent.NewProviderServices(sink)).Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"npm run dev","rawInput":{"_toolName":"shell","command":"npm run dev"}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-sh","status":"completed","title":"npm run dev","rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
	assert.Equal(t, "npm run dev", tasks[0].Title)
}

// The note is per tool call and is dropped when the call ends, so a later tool
// call that reuses nothing of it is classified on its own evidence.
func TestACP_CursorTaskNoteDoesNotOutliveItsToolCall(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))
	b := &a.Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-1","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-1","status":"completed","rawOutput":{"isBackground":true}}`))

	a.Mu.Lock()
	remaining := len(a.taskToolCalls)
	a.Mu.Unlock()
	assert.Zero(t, remaining, "the note is dropped when the call ends")
}

// A tool_call that arrives ALREADY final is applied and returns, so no closing
// update ever follows to drop a note. Writing one there left it for the life of
// the agent -- a session/load replay of a finished task is exactly that shape --
// and a later call that reused the id would read it and file a backgrounded
// shell as a subagent.
func TestACP_CursorAlreadyFinalTaskCallLeavesNoNote(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))
	b := &a.Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","status":"completed","title":"Task: go","rawInput":{"_toolName":"task"}}`))

	require.Len(t, sink.BackgroundTasks(), 1, "the registry row is still upserted")
	a.Mu.Lock()
	remaining := len(a.taskToolCalls)
	a.Mu.Unlock()
	assert.Zero(t, remaining, "an already-final call has no later update to read a note")

	// A later backgrounded SHELL that reuses the id is still a shell.
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"npm run dev","rawOutput":{"isBackground":true}}`))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind, "no stale note reclassified it")
}

// ClearContext replaces the session, so every note is keyed by a tool call that
// will never report again. Base clears its own subagentPrompts there for the
// same reason.
func TestACP_CursorClearProviderStateDropsTheNotes(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))

	a.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))
	a.Mu.Lock()
	before := len(a.taskToolCalls)
	a.Mu.Unlock()
	require.Equal(t, 1, before, "the in-flight task left a note")

	require.NotNil(t, a.HooksForTest().ClearProviderState, "Cursor registers the ClearContext hook")
	a.HooksForTest().ClearProviderState()

	a.Mu.Lock()
	after := len(a.taskToolCalls)
	a.Mu.Unlock()
	assert.Zero(t, after, "the outgoing session's notes are gone")
}

// The task tool's row survives its own closing update: still a subagent, still
// carrying the trimmed title the spawn gave it.
func TestACP_CursorTaskRowStaysASubagentThroughItsClose(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &newCursorTestAgent(agent.NewProviderServices(sink)).Base

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: build the feature","rawInput":{"_toolName":"task","prompt":"do it"}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"Task: build the feature","rawInput":{"_toolName":"task"},"rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
	assert.Equal(t, "build the feature", tasks[0].Title,
		"the closing update must not overwrite the spawn's trimmed title")
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

// TestCursorSubagentDetectorsClaimOnlyTheSpawn pins that the detector claims its own spawn payload, and no ordinary
// tool call. TestACP_SpawnToolCallOpensNoSpan pins what the base does with a claim.
func TestCursorSubagentDetectorsClaimOnlyTheSpawn(t *testing.T) {
	t.Parallel()

	spawn := cursorSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "c", Title: "Task: go", RawInput: json.RawMessage(`{"_toolName":"task"}`)})
	if assert.NotNil(t, spawn, "the detector fires on its spawn payload") {
		assert.True(t, spawn.Spawns, "the spawn observation claims the spawn")
		assert.True(t, acp.ObservationIsSpawn(spawn), "the spawn takes no span")
	}
	assert.Nil(t, cursorSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-plain", Kind: "read", Title: "Read", RawInput: json.RawMessage(`{"_toolName":"read","path":"/tmp/a"}`)}), "an ordinary tool call is no subagent")
	// A progress or closing observation describes a row that already exists.
	for what, obs := range map[string]*acp.SubagentObservation{
		"close": cursorSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "c", Status: "completed"}, false),
		"background shell": cursorSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "c", Title: "npm run dev", Status: "completed",
			RawOutput: json.RawMessage(`{"isBackground":true}`)}, false),
	} {
		if assert.NotNil(t, obs, "%s still produces an observation", what) {
			assert.False(t, obs.Spawns, "%s is not a spawn", what)
			assert.False(t, acp.ObservationIsSpawn(obs), "%s must not take a span", what)
		}
	}
}
