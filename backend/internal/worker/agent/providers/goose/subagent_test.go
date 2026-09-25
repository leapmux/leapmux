package goose

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
	tcu := acp.ToolCallUpdateEnvelope{
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

func TestACP_GooseBackgroundLaunchIsNotAReport(t *testing.T) {
	t.Parallel()
	var content []acp.ToolCallBlock
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"content","content":{"type":"text","text":"Delegation started"}}]`), &content))
	obs := gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "delegate-call",
		Status:     "completed",
		RawInput:   json.RawMessage(`{"async":true}`),
		Content:    content,
	})
	require.NotNil(t, obs)
	assert.Empty(t, obs.Report.Text)
}

// A request that states no tool call is still a request, so the row opens with the
// neutral activity line rather than with "tool: ".
//
// The worker indexes the `tool_call` key with contracts.GooseSubagentRequestToolCall,
// because a Go struct tag cannot hold a constant. The last case below is what a stale
// hand-written tag would produce after Goose renamed the key.
func TestACP_GooseSubagentFromToolCallUpdate_RequestWithNoToolCall(t *testing.T) {
	for name, data := range map[string]string{
		"no tool_call at all":        `{"type":"subagent_tool_request","subagent_id":"g-sub-1"}`,
		"a tool_call with no name":   `{"type":"subagent_tool_request","tool_call":{}}`,
		"the name under another key": `{"type":"subagent_tool_request","toolCall":{"name":"Read"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			obs := gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
				ToolCallID: "tc-goose",
				Status:     "in_progress",
				Meta:       json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":` + data + `}}}`),
			})
			if assert.NotNil(t, obs) {
				assert.Equal(t, "tool request", obs.Activity)
				assert.Equal(t, "tc-goose", obs.RowKey)
				assert.Equal(t, bgtask.StatusRunning, obs.Status)
			}
		})
	}
}

func TestACP_GooseSubagentFromToolCallUpdate_NonSubagentReturnsNil(t *testing.T) {
	meta := json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"other"}}}}`)
	tcu := acp.ToolCallUpdateEnvelope{ToolCallID: "tc-x", Meta: meta}
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
	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))

	// Spawn tool_call opens a row under the spawn toolCallId.
	spawnObs := gooseSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-spawn",
		Title:      "Goose subagent",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
	})
	require.NotNil(t, spawnObs)
	b.ApplySubagentObservation(spawnObs)

	// A tool-request update carries a DIFFERENT per-request subagent_id; the fix
	// keys the row/link off the spawn toolCallId so no second row opens.
	reqMeta := json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"g-sub-1","tool_call":{"name":"Read"}}}}}`)
	reqObs := gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-spawn",
		Status:     "in_progress",
		Meta:       reqMeta,
	})
	require.NotNil(t, reqObs)
	b.ApplySubagentObservation(reqObs)

	// Exactly one row, keyed by the spawn toolCallId (not g-sub-1).
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "spawn + tool-request produce one row keyed by the spawn toolCallId")
	assert.Equal(t, "call-spawn", tasks[0].RowKey)

	// Final close (which knows only the spawn toolCallId) reaches the row.
	closeObs := gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-spawn",
		Status:     "completed",
	})
	require.NotNil(t, closeObs)
	b.ApplySubagentObservation(closeObs)

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
	payload := gooseSubagentToolRequestPayload(acp.ToolCallUpdateEnvelope{
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
	tcu := acp.ToolCallUpdateEnvelope{ToolCallID: "tc-x", Status: "completed"}
	obs := gooseSubagentFromToolCallUpdate(tcu)
	if assert.NotNil(t, obs, "final update closes the registry row") {
		assert.True(t, obs.CloseRow)
		assert.Equal(t, bgtask.StatusCompleted, obs.Status)
		assert.Equal(t, "tc-x", obs.RowKey)
	}
}

func TestACP_GooseSubagentFromToolCall_DelegateSummonDetectsSpawn(t *testing.T) {
	meta := json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`)
	tc := acp.ToolCallEnvelope{
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
	tc := acp.ToolCallEnvelope{ToolCallID: "tc-x", Meta: meta}
	assert.Nil(t, gooseSubagentFromToolCall(tc))
}

func TestACP_GooseSubagentFromToolCall_NoMetaReturnsNil(t *testing.T) {
	tc := acp.ToolCallEnvelope{ToolCallID: "tc-x"}
	assert.Nil(t, gooseSubagentFromToolCall(tc))
}

func TestACP_WireDecode_GooseMetaParses(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-g","title":"delegate","status":"in_progress","_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`
	var tc acp.ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tc))
	assert.NotEmpty(t, tc.Meta, "_meta must decode for Goose spawn detection")
	obs := gooseSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Goose delegate/summon detector fires on decoded wire payload") {
		assert.Equal(t, "tc-g", obs.RowKey)
	}
}

// Each ACP provider that exposes a spawn prompt carries it into the child
// transcript. Goose spells that field `instructions` on the delegate tool.
func TestACPSubagentDetectors_CarryTheSpawnPrompt(t *testing.T) {
	t.Parallel()

	goose := gooseSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "tc-1",
		Title:      "delegate",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
		RawInput:   json.RawMessage(`{"source":"reviewer","instructions":"Review the diff."}`),
	})
	require.NotNil(t, goose)
	assert.Equal(t, "Review the diff.", goose.Prompt)
	assert.Equal(t, "tc-1", goose.ChildAgentKey)
}

// A spawn payload with no task text must leave Prompt empty rather than
// inventing one, so PersistChildPrompt writes nothing.
func TestACPSubagentDetectors_EmptyPromptWhenTheSpawnCarriesNone(t *testing.T) {
	t.Parallel()

	goose := gooseSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "tc-1",
		Meta:       json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`),
	})
	require.NotNil(t, goose)
	assert.Empty(t, goose.Prompt)
}

func TestACP_GoosePersistsTheFinalReportAfterItsToolRequests(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: gooseSubagentFromToolCall, SubagentFromToolCallUpdate: gooseSubagentFromToolCallUpdate}
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"delegate-call","title":"Review","status":"pending","rawInput":{"instructions":"Review the diff."},"_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"delegate-call","status":"completed","content":[{"type":"content","content":{"type":"text","text":"No defects found."}}]}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Review the diff."}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "No defects found.", reports[0]["text"])
}

// Goose's subagent_tool_request rides an in-progress update and reports on a
// subagent that ALREADY runs. The old inference read "upserts a running row" as
// "spawns a subagent", so it took the span off the tool call the update rode on.
func TestACP_GooseToolRequestDoesNotDiscardTheToolsSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCallUpdate: gooseSubagentFromToolCallUpdate}

	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-x","kind":"read","title":"Read"}`))
	require.Len(t, sink.OpenSpans(), 1, "an ordinary tool call opens a span")

	b.HandleToolCallUpdateForTest(json.RawMessage(
		`{"toolCallId":"call-x","status":"in_progress","_meta":{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"s1","tool_call":{"name":"grep"}}}}}}`))

	assert.Empty(t, sink.ClosedSpans(),
		"a tool-request observation reports progress on a running subagent, not a spawn")

	// The tool keeps its rail: a row persisted now still draws its column.
	b.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-y","kind":"read","title":"Read again"}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	require.Len(t, msgs[1].SpansOpenAtPersist, 1, "the first tool's rail survived the update")
	assert.Equal(t, "call-x", msgs[1].SpansOpenAtPersist[0].SpanID)
}

// TestGooseSubagentDetectorsClaimOnlyTheSpawn pins that the detector claims its own spawn payload, and no ordinary
// tool call. TestACP_SpawnToolCallOpensNoSpan pins what the base does with a claim.
func TestGooseSubagentDetectorsClaimOnlyTheSpawn(t *testing.T) {
	t.Parallel()

	spawn := gooseSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "g", Title: "Goose subagent", RawInput: json.RawMessage(`{"instructions":"go"}`),
		Meta: json.RawMessage(`{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}`)})
	if assert.NotNil(t, spawn, "the detector fires on its spawn payload") {
		assert.True(t, spawn.Spawns, "the spawn observation claims the spawn")
		assert.True(t, acp.ObservationIsSpawn(spawn), "the spawn takes no span")
	}
	assert.Nil(t, gooseSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-plain", Kind: "read", Title: "Read",
		Meta: json.RawMessage(`{"goose":{"toolCall":{"toolName":"text_editor","extensionName":"developer"}}}`)}), "an ordinary tool call is no subagent")
	// A progress or closing observation describes a row that already exists.
	for what, obs := range map[string]*acp.SubagentObservation{
		"tool request": gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "g", Status: "in_progress",
			Meta: json.RawMessage(`{"toolNotification":{"type":"message","params":{"data":{"type":"subagent_tool_request","subagent_id":"s1","tool_call":{"name":"grep"}}}}}`)}),
		"close": gooseSubagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: "g", Status: "completed"}),
	} {
		if assert.NotNil(t, obs, "%s still produces an observation", what) {
			assert.False(t, obs.Spawns, "%s is not a spawn", what)
			assert.False(t, acp.ObservationIsSpawn(obs), "%s must not take a span", what)
		}
	}
}
