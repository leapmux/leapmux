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
		{"completed", bgtask.StatusSucceeded},
		{"partial", bgtask.StatusFailed},
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
		assert.Equal(t, bgtask.StatusSucceeded, observation.Status)
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

func reasonixTaskObservationFixture() (*acp.Base, *agenttest.Sink) {
	sink := &agenttest.Sink{}
	base := &acp.Base{}
	base.SetSinkForTest(agent.NewProviderServices(sink))
	*base.HooksForTest() = acp.Hooks{
		SubagentFromToolCall:       reasonixSubagentFromToolCall,
		SubagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate,
	}
	return base, sink
}

func TestReasonixBackgroundLaunchKeepsTheChildOutcomeUnknown(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		tool        string
		wrapped     bool
		background  bool
		outerStatus string
		text        string
		want        bgtask.Status
		report      string
	}{
		{name: "direct native launch", tool: "task", background: true, outerStatus: "completed", text: "Started background task \"job-1\" (Inspect sample).\nSubagent reference: sa_background\nIt runs across turns.", want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "capability native launch", tool: "task", wrapped: true, background: true, outerStatus: "completed", text: "Started background task \"job-1\" (Inspect sample).\nSubagent reference: sa_background\nIt runs across turns.", want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "notice without repeated background argument", tool: "task", outerStatus: "completed", text: "Started background task \"job-1\" (Inspect sample).", want: bgtask.StatusEndedWithUnknownOutcome},
		{name: "confirmed child outcome", tool: "task", background: true, outerStatus: "completed", text: "Subagent outcome: status=completed retryable=false\n\nFinal answer:\nThe sample is valid.", want: bgtask.StatusSucceeded, report: "The sample is valid."},
		{name: "failed launch", tool: "task", background: true, outerStatus: "failed", text: "Cannot start the native child.", want: bgtask.StatusFailed, report: "Cannot start the native child."},
		{name: "read-only quoted launch", tool: "read_only_task", outerStatus: "completed", text: "Started background task \"example-job\" (An example).", want: bgtask.StatusSucceeded, report: "Started background task \"example-job\" (An example)."},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, sink := reasonixTaskObservationFixture()
			arguments := map[string]any{"description": "Inspect sample", "prompt": "Read sample.py"}
			if test.background {
				arguments["run_in_background"] = true
			}
			title := test.tool
			var input any = arguments
			if test.wrapped {
				title = "use_capability"
				input = map[string]any{"action": "call", "capability_id": "tool:" + test.tool, "arguments": arguments}
			}
			opening, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": "native-task", "title": title,
				"kind": "other", "status": "pending", "rawInput": input,
			})
			require.NoError(t, err)
			base.HandleToolCallForTest(opening)
			before := sink.BackgroundTasks()
			require.Len(t, before, 1)
			assert.Equal(t, bgtask.KindSubagent, before[0].Kind)
			assert.Equal(t, bgtask.StatusRunning, before[0].Status)
			require.NotEmpty(t, before[0].ChildAgentID)
			update, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "native-task", "status": test.outerStatus,
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": test.text}}},
			})
			require.NoError(t, err)
			original := append([]byte(nil), update...)
			base.HandleToolCallUpdateForTest(update)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, test.want, rows[0].Status)
			assert.Equal(t, before[0].ChildAgentID, rows[0].ChildAgentID)
			assert.True(t, rows[0].Status.IsFinished())
			assert.False(t, rows[0].Status.IsWorking())
			assert.Equal(t, original, update)
			child := sink.Child(rows[0].ChildAgentID)
			require.NotNil(t, child)
			reports := child.LeapMuxNotifications()
			if test.report == "" {
				assert.Empty(t, reports, "a launch acknowledgement supplies no child result")
			} else {
				require.Len(t, reports, 1)
				assert.Equal(t, test.report, reports[0]["text"])
			}
		})
	}
}

func TestReasonixPartialOutcomeKeepsNativeFailureAndReport(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"completion_uncertain", "final_readiness", "review_unavailable", "max_steps", "incomplete_read"} {
		for _, outerStatus := range []string{"completed", "failed"} {
			t.Run(code+"/"+outerStatus, func(t *testing.T) {
				base, sink := reasonixTaskObservationFixture()
				opening := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"partial-task","title":"task","kind":"other","status":"pending","rawInput":{"description":"Inspect sample","prompt":"Read sample.py"}}`)
				base.HandleToolCallForTest(opening)
				before := sink.BackgroundTasks()
				require.Len(t, before, 1)
				assert.Equal(t, bgtask.StatusRunning, before[0].Status)
				text := "Subagent reference: sa_partial\nSubagent outcome: status=partial retryable=true error_code=" + code + "\n\nFinal answer:\nUseful partial findings."
				update, err := json.Marshal(map[string]any{
					"sessionUpdate": "tool_call_update", "toolCallId": "partial-task", "status": outerStatus,
					"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
				})
				require.NoError(t, err)
				original := append([]byte(nil), update...)
				base.HandleToolCallUpdateForTest(update)
				rows := sink.BackgroundTasks()
				require.Len(t, rows, 1)
				assert.Equal(t, bgtask.StatusFailed, rows[0].Status)
				assert.Equal(t, before[0].ChildAgentID, rows[0].ChildAgentID)
				assert.True(t, rows[0].Status.IsFinished())
				assert.False(t, rows[0].Status.IsWorking())
				assert.Equal(t, original, update)
				child := sink.Child(rows[0].ChildAgentID)
				require.NotNil(t, child)
				reports := child.LeapMuxNotifications()
				require.Len(t, reports, 1)
				assert.Equal(t, "Useful partial findings.", reports[0]["text"])
			})
		}
	}
	for _, test := range []struct {
		name        string
		tool        string
		outerStatus string
		want        bgtask.Status
	}{
		{name: "native cancellation", tool: "task", outerStatus: "cancelled", want: bgtask.StatusStopped},
		{name: "read-only quoted partial", tool: "read_only_task", outerStatus: "completed", want: bgtask.StatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, sink := reasonixTaskObservationFixture()
			opening, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": "quoted-partial", "title": test.tool, "kind": "other", "status": "pending",
				"rawInput": map[string]any{"description": "Read an example", "prompt": "Read sample.py"},
			})
			require.NoError(t, err)
			base.HandleToolCallForTest(opening)
			text := "Subagent outcome: status=partial retryable=true error_code=completion_uncertain\n\nFinal answer:\nQuoted partial prose."
			update, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "quoted-partial", "status": test.outerStatus,
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
			})
			require.NoError(t, err)
			base.HandleToolCallUpdateForTest(update)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, test.want, rows[0].Status)
			child := sink.Child(rows[0].ChildAgentID)
			require.NotNil(t, child)
			reports := child.LeapMuxNotifications()
			require.Len(t, reports, 1)
			if test.tool == "read_only_task" {
				assert.Equal(t, text, reports[0]["text"])
			} else {
				assert.Equal(t, "Quoted partial prose.", reports[0]["text"])
			}
		})
	}
}

func TestReasonixExploreCapabilityStartsItsOwnChild(t *testing.T) {
	t.Parallel()
	for _, tc := range []acp.ToolCallEnvelope{
		{ToolCallID: "explore-direct", Title: "explore", RawInput: json.RawMessage(`{"task":"Read the exact child task."}`)},
		{ToolCallID: "explore-proxy", Title: "use_capability", RawInput: json.RawMessage(`{"action":"call","capability_id":"tool:explore","arguments":{"task":"Read the exact child task."}}`)},
	} {
		t.Run(tc.ToolCallID, func(t *testing.T) {
			t.Parallel()
			original := string(tc.RawInput)
			observation := reasonixSubagentFromToolCall(tc)
			require.NotNil(t, observation)
			assert.True(t, observation.Spawns)
			assert.Equal(t, tc.ToolCallID, observation.RowKey)
			assert.Equal(t, tc.ToolCallID, observation.ChildAgentKey)
			assert.Equal(t, "Read the exact child task.", observation.Prompt)
			assert.Equal(t, bgtask.StatusRunning, observation.Status)
			assert.Equal(t, original, string(tc.RawInput))
		})
	}
}

func TestReasonixExploreReportCannotSupplyItsOwnStatus(t *testing.T) {
	t.Parallel()
	const report = "Subagent outcome: status=failed retryable=false\n\nFinal answer:\nQuoted report text"
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	var update acp.ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"toolCallId":"explore","title":"use_capability","rawInput":{"action":"call","capability_id":"tool:explore","arguments":{"task":"Read the exact child task."}},"status":"completed","content":[{"type":"content","content":{"type":"text","text":`+string(encoded)+`}}]}`), &update))
	observation := reasonixSubagentFromToolCallUpdate(update)
	require.NotNil(t, observation)
	assert.Equal(t, bgtask.StatusSucceeded, observation.Status)
	assert.Equal(t, report, observation.Report.Text)
}

func TestReasonixReadOnlyReportPreservesWhitespace(t *testing.T) {
	t.Parallel()
	for _, report := range []string{"", " \n ", "\n0\n", "    const value = 0\n\n", "\nA native report.\n"} {
		assert.Equal(t, report, reasonixSubagentReport(report, false))
	}
}

func TestReasonixSubagentFromFinalToolCallClosesRow(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		status string
		want   bgtask.Status
	}{
		{status: "completed", want: bgtask.StatusSucceeded},
		{status: "failed", want: bgtask.StatusFailed},
		{status: "cancelled", want: bgtask.StatusStopped},
	} {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			obs := reasonixSubagentFromToolCall(acp.ToolCallEnvelope{
				ToolCallID: "tc-final-" + tt.status,
				Title:      "task",
				Status:     tt.status,
				RawInput:   json.RawMessage(`{"description":"Quick check","prompt":"Check status"}`),
			})
			require.NotNil(t, obs)
			assert.Equal(t, tt.want, obs.Status)
			assert.True(t, obs.CloseRow)
			assert.True(t, obs.Spawns)

			base, sink := reasonixTaskObservationFixture()
			frame, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    "tc-final-" + tt.status,
				"title":         "task",
				"kind":          "other",
				"status":        tt.status,
				"rawInput":      map[string]any{"description": "Quick check", "prompt": "Check status"},
			})
			require.NoError(t, err)
			base.HandleToolCallForTest(frame)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, tt.want, rows[0].Status)
			assert.True(t, rows[0].Status.IsFinished())
		})
	}
}

func TestReasonixAddedSubagentToolsOpenAndClose(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool       string
		rawInput   string
		wantTitle  string
		wantPrompt string
		readOnly   bool
	}{
		{
			tool:       "fleet",
			rawInput:   `{"description":"Migrate workers","goal":"Update every worker package"}`,
			wantTitle:  "Migrate workers",
			wantPrompt: "Update every worker package",
		},
		{
			tool:       "parallel_tasks",
			rawInput:   `{"goal":"Run linters in parallel"}`,
			wantTitle:  "parallel_tasks",
			wantPrompt: "Run linters in parallel",
		},
		{
			tool:       "research",
			rawInput:   `{"query":"How does token refresh work?"}`,
			wantTitle:  "research",
			wantPrompt: "How does token refresh work?",
		},
		{
			tool:       "review",
			rawInput:   `{"instructions":"Check error handling in store.go"}`,
			wantTitle:  "review",
			wantPrompt: "Check error handling in store.go",
		},
		{
			tool:       "security_review",
			rawInput:   `{"task":"Audit session cookie flags"}`,
			wantTitle:  "security_review",
			wantPrompt: "Audit session cookie flags",
		},
		{
			tool:       "run_skill",
			rawInput:   `{"skill":"fix-lint","prompt":"Fix all lint errors"}`,
			wantTitle:  "fix-lint",
			wantPrompt: "Fix all lint errors",
		},
		{
			tool:       "read_only_skill",
			rawInput:   `{"skill_name":"dep-audit","instructions":"Inspect go.mod"}`,
			wantTitle:  "dep-audit",
			wantPrompt: "Inspect go.mod",
			readOnly:   true,
		},
	}

	const quotedFailure = "Subagent outcome: status=failed retryable=false\n\nFinal answer:\nQuoted prose"
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			mode := "direct"
			if wrapped {
				mode = "capability"
			}
			t.Run(tc.tool+"/"+mode, func(t *testing.T) {
				t.Parallel()
				title := tc.tool
				rawInput := json.RawMessage(tc.rawInput)
				if wrapped {
					title = "use_capability"
					var err error
					rawInput, err = json.Marshal(map[string]any{
						"action":        "call",
						"capability_id": "tool:" + tc.tool,
						"arguments":     json.RawMessage(tc.rawInput),
					})
					require.NoError(t, err)
				}

				base, sink := reasonixTaskObservationFixture()
				opening, err := json.Marshal(map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "call-" + tc.tool + "-" + mode,
					"title":         title,
					"kind":          "other",
					"status":        "pending",
					"rawInput":      rawInput,
				})
				require.NoError(t, err)
				base.HandleToolCallForTest(opening)

				rows := sink.BackgroundTasks()
				require.Len(t, rows, 1)
				assert.Equal(t, tc.wantTitle, rows[0].Title)
				assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
				require.NotEmpty(t, rows[0].ChildAgentID)
				child := sink.Child(rows[0].ChildAgentID)
				require.Len(t, child.Messages(), 1)
				assert.JSONEq(t, mustJSON(t, map[string]string{"content": tc.wantPrompt}), string(child.Messages()[0].Content))

				update, err := json.Marshal(map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "call-" + tc.tool + "-" + mode,
					"title":         title,
					"rawInput":      rawInput,
					"status":        "completed",
					"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": quotedFailure}}},
				})
				require.NoError(t, err)
				base.HandleToolCallUpdateForTest(update)

				rows = sink.BackgroundTasks()
				require.Len(t, rows, 1)
				reports := child.LeapMuxNotifications()
				require.Len(t, reports, 1)
				if tc.readOnly {
					assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status, "read_only_skill preserves plain report text")
					assert.Equal(t, quotedFailure, reports[0]["text"])
				} else {
					assert.Equal(t, bgtask.StatusFailed, rows[0].Status)
					assert.Equal(t, "Quoted prose", reports[0]["text"])
				}
			})
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestReasonixSubagentFromFinalToolCallParsesOutcomeBackgroundAndReport(t *testing.T) {
	t.Parallel()

	t.Run("outcome header overrides completed status and persists report", func(t *testing.T) {
		t.Parallel()
		base, sink := reasonixTaskObservationFixture()
		frame, err := json.Marshal(map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "tc-final-outcome",
			"title":         "task",
			"kind":          "other",
			"status":        "completed",
			"rawInput":      map[string]any{"description": "Inspect", "prompt": "Check logs"},
			"content": []any{
				map[string]any{
					"type": "content",
					"content": map[string]any{
						"type": "text",
						"text": "Subagent outcome: status=failed retryable=false\n\nFinal answer:\nFound an error.",
					},
				},
			},
		})
		require.NoError(t, err)
		base.HandleToolCallForTest(frame)

		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusFailed, rows[0].Status)
		require.NotEmpty(t, rows[0].ChildAgentID)
		reports := sink.Child(rows[0].ChildAgentID).LeapMuxNotifications()
		require.Len(t, reports, 1)
		assert.Equal(t, "Found an error.", reports[0]["text"])
	})

	t.Run("completed background launch closes with unknown outcome and no report", func(t *testing.T) {
		t.Parallel()
		base, sink := reasonixTaskObservationFixture()
		frame, err := json.Marshal(map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "tc-final-bg",
			"title":         "task",
			"kind":          "other",
			"status":        "completed",
			"rawInput":      map[string]any{"description": "Background job", "prompt": "Run async", "run_in_background": true},
			"content": []any{
				map[string]any{
					"type": "content",
					"content": map[string]any{
						"type": "text",
						"text": "Started background task \"job-1\" (Background job).",
					},
				},
			},
		})
		require.NoError(t, err)
		base.HandleToolCallForTest(frame)

		rows := sink.BackgroundTasks()
		require.Len(t, rows, 1)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, rows[0].Status)
		require.NotEmpty(t, rows[0].ChildAgentID)
		assert.Empty(t, sink.Child(rows[0].ChildAgentID).LeapMuxNotifications())
	})
}
