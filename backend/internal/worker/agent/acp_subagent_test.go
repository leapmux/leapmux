package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACP_SubagentFromToolCall_OpenCodeSpawnShape(t *testing.T) {
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-1",
		Title:      "",
		RawInput:   json.RawMessage(`{"description":"build feature","prompt":"do the thing","subagent_type":"build"}`),
	}
	obs := openCodeSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Equal(t, "build feature", obs.Title)
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.False(t, obs.CloseRow)
	}
}

func TestACP_SubagentFromToolCall_OpenCodeFallsBackToType(t *testing.T) {
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-2",
		RawInput:   json.RawMessage(`{"prompt":"x","subagent_type":"plan"}`),
	}
	obs := openCodeSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "plan", obs.Title)
	}
}

func TestACP_SubagentFromToolCall_NonSpawnReturnsNil(t *testing.T) {
	// No prompt + no subagent_type => not a spawn.
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-3",
		RawInput:   json.RawMessage(`{"command":"ls"}`),
	}
	assert.Nil(t, openCodeSubagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCall_EmptyInputReturnsNil(t *testing.T) {
	tc := acpToolCallEnvelope{ToolCallID: "tc-4"}
	assert.Nil(t, openCodeSubagentFromToolCall(tc))
}

func TestACP_SubagentFromToolCallUpdate_FinalStatusCloses(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := openCodeSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusCompleted, obs.Status)
	}
}

func TestACP_SubagentFromToolCallUpdate_RekeysToSessionID(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"child-sess-1"}}`),
	}
	obs := openCodeSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "child-sess-1", obs.RowKey, "renamed to the child session id")
		// The spawn row was opened under the toolCallId; RenameFrom carries it so
		// the translator renames the single row before closing (one row, not two).
		assert.Equal(t, "tc-1", obs.RenameFrom, "rename-from the spawn toolCallId")
		assert.True(t, obs.CloseRow)
	}
}

// TestACP_SubagentFromToolCallUpdate_NoSessionIDKeepsSpawnKey covers the case
// where the final update carries no metadata.sessionId: the close keys off
// the spawn toolCallId directly, and RenameFrom stays empty (no rename).
func TestACP_SubagentFromToolCallUpdate_NoSessionIDKeepsSpawnKey(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "completed",
	}
	obs := openCodeSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Empty(t, obs.RenameFrom, "no rename when no session id surfaced")
	}
}

func TestACP_SubagentFromToolCallUpdate_InProgressReturnsNil(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "tc-1",
		Status:     "in_progress",
	}
	assert.Nil(t, openCodeSubagentFromToolCallUpdate(tcu))
}

func TestACP_FinalStatusMap(t *testing.T) {
	assert.Equal(t, bgtask.StatusCompleted, acpFinalStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, acpFinalStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, acpFinalStatus("cancelled"))
	assert.Equal(t, bgtask.StatusStopped, acpFinalStatus("unknown"))
}

func TestACP_GooseSubagentFromToolCallUpdate_ToolRequest(t *testing.T) {
	meta := json.RawMessage(`{
		"toolNotification": {
			"type": "message",
			"params": {
				"data": {
					"type": "subagent_tool_request",
					"subagent_id": "g-sub-1",
					"tool_call": {"name": "Read"}
				}
			}
		}
	}`)
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "tc-goose",
		Status:     "in_progress",
		Meta:       meta,
	}
	obs := gooseSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		// The registry row, the EnsureChildAgent linkage, AND the closing update
		// all key off the SPAWN toolCallId. ChildAgentKey must match RowKey, or
		// EnsureChildAgent would open a second row keyed by subagent_id that the
		// closing update (which knows only toolCallId) never reaches.
		assert.Equal(t, "tc-goose", obs.RowKey)
		assert.Equal(t, "tool: Read", obs.Activity)
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.Equal(t, "tc-goose", obs.ChildAgentKey)
		if assert.NotNil(t, obs.ChildTranscriptPayload) {
			// The payload must be a tool_call_update-shaped envelope carrying
			// sessionUpdate + the _meta so the shared ACP classifier recognizes
			// the row (a plain re-marshal of the parsed struct drops sessionUpdate
			// and the row renders as a raw-JSON dump).
			var decoded map[string]json.RawMessage
			if assert.NoError(t, json.Unmarshal(obs.ChildTranscriptPayload, &decoded)) {
				assert.JSONEq(t, `"tool_call_update"`, string(decoded["sessionUpdate"]))
				assert.JSONEq(t, `"in_progress"`, string(decoded["status"]))
				assert.Contains(t, string(decoded["_meta"]), "subagent_tool_request")
				assert.Contains(t, string(decoded["_meta"]), "Read")
			}
		}
	}
}

func TestACP_GooseSubagentFromToolCallUpdate_NonSubagentReturnsNil(t *testing.T) {
	meta := json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"other"}}}}`)
	tcu := acpToolCallUpdateEnvelope{ToolCallID: "tc-x", Meta: meta}
	assert.Nil(t, gooseSubagentFromToolCallUpdate(tcu))
}

// TestACP_GooseSpawnAndToolRequestProduceOneRow verifies the FOOTGUNS-2 fix: a
// Goose spawn tool_call followed by tool-request updates and a closing update
// collapse to exactly ONE registry row keyed by the spawn toolCallId. Before the
// fix, EnsureChildAgent was called with the per-request subagent_id (different
// from the spawn toolCallId), opening a second row keyed by subagent_id that the
// closing update (which knows only toolCallId) never reached -- an orphaned
// Running row that pinned the parent's thinking indicator.
func TestACP_GooseSpawnAndToolRequestProduceOneRow(t *testing.T) {
	sink := &testSink{}
	b := &acpBase{sink: sink}

	// Spawn tool_call opens a row under the spawn toolCallId.
	spawnObs := gooseSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "call-spawn",
		Title:      "Goose subagent",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
	})
	require.NotNil(t, spawnObs)
	b.applySubagentObservation(spawnObs)

	// A tool-request update carries a DIFFERENT per-request subagent_id; the fix
	// keys the row/link off the spawn toolCallId so no second row opens.
	reqMeta := json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"g-sub-1","tool_call":{"name":"Read"}}}}}`)
	reqObs := gooseSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-spawn",
		Status:     "in_progress",
		Meta:       reqMeta,
	})
	require.NotNil(t, reqObs)
	b.applySubagentObservation(reqObs)

	// Exactly one row, keyed by the spawn toolCallId (not g-sub-1).
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "spawn + tool-request produce one row keyed by the spawn toolCallId")
	assert.Equal(t, "call-spawn", tasks[0].RowKey)

	// Final close (which knows only the spawn toolCallId) reaches the row.
	closeObs := gooseSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-spawn",
		Status:     "completed",
	})
	require.NotNil(t, closeObs)
	b.applySubagentObservation(closeObs)

	tasks = sink.BackgroundTasks()
	require.Len(t, tasks, 1, "still one row after close")
	assert.True(t, tasks[0].Status.IsFinished(), "the closing update reached the row")
}

// TestACP_GooseSubagentToolRequestPayload_SynthesizesMetaWhenEnvelopeLacksIt
// covers the defensive fallback in gooseSubagentToolRequestPayload: when the
// parsed envelope has no _meta (the hook only fires when it does, but the
// builder stays robust), the payload synthesizes a _meta from the raw
// notification params so the frontend renderer can still read the tool name.
func TestACP_GooseSubagentToolRequestPayload_SynthesizesMetaWhenEnvelopeLacksIt(t *testing.T) {
	notificationParams := json.RawMessage(`{"data":{"type":"subagent_tool_request","subagent_id":"g-sub-2","tool_call":{"name":"Write"}}}`)
	// Empty Meta triggers the synthesis arm.
	payload := gooseSubagentToolRequestPayload(acpToolCallUpdateEnvelope{
		ToolCallID: "tc-synth",
		Status:     "in_progress",
	}, notificationParams)
	assert.NotEmpty(t, payload)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.JSONEq(t, `"tool_call_update"`, string(decoded["sessionUpdate"]))
	// The synthesized _meta re-wraps the raw params, so the discriminator + the
	// tool name survive for the frontend renderer.
	assert.Contains(t, string(decoded["_meta"]), "subagent_tool_request")
	assert.Contains(t, string(decoded["_meta"]), "Write")
}

func TestACP_GooseSubagentFromToolCallUpdate_FinalClosesRow(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{ToolCallID: "tc-x", Status: "completed"}
	obs := gooseSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs, "final update closes the registry row") {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusCompleted, obs.Status)
		assert.Equal(t, "tc-x", obs.RowKey)
	}
}

func TestACP_GooseSubagentFromToolCall_DelegateSummonDetectsSpawn(t *testing.T) {
	meta := json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`)
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-goose-spawn",
		Title:      "delegate to subagent",
		Meta:       meta,
	}
	obs := gooseSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-goose-spawn", obs.RowKey)
		assert.Equal(t, "delegate to subagent", obs.Title)
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.False(t, obs.CloseRow)
	}
}

func TestACP_GooseSubagentFromToolCall_NonDelegateReturnsNil(t *testing.T) {
	meta := json.RawMessage(`{"goose":{"toolCall":{"toolName":"read","extensionName":"developer"}}}`)
	tc := acpToolCallEnvelope{ToolCallID: "tc-x", Meta: meta}
	assert.Nil(t, gooseSubagentFromToolCall(tc))
}

func TestACP_GooseSubagentFromToolCall_NoMetaReturnsNil(t *testing.T) {
	tc := acpToolCallEnvelope{ToolCallID: "tc-x"}
	assert.Nil(t, gooseSubagentFromToolCall(tc))
}

func TestACP_ReasonixSubagentFromToolCall_SpawnShapeNoSubagentType(t *testing.T) {
	// Reasonix rawInput carries {description, prompt} with NO subagent_type.
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-rx",
		Title:      "task",
		RawInput:   json.RawMessage(`{"description":"write tests","prompt":"write the test suite"}`),
	}
	obs := reasonixSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "tc-rx", obs.RowKey)
		assert.Equal(t, "write tests", obs.Title, "title 'task' falls back to description")
		assert.Equal(t, bgtask.StatusRunning, obs.Status)
		assert.False(t, obs.CloseRow)
	}
}

func TestACP_ReasonixSubagentFromToolCall_PromptOnlyUsesDefaultTitle(t *testing.T) {
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-rx2",
		RawInput:   json.RawMessage(`{"prompt":"do thing"}`),
	}
	obs := reasonixSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "Reasonix subagent", obs.Title)
	}
}

func TestACP_ReasonixSubagentFromToolCall_NonSpawnReturnsNil(t *testing.T) {
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-rx3",
		RawInput:   json.RawMessage(`{"command":"ls"}`),
	}
	assert.Nil(t, reasonixSubagentFromToolCall(tc))
}

// The prompt is the only discriminator Reasonix supplies, so it is required.
// A `description` is an ordinary tool argument: treating one as a spawn used to
// add a false sidebar row, and now also strips that tool's span, so its card
// loses its border and its result row loses the connector back to the call.
func TestACP_ReasonixSubagentFromToolCall_DescriptionAloneIsNotASpawn(t *testing.T) {
	t.Parallel()

	for _, rawInput := range []string{
		`{"description":"add the guard","path":"/a.go"}`,
		`{"description":"run it","prompt":null}`,
		`{"description":"x","prompt":""}`,
	} {
		assert.Nil(t, reasonixSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "tc-rx-desc",
			Kind:       "edit",
			RawInput:   json.RawMessage(rawInput),
		}), "a tool argument named description does not spawn: %s", rawInput)
	}

	// The recorded spawn shape still fires.
	assert.NotNil(t, reasonixSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "tc-rx-spawn",
		Kind:       "other",
		Title:      "task",
		RawInput:   json.RawMessage(`{"description":"explore","prompt":"go"}`),
	}))
}

func TestACP_CursorSubagentFromToolCall_TaskToolNameDetectsSpawn(t *testing.T) {
	tc := acpToolCallEnvelope{
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
	tc := acpToolCallEnvelope{
		ToolCallID: "tc-c1",
		Title:      "Task: x",
		RawInput:   json.RawMessage(`{"_toolName":"read"}`),
	}
	assert.Nil(t, cursorSubagentFromToolCall(tc))
}

func TestACP_CursorSubagentFromToolCallUpdate_FinalCloses(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
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
	tcu := acpToolCallUpdateEnvelope{
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
		assert.Equal(t, acpModeUpsert, obs.Mode)
		assert.True(t, obs.CloseRow)
	}
}

// Cursor does not always echo the input on an update. With nothing to say it
// was the task tool, a backgrounded call is still a shell -- that is the case
// this bug was reported for.
func TestACP_CursorSubagentFromToolCallUpdate_BackgroundWithoutInputIsAShellRow(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
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
	tcu := acpToolCallUpdateEnvelope{
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
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "call-fg-0",
		Title:      "Read file",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"isBackground":false}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu, false)
	if assert.NotNil(t, obs) {
		assert.Equal(t, acpModeCloseOnly, obs.Mode)
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
	tcu := acpToolCallUpdateEnvelope{ToolCallID: "tc-c2", Status: "in_progress"}
	assert.Nil(t, cursorSubagentFromToolCallUpdate(tcu, false))
}

// --- Wire-JSON decode tests ---
//
// The tests above construct Go structs directly. These decode REAL ACP wire
// payloads (the inner `update` object, matching the format in
// opencode_output_test.go / kilo_output_test.go) through the envelope structs.
// They are the guard against a JSON-tag mismatch (e.g. `json:"input"` vs the
// wire's `rawInput`) -- the exact bug that left OpenCode, Kilo, Reasonix and
// Cursor subagent detection inert at runtime while every struct-construction
// test passed.

func decodeToolCallUpdate(t *testing.T, wire string) acpToolCallEnvelope {
	t.Helper()
	var tc acpToolCallEnvelope
	if err := json.Unmarshal([]byte(wire), &tc); err != nil {
		t.Fatalf("unmarshal tool_call wire: %v", err)
	}
	return tc
}

func decodeToolCallUpdateUpdate(t *testing.T, wire string) acpToolCallUpdateEnvelope {
	t.Helper()
	var tcu acpToolCallUpdateEnvelope
	if err := json.Unmarshal([]byte(wire), &tcu); err != nil {
		t.Fatalf("unmarshal tool_call_update wire: %v", err)
	}
	return tcu
}

func TestACP_WireDecode_ToolCallParsesRawInput(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"task","kind":"other","status":"in_progress","rawInput":{"description":"build feature","prompt":"do the thing","subagent_type":"build"}}`
	tc := decodeToolCallUpdate(t, wire)
	assert.Equal(t, "tc-1", tc.ToolCallID)
	assert.NotEmpty(t, tc.RawInput, "rawInput must decode -- a tag mismatch leaves this empty")
	// OpenCode shape should fire now that RawInput is populated.
	obs := openCodeSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "OpenCode detector fires on decoded wire payload") {
		assert.Equal(t, "tc-1", obs.RowKey)
		assert.Equal(t, "task", obs.Title, "non-empty wire title takes precedence over description")
	}
}

func TestACP_WireDecode_ToolCallUpdateParsesRawOutput(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","rawOutput":{"metadata":{"sessionId":"child-sess-1"}}}`
	tcu := decodeToolCallUpdateUpdate(t, wire)
	assert.Equal(t, "tc-1", tcu.ToolCallID)
	assert.NotEmpty(t, tcu.RawOutput, "rawOutput must decode -- a tag mismatch leaves this empty")
	obs := openCodeSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs, "OpenCode final-update detector fires on decoded wire payload") {
		assert.Equal(t, "child-sess-1", obs.RowKey, "re-keyed to child session id from rawOutput")
		assert.True(t, obs.CloseRow)
	}
}

func TestACP_WireDecode_ReasonixSpawnShape(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-rx","title":"task","kind":"other","status":"in_progress","rawInput":{"description":"write tests","prompt":"write the test suite"}}`
	tc := decodeToolCallUpdate(t, wire)
	assert.NotEmpty(t, tc.RawInput)
	obs := reasonixSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Reasonix detector fires on decoded wire payload (no subagent_type)") {
		assert.Equal(t, "tc-rx", obs.RowKey)
		assert.Equal(t, "write tests", obs.Title)
	}
}

func TestACP_WireDecode_CursorTaskToolName(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"call-abc-0","title":"Task: build the feature","status":"in_progress","rawInput":{"_toolName":"task","prompt":"do it"}}`
	tc := decodeToolCallUpdate(t, wire)
	assert.NotEmpty(t, tc.RawInput)
	obs := cursorSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Cursor detector fires on decoded wire payload") {
		assert.Equal(t, "call-abc-0", obs.RowKey)
		assert.Equal(t, "build the feature", obs.Title, "Task: prefix stripped")
	}
}

func TestACP_WireDecode_GooseMetaParses(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-g","title":"delegate","status":"in_progress","_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`
	tc := decodeToolCallUpdate(t, wire)
	assert.NotEmpty(t, tc.Meta, "_meta must decode for Goose spawn detection")
	obs := gooseSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Goose delegate/summon detector fires on decoded wire payload") {
		assert.Equal(t, "tc-g", obs.RowKey)
	}
}

// TestACP_ApplySubagentObservation_SpawnRowKeyClosesBothRows verifies that a
// close observation carrying SpawnRowKey gives it a final status the ORIGINAL spawn row
// TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow
// verifies the rename path: a spawn opens a row under the toolCallId, then a
// final update re-keys it to the child session id via RenameFrom. One row
// tracks the lifecycle and ends final; the original spawn key is gone (not
// leaked as a separate Running row).
func TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow(t *testing.T) {
	sink := &testSink{}
	b := &acpBase{sink: sink}

	// Spawn opens a row under the toolCallId.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "call-123",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})
	require.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)

	// Final update renames call-123 -> sess-abc, then closes sess-abc.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:     "sess-abc",
		RenameFrom: "call-123",
		Status:     bgtask.StatusCompleted,
		CloseRow:   true,
	})

	// One row under the renamed key, final. The spawn key is gone.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "rename + close collapsed the lifecycle to one row")
	assert.Equal(t, "sess-abc", tasks[0].RowKey)
	assert.True(t, tasks[0].Status.IsFinished(), "renamed row is final")
}

// TestACP_ApplySubagentObservation_CloseOnlyModeSkipsUpsert verifies that an
// observation with Mode == acpModeCloseOnly closes an existing row WITHOUT
// first upserting one (the detector sets the Mode explicitly instead of relying
// on which fields happen to be empty).
func TestACP_ApplySubagentObservation_CloseOnlyModeSkipsUpsert(t *testing.T) {
	sink := &testSink{}
	b := &acpBase{sink: sink}

	// Open a row.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "call-1",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})

	// Close-only: closes the existing row, does NOT upsert.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:   "call-1",
		Status:   bgtask.StatusCompleted,
		CloseRow: true,
		Mode:     acpModeCloseOnly,
	})
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "close-only must not create a new row")
	assert.True(t, tasks[0].Status.IsFinished(), "existing row reached a final status")
}

// TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth verifies that an
// observation with Mode == acpModeUpsert (default) and CloseRow upserts THEN
// closes — the behavior when a final observation also carries descriptive
// fields (e.g. a Cursor background task with an activity line).
func TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth(t *testing.T) {
	sink := &testSink{}
	b := &acpBase{sink: sink}

	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:   "call-bg",
		Title:    "bg task",
		Activity: "background task",
		Status:   bgtask.StatusCompleted,
		CloseRow: true,
		// Mode defaults to acpModeUpsert (zero value).
	})
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "bg task", tasks[0].Title, "upsert carried the title")
	assert.True(t, tasks[0].Status.IsFinished(), "the close gave the row a final status")
}

// The spawn payload carries the prompt, but the child transcript that should
// open with it is created LATER, on a different observation (Goose learns its
// child only from the first forwarded tool request). applySubagentObservation
// holds the prompt across that gap.
func TestACPSubagentPrompt_HeldFromSpawnUntilTheChildExists(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink}

	// 1. Spawn: prompt recorded, no child yet.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "tc-1",
		Title:  "Goose subagent",
		Status: bgtask.StatusRunning,
		Prompt: "Review the diff.",
	})
	assert.Equal(t, "Review the diff.", b.subagentPrompts.peek("tc-1"))

	// 2. The observation that links the child spends it.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:        "tc-1",
		ChildAgentKey: "tc-1",
		Status:        bgtask.StatusRunning,
	})
	child, ok := sink.ChildSink("child-of-tc-1").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source)
	assert.JSONEq(t, `{"content":"Review the diff."}`, string(msgs[0].Content))
	assert.Zero(t, b.subagentPrompts.count(), "spent, so a later observation cannot repeat it")
}

// A provider that never links a child must not leak the remembered prompt: the
// row's close drops it.
func TestACPSubagentPrompt_DroppedWhenTheRowClosesWithNoChild(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink}
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "tc-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.count())

	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "tc-1", Status: bgtask.StatusCompleted, CloseRow: true, Mode: acpModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.count())
}

// A closing observation that RE-KEYS the row must drop the prompt under the
// key the spawn used, not under the new one. A provider that learns the child's
// stable id only on the closing update (OpenCode, Kilo) arrives here with
// RowKey = the new key and RenameFrom = the spawn key, so forgetting only
// RowKey deletes an entry that was never inserted and leaves the spawn's own to
// accumulate for the life of the agent process.
func TestACPSubagentPrompt_DroppedUnderTheSpawnKeyAfterARename(t *testing.T) {
	t.Parallel()

	b := &acpBase{sink: &testSink{}}
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "call-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.count())

	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:     "ses-child",
		RenameFrom: "call-1",
		Status:     bgtask.StatusCompleted,
		CloseRow:   true,
		Mode:       acpModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.count(), "the entry sits under the SPAWN key, not the renamed one")
}

// Replacing the session drops every unspent prompt: those rows belong to the
// outgoing session and no closing observation will ever arrive for them, so
// without this they are held for the life of the agent process.
func TestACPSubagentPrompt_ClearedWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()

	b := &acpBase{sink: &testSink{}}
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey: "tc-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.count())

	b.subagentPrompts.clear()
	assert.Zero(t, b.subagentPrompts.count())
}

// The spawn's own text wins: a later observation that re-reports a prompt for
// the same row must not overwrite it.
func TestACPSubagentPrompt_FirstWriteWins(t *testing.T) {
	t.Parallel()

	b := &acpBase{sink: &testSink{}}
	b.applySubagentObservation(&acpSubagentObservation{RowKey: "tc-1", Prompt: "first", Status: bgtask.StatusRunning})
	b.applySubagentObservation(&acpSubagentObservation{RowKey: "tc-1", Prompt: "second", Status: bgtask.StatusRunning})
	assert.Equal(t, "first", b.subagentPrompts.peek("tc-1"))
}

// Goose is the only ACP provider that opens a child transcript, so it is the
// only one whose detector reads the spawn's task text. It names that field
// `instructions` on the delegate tool (summon.rs).
func TestACPSubagentDetectors_CarryTheSpawnPrompt(t *testing.T) {
	t.Parallel()

	goose := gooseSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "tc-1",
		Title:      "delegate",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
		RawInput:   json.RawMessage(`{"source":"reviewer","instructions":"Review the diff."}`),
	})
	require.NotNil(t, goose)
	assert.Equal(t, "Review the diff.", goose.Prompt)
}

// A registry-only provider must leave Prompt EMPTY even when its spawn payload
// carries one. It never reports a ChildAgentKey, so takeSubagentPrompt never
// runs and the entry can only be dropped by the closing observation -- which
// Reasonix does not produce at all (it wires no update hook). Recording a
// prompt here holds a string for the life of the agent process that nothing
// will ever read.
func TestACPSubagentDetectors_RegistryOnlyProvidersRecordNoPrompt(t *testing.T) {
	t.Parallel()

	// OpenCode / Kilo: `prompt` on the task tool (tool/task.ts). Still the spawn
	// discriminator -- its PRESENCE is what identifies the shape.
	oc := openCodeSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "tc-2",
		RawInput:   json.RawMessage(`{"description":"scan","prompt":"Find the bug.","subagent_type":"general"}`),
	})
	require.NotNil(t, oc, "the prompt field still discriminates the spawn shape")
	assert.Empty(t, oc.Prompt)
	assert.Empty(t, oc.ChildAgentKey, "no child transcript means nothing can spend a prompt")

	rx := reasonixSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "tc-3",
		RawInput:   json.RawMessage(`{"description":"scan","prompt":"Trace it."}`),
	})
	require.NotNil(t, rx)
	assert.Empty(t, rx.Prompt)
	assert.Empty(t, rx.ChildAgentKey)
}

// A spawn payload with no task text must leave Prompt empty rather than
// inventing one, so PersistChildPrompt writes nothing.
func TestACPSubagentDetectors_EmptyPromptWhenTheSpawnCarriesNone(t *testing.T) {
	t.Parallel()

	goose := gooseSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "tc-1",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
	})
	require.NotNil(t, goose)
	assert.Empty(t, goose.Prompt)
}

// Kilo opens its spawn tool_call with `rawInput: {}` and only fills the spawn
// shape on the first IN-PROGRESS tool_call_update (verified against kilo 7.4.20
// over ACP). Detecting only on the tool_call left the spawn with no registry
// row at all, and the final update then closed a row that was never opened.
func TestACP_OpenCodeSpawnDetectedOnTheInProgressUpdate(t *testing.T) {
	t.Parallel()

	// The tool_call carries nothing to detect on.
	assert.Nil(t, openCodeSubagentFromToolCall(acpToolCallEnvelope{
		ToolCallID: "call-1", Title: "task", RawInput: json.RawMessage(`{}`),
	}))

	// The in-progress update carries the real shape.
	obs := openCodeSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-1",
		Status:     "in_progress",
		Title:      "Run echo kilo-done",
		RawInput:   json.RawMessage(`{"description":"Run echo kilo-done","prompt":"Run it.","subagent_type":"general"}`),
	})
	require.NotNil(t, obs)
	assert.Equal(t, "call-1", obs.RowKey)
	assert.Equal(t, "Run echo kilo-done", obs.Title)
	assert.Equal(t, bgtask.StatusRunning, obs.Status)
	// Registry-only: the prompt discriminates the shape but is not recorded.
	assert.Empty(t, obs.Prompt)
	assert.False(t, obs.CloseRow)
}

// A non-final update on a PLAIN tool must not open a subagent row.
func TestACP_OpenCodeNonSpawnUpdateIsIgnored(t *testing.T) {
	t.Parallel()

	assert.Nil(t, openCodeSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress", RawInput: json.RawMessage(`{"command":"ls"}`),
	}))
	assert.Nil(t, openCodeSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-1", Status: "in_progress",
	}))
}

// The final update still closes (and re-keys to the child session id), so
// adding the spawn arm above did not swallow the close.
func TestACP_OpenCodeFinalUpdateStillClosesAndRekeys(t *testing.T) {
	t.Parallel()

	obs := openCodeSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-1",
		Status:     "completed",
		RawInput:   json.RawMessage(`{"description":"d","prompt":"p","subagent_type":"general"}`),
		RawOutput:  json.RawMessage(`{"metadata":{"sessionId":"ses-child"}}`),
	})
	require.NotNil(t, obs)
	assert.True(t, obs.CloseRow)
	assert.Equal(t, "ses-child", obs.RowKey)
	assert.Equal(t, "call-1", obs.RenameFrom)
	assert.Equal(t, bgtask.StatusCompleted, obs.Status)
}

// --- A subagent spawn owns no span ---

func TestACP_ObservationIsSpawn(t *testing.T) {
	t.Parallel()

	assert.False(t, acpObservationIsSpawn(nil), "no observation, no spawn")
	assert.False(t, acpObservationIsSpawn(&acpSubagentObservation{}), "an empty row key names nothing")
	assert.False(t, acpObservationIsSpawn(&acpSubagentObservation{
		RowKey: "call-1", Spawns: false, Status: bgtask.StatusRunning,
	}), "a running row is not a spawn unless the provider says so")
	assert.False(t, acpObservationIsSpawn(&acpSubagentObservation{
		Spawns: true,
	}), "an observation that names no row must not take a span either")
	assert.True(t, acpObservationIsSpawn(&acpSubagentObservation{
		RowKey: "call-1", Spawns: true,
	}))
}

// Every ACP detector that recognizes its provider's spawn payload states it, and
// every other observation leaves the field false. Asserted over the real hooks,
// because a hand-built struct proves only what the test author believed.
func TestACP_OnlyTheSpawnDetectorsClaimASpawn(t *testing.T) {
	t.Parallel()

	spawns := []struct {
		provider string
		obs      *acpSubagentObservation
	}{
		{"cursor", cursorSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "c", Title: "Task: go", RawInput: json.RawMessage(`{"_toolName":"task"}`)})},
		{"opencode", openCodeSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "o", RawInput: json.RawMessage(`{"prompt":"go","subagent_type":"general"}`)})},
		{"goose", gooseSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "g", Title: "Goose subagent", RawInput: json.RawMessage(`{"instructions":"go"}`),
			Meta: json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`)})},
		{"reasonix", reasonixSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "r", Title: "task", RawInput: json.RawMessage(`{"description":"d","prompt":"go"}`)})},
	}
	for _, s := range spawns {
		if assert.NotNil(t, s.obs, "%s detector fires on its spawn payload", s.provider) {
			assert.True(t, s.obs.Spawns, "%s spawn observation claims the spawn", s.provider)
		}
	}

	// A progress or closing observation describes a row that already exists.
	notSpawns := []struct {
		what string
		obs  *acpSubagentObservation
	}{
		{"goose tool request", gooseSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
			ToolCallID: "g", Status: "in_progress",
			Meta: json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"s1","tool_call":{"name":"grep"}}}}}`)})},
		{"goose close", gooseSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
			ToolCallID: "g", Status: "completed"})},
		{"cursor close", cursorSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
			ToolCallID: "c", Status: "completed"}, false)},
		{"cursor background shell", cursorSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
			ToolCallID: "c", Title: "npm run dev", Status: "completed",
			RawOutput: json.RawMessage(`{"isBackground":true}`)}, false)},
		{"opencode close", openCodeSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
			ToolCallID: "o", Status: "completed"})},
	}
	for _, n := range notSpawns {
		if assert.NotNil(t, n.obs, "%s still produces an observation", n.what) {
			assert.False(t, n.obs.Spawns, "%s is not a spawn", n.what)
			assert.False(t, acpObservationIsSpawn(n.obs), "%s must not take a span", n.what)
		}
	}
}

// Goose's subagent_tool_request rides an in-progress update and reports on a
// subagent that ALREADY runs. The old inference read "upserts a running row" as
// "spawns a subagent", so it took the span off the tool call the update rode on.
func TestACP_GooseToolRequestDoesNotDiscardTheToolsSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink, subagentFromToolCallUpdate: gooseSubagentFromToolCallUpdate}

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-x","kind":"read","title":"Read"}`))
	require.Len(t, sink.OpenSpans(), 1, "an ordinary tool call opens a span")

	b.handleToolCallUpdate(json.RawMessage(
		`{"toolCallId":"call-x","status":"in_progress","_meta":{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"s1","tool_call":{"name":"grep"}}}}}}`))

	assert.Empty(t, sink.ClosedSpans(),
		"a tool-request observation reports progress on a running subagent, not a spawn")

	// The tool keeps its rail: a row persisted now still draws its column.
	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-y","kind":"read","title":"Read again"}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	require.Len(t, msgs[1].SpansOpenAtPersist, 1, "the first tool's rail survived the update")
	assert.Equal(t, "call-x", msgs[1].SpansOpenAtPersist[0].SpanID)
}

// Every ACP provider whose detector fires at the tool_call opens no span for a
// spawn, and still opens one for an ordinary tool call.
func TestACP_SpawnToolCallOpensNoSpan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		provider  string
		hook      func(acpToolCallEnvelope) *acpSubagentObservation
		spawnCall string
		plainCall string
	}{
		{
			provider:  "cursor",
			hook:      cursorSubagentFromToolCall,
			spawnCall: `{"toolCallId":"call-spawn","kind":"other","title":"Task: explore","rawInput":{"_toolName":"task"}}`,
			plainCall: `{"toolCallId":"call-plain","kind":"read","title":"Read","rawInput":{"_toolName":"read","path":"/tmp/a"}}`,
		},
		{
			provider:  "opencode",
			hook:      openCodeSubagentFromToolCall,
			spawnCall: `{"toolCallId":"call-spawn","kind":"other","title":"explore","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`,
			plainCall: `{"toolCallId":"call-plain","kind":"read","title":"Read","rawInput":{"filePath":"/tmp/a"}}`,
		},
		{
			provider:  "goose",
			hook:      gooseSubagentFromToolCall,
			spawnCall: `{"toolCallId":"call-spawn","kind":"other","title":"Goose subagent","rawInput":{"instructions":"go"},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`,
			plainCall: `{"toolCallId":"call-plain","kind":"read","title":"Read","_meta":{"goose":{"toolCall":{"toolName":"text_editor","extensionName":"developer"}}}}`,
		},
		{
			provider:  "reasonix",
			hook:      reasonixSubagentFromToolCall,
			spawnCall: `{"toolCallId":"call-spawn","kind":"other","title":"task","rawInput":{"description":"explore","prompt":"go"}}`,
			plainCall: `{"toolCallId":"call-plain","kind":"read","title":"Read","rawInput":{"path":"/tmp/a"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			b := &acpBase{sink: sink, subagentFromToolCall: tc.hook}

			b.handleToolCall(json.RawMessage(tc.spawnCall))
			assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
			assert.Empty(t, sink.ReservedColorSpans(), "and reserves no color")
			assert.Equal(t, "other", sink.GetSpanType("call-spawn"),
				"the span type is still recorded for the closing update")

			b.handleToolCall(json.RawMessage(tc.plainCall))
			open := sink.OpenSpans()
			require.Len(t, open, 1, "an ordinary tool call still opens a span")
			assert.Equal(t, "call-plain", open[0].SpanID)
			assert.Equal(t, []string{"call-plain"}, sink.ReservedColorSpans())

			// The spawn row was persisted before the plain call, so it drew no
			// rail; the plain row persists before its own span opens, so it
			// draws none either.
			msgs := sink.Messages()
			require.Len(t, msgs, 2)
			assert.Empty(t, msgs[0].SpansOpenAtPersist)
			assert.Equal(t, "call-spawn", msgs[0].SpanID)
		})
	}
}

// Kilo opens its spawn with `rawInput: {}` and reveals the spawn shape only on
// the first in-progress update, so the span is already open by then. The update
// gives it back with CloseSpan, which frees the column but keeps the recorded
// span type that the closing update reads back.
func TestACP_KiloLateSpawnGivesItsSpanBack(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{
		sink:                       sink,
		subagentFromToolCall:       openCodeSubagentFromToolCall,
		subagentFromToolCallUpdate: openCodeSubagentFromToolCallUpdate,
	}

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-spawn","kind":"other","title":"new_task","rawInput":{}}`))
	require.Len(t, sink.OpenSpans(), 1, "the empty rawInput hides the spawn, so a span opens")

	b.handleToolCallUpdate(json.RawMessage(
		`{"toolCallId":"call-spawn","status":"in_progress","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`))

	assert.Equal(t, []string{"call-spawn"}, sink.ClosedSpans(),
		"the span is given back although the subagent keeps running")
	assert.Equal(t, "other", sink.GetSpanType("call-spawn"),
		"and the recorded type survives, so the closing update reads it back")

	// The detector re-runs on every update, and Kilo keeps echoing its rawInput,
	// so it re-reports the spawn. The release must not repeat: each repeat would
	// take the tracker mutex and re-scan the active set for a span already gone.
	b.handleToolCallUpdate(json.RawMessage(
		`{"toolCallId":"call-spawn","status":"in_progress","rawInput":{"description":"explore","prompt":"go","subagent_type":"general"}}`))
	assert.Equal(t, []string{"call-spawn"}, sink.ClosedSpans(), "released once, not once per update")

	// A tool call that starts after the discard sits at column 0, not column 1.
	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-plain","kind":"read","title":"Read"}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn rail is gone")

	// The closing update persists the recorded kind, not the "tool_call"
	// fallback -- the span type outlives the close that freed the column.
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-spawn","status":"completed","rawOutput":{"metadata":{"sessionId":"ses-child"}}}`))
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

	sink := &testSink{}
	b := &acpBase{
		sink:                       sink,
		subagentFromToolCall:       openCodeSubagentFromToolCall,
		subagentFromToolCallUpdate: openCodeSubagentFromToolCallUpdate,
	}

	// A real shell payload: a command, and none of the spawn discriminators.
	shell := `"rawInput":{"command":"npm run dev"}`
	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"bash",` + shell + `}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1, "the detector does not fire, so the shell keeps its span")
	assert.Equal(t, "call-sh", open[0].SpanID)
	assert.Equal(t, []string{"call-sh"}, sink.ReservedColorSpans())

	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-sh","status":"in_progress",` + shell + `}`))
	require.Len(t, sink.OpenSpans(), 1, "and the update leaves it alone")

	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-sh","status":"completed",` + shell + `}`))
	assert.Equal(t, []string{"call-sh"}, sink.ClosedSpans(), "it closes normally")
}

// A tool_call that arrives already final never opened a span, so the spawn
// detector must not disturb it -- it only feeds the registry.
func TestACP_FinalToolCallSpawnStillFeedsTheRegistry(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink, subagentFromToolCall: openCodeSubagentFromToolCall}

	b.handleToolCall(json.RawMessage(
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

// newCursorTestAgent wires a CursorCLIAgent exactly as StartCursorCLI does, so
// the two hooks share the per-agent note about which tool calls are the `task`
// tool. Building a bare acpBase from the package-level functions instead would
// drop that note and test a wiring production never uses.
func newCursorTestAgent(sink OutputSink) *CursorCLIAgent {
	a := &CursorCLIAgent{}
	a.sink = sink
	a.subagentFromToolCall = a.spawnObservation
	a.subagentFromToolCallUpdate = a.finishedObservation
	a.clearProviderState = a.clearTaskToolCalls
	return a
}

// Cursor's closing hook fires for EVERY finished tool call. Its close-only
// observation must not be read as a spawn, or an ordinary tool would lose the
// span it is about to close.
func TestACP_CursorClosingUpdateDoesNotDiscardAPlainToolSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &newCursorTestAgent(sink).acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-read","kind":"read","title":"Read","rawInput":{"_toolName":"read"}}`))
	require.Len(t, sink.OpenSpans(), 1)

	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-read","status":"completed"}`))

	// Closed exactly ONCE, by the closing arm. A close-only observation read as
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

	sink := &testSink{}
	b := &newCursorTestAgent(sink).acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"npm run dev","rawInput":{"_toolName":"shell","command":"npm run dev"}}`))
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-sh","status":"completed","title":"npm run dev","rawInput":{"_toolName":"shell"},"rawOutput":{"isBackground":true}}`))

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

	sink := &testSink{}
	b := &newCursorTestAgent(sink).acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: build the feature","rawInput":{"_toolName":"task","prompt":"do it"}}`))
	// No rawInput on the close, and isBackground true.
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"Task: build the feature","rawOutput":{"isBackground":true}}`))

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

	sink := &testSink{}
	b := &newCursorTestAgent(sink).acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-sh","kind":"execute","title":"npm run dev","rawInput":{"_toolName":"shell","command":"npm run dev"}}`))
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-sh","status":"completed","title":"npm run dev","rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
	assert.Equal(t, "npm run dev", tasks[0].Title)
}

// The note is per tool call and is dropped when the call ends, so a later tool
// call that reuses nothing of it is classified on its own evidence.
func TestACP_CursorTaskNoteDoesNotOutliveItsToolCall(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newCursorTestAgent(sink)
	b := &a.acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-1","status":"completed","rawOutput":{"isBackground":true}}`))

	a.mu.Lock()
	remaining := len(a.taskToolCalls)
	a.mu.Unlock()
	assert.Zero(t, remaining, "the note is dropped when the call ends")
}

// A tool_call that arrives ALREADY final is applied and returns, so no closing
// update ever follows to drop a note. Writing one there left it for the life of
// the agent -- a session/load replay of a finished task is exactly that shape --
// and a later call that reused the id would read it and file a backgrounded
// shell as a subagent.
func TestACP_CursorAlreadyFinalTaskCallLeavesNoNote(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newCursorTestAgent(sink)
	b := &a.acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-task","kind":"other","status":"completed","title":"Task: go","rawInput":{"_toolName":"task"}}`))

	require.Len(t, sink.BackgroundTasks(), 1, "the registry row is still upserted")
	a.mu.Lock()
	remaining := len(a.taskToolCalls)
	a.mu.Unlock()
	assert.Zero(t, remaining, "an already-final call has no later update to read a note")

	// A later backgrounded SHELL that reuses the id is still a shell.
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"npm run dev","rawOutput":{"isBackground":true}}`))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind, "no stale note reclassified it")
}

// ClearContext replaces the session, so every note is keyed by a tool call that
// will never report again. acpBase clears its own subagentPrompts there for the
// same reason.
func TestACP_CursorClearProviderStateDropsTheNotes(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newCursorTestAgent(sink)

	a.handleToolCall(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))
	a.mu.Lock()
	before := len(a.taskToolCalls)
	a.mu.Unlock()
	require.Equal(t, 1, before, "the in-flight task left a note")

	require.NotNil(t, a.clearProviderState, "Cursor registers the ClearContext hook")
	a.clearProviderState()

	a.mu.Lock()
	after := len(a.taskToolCalls)
	a.mu.Unlock()
	assert.Zero(t, after, "the outgoing session's notes are gone")
}

// The task tool's row survives its own closing update: still a subagent, still
// carrying the trimmed title the spawn gave it.
func TestACP_CursorTaskRowStaysASubagentThroughItsClose(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &newCursorTestAgent(sink).acpBase

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: build the feature","rawInput":{"_toolName":"task","prompt":"do it"}}`))
	b.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-task","status":"completed","title":"Task: build the feature","rawInput":{"_toolName":"task"},"rawOutput":{"isBackground":true}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
	assert.Equal(t, "build the feature", tasks[0].Title,
		"the closing update must not overwrite the spawn's trimmed title")
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

// The shell case now lives in TestACP_OnlyTheSpawnDetectorsClaimASpawn, which
// asserts it over the REAL Cursor hook: the backgrounded-shell arm never claims
// a spawn. The kind of a row no longer decides, so a struct built by hand can no
// longer state the case.
