package opencode

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACP_SubagentFromToolCall_OpenCodeSpawnShape(t *testing.T) {
	family := &FamilyBase{}
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-1",
		Title:      "",
		RawInput:   json.RawMessage(`{"description":"build feature","prompt":"do the thing","subagent_type":"build"}`),
	}
	obs := family.subagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Equal(t, "build feature", obs.Title)
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.Empty(t, obs.ChildAgentKey)
		assert.Equal(t, "tc-1", obs.ChildSpawnSpanID)
		assert.Equal(t, "do the thing", obs.Prompt)
		assert.False(t, obs.CloseRow)
	}
}

func TestACP_SubagentFromToolCall_OpenCodeFallsBackToType(t *testing.T) {
	family := &FamilyBase{}
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-2",
		RawInput:   json.RawMessage(`{"prompt":"x","subagent_type":"plan"}`),
	}
	obs := family.subagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "plan", obs.Title)
	}
}

func TestACP_SubagentFromToolCall_NonSpawnReturnsNil(t *testing.T) {
	family := &FamilyBase{}
	// No prompt + no subagent_type => not a spawn.
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-3",
		RawInput:   json.RawMessage(`{"command":"ls"}`),
	}
	assert.Nil(t, family.subagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCall_EmptyInputReturnsNil(t *testing.T) {
	family := &FamilyBase{}
	tc := acp.ToolCallEnvelope{ToolCallID: "tc-4"}
	assert.Nil(t, family.subagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCallUpdate_FinalStatusCloses(t *testing.T) {
	family := &FamilyBase{}
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := family.subagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusSucceeded, obs.Status)
	}
}

func TestACP_SubagentFromToolCallUpdate_RekeysToSessionID(t *testing.T) {
	family := &FamilyBase{}
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"child-sess-1"}}`),
	}
	obs := family.subagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "child-sess-1", obs.RowKey, "renamed to the child session id")
		// The spawn row was opened under the toolCallId; RenameFrom carries it so
		// the translator renames the single row before closing (one row, not two).
		assert.Equal(t, "tc-1", obs.RenameFrom, "rename-from the spawn toolCallId")
		assert.Equal(t, "child-sess-1", obs.ChildAgentKey)
		assert.Equal(t, "tc-1", obs.ChildSpawnSpanID)
		assert.True(t, obs.CloseRow)
	}
}

func TestACP_OpenCodeBackgroundLaunchIsNotAReport(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}
	var content []acp.ToolCallBlock
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"content","content":{"type":"text","text":"Background task started"}}]`), &content))
	obs := family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"child-sess-1","background":true}}`),
		Content:    content,
	})
	require.NotNil(t, obs)
	assert.Empty(t, obs.Report.Text)
}

func TestACP_OpenCodeFamilyPersistsThePromptAndReportInTheChildTranscript(t *testing.T) {
	t.Parallel()

	b, sink := newOpenCodeChildBase(t)
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"task","kind":"think","status":"pending","rawInput":{"description":"Inspect the code","prompt":"Read the entry points.","subagent_type":"explore"}}`))
	initial, found := sink.BackgroundTask("task-call")
	require.True(t, found)
	require.NotEmpty(t, initial.ChildAgentID)
	initialMessages := sink.Child(initial.ChildAgentID).Messages()
	require.Len(t, initialMessages, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, initialMessages[0].Source)
	assert.JSONEq(t, `{"content":"Read the entry points."}`, string(initialMessages[0].Content))
	completed := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"task-call","status":"completed","rawOutput":{"metadata":{"sessionId":"child-session"}},"content":[{"type":"content","content":{"type":"text","text":"The parser is in parser.go."}}]}`)
	b.HandleToolCallUpdateForTest(completed)
	// session/load can replay the completed task. The final registry status is
	// the durable guard against copying the same report into the child again.
	b.HandleToolCallUpdateForTest(completed)

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "child-session", rows[0].RowKey)
	require.NotEmpty(t, rows[0].ChildAgentID)
	assert.Equal(t, initial.ChildAgentID, rows[0].ChildAgentID)
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())
	child := sink.Child(rows[0].ChildAgentID)
	messages := child.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source)
	assert.JSONEq(t, `{"content":"Read the entry points."}`, string(messages[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The parser is in parser.go.", reports[0]["text"])
}

// TestACP_SubagentFromToolCallUpdate_NoSessionIDKeepsSpawnKey covers the case
// where the final update carries no metadata.sessionId: the close keys off
// the spawn toolCallId directly, and RenameFrom stays empty (no rename).
func TestACP_SubagentFromToolCallUpdate_NoSessionIDKeepsSpawnKey(t *testing.T) {
	family := &FamilyBase{}
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := family.subagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Empty(t, obs.RenameFrom, "no rename when no session id surfaced")
	}
}

func TestOpenCodeSubagentReport(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "The report.", openCodeSubagentReport(`<task id="child" state="completed">
<task_result>
The report.
</task_result>
</task>`, false))
	assert.Equal(t, "plain report", openCodeSubagentReport(" plain report ", false))
	assert.Empty(t, openCodeSubagentReport("Background task started", true))
}

func TestACP_SubagentFromToolCallUpdate_InProgressReturnsNil(t *testing.T) {
	family := &FamilyBase{}
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "in_progress",
	}
	assert.Nil(t, family.subagentFromToolCallUpdate(tcu))
}

func TestACP_WireDecode_ToolCallParsesRawInput(t *testing.T) {
	family := &FamilyBase{}
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"task","kind":"other","status":"in_progress","rawInput":{"description":"build feature","prompt":"do the thing","subagent_type":"build"}}`
	var tc acp.ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tc))
	assert.Equal(t, "tc-1", tc.ToolCallID)
	assert.NotEmpty(t, tc.RawInput, "rawInput must decode -- a tag mismatch leaves this empty")
	// OpenCode shape should fire now that RawInput is populated.
	obs := family.subagentFromToolCall(tc)
	if assert.NotNil(t, obs, "OpenCode detector fires on decoded wire payload") {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Equal(t, "build feature", obs.Title, "the description replaces the generic native task title")
	}
}

func TestACP_WireDecode_ToolCallUpdateParsesRawOutput(t *testing.T) {
	family := &FamilyBase{}
	wire := `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","rawOutput":{"metadata":{"sessionId":"child-sess-1"}}}`
	var tcu acp.ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tcu))
	assert.Equal(t, "tc-1", tcu.ToolCallID)
	assert.NotEmpty(t, tcu.RawOutput, "rawOutput must decode -- a tag mismatch leaves this empty")
	obs := family.subagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs, "OpenCode final-update detector fires on decoded wire payload") {
		assert.Equal(t, "child-sess-1", obs.RowKey, "re-keyed to child session id from rawOutput")
		assert.True(t, obs.CloseRow)
	}
}

// Kilo opens its spawn tool_call with `rawInput: {}` and only fills the spawn
// shape on the first IN-PROGRESS tool_call_update (verified against kilo 7.4.20
// over ACP). Detecting only on the tool_call left the spawn with no registry
// row at all, and the final update then closed a row that was never opened.
func TestACP_OpenCodeSpawnDetectedOnTheInProgressUpdate(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}

	// The tool_call carries nothing to detect on.
	assert.Nil(t, family.subagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-1", Title: "task", RawInput: json.RawMessage(`{}`),
	}))

	// The in-progress update carries the real shape.
	obs := family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1",
		Status:     "in_progress",
		Title:      "Run echo kilo-done",
		RawInput:   json.RawMessage(`{"description":"Run echo kilo-done","prompt":"Run it.","subagent_type":"general"}`),
	})
	require.NotNil(t, obs)
	assert.Equal(t, "call-1", obs.RowKey)
	assert.Equal(t, "Run echo kilo-done", obs.Title)
	assert.Equal(t, bgtask.StatusRunning, obs.Status)
	assert.Equal(t, "Run it.", obs.Prompt)
	assert.Empty(t, obs.ChildAgentKey)
	assert.Equal(t, "call-1", obs.ChildSpawnSpanID)
	assert.False(t, obs.CloseRow)
}

// A non-final update on a PLAIN tool must not open a subagent row.
func TestACP_OpenCodeNonSpawnUpdateIsIgnored(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}

	assert.Nil(t, family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress", RawInput: json.RawMessage(`{"command":"ls"}`),
	}))
	assert.Nil(t, family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress",
	}))
}

// The final update still closes (and re-keys to the child session id), so
// adding the spawn branch above did not swallow the close.
func TestACP_OpenCodeFinalUpdateStillClosesAndRekeys(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}

	obs := family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1",
		Status:     "completed",
		RawInput:   json.RawMessage(`{"description":"d","prompt":"p","subagent_type":"general"}`),
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"ses-child"}}`),
	})
	require.NotNil(t, obs)
	assert.True(t, obs.CloseRow)
	assert.Equal(t, "ses-child", obs.RowKey)
	assert.Equal(t, "call-1", obs.RenameFrom)
	assert.Equal(t, bgtask.StatusSucceeded, obs.Status)
}

func TestACP_OpenCodeFamilyTaskWithoutArgumentsOpensNoSpan(t *testing.T) {
	t.Parallel()
	b, sink := newOpenCodeChildBase(t)
	// Both installed providers emit this request before their tool arguments arrive.
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"task","kind":"think","status":"pending","rawInput":{}}`))
	assert.Empty(t, sink.OpenSpans())
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].NoSpan)
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"task-call","status":"in_progress","rawInput":{"description":"Inspect the code","prompt":"Read the entry points.","subagent_type":"explore"}}`))
	assert.Empty(t, sink.OpenSpans())
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"task-call","status":"completed","rawOutput":{"metadata":{"sessionId":"child-session"}}}`))
	require.Len(t, sink.Messages(), 2)
	assert.Empty(t, sink.Messages()[1].SpansOpenAtPersist)
}

func TestACP_OpenCodeTaskIdentityWithoutArguments(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		obs := family.subagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "task-call", Title: "task", Kind: "think", RawInput: raw})
		require.NotNil(t, obs)
		assert.True(t, obs.Spawns)
		assert.Equal(t, "Subagent", obs.Title)
	}
	assert.Nil(t, family.subagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "other-call", Title: "task", Kind: "other", RawInput: json.RawMessage(`{}`)}))
}

// An unrecognized title needs later arguments to identify a subagent.
// Releasing that span keeps its recorded type for the result row.
func TestACP_LateSpawnGivesItsSpanBack(t *testing.T) {
	t.Parallel()

	b, sink := newOpenCodeChildBase(t)

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-spawn","kind":"other","title":"new_task","rawInput":{}}`))
	require.Len(t, sink.OpenSpans(), 1, "the empty rawInput hides the spawn, so a span opens")

	b.HandleToolCallUpdateForTest(json.RawMessage(
		`{"toolCallId":"call-spawn","status":"in_progress","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`))

	assert.Equal(t, []string{"call-spawn"}, sink.ClosedSpans(),
		"the span is given back although the subagent keeps running")
	assert.Equal(t, "other", sink.GetSpanType("call-spawn"),
		"and the recorded type survives, so the closing update reads it back")

	// The detector re-runs on every update, and Kilo keeps echoing its rawInput,
	// so it re-reports the spawn. The release must not repeat: each repeat would
	// take the tracker mutex and re-scan the active set for a span already gone.
	b.HandleToolCallUpdateForTest(json.RawMessage(
		`{"toolCallId":"call-spawn","status":"in_progress","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`))
	assert.Equal(t, []string{"call-spawn"}, sink.ClosedSpans(), "released once, not once per update")

	// A tool call that starts after the discard sits at column 0, not column 1.
	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-plain","kind":"read","title":"Read"}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn rail is gone")

	// The closing update persists the recorded kind, not the "tool_call"
	// fallback -- the span type outlives the close that freed the column.
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-spawn","status":"completed","rawOutput":{"metadata":{"sessionId":"ses-child"}}}`))
	msgs = sink.Messages()
	require.Len(t, msgs, 3)
	assert.Equal(t, "other", msgs[2].SpanType)
	assert.True(t, msgs[2].Closing)
}

// A backgrounded shell keeps its rail because no detector claims it as a spawn
// -- not because the shared layer exempts its tool kind. The `execute` carve-out
// that used to spare it is gone: it second-guessed a provider that had already
// stated the answer, and it half-obeyed, suppressing the span while still
// upserting a subagent row.
//
// Driven through the real OpenCode hooks on an `execute` call, which is the kind
// a shell arrives under.
func TestACP_ABackgroundShellKeepsItsSpanWithNoKindExemption(t *testing.T) {
	t.Parallel()

	b, sink := newOpenCodeChildBase(t)

	// A real shell payload: a command, and none of the spawn discriminators.
	shell := `"rawInput":{"command":"npm run dev"}`
	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"bash",` + shell + `}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1, "the detector does not fire, so the shell keeps its span")
	assert.Equal(t, "call-sh", open[0].SpanID)
	assert.Equal(t, []string{"call-sh"}, sink.ReservedColorSpans())

	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-sh","status":"in_progress",` + shell + `}`))
	require.Len(t, sink.OpenSpans(), 1, "and the update leaves it alone")

	b.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-sh","status":"completed",` + shell + `}`))
	assert.Equal(t, []string{"call-sh"}, sink.ClosedSpans(), "it closes normally")
}

// A tool_call that arrives already final never opened a span, so the spawn
// detector must not disturb it -- it only feeds the registry.
func TestACP_FinalToolCallSpawnStillFeedsTheRegistry(t *testing.T) {
	t.Parallel()

	b, sink := newOpenCodeChildBase(t)

	b.HandleToolCallForTest(json.RawMessage(
		`{"toolCallId":"call-spawn","kind":"other","status":"completed","title":"explore","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`))

	assert.Empty(t, sink.OpenSpans())
	assert.Empty(t, sink.ClosedSpans())
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "the registry row is still upserted")
	assert.Equal(t, "call-spawn", tasks[0].RowKey)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].Closing)
}

func TestOpenCodeSubagentDetectorCarriesThePrompt(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}

	oc := family.subagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "tc-2",
		RawInput:   json.RawMessage(`{"description":"scan","prompt":"Find the bug.","subagent_type":"general"}`),
	})
	require.NotNil(t, oc)
	assert.Equal(t, "Find the bug.", oc.Prompt)
	assert.Empty(t, oc.ChildAgentKey)
	assert.Equal(t, "tc-2", oc.ChildSpawnSpanID)
}

func newOpenCodeChildBase(t *testing.T) (*acp.Base, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	family := &FamilyBase{}
	b := &family.Base
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = FamilyHooks(family)
	return b, sink
}

func openCodeTaskRequest(t *testing.T, callID, taskID, prompt string) json.RawMessage {
	t.Helper()
	input := map[string]any{"description": "Inspect " + callID, "prompt": prompt, "subagent_type": "explore"}
	if taskID != "" {
		input["task_id"] = taskID
	}
	frame, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": callID, "title": "task", "kind": "think", "status": "pending", "rawInput": input,
	})
	require.NoError(t, err)
	return frame
}

func openCodeTaskResult(t *testing.T, callID, nativeSession, report string) json.RawMessage {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "completed", "title": "Inspect " + callID,
		"rawOutput": map[string]any{"output": report, "metadata": map[string]any{"sessionId": nativeSession}},
		"content":   []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": report}}},
	})
	require.NoError(t, err)
	return frame
}

func TestOpenCodeTaskCreationRequiresParsedFreshArguments(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name string
		raw  json.RawMessage
		span bool
	}{
		{name: "absent arguments"},
		{name: "empty arguments", raw: json.RawMessage(`{}`)},
		{name: "null arguments", raw: json.RawMessage(`null`)},
		{name: "malformed json", raw: json.RawMessage(`{`)},
		{name: "wrong prompt type", raw: json.RawMessage(`{"prompt":false,"subagent_type":"explore"}`)},
		{name: "wrong subagent type", raw: json.RawMessage(`{"prompt":"Inspect.","subagent_type":0}`)},
		{name: "wrong task id type", raw: json.RawMessage(`{"prompt":"Inspect.","subagent_type":"explore","task_id":false}`)},
		{name: "nonempty task id", raw: json.RawMessage(`{"prompt":"Continue.","subagent_type":"explore","task_id":"ses-existing"}`)},
		{name: "fresh task", raw: json.RawMessage(`{"prompt":"Inspect.","subagent_type":"explore"}`), span: true},
		{name: "empty task id", raw: json.RawMessage(`{"prompt":"Inspect.","subagent_type":"explore","task_id":""}`), span: true},
		{name: "valid empty prompt", raw: json.RawMessage(`{"prompt":"","subagent_type":"explore"}`), span: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			family := &FamilyBase{}
			obs := family.subagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "native-call", Title: "task", Kind: "think", RawInput: scenario.raw})
			require.NotNil(t, obs)
			assert.True(t, obs.Spawns)
			assert.Equal(t, "native-call", obs.RowKey)
			assert.Empty(t, obs.ChildAgentKey)
			if scenario.span {
				assert.Equal(t, "native-call", obs.ChildSpawnSpanID)
			} else {
				assert.Empty(t, obs.ChildSpawnSpanID)
			}
		})
	}
}

func TestOpenCodeTaskReuseKeepsOneNativeChildAcrossCallIDs(t *testing.T) {
	t.Parallel()
	b, sink := newOpenCodeChildBase(t)
	b.SetSessionIDForTest("session-1")
	first := openCodeTaskRequest(t, "call-first", "", "Inspect the entry point.")
	b.HandleToolCallForTest(first)
	initial, found := sink.BackgroundTask("call-first")
	require.True(t, found)
	require.NotEmpty(t, initial.ChildAgentID)
	child := sink.Child(initial.ChildAgentID)
	require.Len(t, child.Messages(), 1)
	b.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-first", "ses-native-child", "First native report."))
	finished, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	require.Equal(t, bgtask.StatusSucceeded, finished.Status)

	second := openCodeTaskRequest(t, "call-second", "ses-native-child", "Continue the same native task.")
	b.HandleToolCallForTest(second)
	_, orphanFound := sink.BackgroundTask("call-second")
	assert.False(t, orphanFound, "reusing an existing task_id must not open a separate call row")
	revived, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, revived.Status, "reusing an existing task_id must revive its row")
	assert.Equal(t, initial.ChildAgentID, revived.ChildAgentID)
	assert.Equal(t, []string{"ses-native-child"}, sink.RevivedTasks())
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())

	// Live Job 2 child output must reach the revived child's transcript.
	assert.True(t, b.FeedChildUpdate("ses-native-child", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Job 2 progress."}}`)))

	completed := openCodeTaskResult(t, "call-second", "ses-native-child", "Second native report.")
	b.HandleToolCallUpdateForTest(completed)
	b.HandleToolCallUpdateForTest(completed)
	rows := sink.BackgroundTasks()
	assert.Len(t, rows, 1)
	row, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	assert.Equal(t, initial.ChildAgentID, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())
	var childTexts []string
	for _, msg := range child.Messages() {
		if _, text, _, ok := agenttest.DecodeAssembledMessage(msg.Content); ok {
			childTexts = append(childTexts, text)
		}
	}
	assert.Equal(t, []string{"Job 2 progress."}, childTexts)
	reports := child.LeapMuxNotifications()
	if assert.Len(t, reports, 2) {
		assert.Equal(t, "First native report.", reports[0]["text"])
		assert.Equal(t, "Second native report.", reports[1]["text"])
	}
	var originals []string
	for _, message := range sink.Messages() {
		if message.SpanID == "call-second" && !message.Closing {
			originals = append(originals, string(message.Content))
		}
	}
	require.Len(t, originals, 1)
	assert.JSONEq(t, string(second), originals[0])
}

func TestOpenCodeTaskReuseUsesTheReturnedSessionAfterMissingTaskID(t *testing.T) {
	t.Parallel()
	b, sink := newOpenCodeChildBase(t)
	b.HandleToolCallForTest(openCodeTaskRequest(t, "call-missing", "ses-missing", "Inspect the new task."))
	assert.Empty(t, sink.ChildAgentIDs())
	pending, found := sink.BackgroundTask("call-missing")
	require.True(t, found)
	assert.Empty(t, pending.ChildAgentID)
	b.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-missing", "ses-returned", "Returned-session report."))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "ses-returned", rows[0].RowKey)
	require.NotEmpty(t, rows[0].ChildAgentID)
	assert.Equal(t, []string{rows[0].ChildAgentID}, sink.ChildAgentIDs())
	child := sink.Child(rows[0].ChildAgentID)
	span, err := sink.ChildSpawnSpan(rows[0].ChildAgentID)
	require.NoError(t, err)
	assert.Equal(t, "call-missing", span)
	messages := child.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source)
	assert.JSONEq(t, `{"content":"Inspect the new task."}`, string(messages[0].Content))
	reports := child.LeapMuxNotifications()
	if assert.Len(t, reports, 1) {
		assert.Equal(t, "Returned-session report.", reports[0]["text"])
	}
}

func TestOpenCodePlainFinalToolCreatesNoChild(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			b, sink := newOpenCodeChildBase(t)
			b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"plain-shell","title":"bash","kind":"execute","status":"pending","rawInput":{"command":"echo native"}}`))
			closing, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "plain-shell", "status": status,
				"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "Native shell result."}}},
			})
			require.NoError(t, err)
			b.HandleToolCallUpdateForTest(closing)
			assert.Empty(t, sink.ChildAgentIDs())
			assert.Empty(t, sink.BackgroundTasks())
			var originals []string
			for _, message := range sink.Messages() {
				if message.SpanID == "plain-shell" && message.Closing {
					originals = append(originals, string(message.Content))
				}
			}
			require.Len(t, originals, 1)
			assert.JSONEq(t, string(closing), originals[0])
		})
	}
}

// TestOpenCodeSubagentDetectorsClaimOnlyTheSpawn pins that the detector claims its own spawn payload, and no ordinary
// tool call. TestACP_SpawnToolCallOpensNoSpan pins what the base does with a claim.
func TestOpenCodeSubagentDetectorsClaimOnlyTheSpawn(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}

	spawn := family.subagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "o", RawInput: json.RawMessage(`{"prompt":"go","subagent_type":"general"}`)})
	if assert.NotNil(t, spawn, "the detector fires on its spawn payload") {
		assert.True(t, spawn.Spawns, "the spawn observation claims the spawn")
		assert.True(t, acp.ObservationIsSpawn(spawn), "the spawn takes no span")
	}
	assert.Nil(t, family.subagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-plain", Kind: "read", Title: "Read", RawInput: json.RawMessage(`{"filePath":"/tmp/a"}`)}), "an ordinary tool call is no subagent")
	// A progress or closing observation describes a row that already exists.
	for what, obs := range map[string]*acp.SubagentObservation{
		"close": family.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "o", Status: "completed"}),
	} {
		if assert.NotNil(t, obs, "%s still produces an observation", what) {
			assert.False(t, obs.Spawns, "%s is not a spawn", what)
			assert.False(t, acp.ObservationIsSpawn(obs), "%s must not take a span", what)
		}
	}
}

func openCodeBackgroundLaunchResult(t *testing.T, callID, nativeSession string) json.RawMessage {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "completed", "title": "Inspect " + callID,
		"rawOutput": map[string]any{"output": "Background task started", "metadata": map[string]any{"sessionId": nativeSession, "background": true}},
		"content":   []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "Background task started"}}},
	})
	require.NoError(t, err)
	return frame
}

func newOpenCodeFamilyBaseWithQueue(t *testing.T) (*FamilyBase, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	family := &FamilyBase{}
	queue := &agent.OutputQueue{}
	family.SetSinkForTest(agent.NewOrderedProviderServices(queue, agent.NewProviderServices(sink)))
	family.SetSessionIDForTest("session-1")
	*family.HooksForTest() = FamilyHooks(family)
	return family, sink
}

func TestKiloBackgroundSubagentStaysOpenUntilDaemonFinish(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		event    string
		expected bgtask.Status
	}{
		{
			name:     "session.status idle closes succeeded",
			event:    `{"type":"session.status","properties":{"sessionID":"ses-bg","status":{"type":"idle"}}}`,
			expected: bgtask.StatusSucceeded,
		},
		{
			name:     "session.error closes failed",
			event:    `{"type":"session.error","properties":{"sessionID":"ses-bg","error":{"message":"boom"}}}`,
			expected: bgtask.StatusFailed,
		},
		{
			name:     "session.deleted with info.id closes stopped",
			event:    `{"type":"session.deleted","properties":{"info":{"id":"ses-bg"}}}`,
			expected: bgtask.StatusStopped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			family, sink := newOpenCodeFamilyBaseWithQueue(t)
			family.HandleToolCallForTest(openCodeTaskRequest(t, "call-bg", "", "Run in background."))
			family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-bg", "ses-bg"))

			row, found := sink.BackgroundTask("ses-bg")
			require.True(t, found)
			assert.Equal(t, bgtask.StatusRunning, row.Status, "background launch must stay running until daemon completion")

			// Daemon SSE completion must close and flush immediately without another ACP frame.
			family.handleDaemonEvent(json.RawMessage(tc.event))
			closed, found := sink.BackgroundTask("ses-bg")
			require.True(t, found)
			assert.Equal(t, tc.expected, closed.Status)
		})
	}
}

func TestKiloEarlyChildSSEBufferedAndStaleIdleCleared(t *testing.T) {
	t.Parallel()

	t.Run("early idle before background launch closes immediately on launch update", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-fast", "", "Fast background task."))
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-fast","status":{"type":"idle"}}}`))

		family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-fast", "ses-fast"))
		row, found := sink.BackgroundTask("ses-fast")
		require.True(t, found)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	})

	t.Run("early error before background launch closes failed immediately on launch update", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-fast-err", "", "Fast failing background task."))
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.error","properties":{"sessionID":"ses-fast-err"}}`))
		// A trailing idle event after an error must not overwrite the buffered failure.
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-fast-err","status":{"type":"idle"}}}`))

		family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-fast-err", "ses-fast-err"))
		row, found := sink.BackgroundTask("ses-fast-err")
		require.True(t, found)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
	})

	t.Run("stale idle cleared by busy status does not prematurely close job 2", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-reused","status":{"type":"idle"}}}`))
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-reused","status":{"type":"busy"}}}`))

		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-bg-2", "", "Second run."))
		family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-bg-2", "ses-reused"))
		row, found := sink.BackgroundTask("ses-reused")
		require.True(t, found)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})

	t.Run("stale idle cleared by foreground close does not close later background launch", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-fg", "", "Foreground run."))
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-shared","status":{"type":"idle"}}}`))
		family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-fg", "ses-shared", "Foreground done."))

		// Job 2 launches the same session in background.
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-bg", "ses-shared", "Background run."))
		family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-bg", "ses-shared"))
		row, found := sink.BackgroundTask("ses-shared")
		require.True(t, found)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})

	t.Run("trailing idle after foreground close cleared on task_id reuse", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Job 1."))
		family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-shared", "Job 1 done."))
		// Trailing idle arrives AFTER foreground close.
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-shared","status":{"type":"idle"}}}`))

		// Job 2 reuses ses-shared in background.
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-2", "ses-shared", "Job 2."))
		family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-2", "ses-shared"))
		row, found := sink.BackgroundTask("ses-shared")
		require.True(t, found)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})
}

func TestKiloLateNonFinalToolCallUpdateAfterRenameAndCloseIgnored(t *testing.T) {
	t.Parallel()
	family, sink := newOpenCodeFamilyBaseWithQueue(t)
	family.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Inspect code."))
	family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-child", "Done."))

	// A late in_progress update for the finalized toolCallId must not recreate an orphan call-1 row.
	family.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"Inspect code","status":"in_progress","rawInput":{"description":"Inspect code","prompt":"Inspect code.","subagent_type":"explore"}}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "ses-child", rows[0].RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	_, orphanFound := sink.BackgroundTask("call-1")
	assert.False(t, orphanFound)
}

func TestKiloEmptyToolCallThenInProgressTaskIDRevivesAndRecordsJob2(t *testing.T) {
	t.Parallel()
	family, sink := newOpenCodeFamilyBaseWithQueue(t)

	// Job 1 finishes on ses-native-child.
	family.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"task","kind":"think","status":"pending","rawInput":{}}`))
	family.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"Job 1","status":"in_progress","rawInput":{"description":"Job 1","prompt":"Do job 1.","subagent_type":"explore"}}`))
	initial, found := sink.BackgroundTask("call-1")
	require.True(t, found)
	require.NotEmpty(t, initial.ChildAgentID)
	family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-native-child", "Job 1 report."))
	done1, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	require.Equal(t, bgtask.StatusSucceeded, done1.Status)

	// Job 2 opens with empty rawInput, then reveals task_id in an in_progress update.
	family.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-2","title":"task","kind":"think","status":"pending","rawInput":{}}`))
	family.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-2","title":"Job 2","status":"in_progress","rawInput":{"description":"Job 2","prompt":"Do job 2.","subagent_type":"explore","task_id":"ses-native-child"}}`))

	_, orphanFound := sink.BackgroundTask("call-2")
	assert.False(t, orphanFound, "call-2 must collapse into ses-native-child")
	revived, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, revived.Status)
	assert.Equal(t, initial.ChildAgentID, revived.ChildAgentID)
	assert.Equal(t, []string{"ses-native-child"}, sink.RevivedTasks())
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())

	// Live child session update for Job 2 reaches the existing child transcript.
	family.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses-native-child","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Job 2 output."}}}}`))

	// Job 2 fails; ses-native-child must record StatusFailed (not stay Job 1's StatusSucceeded).
	family.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-2","title":"Job 2","status":"failed","rawOutput":{"error":"Job 2 failed","metadata":{"sessionId":"ses-native-child"}},"content":[{"type":"content","content":{"type":"text","text":"Job 2 failed"}}]}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	finalRow, found := sink.BackgroundTask("ses-native-child")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, finalRow.Status)
	assert.Equal(t, initial.ChildAgentID, finalRow.ChildAgentID)
	child := sink.Child(initial.ChildAgentID)
	var childTexts []string
	for _, msg := range child.Messages() {
		if _, text, _, ok := agenttest.DecodeAssembledMessage(msg.Content); ok {
			childTexts = append(childTexts, text)
		}
	}
	assert.Equal(t, []string{"Job 2 output."}, childTexts)
	reports := child.LeapMuxNotifications()
	if assert.Len(t, reports, 2) {
		assert.Equal(t, "Job 1 report.", reports[0]["text"])
		assert.Equal(t, "Job 2 failed", reports[1]["text"])
	}
}

func TestOpenCodeFinalOnArrivalToolCallHandlesSessionBackgroundStopAndReuse(t *testing.T) {
	t.Parallel()

	t.Run("final-on-arrival task call inspects rawOutput sessionId and consumes child stop", func(t *testing.T) {
		t.Parallel()
		family, _ := newOpenCodeFamilyBaseWithQueue(t)
		family.recordChildStop("call-final", true)

		obs := family.subagentFromToolCall(acp.ToolCallEnvelope{
			ToolCallID: "call-final",
			Title:      "task",
			Kind:       "think",
			Status:     "failed",
			RawInput:   json.RawMessage(`{"description":"Inspect","prompt":"Check.","subagent_type":"explore"}`),
			RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"ses-final"}}`),
		})
		require.NotNil(t, obs)
		assert.Equal(t, "ses-final", obs.RowKey)
		assert.Equal(t, "ses-final", obs.ChildAgentKey)
		assert.Equal(t, "call-final", obs.ChildSpawnSpanID)
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusInterrupted, obs.Status)
		assert.False(t, family.takeChildStop("call-final"), "stop marker must be consumed")
	})

	t.Run("final-on-arrival background launch claims background session and handles early SSE", func(t *testing.T) {
		t.Parallel()
		family, _ := newOpenCodeFamilyBaseWithQueue(t)
		family.handleDaemonEvent(json.RawMessage(`{"type":"session.error","properties":{"sessionID":"ses-bg-early"}}`))

		obs := family.subagentFromToolCall(acp.ToolCallEnvelope{
			ToolCallID: "call-bg-final",
			Title:      "task",
			Kind:       "think",
			Status:     "completed",
			RawInput:   json.RawMessage(`{"description":"BG","prompt":"Run.","subagent_type":"explore"}`),
			RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"ses-bg-early","background":true}}`),
		})
		require.NotNil(t, obs)
		assert.Equal(t, "ses-bg-early", obs.RowKey)
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusFailed, obs.Status)
	})

	t.Run("final-on-arrival reused task does not leak reusedTaskCalls or attach session", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Job 1."))
		family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-reused", "Done 1."))
		_, attachedBefore := family.SessionOwner("ses-reused")
		require.False(t, attachedBefore)

		obs := family.subagentFromToolCall(acp.ToolCallEnvelope{
			ToolCallID: "call-2",
			Title:      "task",
			Kind:       "think",
			Status:     "completed",
			RawInput:   json.RawMessage(`{"description":"Job 2","prompt":"Continue.","subagent_type":"explore","task_id":"ses-reused"}`),
			RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"ses-reused"}}`),
		})
		require.NotNil(t, obs)
		assert.Equal(t, "ses-reused", obs.RowKey)
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusSucceeded, obs.Status)

		family.childSessionsMu.Lock()
		_, leakedReuse := family.reusedTaskCalls["call-2"]
		family.childSessionsMu.Unlock()
		assert.False(t, leakedReuse, "final-on-arrival reused task must not leak reusedTaskCalls")
		_, attachedAfter := family.SessionOwner("ses-reused")
		assert.False(t, attachedAfter, "final-on-arrival reused task must not attach a child session")
		assert.Len(t, sink.BackgroundTasks(), 1)
	})
}

func TestKiloStrongerEarlyChildEventOverwritesEarlierIdle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		second   string
		expected bgtask.Status
	}{
		{
			name:     "session.error overwrites earlier idle",
			second:   `{"type":"session.error","properties":{"sessionID":"ses-race"}}`,
			expected: bgtask.StatusFailed,
		},
		{
			name:     "session.deleted overwrites earlier idle",
			second:   `{"type":"session.deleted","properties":{"info":{"id":"ses-race"}}}`,
			expected: bgtask.StatusStopped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			family, sink := newOpenCodeFamilyBaseWithQueue(t)
			family.HandleToolCallForTest(openCodeTaskRequest(t, "call-race", "", "Run in background."))
			family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-race","status":{"type":"idle"}}}`))
			family.handleDaemonEvent(json.RawMessage(tc.second))

			family.HandleToolCallUpdateForTest(openCodeBackgroundLaunchResult(t, "call-race", "ses-race"))
			row, found := sink.BackgroundTask("ses-race")
			require.True(t, found)
			assert.Equal(t, tc.expected, row.Status)
		})
	}
}

func TestOpenCodeChildTrackingMapsAreCappedAndCleaned(t *testing.T) {
	t.Parallel()
	family, _ := newOpenCodeFamilyBaseWithQueue(t)

	for i := range 600 {
		family.handleDaemonEvent(json.RawMessage(fmt.Sprintf(`{"type":"session.status","properties":{"sessionID":"ses-%03d","status":{"type":"idle"}}}`, i)))
		family.markTaskCallFinalized(fmt.Sprintf("call-%03d", i), "session-1")
	}

	family.childSessionsMu.Lock()
	earlyLen := family.earlyChildEvents.Len()
	finalizedLen := family.finalizedTaskCalls.Len()
	family.childSessionsMu.Unlock()
	assert.LessOrEqual(t, earlyLen, 512, "earlyChildEvents must stay capped")
	assert.LessOrEqual(t, finalizedLen, 512, "finalizedTaskCalls must stay capped")

	// Finalizing a reused task deletes its reusedTaskCalls entry.
	family.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Job 1."))
	family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-fg-done", "Done."))
	family.HandleToolCallForTest(openCodeTaskRequest(t, "call-2", "ses-fg-done", "Job 2."))
	family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-2", "ses-fg-done", "Done 2."))

	family.childSessionsMu.Lock()
	_, reuseStillPresent := family.reusedTaskCalls["call-2"]
	family.childSessionsMu.Unlock()
	assert.False(t, reuseStillPresent, "finalizing a reused call must delete reusedTaskCalls entry")

	// Trailing daemon idle for a session already finished in foreground must not buffer into earlyChildEvents.
	family.handleDaemonEvent(json.RawMessage(`{"type":"session.status","properties":{"sessionID":"ses-fg-done","status":{"type":"idle"}}}`))
	family.childSessionsMu.Lock()
	bufferedAfterFG := family.earlyChildEvents.Has("ses-fg-done")
	family.childSessionsMu.Unlock()
	assert.False(t, bufferedAfterFG, "foreground-finished session must not buffer trailing earlyChildEvents")
}

func TestOpenCodeInlineFinalToolCallBackgroundAndReportPreservation(t *testing.T) {
	t.Parallel()

	t.Run("inline-final background launch stays running and revives reused task", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)

		// Job 1 runs and completes so ses-bg-reuse is finished.
		family.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Job 1."))
		family.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-bg-reuse", "Job 1 done."))
		row, ok := sink.BackgroundTask("ses-bg-reuse")
		require.True(t, ok)
		require.Equal(t, bgtask.StatusSucceeded, row.Status)

		// Job 2 arrives as an inline-final background launch tool_call reusing ses-bg-reuse.
		raw, err := json.Marshal(map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "call-2",
			"title":         "task",
			"kind":          "think",
			"status":        "completed",
			"rawInput": map[string]any{
				"description":   "Job 2",
				"prompt":        "Run in background.",
				"subagent_type": "explore",
				"task_id":       "ses-bg-reuse",
			},
			"rawOutput": map[string]any{
				"metadata": map[string]any{
					"sessionId":  "ses-bg-reuse",
					"background": true,
				},
			},
		})
		require.NoError(t, err)
		family.HandleToolCallForTest(raw)

		assert.Contains(t, sink.RevivedTasks(), "ses-bg-reuse", "inline-final reused background launch must revive the finished row")
		row, ok = sink.BackgroundTask("ses-bg-reuse")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status, "inline-final background launch must stay running until daemon SSE completes")
	})

	t.Run("inline-final foreground tool_call preserves content report text", func(t *testing.T) {
		t.Parallel()
		family, sink := newOpenCodeFamilyBaseWithQueue(t)
		raw, err := json.Marshal(map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "call-inline-fg",
			"title":         "task",
			"kind":          "think",
			"status":        "completed",
			"rawInput": map[string]any{
				"description":   "Quick check",
				"prompt":        "Check status.",
				"subagent_type": "explore",
			},
			"rawOutput": map[string]any{
				"metadata": map[string]any{
					"sessionId": "ses-inline-fg",
				},
			},
			"content": []any{
				map[string]any{
					"type": "content",
					"content": map[string]any{
						"type": "text",
						"text": "Inline final report text.",
					},
				},
			},
		})
		require.NoError(t, err)
		family.HandleToolCallForTest(raw)

		row, ok := sink.BackgroundTask("ses-inline-fg")
		require.True(t, ok)
		require.NotEmpty(t, row.ChildAgentID)
		child := sink.Child(row.ChildAgentID)
		require.Len(t, child.LeapMuxNotifications(), 1, "inline-final foreground task must persist its report text")
		assert.Equal(t, "Inline final report text.", child.LeapMuxNotifications()[0]["text"])
	})

	t.Run("capped tracking map stays within limit on delete and keeps re-inserted live entries", func(t *testing.T) {
		t.Parallel()
		family, _ := newOpenCodeFamilyBaseWithQueue(t)

		// Insert target, delete it, fill to cap, re-insert target, then insert one more.
		family.childSessionsMu.Lock()
		family.recordEarlyChildEventLocked("ses-target", bgtask.StatusSucceeded)
		family.earlyChildEvents.Delete("ses-target")
		for i := range openCodeMaxTrackedEntries {
			family.recordEarlyChildEventLocked(fmt.Sprintf("ses-pad-%04d", i), bgtask.StatusSucceeded)
		}
		// Re-insert ses-target so it is the NEWEST entry; it must survive the next eviction.
		family.recordEarlyChildEventLocked("ses-target", bgtask.StatusFailed)
		family.recordEarlyChildEventLocked("ses-after-target", bgtask.StatusSucceeded)
		status, stillPresent := family.earlyChildEvents.Get("ses-target")
		entryLen := family.earlyChildEvents.Len()
		family.childSessionsMu.Unlock()

		assert.True(t, stillPresent, "re-inserted live entry must not be evicted prematurely")
		assert.Equal(t, bgtask.StatusFailed, status)
		assert.LessOrEqual(t, entryLen, openCodeMaxTrackedEntries, "tracked map must stay within cap")
	})
}

func TestFamilyBaseClearSessionState(t *testing.T) {
	t.Parallel()
	family, _ := newOpenCodeFamilyBaseWithQueue(t)
	require.Equal(t, "session-1", family.CurrentSessionID())

	family.childSessionsMu.Lock()
	family.recordEarlyChildEventLocked("ses-early", bgtask.StatusSucceeded)
	family.recordFinishedForegroundSessionLocked("ses-fg")
	family.childSessionsMu.Unlock()
	family.markTaskCallFinalized("call-s1", "session-1")
	family.markTaskCallFinalized("call-s2", "session-2")

	family.clearSessionState()

	family.childSessionsMu.Lock()
	assert.Zero(t, family.earlyChildEvents.Len())
	assert.Zero(t, family.finishedForegroundSessions.Len())
	assert.True(t, family.finalizedTaskCalls.Has("call-s1"), "current session finalized calls must stay")
	assert.False(t, family.finalizedTaskCalls.Has("call-s2"), "other session finalized calls must be removed")
	assert.Equal(t, 1, family.finalizedTaskCalls.Len())
	family.childSessionsMu.Unlock()

	family.SetSessionIDForTest("")
	family.clearSessionState()

	family.childSessionsMu.Lock()
	assert.Zero(t, family.finalizedTaskCalls.Len(), "empty current session must reset finalizedTaskCalls")
	family.childSessionsMu.Unlock()
}
