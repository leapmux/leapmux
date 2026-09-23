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

func TestACP_FinalStatusMap(t *testing.T) {
	assert.Equal(t, bgtask.StatusCompleted, FinalStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, FinalStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, FinalStatus("cancelled"))
	assert.Equal(t, bgtask.StatusStopped, FinalStatus("unknown"))
}

// TestACPEnvelopesDecodeTheWireFieldNames decodes REAL ACP wire payloads (the
// inner `update` object) through the envelope structs.
//
// A test that constructs the structs directly cannot see a JSON-tag mismatch (for
// example `json:"input"` against the wire's `rawInput`). That exact bug left
// OpenCode, Kilo, Reasonix and Cursor subagent detection inert at runtime while
// every struct-construction test passed. Each provider's own wire-decode test
// then runs its detector on a payload decoded the same way.
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

// TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow
// verifies the rename path: a spawn opens a row under the toolCallId, then a
// final update re-keys it to the child session id via RenameFrom. One row
// tracks the lifecycle and ends final; the original spawn key is gone (not
// leaked as a separate Running row).
func TestACP_ApplySubagentObservation_RenameFromCollapsesToOneFinalRow(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// Spawn opens a row under the toolCallId.
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "call-123",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})
	require.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)

	// Final update renames call-123 -> sess-abc, then closes sess-abc.
	b.applySubagentObservation(&SubagentObservation{
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
// observation with Mode == ModeCloseOnly closes an existing row WITHOUT
// first upserting one (the detector sets the Mode explicitly instead of relying
// on which fields happen to be empty).
func TestACP_ApplySubagentObservation_CloseOnlyModeSkipsUpsert(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// Open a row.
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "call-1",
		Title:  "spawn",
		Status: bgtask.StatusRunning,
	})

	// Close-only: closes the existing row, does NOT upsert.
	b.applySubagentObservation(&SubagentObservation{
		RowKey:   "call-1",
		Status:   bgtask.StatusCompleted,
		CloseRow: true,
		Mode:     ModeCloseOnly,
	})
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "close-only must not create a new row")
	assert.True(t, tasks[0].Status.IsFinished(), "existing row reached a final status")
}

// TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth verifies that an
// observation with Mode == ModeUpsert (default) and CloseRow upserts THEN
// closes — the behavior when a final observation also carries descriptive
// fields (e.g. a Cursor background task with an activity line).
func TestACP_ApplySubagentObservation_UpsertModeWithCloseDoesBoth(t *testing.T) {
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	b.applySubagentObservation(&SubagentObservation{
		RowKey:   "call-bg",
		Title:    "bg task",
		Activity: "background task",
		Status:   bgtask.StatusCompleted,
		CloseRow: true,
		// Mode defaults to ModeUpsert (zero value).
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

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	// 1. Spawn: prompt recorded, no child yet.
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "tc-1",
		Title:  "Goose subagent",
		Status: bgtask.StatusRunning,
		Prompt: "Review the diff.",
	})
	assert.Equal(t, "Review the diff.", b.subagentPrompts.PeekForTest("tc-1"))

	// 2. The observation that links the child spends it.
	b.applySubagentObservation(&SubagentObservation{
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
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "tc-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.CountForTest())

	b.applySubagentObservation(&SubagentObservation{
		RowKey: "tc-1", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.CountForTest())
}

// A closing observation that RE-KEYS the row must drop the prompt under the
// key the spawn used, not under the new one. A provider that learns the child's
// stable id only on the closing update (OpenCode, Kilo) arrives here with
// RowKey = the new key and RenameFrom = the spawn key, so forgetting only
// RowKey deletes an entry that was never inserted and leaves the spawn's own to
// accumulate for the life of the agent process.
func TestACPSubagentPrompt_DroppedUnderTheSpawnKeyAfterARename(t *testing.T) {
	t.Parallel()

	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "call-1", Title: "task", Status: bgtask.StatusRunning, Prompt: "Do it.",
	})
	require.Equal(t, 1, b.subagentPrompts.CountForTest())

	b.applySubagentObservation(&SubagentObservation{
		RowKey:     "ses-child",
		RenameFrom: "call-1",
		Status:     bgtask.StatusCompleted,
		CloseRow:   true,
		Mode:       ModeCloseOnly,
	})
	assert.Zero(t, b.subagentPrompts.CountForTest(), "the entry sits under the SPAWN key, not the renamed one")
}

// Replacing the session drops every unspent prompt: those rows belong to the
// outgoing session and no closing observation will ever arrive for them, so
// without this they are held for the life of the agent process.
func TestACPSubagentPrompt_ClearedWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()

	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.applySubagentObservation(&SubagentObservation{
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
	b.applySubagentObservation(&SubagentObservation{RowKey: "tc-1", Prompt: "first", Status: bgtask.StatusRunning})
	b.applySubagentObservation(&SubagentObservation{RowKey: "tc-1", Prompt: "second", Status: bgtask.StatusRunning})
	assert.Equal(t, "first", b.subagentPrompts.PeekForTest("tc-1"))
}

func TestACP_SubagentReportLookupFailureWritesNoUnverifiedReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "task-call", Title: "Inspect", Status: bgtask.StatusRunning,
		ChildAgentKey: "task-call", Prompt: "Inspect it.",
	})
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)

	sink.LookupErr = errors.New("registry read failed")
	b.applySubagentObservation(&SubagentObservation{
		RowKey: "task-call", Status: bgtask.StatusCompleted, CloseRow: true,
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

// The shell case now lives in TestACP_OnlyTheSpawnDetectorsClaimASpawn, which
// asserts it over the REAL Cursor hook: the backgrounded-shell arm never claims
// a spawn. The kind of a row no longer decides, so a struct built by hand can no
// longer state the case.

// A spawn owns no span: the base opens none for it and reserves no color, but it
// still records the span type for the closing update. An ordinary tool call keeps
// its span. The stub detector claims one call; each provider pins that its own
// detector claims its spawn payload and nothing else.
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

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-spawn","kind":"other","title":"explore","rawInput":{"prompt":"go"}}`))
	assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
	assert.Empty(t, sink.ReservedColorSpans(), "and reserves no color")
	assert.Equal(t, "other", sink.GetSpanType("call-spawn"),
		"the span type is still recorded for the closing update")

	b.handleToolCall(json.RawMessage(`{"toolCallId":"call-plain","kind":"read","title":"Read","rawInput":{"path":"/tmp/a"}}`))
	open := sink.OpenSpans()
	require.Len(t, open, 1, "an ordinary tool call still opens a span")
	assert.Equal(t, "call-plain", open[0].SpanID)
	assert.Equal(t, []string{"call-plain"}, sink.ReservedColorSpans())

	// The spawn row was persisted before the plain call, so it drew no rail; the
	// plain row persists before its own span opens, so it draws none either.
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[0].SpansOpenAtPersist)
	assert.Equal(t, "call-spawn", msgs[0].SpanID)
}
