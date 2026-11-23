package pi

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPi_ExtractDescription(t *testing.T) {
	assert.Equal(t, "build it", piExtractDescription(json.RawMessage(`{"description":"build it","prompt":"long"}`), "tool"))
	assert.Equal(t, "first line", piExtractDescription(json.RawMessage(`{"prompt":"first line\nsecond"}`), "tool"))
	assert.Equal(t, "tool", piExtractDescription(json.RawMessage(`{}`), "tool"))
	assert.Equal(t, "tool", piExtractDescription(nil, "tool"))
}

func TestPi_SubagentFromDetails_Running(t *testing.T) {
	details := json.RawMessage(`{"status":"running","activity":"executing","agentId":"a-1"}`)
	obs := piSubagentFromDetails(details, "tc-1", "title")
	require.NotNil(t, obs)
	assert.Equal(t, "a-1", obs.RowKey)
	assert.Equal(t, "executing", obs.ActiveForm)
	assert.Equal(t, bgtask.StatusRunning, obs.Status)
}

func TestPi_SubagentFromDetails_NilForNoStatus(t *testing.T) {
	assert.Nil(t, piSubagentFromDetails(json.RawMessage(`{"activity":"x"}`), "tc-1", "title"))
	assert.Nil(t, piSubagentFromDetails(nil, "tc-1", "title"))
}

func TestPi_SubagentReportTextPreservesTextBlockBoundaries(t *testing.T) {
	t.Parallel()

	result := json.RawMessage(`{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)
	assert.Equal(t, "first\nsecond", piSubagentReportText(result))
}

func TestPi_FinalStatus(t *testing.T) {
	s, ok := piFinalStatus("completed")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, s)
	s, ok = piFinalStatus("error")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, s)
	s, ok = piFinalStatus("stopped")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, s)
	_, ok = piFinalStatus("running")
	assert.False(t, ok)
}

func TestPi_ApplySubagentEnd_FinalStatus(t *testing.T) {
	sink := &agenttest.Sink{}
	result := json.RawMessage(`{"content":[{"type":"text","text":"The review passed."}],"details":{"status":"completed","agentId":"a-1"}}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "Review it.")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
	require.NotEmpty(t, tasks[0].ChildAgentID)
	child := sink.Child(tasks[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Review it."}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The review passed.", reports[0]["text"])
	piApplySubagentEnd(sink, result, "tc-1", "title", "Review it.")
	assert.Len(t, child.LeapMuxNotifications(), 1, "a replayed final result must not duplicate the child report")
}

func TestPi_ApplySubagentEnd_BackgroundRekey(t *testing.T) {
	sink := &agenttest.Sink{}
	result := json.RawMessage(`{"content":[],"details":{"status":"background","agentId":"bg-1"}}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "bg-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	// The re-keyed row links to a child transcript through the spawn span.
	assert.Equal(t, "child-of-tc-1", tasks[0].ChildAgentID, "re-keyed row carries child linkage")
}

// An unrecognized status must NOT give a final status to the row. piApplySubagentEnd keeps
// the row Running so a later final event can still close it.
func TestPi_ApplySubagentEnd_UnrecognizedStatusStaysRunning(t *testing.T) {
	sink := &agenttest.Sink{}
	result := json.RawMessage(`{"content":[],"details":{"status":"thinking","agentId":"a-1"}}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status, "unrecognized status stays running")
}

func TestPi_ApplySubagentEnd_FallbackRegex(t *testing.T) {
	sink := &agenttest.Sink{}
	// A standalone "Agent ID: X" line matches the anchored regex and renames the
	// row to the captured agent ID so later notifications target the same row.
	piApplySubagentEnd(sink, json.RawMessage(`"Agent ID: regex-1\n"`), "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "regex-1", tasks[0].RowKey, "regex fallback renames toolCallID to the captured agent ID")
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
}

// Free-form prose that mentions "Agent ID:" mid-sentence must NOT match the
// anchored regex and must NOT create a phantom registry row.
func TestPi_ApplySubagentEnd_FallbackRegexIgnoresProse(t *testing.T) {
	sink := &agenttest.Sink{}
	piApplySubagentEnd(sink, json.RawMessage(`"the model said Agent ID: something here"`), "tc-1", "title", "")
	assert.Empty(t, sink.BackgroundTasks(), "mid-sentence mention does not create a phantom row")
}

func TestPi_ApplySubagentNotification_FinalStatus(t *testing.T) {
	sink := &agenttest.Sink{}
	msg, _ := json.Marshal(map[string]any{
		"role":       "custom",
		"customType": "subagent-notification",
		"details": map[string]any{
			"status": "completed",
			"id":     "a-1",
		},
	})
	piApplySubagentNotification(sink, append(append([]byte(`{"message":`), msg...), byte('}')))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
}

func TestPi_ApplySubagentNotification_GroupOthers(t *testing.T) {
	sink := &agenttest.Sink{}
	msg, _ := json.Marshal(map[string]any{
		"role":       "custom",
		"customType": "subagent-notification",
		"details": map[string]any{
			"status": "running",
			"id":     "a-1",
			"others": []map[string]any{
				{"id": "a-2", "status": "completed"},
				{"id": "a-3", "status": "error"},
			},
		},
	})
	piApplySubagentNotification(sink, append(append([]byte(`{"message":`), msg...), byte('}')))
	tasks := sink.BackgroundTasks()
	// a-1 (running) + a-2 (completed) + a-3 (failed) = 3 rows.
	require.Len(t, tasks, 3)
}

func TestPi_ApplySubagentNotification_NonNotificationNoop(t *testing.T) {
	sink := &agenttest.Sink{}
	msg, _ := json.Marshal(map[string]any{"customType": "other"})
	piApplySubagentNotification(sink, append(append([]byte(`{"message":`), msg...), byte('}')))
	assert.Empty(t, sink.BackgroundTasks())
}

// TestPi_ExtractDescription_MultibyteTruncateNoInvalidUTF8 verifies that the
// first-line cut lands on a rune boundary. A byte slice at 80 on a
// multi-byte-rune prompt splits the rune and emits invalid UTF-8, which fails
// the proto broadcast marshal.
func TestPi_ExtractDescription_MultibyteTruncateNoInvalidUTF8(t *testing.T) {
	// 100 U+4E2D ("中") runes; each is 3 bytes. Byte offset 80 lands mid-rune
	// (26 full runes = 78 bytes + 2 bytes of the 27th rune).
	//
	// The result is 42 runes and not 80, because CleanTitleRunes cleans first
	// and the clean caps at validate.NameByteLimit (128 bytes = 42 CJK runes).
	// The 80-rune cap is a DISPLAY cap on top of that byte cap, so it binds
	// for ASCII and the byte cap binds for CJK. The sink applies the same byte
	// cap afterwards, so 80 was never what this path could store.
	runes := strings.Repeat("中", 100)
	got := piExtractDescription(json.RawMessage(`{"prompt":`+strconv.Quote(runes)+`}`), "tool")
	assert.Equal(t, strings.Repeat("中", 42), got, "the byte cap binds before the 80-rune cap")
	assert.True(t, utf8.ValidString(got), "title must be valid UTF-8")
}

// TestPi_ExtractDescription_CleansBeforeItCuts pins the order that
// CleanTitleRunes exists for. A cut that ran first spent its whole budget on
// characters the clean was about to remove, and the row then kept the title it
// already held -- the same defect validate.CleanName removed from itself.
func TestPi_ExtractDescription_CleansBeforeItCuts(t *testing.T) {
	// 85 zero width spaces, then the text. A cut-first rule kept the first 80
	// invisible runes, the clean emptied them, and "Fix the auth bug" never
	// reached the registry row.
	prompt := strings.Repeat("\u200b", 85) + "Fix the auth bug"
	got := piExtractDescription(json.RawMessage(`{"prompt":`+strconv.Quote(prompt)+`}`), "tool")
	assert.Equal(t, "Fix the auth bug", got)

	// The description branch takes the same rule as the prompt branch, so a
	// caller that reads one does not have to know which branch answered.
	desc := strings.Repeat("\u200b", 85) + "Run the linter"
	got = piExtractDescription(json.RawMessage(`{"description":`+strconv.Quote(desc)+`}`), "tool")
	assert.Equal(t, "Run the linter", got)

	// A model-written description can exceed the display limit, so this branch applies the same limit.
	long := strings.Repeat("a", 500)
	got = piExtractDescription(json.RawMessage(`{"description":`+strconv.Quote(long)+`}`), "tool")
	assert.Equal(t, strings.Repeat("a", 80), got, "the 80-rune display cap binds for ASCII")
}

// Compile-time check: ensure the helpers participate in the package's sink
// interface (the Sink satisfies ProviderServices).
var _ agent.ProviderServices = agent.NewProviderServices((*agenttest.Sink)(nil))
var _ leapmuxv1.MessageSource

// pi-subagents declares the nested agent's task as `prompt`
// (src/nested-tools.ts). piExtractDescription truncates it to a one-line label;
// the transcript's first message needs the whole thing.
func TestPi_ExtractPrompt(t *testing.T) {
	assert.Equal(t, "first line\nsecond", piExtractPrompt(json.RawMessage(`{"prompt":"first line\nsecond"}`)))
	assert.Empty(t, piExtractPrompt(json.RawMessage(`{"description":"build it"}`)))
	assert.Empty(t, piExtractPrompt(json.RawMessage(`{}`)))
	assert.Empty(t, piExtractPrompt(nil))
	assert.Empty(t, piExtractPrompt(json.RawMessage(`not json`)))
}

// The end helper also creates the child when startup state is absent. This
// covers a resumed stream whose tool_execution_start did not replay.
func TestPi_ApplySubagentEnd_BackgroundRekeyPersistsThePrompt(t *testing.T) {
	sink := &agenttest.Sink{}
	result := json.RawMessage(`{"content":[],"details":{"status":"background","agentId":"bg-1"}}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "Write the essay.")

	child := sink.Child("child-of-tc-1")
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source)
	assert.JSONEq(t, `{"content":"Write the essay."}`, string(msgs[0].Content))
}

func TestPi_ApplySubagentEnd_FinalStatusCreatesAChildTranscript(t *testing.T) {
	sink := &agenttest.Sink{}
	piApplySubagentEnd(sink, json.RawMessage(`{"content":[],"details":{"status":"completed","agentId":"a-1"}}`), "tc-1", "title", "Write the essay.")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "child-of-tc-1", tasks[0].ChildAgentID)
	child := sink.Child(tasks[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Write the essay."}`, string(child.Messages()[0].Content))
}

// --- A subagent launch opens no span ---

// pi-subagents registers its spawn tool as `Agent` (SUBAGENT_TOOL_NAMES.AGENT).
// That call owns no span: the subagent's output lands in its own child
// transcript, so a rail held open for the whole run only pushes every
// concurrent tool one column right.
func TestPi_AgentToolStartOpensNoSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-spawn","toolName":"Agent","input":{"description":"explore","prompt":"look around"}}`)))

	assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
	assert.Empty(t, sink.ReservedColorSpans(), "and reserves no color")
	assert.Equal(t, contracts.PiToolAgent, sink.GetSpanType("tc-spawn"),
		"the span type is still recorded for tool_execution_end")

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tc-spawn", msgs[0].SpanID, "the row still carries the span id")
	assert.Empty(t, msgs[0].SpansOpenAtPersist, "nothing else was open, so no rail")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID, "the prompt is inspectable before the child finishes")
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"look around"}`, string(child.Messages()[0].Content))
}

// Only the spawn loses its span. Every other Pi tool -- including the two
// subagent-control tools, which act on an agent that already runs -- keeps one.
func TestPi_NonSpawnToolsStillOpenASpan(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{contracts.PiToolBash, contracts.PiToolRead, "get_subagent_result", "steer_subagent"} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"tool_execution_start","toolCallId":"tc-1","toolName":"`+toolName+`","input":{"prompt":"x"}}`)))

			open := sink.OpenSpans()
			require.Len(t, open, 1)
			assert.Equal(t, "tc-1", open[0].SpanID)
			assert.Equal(t, []string{"tc-1"}, sink.ReservedColorSpans())
		})
	}
}

// A spawn that starts while a bash call is running draws that call's rail and
// nothing more -- one column, not two.
func TestPi_SpawnInsideOpenToolDrawsOneColumn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-bash","toolName":"bash","input":{"command":"ls"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-spawn","toolName":"Agent","input":{"prompt":"go"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-spawn","toolName":"Agent","result":{"status":"completed"}}`)))

	msgs := sink.Messages()
	require.Len(t, msgs, 3)
	for i, msg := range msgs[1:] {
		require.Len(t, msg.SpansOpenAtPersist, 1, "spawn row %d draws exactly one column", i)
		assert.Equal(t, "tc-bash", msg.SpansOpenAtPersist[0].SpanID)
	}
	assert.True(t, msgs[2].Closing, "the end row is still a closer")

	open := sink.OpenSpans()
	require.Len(t, open, 1, "only the bash call ever opened a span")
	assert.Equal(t, "tc-bash", open[0].SpanID)
}

func TestPi_NativeSubagentResultAndNotification(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"native-call","toolName":"Agent","args":{"description":"Inspect the sample","prompt":"Read the sample."}}`)))
	result := []byte(`{"type":"tool_execution_end","toolCallId":"native-call","toolName":"Agent","result":{"content":[{"type":"text","text":"Agent started"}],"details":{"status":"background","agentId":"native-agent"}},"isError":false}`)
	handlePiOutput(a, providerkit.ParseLine(result))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "native-agent", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, "Inspect the sample", tasks[0].Title)
	assert.NotEmpty(t, tasks[0].ChildAgentID)
	assert.Equal(t, result, sink.Messages()[1].Content)

	notification := []byte(`{"type":"message_end","message":{"role":"custom","customType":"subagent-notification","content":"Child finished","display":true,"details":{"id":"native-agent","description":"Inspect the sample","status":"completed","resultPreview":"Read the sample."}}}`)
	handlePiOutput(a, providerkit.ParseLine(notification))
	tasks = sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
	assert.Equal(t, notification, sink.Messages()[2].Content)
	child := sink.Child(tasks[0].ChildAgentID)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Read the sample.", reports[0]["text"])
}

func TestPi_OrdinaryToolStatusCreatesNoSubagent(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"ordinary","toolName":"get_goal","args":{}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_update","toolCallId":"ordinary","toolName":"get_goal","partialResult":{"content":[],"details":{"status":"running"}}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_end","toolCallId":"ordinary","toolName":"get_goal","result":{"content":[],"details":{"status":"completed"}}}`)))
	assert.Empty(t, sink.BackgroundTasks())
}

func TestPi_WorkflowLaunchOpensNoSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"workflow-call","toolName":"SubagentWorkflow","args":{"script":"export const meta = { name: 'Probe', description: 'Probe' }; return 0;"}}`)))
	assert.Empty(t, sink.OpenSpans())
	assert.Empty(t, sink.ReservedColorSpans())
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_end","toolCallId":"workflow-call","toolName":"SubagentWorkflow","result":{"content":[{"type":"text","text":"Workflow started"}],"details":{"taskId":"wf_probe"}},"isError":false}`)))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "wf_probe", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"message_end","message":{"role":"custom","customType":"subagent-notification","content":"Workflow complete","details":{"id":"wf_probe","description":"Workflow probe","status":"completed"}}}`)))
	tasks = sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
	for _, message := range sink.Messages() {
		assert.Empty(t, message.SpansOpenAtPersist)
	}
}

func TestPi_SubagentFinalResultReplacesTheProvisionalRow(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"background", "completed", "error", "stopped"} {
		t.Run(status, func(t *testing.T) {
			sink := &agenttest.Sink{}
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "tool-call", Kind: bgtask.KindSubagent, Title: "Inspect sample", Status: bgtask.StatusRunning}))
			piApplySubagentEnd(sink, json.RawMessage(`{"content":[],"details":{"agentId":"child","status":"`+status+`"}}`), "tool-call", "Inspect sample", "")
			tasks := sink.BackgroundTasks()
			require.Len(t, tasks, 1)
			assert.Equal(t, "child", tasks[0].RowKey)
			if status == "background" {
				assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
			} else {
				assert.True(t, tasks[0].Status.IsFinished())
			}
		})
	}
}

// piRenameFailureSink refuses every registry rename, as a busy store does.
type piRenameFailureSink struct{ *agenttest.Sink }

func (piRenameFailureSink) RenameBackgroundTask(string, string) error {
	return errors.New("the background-task store is busy")
}

func TestPiApplySubagentEndClosesTheOriginalRowWhenTheRenameFails(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// tool_execution_start opened the row under the tool-call id.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "tc-1", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
	result := json.RawMessage(`{"content":[],"details":{"status":"completed","agentId":"a-1"}}`)
	piApplySubagentEnd(piRenameFailureSink{sink}, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "tc-1", tasks[0].RowKey)
	// piApplySubagentNotification writes to the agent id, and no row holds that key,
	// so a skipped final status would leave the row Running for the whole session.
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status)
}

func TestPiApplySubagentEndKeepsTheToolCallRowWhenTheBackgroundRenameFails(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "tc-1", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
	result := json.RawMessage(`{"content":[],"details":{"status":"background","agentId":"a-1"}}`)
	piApplySubagentEnd(piRenameFailureSink{sink}, result, "tc-1", "title", "spawn prompt")
	assert.Equal(t, []string{"tc-1"}, agenttest.RowKeys(sink), "a row that kept its key must not gain a second row")
	assert.Equal(t, []string{"child-of-tc-1"}, sink.ChildAgentIDs(),
		"the child remains linked to the original row when the rename fails")
}

func TestPi_AgentToolEndWithoutDetailsStatusClosesRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		isError bool
		want    bgtask.Status
	}{
		{name: "errored tool closes as failed", isError: true, want: bgtask.StatusFailed},
		{name: "non-error abort closes as stopped", isError: false, want: bgtask.StatusStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"tool_execution_start","toolCallId":"tc-err","toolName":"Agent","args":{"description":"Inspect","prompt":"Check files"}}`)))
			endLine := `{"type":"tool_execution_end","toolCallId":"tc-err","toolName":"Agent","result":{"content":[{"type":"text","text":"operation aborted"}]},"isError":` + strconv.FormatBool(tc.isError) + `}`
			handlePiOutput(a, providerkit.ParseLine([]byte(endLine)))

			tasks := sink.BackgroundTasks()
			require.Len(t, tasks, 1)
			assert.Equal(t, "tc-err", tasks[0].RowKey)
			assert.Equal(t, tc.want, tasks[0].Status)
		})
	}
}

func TestPi_PersistIncompletePiToolsClosesInFlightAgentRowsOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		completion agent.MessageCompletion
		want       bgtask.Status
	}{
		{name: "error closes as failed", completion: agent.MessageCompletionError, want: bgtask.StatusFailed},
		{name: "interrupt closes as stopped", completion: agent.MessageCompletionInterrupted, want: bgtask.StatusStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			// Start and background one Agent row first; ordinary agent_end must preserve it.
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"tool_execution_start","toolCallId":"tc-bg","toolName":"Agent","args":{"description":"Background","prompt":"Run in bg"}}`)))
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"tool_execution_end","toolCallId":"tc-bg","toolName":"Agent","result":{"content":[],"details":{"status":"background","agentId":"bg-1"}},"isError":false}`)))
			// Start an in-flight Agent row that never finishes.
			handlePiOutput(a, providerkit.ParseLine([]byte(
				`{"type":"tool_execution_start","toolCallId":"tc-live","toolName":"Agent","args":{"description":"Live","prompt":"Still running"}}`)))

			a.persistIncompletePiTools(tc.completion)

			liveRow, ok := sink.BackgroundTask("tc-live")
			require.True(t, ok)
			assert.Equal(t, tc.want, liveRow.Status, "in-flight Agent row closes on incomplete tool cleanup")
			bgRow, ok := sink.BackgroundTask("bg-1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusRunning, bgRow.Status, "already-backgrounded row stays running across ordinary turn end")
		})
	}
}

func TestPi_ClearContextStopAndWaitCloseOpenBackgroundTasks(t *testing.T) {
	t.Parallel()

	openBackgroundRows := func(a *Agent) {
		handlePiOutput(a, providerkit.ParseLine([]byte(
			`{"type":"tool_execution_start","toolCallId":"tc-bg","toolName":"Agent","args":{"description":"Bg agent","prompt":"Work"}}`)))
		handlePiOutput(a, providerkit.ParseLine([]byte(
			`{"type":"tool_execution_end","toolCallId":"tc-bg","toolName":"Agent","result":{"content":[],"details":{"status":"background","agentId":"bg-agent"}},"isError":false}`)))
		handlePiOutput(a, providerkit.ParseLine([]byte(
			`{"type":"tool_execution_start","toolCallId":"tc-wf","toolName":"SubagentWorkflow","args":{"script":"return 0;"}}`)))
		handlePiOutput(a, providerkit.ParseLine([]byte(
			`{"type":"tool_execution_end","toolCallId":"tc-wf","toolName":"SubagentWorkflow","result":{"content":[],"details":{"taskId":"wf-1"}},"isError":false}`)))
	}

	t.Run("ClearContext", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
			switch req.Type {
			case CommandNewSession:
				return json.RawMessage(`{"cancelled":false}`), true, ""
			case CommandGetState:
				return json.RawMessage(`{"sessionId":"s2","sessionFile":"/tmp/s2.jsonl"}`), true, ""
			}
			return nil, true, ""
		})
		openBackgroundRows(rig.agent)
		_, err := rig.agent.ClearContext()
		require.NoError(t, err)

		for _, key := range []string{"bg-agent", "wf-1"} {
			row, ok := sink.BackgroundTask(key)
			require.True(t, ok, "row %s exists", key)
			assert.Equal(t, bgtask.StatusStopped, row.Status, "ClearContext closes %s", key)
		}
	})

	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		openBackgroundRows(rig.agent)
		rig.agent.SimulateExitForTest()
		rig.agent.Stop()

		for _, key := range []string{"bg-agent", "wf-1"} {
			row, ok := sink.BackgroundTask(key)
			require.True(t, ok, "row %s exists", key)
			assert.True(t, row.Status.IsFinished(), "Stop closes %s", key)
		}
	})

	t.Run("Wait", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		openBackgroundRows(rig.agent)
		rig.agent.SimulateExitForTest()
		_ = rig.agent.Wait()

		for _, key := range []string{"bg-agent", "wf-1"} {
			row, ok := sink.BackgroundTask(key)
			require.True(t, ok, "row %s exists", key)
			assert.True(t, row.Status.IsFinished(), "Wait closes %s", key)
		}
	})
}

func TestPi_GetSubagentResultClosesBackgroundRowAndKeepsSpan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-spawn","toolName":"Agent","args":{"description":"Inspect","prompt":"Run checks"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-spawn","toolName":"Agent","result":{"content":[],"details":{"status":"background","agentId":"bg-1"}},"isError":false}`)))
	row, ok := sink.BackgroundTask("bg-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, row.Status)
	childID := row.ChildAgentID

	// Polling while still running must not close the row.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-poll-1","toolName":"get_subagent_result","args":{"agentId":"bg-1"}}`)))
	assert.Len(t, sink.OpenSpans(), 1, "get_subagent_result keeps its normal tool span")
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-poll-1","toolName":"get_subagent_result","result":{"content":[{"type":"text","text":"still running"}],"details":{"agentId":"bg-1","status":"running"}},"isError":false}`)))
	row, _ = sink.BackgroundTask("bg-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "non-final get_subagent_result keeps the row running")

	// Final result closes the background row and persists its report.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-poll-2","toolName":"get_subagent_result","args":{"agentId":"bg-1"}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-poll-2","toolName":"get_subagent_result","result":{"content":[{"type":"text","text":"All checks green."}],"details":{"status":"completed"}},"isError":false}`)))

	row, _ = sink.BackgroundTask("bg-1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Len(t, sink.OpenSpans(), 2, "both get_subagent_result calls opened normal tool spans")
	assert.Contains(t, sink.ClosedSpans(), "tc-poll-1")
	assert.Contains(t, sink.ClosedSpans(), "tc-poll-2")
	reports := sink.Child(childID).LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "All checks green.", reports[0]["text"])
}

func TestPi_AgentResumeRevivesExistingRowWithoutSecondChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	// Job 1 spawns and completes under agent-1.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-1","toolName":"Agent","args":{"description":"Review","prompt":"First pass."}}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-1","toolName":"Agent","result":{"content":[{"type":"text","text":"First report."}],"details":{"status":"completed","agentId":"agent-1"}},"isError":false}`)))

	row1, ok := sink.BackgroundTask("agent-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row1.Status)
	childID := row1.ChildAgentID
	require.NotEmpty(t, childID)

	// Job 2 resumes agent-1 with a follow-up prompt.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-2","toolName":"Agent","args":{"resume":"agent-1","description":"Follow up","prompt":"Second pass."}}`)))

	assert.Equal(t, []string{"agent-1"}, agenttest.RowKeys(sink), "resume must not create a second row under tc-2")
	assert.Equal(t, []string{childID}, sink.ChildAgentIDs(), "resume must not create a second ChildAgentID")
	assert.Equal(t, []string{"agent-1"}, sink.RevivedTasks(), "resume must revive the finished row")
	rowMid, _ := sink.BackgroundTask("agent-1")
	assert.Equal(t, bgtask.StatusRunning, rowMid.Status)

	// Follow-up prompt is appended to the existing child transcript.
	childMsgs := sink.Child(childID).Messages()
	require.Len(t, childMsgs, 2)
	assert.JSONEq(t, `{"content":"First pass."}`, string(childMsgs[0].Content))
	assert.JSONEq(t, `{"content":"Second pass."}`, string(childMsgs[1].Content))

	// Job 2 finishes and stores its report under toolCallID so it is not deduplicated against Job 1.
	handlePiOutput(a, providerkit.ParseLine([]byte(
		`{"type":"tool_execution_end","toolCallId":"tc-2","toolName":"Agent","result":{"content":[{"type":"text","text":"Second report."}],"details":{"status":"completed","agentId":"agent-1"}},"isError":false}`)))

	assert.Equal(t, []string{"agent-1"}, agenttest.RowKeys(sink))
	row2, _ := sink.BackgroundTask("agent-1")
	assert.Equal(t, bgtask.StatusSucceeded, row2.Status)
	reports := sink.Child(childID).LeapMuxNotifications()
	require.Len(t, reports, 2, "Job 2 report must not be deduplicated against Job 1")
	assert.Equal(t, "First report.", reports[0]["text"])
	assert.Equal(t, "Second report.", reports[1]["text"])
}
