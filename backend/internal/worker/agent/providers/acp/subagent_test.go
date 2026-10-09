package acp

import (
	"encoding/json"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPSpanOnlyChildObservationCreatesThePromptBeforeItsNativeKey(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.hooks.SubagentFromToolCall = func(tc ToolCallEnvelope) *SubagentObservation {
		return &SubagentObservation{
			RowKey: "registry-launch", ChildSpawnSpanID: tc.ToolCallID,
			Title: "Native helper", Prompt: "Inspect the entry point.", Status: bgtask.StatusRunning, Spawns: true,
		}
	}
	b.hooks.SubagentFromToolCallUpdate = func(tc ToolCallUpdateEnvelope) *SubagentObservation {
		return &SubagentObservation{
			RowKey: "native-child", RenameFrom: "registry-launch", ChildSpawnSpanID: tc.ToolCallID,
			ChildAgentKey: "native-child", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly,
			ReportID: tc.ToolCallID, Report: agent.SubagentReport{Text: ToolCallText(tc.Content)},
		}
	}
	b.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"native-spawn","title":"Task","status":"pending"}`))
	initial, found := sink.BackgroundTask("registry-launch")
	require.True(t, found)
	require.NotEmpty(t, initial.ChildAgentID)
	child := sink.Child(initial.ChildAgentID)
	before := child.Messages()
	require.Len(t, before, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, before[0].Source)
	assert.JSONEq(t, `{"content":"Inspect the entry point."}`, string(before[0].Content))
	const completed = `{"sessionUpdate":"tool_call_update","toolCallId":"native-spawn","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Native final report."}}]}`
	b.HandleToolCallUpdateForTest(json.RawMessage(completed))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "native-child", rows[0].RowKey)
	assert.Equal(t, initial.ChildAgentID, rows[0].ChildAgentID)
	assert.Equal(t, []string{initial.ChildAgentID}, sink.ChildAgentIDs())
	assert.Equal(t, before, child.Messages())
	span, err := sink.ChildSpawnSpan(initial.ChildAgentID)
	require.NoError(t, err)
	assert.Equal(t, "native-spawn", span)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Native final report.", reports[0]["text"])
	var originals []string
	for _, message := range sink.Messages() {
		if message.Closing && message.SpanID == "native-spawn" {
			originals = append(originals, string(message.Content))
		}
	}
	require.Len(t, originals, 1)
	assert.JSONEq(t, completed, originals[0])
}

func TestACPSpanOnlyObservationCannotRestoreAnIdentityRefusedChild(t *testing.T) {
	t.Parallel()
	b, sink, child, services := newChildRefusalBase(t, "native-old")
	validateRefusalChild(b, "call-spawn", "native-new")
	before := child.Messages()
	validations := services.validationCount()
	services.resetWrites()
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-spawn", ChildSpawnSpanID: "call-spawn", Prompt: "Unproved replacement prompt.",
		Status: bgtask.StatusRunning,
	})
	assert.Equal(t, validations, services.validationCount())
	assert.Empty(t, services.recordedWrites())
	assert.False(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Unproved step.")))
	assert.Equal(t, before, child.Messages())
	row, found := sink.BackgroundTask("call-spawn")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	validateRefusalChild(b, "call-spawn", "native-old")
	require.True(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Validated step.")))
	assert.Len(t, child.Messages(), len(before)+1)
}

func TestACPChildIdentityRefusalStopsObservationWrites(t *testing.T) {
	t.Parallel()
	b, sink, child, services := newChildRefusalBase(t, "native-old")
	before := child.Messages()
	row, found := sink.BackgroundTask("call-spawn")
	require.True(t, found)
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-spawn", ChildAgentKey: "call-spawn", ChildAgentSessionID: "native-new",
		Title: "Rejected replacement", Prompt: "Rejected prompt.", Status: bgtask.StatusSucceeded,
		ChildTranscriptPayload: []byte(`{"content":"Rejected payload."}`),
		ReportID:               "rejected-report", Report: agent.SubagentReport{Text: "Rejected report."}, CloseRow: true,
	})
	assert.Empty(t, services.recordedWrites())
	assert.Equal(t, before, child.Messages())
	after, found := sink.BackgroundTask("call-spawn")
	require.True(t, found)
	assert.Equal(t, row, after)
}

func TestACPChildIdentityRefusalStopsRegistryReportAndCloseRecovery(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"report", "close"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			b, sink, child, services := newChildRefusalBase(t, "native-old")
			validateRefusalChild(b, "call-spawn", "native-new")
			before := child.Messages()
			row, found := sink.BackgroundTask("call-spawn")
			require.True(t, found)
			services.resetWrites()
			obs := &SubagentObservation{RowKey: "call-spawn", Status: bgtask.StatusSucceeded, Mode: ModeCloseOnly}
			if operation == "report" {
				obs.ReportID, obs.Report = "late-report", agent.SubagentReport{Text: "Rejected report."}
			} else {
				obs.CloseRow = true
			}
			b.ApplySubagentObservation(obs)
			assert.Empty(t, services.recordedWrites())
			assert.Equal(t, before, child.Messages())
			after, found := sink.BackgroundTask("call-spawn")
			require.True(t, found)
			assert.Equal(t, row, after)
		})
	}
}

func TestACPChildIdentityRefusalPreservesTheNativeParentClosingFrame(t *testing.T) {
	t.Parallel()
	b, sink, child, services := newChildRefusalBase(t, "native-old")
	b.hooks.SubagentFromToolCall = func(tc ToolCallEnvelope) *SubagentObservation {
		return &SubagentObservation{
			RowKey: tc.ToolCallID, ChildAgentKey: tc.ToolCallID, ChildAgentSessionID: "native-new",
			Status: bgtask.StatusRunning, Spawns: true,
		}
	}
	before := child.Messages()
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"call-spawn","title":"spawn","status":"pending"}`))
	services.resetWrites()
	const closing = `{"sessionUpdate":"tool_call_update","toolCallId":"call-spawn","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Native parent result."}}]}`
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", closing))
	assert.Equal(t, []agent.MessageCompletion{""}, closingRows(sink, "call-spawn"))
	var originals []string
	for _, message := range sink.Messages() {
		if message.SpanID == "call-spawn" && message.Closing {
			originals = append(originals, string(message.Content))
		}
	}
	require.Len(t, originals, 1)
	assert.JSONEq(t, closing, originals[0])
	assert.Empty(t, services.recordedWrites())
	assert.Equal(t, before, child.Messages())
	row, found := sink.BackgroundTask("call-spawn")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestACPChildIdentityValidationKeepsOrdinaryClosingWrites(t *testing.T) {
	t.Parallel()
	b, sink, child, services := newChildRefusalBase(t, "native-old")
	validateRefusalChild(b, "call-spawn", "native-old")
	require.True(t, b.FeedChildUpdate("call-spawn", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Accepted answer."}}`)))
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-spawn", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly,
		ReportID: "accepted-report", Report: agent.SubagentReport{Text: "Accepted report."},
	})
	assert.Equal(t, []string{"text:Accepted answer."}, assembledTexts(t, child.Messages()))
	assert.Equal(t, []string{"report", "close", "cleanup"}, services.recordedWrites())
	row, found := sink.BackgroundTask("call-spawn")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.False(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Late step.")))
}

func TestACPChildIdentityRefusalSurvivesRenameWithoutRegistryMutation(t *testing.T) {
	t.Parallel()
	b, sink, child, services := newChildRefusalBase(t, "native-old")
	validateRefusalChild(b, "call-spawn", "native-new")
	before := child.Messages()
	services.resetWrites()
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "renamed-spawn", RenameFrom: "call-spawn", Status: bgtask.StatusRunning,
	})
	assert.Empty(t, services.recordedWrites())
	_, found := sink.BackgroundTask("call-spawn")
	assert.True(t, found)
	_, renamed := sink.BackgroundTask("renamed-spawn")
	assert.False(t, renamed)
	assert.False(t, b.FeedChildUpdate("call-spawn", childRefusalPlan(t, "Old key step.")))
	assert.False(t, b.FeedChildUpdate("renamed-spawn", childRefusalPlan(t, "New key step.")))
	assert.Equal(t, before, child.Messages())
	for _, update := range []string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Rejected renamed text."},"_meta":{"parentToolCallId":"renamed-spawn"}}`,
		`{"sessionUpdate":"plan","entries":[{"content":"Rejected renamed step.","status":"pending"}],"_meta":{"parentToolCallId":"renamed-spawn"}}`,
	} {
		b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", update))
	}
	b.FinishChildTurn("renamed-spawn")
	assert.Empty(t, b.TurnAssistantTextForTest().String())
	assert.Empty(t, sink.Messages())
	assert.Equal(t, before, child.Messages())
}

func TestACPChildIdentityRestorationUsesTheDirectNestedDelegate(t *testing.T) {
	t.Parallel()
	b, root := newChildRouteBase(t)
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "parent-child", ChildAgentKey: "parent-child", ChildAgentSessionID: "native-parent", Status: bgtask.StatusRunning,
	})
	b.AttachChildSession("native-parent", "parent-child")
	parent := root.Child("child-of-parent-child")
	grandchild := seedRefusalChild(t, parent, "reused-spawn", "native-grandchild")
	rootChild := seedRefusalChild(t, root, "reused-spawn", "native-root-child")
	rootBefore := rootChild.Messages()
	require.True(t, b.ApplySubagentObservationForSession("native-parent", &SubagentObservation{
		RowKey: "reused-spawn", ChildAgentKey: "reused-spawn", ChildAgentSessionID: "native-grandchild", Status: bgtask.StatusRunning,
	}))
	require.True(t, b.FeedChildUpdate("reused-spawn", childRefusalPlan(t, "Nested restored step.")))
	assert.Len(t, grandchild.Messages(), 2)
	assert.Contains(t, string(grandchild.Messages()[1].Content), "Nested restored step.")
	assert.Equal(t, rootBefore, rootChild.Messages())
}

func TestACP_FinalStatusMap(t *testing.T) {
	assert.Equal(t, bgtask.StatusSucceeded, FinalStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, FinalStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, FinalStatus("cancelled"))
	assert.Equal(t, bgtask.StatusStopped, FinalStatus("unknown"))
}

// TestACPEnvelopesDecodeTheWireFieldNames decodes actual ACP update payloads through the envelope structures.
//
// Direct structure construction cannot detect a JSON-tag mismatch, such as json:"input" for the native rawInput field.
// That mismatch disabled subagent detection for these providers while every direct structure test passed:
//   - OpenCode.
//   - Kilo.
//   - Reasonix.
//   - Cursor.
//
// Each provider's decode test also runs its detector on a payload decoded through this same path.
func TestACPEnvelopesDecodeTheWireFieldNames(t *testing.T) {
	t.Parallel()

	var tc ToolCallEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Task","kind":"other","status":"in_progress",`+
		`"rawInput":{"prompt":"go"},"rawOutput":{"ok":true},"_meta":{"vendor":{}}}`), &tc))
	assert.Equal(t, ToolCallEnvelope{
		ToolCallID: "call-1", Title: "Task", Kind: "other", Status: "in_progress",
		RawInput: json.RawMessage(`{"prompt":"go"}`), RawOutput: json.RawMessage(`{"ok":true}`),
		Meta: json.RawMessage(`{"vendor":{}}`),
	}, tc)

	var tcu ToolCallUpdateEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed","title":"Task",`+
		`"content":[{"type":"content","content":{"type":"text","text":"done"}}],"rawInput":{"prompt":"go"},"rawOutput":{"ok":true},"_meta":{"vendor":{}}}`), &tcu))
	assert.Equal(t, "call-1", tcu.ToolCallID)
	assert.Equal(t, "completed", tcu.Status)
	assert.Equal(t, "Task", tcu.Title)
	assert.Len(t, tcu.Content, 1)
	assert.JSONEq(t, `{"prompt":"go"}`, string(tcu.RawInput))
	assert.JSONEq(t, `{"ok":true}`, string(tcu.RawOutput))
	assert.JSONEq(t, `{"vendor":{}}`, string(tcu.Meta))
}

// TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow verifies the complete rename lifecycle.
// A spawn opens a row under toolCallId, and RenameFrom moves it to the child session ID on the final update.
// One final row remains, and the original spawn key leaves no separate Running row.
func TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// Spawn opens a row under the toolCallId.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-123",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})
	require.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)

	// Final update renames call-123 -> sess-abc, then closes sess-abc.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey:     "sess-abc",
		RenameFrom: "call-123",
		Status:     bgtask.StatusSucceeded,
		CloseRow:   true,
	})

	// Only one final row remains under the new key. The spawn key identifies no row.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "rename + close collapsed the lifecycle to one row")
	assert.Equal(t, "sess-abc", tasks[0].RowKey)
	assert.True(t, tasks[0].Status.IsFinished(), "renamed row is final")
}

// TestACP_ApplySubagentObservation_CloseOnlyModeSkipsUpsert verifies that ModeCloseOnly closes an existing row without an upsert.
// The detector sets Mode explicitly instead of inferring it from empty descriptive fields.
func TestACP_ApplySubagentObservation_CloseOnlyModeSkipsUpsert(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// Open a row.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-1",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})

	// Close-only: closes the existing row, does NOT upsert.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey:   "call-1",
		Status:   bgtask.StatusSucceeded,
		CloseRow: true,
		Mode:     ModeCloseOnly,
	})
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "close-only must not create a new row")
	assert.True(t, tasks[0].Status.IsFinished(), "existing row reached a final status")
}

// TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth verifies that ModeUpsert with CloseRow upserts the row before closing it.
// This default mode permits descriptive fields on a final observation, such as a Cursor background task's activity line.
func TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	b.ApplySubagentObservation(&SubagentObservation{
		RowKey:   "call-bg",
		Title:    "bg task",
		Activity: "background task",
		Status:   bgtask.StatusSucceeded,
		CloseRow: true,
		// Mode defaults to ModeUpsert (zero value).
	})
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "bg task", tasks[0].Title, "upsert carried the title")
	assert.True(t, tasks[0].Status.IsFinished(), "the close gave the row a final status")
}

// The spawn payload supplies the prompt before another observation creates the child transcript.
// Goose learns its child from the first forwarded tool request.
// applySubagentObservation retains the prompt until that child exists, so the transcript can open with it.
func TestACPSubagentPrompt_HeldFromSpawnUntilTheChildExists(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// 1. Spawn: prompt recorded, no child yet.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "tc-1",
		Title:  "Goose subagent",
		Status: bgtask.StatusRunning,
		Prompt: "Review the diff.",
	})
	assert.Equal(t, "Review the diff.", b.subagentPrompts.PeekForTest("tc-1"))

	// 2. The observation that links the child consumes the retained prompt.
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey:        "tc-1",
		ChildAgentKey: "tc-1",
		Status:        bgtask.StatusRunning,
	})
	child := sink.Child("child-of-tc-1")
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source)
	assert.JSONEq(t, `{"content":"Review the diff."}`, string(msgs[0].Content))
	assert.Zero(t, b.subagentPrompts.CountForTest(), "spent, so a later observation cannot repeat it")
}

// A provider that never links a child must not leak the remembered prompt: the
// row's close drops it.
func TestACPSubagentPrompt_DroppedWhenTheRowClosesWithNoChild(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "tc-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.CountForTest())

	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "tc-1", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.CountForTest())
}

// A closing observation that renames a row must remove the prompt under the original spawn key.
// OpenCode and Kilo learn the stable child ID on the closing update and supply it as RowKey, with the spawn key in RenameFrom.
// Removing only RowKey would address an entry that never existed and retain the original prompt for the agent process's lifetime.
func TestACPSubagentPrompt_DroppedUnderTheSpawnKeyAfterARename(t *testing.T) {
	t.Parallel()

	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.CountForTest())

	b.ApplySubagentObservation(&SubagentObservation{
		RowKey:     "ses-child",
		RenameFrom: "call-1",
		Status:     bgtask.StatusSucceeded,
		CloseRow:   true,
		Mode:       ModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.CountForTest(), "the entry sits under the SPAWN key, not the renamed one")
}

// Replacing the session removes every unconsumed spawn prompt.
// Those rows belong to the outgoing session, which supplies no later closing observation.
// Otherwise the prompts would remain for the agent process's lifetime.
func TestACPSubagentPrompt_ClearedWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()

	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "tc-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.CountForTest())

	b.subagentPrompts.Clear()
	assert.Zero(t, b.subagentPrompts.CountForTest())
}

// The spawn's own text wins: a later observation that re-reports a prompt for
// the same row must not overwrite it.
func TestACPSubagentPrompt_FirstWriteWins(t *testing.T) {
	t.Parallel()

	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "tc-1", Prompt: "first", Status: bgtask.StatusRunning})
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "tc-1", Prompt: "second", Status: bgtask.StatusRunning})
	assert.Equal(t, "first", b.subagentPrompts.PeekForTest("tc-1"))
}

func TestACP_SubagentReportLookupFailureWritesNoUnverifiedReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "task-call", Title: "Inspect", Status: bgtask.StatusRunning,
		ChildAgentKey: "task-call", Prompt: "Inspect it.",
	})
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)

	sink.LookupErr = errors.New("registry read failed")
	b.ApplySubagentObservation(&SubagentObservation{
		RowKey: "task-call", Status: bgtask.StatusSucceeded, CloseRow: true,
		Mode: ModeCloseOnly, ReportID: "call-1", Report: agent.SubagentReport{Text: "Unverified report"},
	})

	assert.Empty(t, child.LeapMuxNotifications())
}

// --- A subagent spawn owns no span ---

func TestACP_ObservationIsSpawn(t *testing.T) {
	t.Parallel()

	assert.False(t, ObservationIsSpawn(nil), "no observation, no spawn")
	assert.False(t, ObservationIsSpawn(&SubagentObservation{}), "an empty row key identifies nothing")
	assert.False(t, ObservationIsSpawn(&SubagentObservation{
		RowKey: "call-1", Spawns: false, Status: bgtask.StatusRunning,
	}), "a running row is not a spawn unless the provider says so")
	assert.False(t, ObservationIsSpawn(&SubagentObservation{
		Spawns: true,
	}), "an observation that identifies no row must not take a span either")
	assert.True(t, ObservationIsSpawn(&SubagentObservation{
		RowKey: "call-1", Spawns: true,
	}))
}

// TestACP_OnlyTheSpawnDetectorsClaimASpawn exercises the shell case through the actual Cursor hook.
// The background-shell branch never identifies a spawn.
// A row's kind no longer determines this result, so a directly constructed observation cannot represent that case.

// A spawn owns no span and reserves no color, but the base still records its span type for the closing update.
// An ordinary tool call keeps its span.
// The stub detector identifies one call, and each provider verifies that its detector accepts only its actual spawn payloads.
func TestACP_SpawnToolCallOpensNoSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	detector := func(tc ToolCallEnvelope) *SubagentObservation {
		if tc.ToolCallID != "call-spawn" {
			return nil
		}
		return &SubagentObservation{RowKey: tc.ToolCallID, Spawns: true}
	}
	b := &Base{sink: agent.NewProviderServices(sink), hooks: Hooks{SubagentFromToolCall: detector}}

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-spawn","kind":"other","title":"explore","rawInput":{"prompt":"go"}}`))
	assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
	assert.Empty(t, sink.ReservedColorSpans(), "and reserves no color")
	assert.Equal(t, "other", sink.GetSpanType("call-spawn"),
		"the span type is still recorded for the closing update")

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-plain","kind":"read","title":"Read","rawInput":{"path":"/tmp/a"}}`))
	open := sink.OpenSpans()
	require.Len(t, open, 1, "an ordinary tool call still opens a span")
	assert.Equal(t, "call-plain", open[0].SpanID)
	assert.Equal(t, []string{"call-plain"}, sink.ReservedColorSpans())

	// The spawn row persists before the ordinary call and therefore draws no rail.
	// The ordinary row also persists before its own span opens, so it draws no rail either.
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[0].SpansOpenAtPersist)
	assert.Equal(t, "call-spawn", msgs[0].SpanID)
}
