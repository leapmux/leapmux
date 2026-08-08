package agent

import (
	"encoding/json"
	"testing"

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

func TestACP_SubagentFromToolCallUpdate_TerminalCloses(t *testing.T) {
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
// where the terminal update carries no metadata.sessionId: the close keys off
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

func TestACP_TerminalStatusMap(t *testing.T) {
	assert.Equal(t, bgtask.StatusCompleted, acpTerminalStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, acpTerminalStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, acpTerminalStatus("cancelled"))
	assert.Equal(t, bgtask.StatusStopped, acpTerminalStatus("unknown"))
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
		// The registry row, the EnsureChildAgent linkage, AND the terminal close
		// all key off the SPAWN toolCallId. ChildAgentKey must match RowKey, or
		// EnsureChildAgent would open a second row keyed by subagent_id that the
		// terminal close (which knows only toolCallId) never reaches.
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
// Goose spawn tool_call followed by tool-request updates and a terminal close
// collapse to exactly ONE registry row keyed by the spawn toolCallId. Before the
// fix, EnsureChildAgent was called with the per-request subagent_id (different
// from the spawn toolCallId), opening a second row keyed by subagent_id that the
// terminal close (which knows only toolCallId) never reached -- an orphaned
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

	// Terminal close (which knows only the spawn toolCallId) reaches the row.
	closeObs := gooseSubagentFromToolCallUpdate(acpToolCallUpdateEnvelope{
		ToolCallID: "call-spawn",
		Status:     "completed",
	})
	require.NotNil(t, closeObs)
	b.applySubagentObservation(closeObs)

	tasks = sink.BackgroundTasks()
	require.Len(t, tasks, 1, "still one row after close")
	assert.True(t, tasks[0].Status.IsTerminal(), "terminal close reached the row")
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

func TestACP_GooseSubagentFromToolCallUpdate_TerminalClosesRow(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{ToolCallID: "tc-x", Status: "completed"}
	obs := gooseSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs, "terminal update closes the registry row") {
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

func TestACP_CursorSubagentFromToolCallUpdate_TerminalCloses(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "call-abc-0",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"durationMs":1200,"isBackground":false}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusCompleted, obs.Status)
		assert.Empty(t, obs.Activity, "isBackground false -> no activity note")
	}
}

func TestACP_CursorSubagentFromToolCallUpdate_BackgroundNotesActivity(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{
		ToolCallID: "call-bg-0",
		Status:     "completed",
		RawOutput:  json.RawMessage(`{"durationMs":5000,"isBackground":true}`),
	}
	obs := cursorSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "background task", obs.Activity)
	}
}

func TestACP_CursorSubagentFromToolCallUpdate_InProgressReturnsNil(t *testing.T) {
	tcu := acpToolCallUpdateEnvelope{ToolCallID: "tc-c2", Status: "in_progress"}
	assert.Nil(t, cursorSubagentFromToolCallUpdate(tcu))
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
	if assert.NotNil(t, obs, "OpenCode terminal detector fires on decoded wire payload") {
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
// close observation carrying SpawnRowKey terminalizes the ORIGINAL spawn row
// TestACP_ApplySubagentObservation_RenameFromCollapsesToOneTerminalRow
// verifies the rename path: a spawn opens a row under the toolCallId, then a
// terminal update re-keys it to the child session id via RenameFrom. One row
// tracks the lifecycle and ends terminal; the original spawn key is gone (not
// leaked as a separate Running row).
func TestACP_ApplySubagentObservation_RenameFromCollapsesToOneTerminalRow(t *testing.T) {
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

	// Terminal update renames call-123 -> sess-abc, then closes sess-abc.
	b.applySubagentObservation(&acpSubagentObservation{
		RowKey:     "sess-abc",
		RenameFrom: "call-123",
		Status:     bgtask.StatusCompleted,
		CloseRow:   true,
	})

	// One row under the renamed key, terminal. The spawn key is gone.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "rename + close collapsed the lifecycle to one row")
	assert.Equal(t, "sess-abc", tasks[0].RowKey)
	assert.True(t, tasks[0].Status.IsTerminal(), "renamed row is terminal")
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
	assert.True(t, tasks[0].Status.IsTerminal(), "existing row is terminalized")
}

// TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth verifies that an
// observation with Mode == acpModeUpsert (default) and CloseRow upserts THEN
// closes — the behavior when a terminal observation also carries descriptive
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
	assert.True(t, tasks[0].Status.IsTerminal(), "close terminalized the row")
}
