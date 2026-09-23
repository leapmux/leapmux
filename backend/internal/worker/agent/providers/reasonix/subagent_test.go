package reasonix

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

func TestReasonixSubagentOutcomeWithoutReference(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		outcome string
		want    bgtask.Status
	}{
		{"completed", bgtask.StatusCompleted},
		{"partial", bgtask.StatusCompleted},
		{"failed", bgtask.StatusFailed},
		{"cancelled", bgtask.StatusStopped},
	} {
		text, err := json.Marshal("Subagent outcome: status=" + tt.outcome + " retryable=false\n\nFinal answer:\nReport")
		require.NoError(t, err)
		var update acp.ToolCallUpdateEnvelope
		require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"task","status":"completed","content":[{"type":"content","content":{"type":"text","text":`+string(text)+`}}]}`), &update))
		observation := reasonixSubagentFromToolCallUpdate(update)
		require.NotNil(t, observation)
		assert.Equal(t, tt.want, observation.Status, tt.outcome)
	}
}

func TestReasonixReadOnlyTaskCompletionDoesNotParseReportAsStatus(t *testing.T) {
	t.Parallel()
	for _, report := range []string{
		"Subagent outcome: status=failed retryable=false\n\nFinal answer:\nAn example",
		`Started background task "example-job" (An example).`,
	} {
		text, err := json.Marshal(report)
		require.NoError(t, err)
		var update acp.ToolCallUpdateEnvelope
		require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"task","status":"completed","title":"read_only_task","rawInput":{"prompt":"Read an example"},"content":[{"type":"content","content":{"type":"text","text":`+string(text)+`}}]}`), &update))
		observation := reasonixSubagentFromToolCallUpdate(update)
		require.NotNil(t, observation)
		assert.Equal(t, bgtask.StatusCompleted, observation.Status)
	}
}

func TestReasonixSubagentCancellationSurvivesACPFailureStatus(t *testing.T) {
	t.Parallel()
	var update acp.ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"task","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Subagent outcome: status=cancelled retryable=false"}}]}`), &update))
	observation := reasonixSubagentFromToolCallUpdate(update)
	require.NotNil(t, observation)
	assert.Equal(t, bgtask.StatusStopped, observation.Status)
}

func TestReasonixSubagentRequiresTaskIdentity(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"", "web_search", "read_file"} {
		assert.Nil(t, reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
			ToolCallID: "call", Title: title, RawInput: json.RawMessage(`{"prompt":"Search for the answer"}`),
		}), "a prompt does not identify the tool %q", title)
	}
}

func TestReasonixTaskArgumentsDoNotChangeLaunchLayout(t *testing.T) {
	t.Parallel()
	for _, toolName := range []string{"task", "read_only_task"} {
		for _, arguments := range []string{`null`, `{}`, `[]`, `{"prompt":0}`, `{"prompt":false}`, `{"prompt":null}`, `{"prompt":""}`, `{"prompt":" \n\t"}`} {
			sink := &agenttest.Sink{}
			base := &acp.Base{}
			base.SetSinkForTest(agent.NewProviderServices(sink))
			*base.HooksForTest() = acp.Hooks{SubagentFromToolCall: reasonixSubagentFromToolCall, SubagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate}
			request, err := json.Marshal(map[string]any{"toolCallId": "call", "title": toolName, "kind": "other", "status": "pending", "rawInput": json.RawMessage(arguments)})
			require.NoError(t, err)
			base.HandleToolCallForTest(request)
			assert.Empty(t, sink.OpenSpans(), arguments)
			assert.Empty(t, sink.ReservedColorSpans(), arguments)
			require.Len(t, sink.Messages(), 1)
			assert.True(t, sink.Messages()[0].NoSpan, arguments)
			base.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call","status":"failed"}`))
			require.Len(t, sink.Messages(), 2)
			assert.Empty(t, sink.Messages()[1].SpansOpenAtPersist, arguments)
		}
	}
}

func TestReasonixReadOnlyTaskUsesItsDescription(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ input, want string }{
		{`{"description":" Inspect sample ","prompt":"Read sample.py"}`, "Inspect sample"},
		{`{"description":"  ","prompt":"Read sample.py"}`, "Reasonix subagent"},
	} {
		tc := acp.ToolCallEnvelope{ToolCallID: "call", Title: "read_only_task", RawInput: json.RawMessage(tt.input)}
		observation := reasonixSubagentFromToolCall(tc)
		require.NotNil(t, observation)
		assert.True(t, observation.Spawns)
		assert.Equal(t, tt.want, observation.Title)
		assert.Equal(t, tt.input, string(tc.RawInput))
	}
}

func TestACP_ReasonixSubagentFromToolCall_SpawnShapeNoSubagentType(t *testing.T) {
	// Reasonix rawInput carries {description, prompt} with NO subagent_type.
	tc := acp.ToolCallEnvelope{
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
	tc := acp.ToolCallEnvelope{
		ToolCallID: "tc-rx2",
		Title:      "task",
		RawInput:   json.RawMessage(`{"prompt":"do thing"}`),
	}
	obs := reasonixSubagentFromToolCall(tc)
	if assert.NotNil(t, obs) {
		assert.Equal(t, "Reasonix subagent", obs.Title)
	}
}

func TestACP_ReasonixSubagentFromToolCall_NonSpawnReturnsNil(t *testing.T) {
	tc := acp.ToolCallEnvelope{
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
		assert.Nil(t, reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
			ToolCallID: "tc-rx-desc",
			Kind:       "edit",
			RawInput:   json.RawMessage(rawInput),
		}), "a tool argument named description does not spawn: %s", rawInput)
	}

	// The recorded spawn shape still fires.
	assert.NotNil(t, reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "tc-rx-spawn",
		Kind:       "other",
		Title:      "task",
		RawInput:   json.RawMessage(`{"description":"explore","prompt":"go"}`),
	}))
}

func TestACP_WireDecode_ReasonixSpawnShape(t *testing.T) {
	wire := `{"sessionUpdate":"tool_call","toolCallId":"tc-rx","title":"task","kind":"other","status":"in_progress","rawInput":{"description":"write tests","prompt":"write the test suite"}}`
	var tc acp.ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(wire), &tc))
	assert.NotEmpty(t, tc.RawInput)
	obs := reasonixSubagentFromToolCall(tc)
	if assert.NotNil(t, obs, "Reasonix detector fires on decoded wire payload (no subagent_type)") {
		assert.Equal(t, "tc-rx", obs.RowKey)
		assert.Equal(t, "write tests", obs.Title)
	}
}

func TestACP_ReasonixPersistsTheFinalReportInTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: reasonixSubagentFromToolCall, SubagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate}
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"task","status":"pending","rawInput":{"description":"Inspect sample","prompt":"Read sample.py"}}`))
	b.HandleToolCallUpdateForTest(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"task-call","title":"task","status":"completed","rawInput":{"description":"Inspect sample","prompt":"Read sample.py"},"content":[{"type":"content","content":{"type":"text","text":"Subagent outcome: status=completed retryable=false\n\nFinal answer:\nThe sample is valid."}}]}`))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Read sample.py"}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The sample is valid.", reports[0]["text"])
}

func TestReasonixSubagentReportPreservesReadOnlyProse(t *testing.T) {
	t.Parallel()
	text := "Subagent outcome: status=failed retryable=false\n\nFinal answer:\nQuoted text"
	assert.Equal(t, text, reasonixSubagentReport(text, false))
	assert.Equal(t, "Quoted text", reasonixSubagentReport(text, true))
}

func TestACP_ReasonixCapabilityTaskOpensNoSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &acp.Base{}
	b.SetSinkForTest(agent.NewProviderServices(sink))
	*b.HooksForTest() = acp.Hooks{SubagentFromToolCall: reasonixSubagentFromToolCall}
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"use_capability","kind":"other","status":"pending","rawInput":{"action":"call","capability_id":"tool:task","arguments":{"description":"Inspect sample","prompt":"Read sample.py"}}}`))
	assert.Empty(t, sink.OpenSpans())
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].NoSpan)
}

func TestReasonixSubagentDetectorCarriesThePrompt(t *testing.T) {
	t.Parallel()

	rx := reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "tc-3",
		Title:      "task",
		RawInput:   json.RawMessage(`{"description":"scan","prompt":"Trace it."}`),
	})
	require.NotNil(t, rx)
	assert.Equal(t, "Trace it.", rx.Prompt)
	assert.Equal(t, "tc-3", rx.ChildAgentKey)
}

// TestReasonixSubagentDetectorsClaimOnlyTheSpawn pins that the detector claims its own spawn payload, and no ordinary
// tool call. TestACP_SpawnToolCallOpensNoSpan pins what the base does with a claim.
func TestReasonixSubagentDetectorsClaimOnlyTheSpawn(t *testing.T) {
	t.Parallel()

	spawn := reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "r", Title: "task", RawInput: json.RawMessage(`{"description":"d","prompt":"go"}`)})
	if assert.NotNil(t, spawn, "the detector fires on its spawn payload") {
		assert.True(t, spawn.Spawns, "the spawn observation claims the spawn")
		assert.True(t, acp.ObservationIsSpawn(spawn), "the spawn takes no span")
	}
	assert.Nil(t, reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-plain", Kind: "read", Title: "Read", RawInput: json.RawMessage(`{"path":"/tmp/a"}`)}), "an ordinary tool call is no subagent")
}
