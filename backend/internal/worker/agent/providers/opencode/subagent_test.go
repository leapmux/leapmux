package opencode

import (
	"encoding/json"
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
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-1",
		Title:      "",
		RawInput:   json.RawMessage(`{"description":"build feature","prompt":"do the thing","subagent_type":"build"}`),
	}
	obs := SubagentFromToolCall(tc)
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
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-2",
		RawInput:   json.RawMessage(`{"prompt":"x","subagent_type":"plan"}`),
	}
	obs := SubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "plan", obs.Title)
	}
}

func TestACP_SubagentFromToolCall_NonSpawnReturnsNil(t *testing.T) {
	// No prompt + no subagent_type => not a spawn.
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-3",
		RawInput:   json.RawMessage(`{"command":"ls"}`),
	}
	assert.Nil(t, SubagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCall_EmptyInputReturnsNil(t *testing.T) {
	tc := acp.ToolCallEnvelope{ToolCallID: "tc-4"}
	assert.Nil(t, SubagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCallUpdate_FinalStatusCloses(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := SubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusSucceeded, obs.Status)
	}
}

func TestACP_SubagentFromToolCallUpdate_RekeysToSessionID(t *testing.T) {
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"child-sess-1"}}`),
	}
	obs := SubagentFromToolCallUpdate(tcu)
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
	var content []acp.ToolCallBlock
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"content","content":{"type":"text","text":"Background task started"}}]`), &content))
	obs := SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
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

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: SubagentFromToolCall, SubagentFromToolCallUpdate: SubagentFromToolCallUpdate}
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
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := SubagentFromToolCallUpdate(tcu)
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
	tcu := acp.ToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "in_progress",
	}
	assert.Nil(t, SubagentFromToolCallUpdate(tcu))
}

func TestACP_WireDecode_ToolCallParsesRawInput(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"task","kind":"other","status":"in_progress","rawInput":{"description":"build feature","prompt":"do the thing","subagent_type":"build"}}`
	var tc acp.ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tc))
	assert.Equal(t, "tc-1", tc.ToolCallID)
	assert.NotEmpty(t, tc.RawInput, "rawInput must decode -- a tag mismatch leaves this empty")
	// OpenCode shape should fire now that RawInput is populated.
	obs := SubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "OpenCode detector fires on decoded wire payload") {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Equal(t, "build feature", obs.Title, "the description replaces the generic native task title")
	}
}

func TestACP_WireDecode_ToolCallUpdateParsesRawOutput(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","rawOutput":{"metadata":{"sessionId":"child-sess-1"}}}`
	var tcu acp.ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tcu))
	assert.Equal(t, "tc-1", tcu.ToolCallID)
	assert.NotEmpty(t, tcu.RawOutput, "rawOutput must decode -- a tag mismatch leaves this empty")
	obs := SubagentFromToolCallUpdate(tcu)
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

	// The tool_call carries nothing to detect on.
	assert.Nil(t, SubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-1", Title: "task", RawInput: json.RawMessage(`{}`),
	}))

	// The in-progress update carries the real shape.
	obs := SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
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

	assert.Nil(t, SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress", RawInput: json.RawMessage(`{"command":"ls"}`),
	}))
	assert.Nil(t, SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress",
	}))
}

// The final update still closes (and re-keys to the child session id), so
// adding the spawn arm above did not swallow the close.
func TestACP_OpenCodeFinalUpdateStillClosesAndRekeys(t *testing.T) {
	t.Parallel()

	obs := SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
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
	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: SubagentFromToolCall, SubagentFromToolCallUpdate: SubagentFromToolCallUpdate}
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
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		obs := SubagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "task-call", Title: "task", Kind: "think", RawInput: raw})
		require.NotNil(t, obs)
		assert.True(t, obs.Spawns)
		assert.Equal(t, "Subagent", obs.Title)
	}
	assert.Nil(t, SubagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "other-call", Title: "task", Kind: "other", RawInput: json.RawMessage(`{}`)}))
}

// An unrecognized title needs later arguments to identify a subagent.
// Releasing that span keeps its recorded type for the result row.
func TestACP_LateSpawnGivesItsSpanBack(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: SubagentFromToolCall, SubagentFromToolCallUpdate: SubagentFromToolCallUpdate}

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

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: SubagentFromToolCall, SubagentFromToolCallUpdate: SubagentFromToolCallUpdate}

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

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: SubagentFromToolCall}

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

	oc := SubagentFromToolCall(acp.ToolCallEnvelope{
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
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = FamilyHooks()
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
			obs := SubagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "native-call", Title: "task", Kind: "think", RawInput: scenario.raw})
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
	first := openCodeTaskRequest(t, "call-first", "", "Inspect the entry point.")
	b.HandleToolCallForTest(first)
	initial, found := sink.BackgroundTask("call-first")
	require.True(t, found)
	require.NotEmpty(t, initial.ChildAgentID)
	child := sink.Child(initial.ChildAgentID)
	before := child.Messages()
	require.Len(t, before, 1)
	b.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-first", "ses-native-child", "First native report."))
	second := openCodeTaskRequest(t, "call-second", "ses-native-child", "Continue the same native task.")
	b.HandleToolCallForTest(second)
	pending, found := sink.BackgroundTask("call-second")
	require.True(t, found)
	assert.Empty(t, pending.ChildAgentID, "a reuse request must wait for the actual native session")
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())
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
	assert.Equal(t, before, child.Messages())
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

	spawn := SubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "o", RawInput: json.RawMessage(`{"prompt":"go","subagent_type":"general"}`)})
	if assert.NotNil(t, spawn, "the detector fires on its spawn payload") {
		assert.True(t, spawn.Spawns, "the spawn observation claims the spawn")
		assert.True(t, acp.ObservationIsSpawn(spawn), "the spawn takes no span")
	}
	assert.Nil(t, SubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-plain", Kind: "read", Title: "Read", RawInput: json.RawMessage(`{"filePath":"/tmp/a"}`)}), "an ordinary tool call is no subagent")
	// A progress or closing observation describes a row that already exists.
	for what, obs := range map[string]*acp.SubagentObservation{
		"close": SubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "o", Status: "completed"}),
	} {
		if assert.NotNil(t, obs, "%s still produces an observation", what) {
			assert.False(t, obs.Spawns, "%s is not a spawn", what)
			assert.False(t, acp.ObservationIsSpawn(obs), "%s must not take a span", what)
		}
	}
}
