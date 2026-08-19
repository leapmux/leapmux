package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

func TestPi_FinalStatus(t *testing.T) {
	s, ok := piFinalStatus("completed")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, s)
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
	sink := &testSink{}
	result := json.RawMessage(`{"status":"completed","agentId":"a-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

func TestPi_ApplySubagentEnd_BackgroundRekey(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"background","agentId":"bg-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "bg-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	// The re-keyed row links to a child transcript (EnsureChildAgent on the
	// spawn span tc-1) so the task is openable as a child tab, not just a
	// registry-only row the user cannot inspect.
	assert.Equal(t, "child-of-tc-1", tasks[0].ChildAgentID, "re-keyed row carries child linkage")
}

// An unrecognized status must NOT give a final status to the row. piApplySubagentEnd keeps
// the row Running so a later final event can still close it.
func TestPi_ApplySubagentEnd_UnrecognizedStatusStaysRunning(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"thinking","agentId":"a-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status, "unrecognized status stays running")
}

func TestPi_ApplySubagentEnd_FallbackRegex(t *testing.T) {
	sink := &testSink{}
	// A standalone "Agent ID: X" line matches the anchored regex. The row keys
	// off the deterministic toolCallID (not the captured prose) so a later
	// final event can close it. The input is a JSON-encoded string.
	piApplySubagentEnd(sink, json.RawMessage(`"Agent ID: regex-1\n"`), "tc-1", "title", "")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "tc-1", tasks[0].RowKey, "row keyed off toolCallID, not the captured prose")
}

// Free-form prose that mentions "Agent ID:" mid-sentence must NOT match the
// anchored regex and must NOT create a phantom registry row.
func TestPi_ApplySubagentEnd_FallbackRegexIgnoresProse(t *testing.T) {
	sink := &testSink{}
	piApplySubagentEnd(sink, json.RawMessage(`"the model said Agent ID: something here"`), "tc-1", "title", "")
	assert.Empty(t, sink.BackgroundTasks(), "mid-sentence mention does not create a phantom row")
}

func TestPi_ApplySubagentNotification_FinalStatus(t *testing.T) {
	sink := &testSink{}
	msg, _ := json.Marshal(map[string]any{
		"customType": "subagent-notification",
		"details": map[string]any{
			"status":  "completed",
			"agentId": "a-1",
		},
	})
	piApplySubagentNotification(sink, msg)
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

func TestPi_ApplySubagentNotification_GroupOthers(t *testing.T) {
	sink := &testSink{}
	msg, _ := json.Marshal(map[string]any{
		"customType": "subagent-notification",
		"details": map[string]any{
			"status":  "running",
			"agentId": "a-1",
			"others": []map[string]any{
				{"agentId": "a-2", "status": "completed"},
				{"agentId": "a-3", "status": "error"},
			},
		},
	})
	piApplySubagentNotification(sink, msg)
	tasks := sink.BackgroundTasks()
	// a-1 (running) + a-2 (completed) + a-3 (failed) = 3 rows.
	require.Len(t, tasks, 3)
}

func TestPi_ApplySubagentNotification_NonNotificationNoop(t *testing.T) {
	sink := &testSink{}
	msg, _ := json.Marshal(map[string]any{"customType": "other"})
	piApplySubagentNotification(sink, msg)
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

	// A model-written description is no more bounded than a prompt is, so the
	// description branch caps too.
	long := strings.Repeat("a", 500)
	got = piExtractDescription(json.RawMessage(`{"description":`+strconv.Quote(long)+`}`), "tool")
	assert.Equal(t, strings.Repeat("a", 80), got, "the 80-rune display cap binds for ASCII")
}

// Compile-time check: ensure the helpers participate in the package's sink
// interface (the testSink satisfies OutputSink).
var _ OutputSink = (*testSink)(nil)
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

// A Pi subagent's child transcript is created only on the background re-key, so
// that is where the spawn prompt becomes its first message.
func TestPi_ApplySubagentEnd_BackgroundRekeyPersistsThePrompt(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"background","agentId":"bg-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title", "Write the essay.")

	child, ok := sink.ChildSink("child-of-tc-1").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source)
	assert.JSONEq(t, `{"content":"Write the essay."}`, string(msgs[0].Content))
}

// A foreground subagent never re-keys, so no child transcript is created and
// nothing is written.
//
// Asserted on the REGISTRY ROW rather than on ChildSink's messages: ChildSink
// creates its recording sink on demand, so the empty-message assertion alone
// would still pass if the code wrongly created a child agent here.
func TestPi_ApplySubagentEnd_FinalStatusWritesNoPrompt(t *testing.T) {
	sink := &testSink{}
	piApplySubagentEnd(sink, json.RawMessage(`{"status":"completed","agentId":"a-1"}`), "tc-1", "title", "Write the essay.")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].ChildAgentID, "a foreground subagent gets no child transcript")
	child, ok := sink.ChildSink("child-of-tc-1").(*testSink)
	require.True(t, ok)
	assert.Empty(t, child.Messages())
}

// --- A subagent spawn owns no span ---

// pi-subagents registers its spawn tool as `Agent` (SUBAGENT_TOOL_NAMES.AGENT).
// That call owns no span: the subagent's output lands in its own child
// transcript, so a rail held open for the whole run only pushes every
// concurrent tool one column right.
func TestPi_AgentToolStartOpensNoSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newPiAgentWithSink(sink)
	handlePiOutput(a, parseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-spawn","toolName":"Agent","input":{"description":"explore","prompt":"look around"}}`)))

	assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
	assert.Empty(t, sink.ReservedColorSpans(), "and reserves no color")
	assert.Equal(t, PiToolAgent, sink.GetSpanType("tc-spawn"),
		"the span type is still recorded for tool_execution_end")

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tc-spawn", msgs[0].SpanID, "the row still carries the span id")
	assert.Empty(t, msgs[0].SpansOpenAtPersist, "nothing else was open, so no rail")
}

// Only the spawn loses its span. Every other Pi tool -- including the two
// subagent-control tools, which act on an agent that already runs -- keeps one.
func TestPi_NonSpawnToolsStillOpenASpan(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{PiToolBash, PiToolRead, "get_subagent_result", "steer_subagent"} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a := newPiAgentWithSink(sink)
			handlePiOutput(a, parseLine([]byte(
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

	sink := &testSink{}
	a := newPiAgentWithSink(sink)
	handlePiOutput(a, parseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-bash","toolName":"bash","input":{"command":"ls"}}`)))
	handlePiOutput(a, parseLine([]byte(
		`{"type":"tool_execution_start","toolCallId":"tc-spawn","toolName":"Agent","input":{"prompt":"go"}}`)))
	handlePiOutput(a, parseLine([]byte(
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
