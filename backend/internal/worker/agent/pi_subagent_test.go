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

func TestPi_TerminalStatus(t *testing.T) {
	s, ok := piTerminalStatus("completed")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, s)
	s, ok = piTerminalStatus("error")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, s)
	s, ok = piTerminalStatus("stopped")
	assert.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, s)
	_, ok = piTerminalStatus("running")
	assert.False(t, ok)
}

func TestPi_ApplySubagentEnd_Terminal(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"completed","agentId":"a-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

func TestPi_ApplySubagentEnd_BackgroundRekey(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"background","agentId":"bg-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "bg-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
}

// An unrecognized status must NOT terminalize the row. piApplySubagentEnd keeps
// the row Running so a later terminal event can still close it.
func TestPi_ApplySubagentEnd_UnrecognizedStatusStaysRunning(t *testing.T) {
	sink := &testSink{}
	result := json.RawMessage(`{"status":"thinking","agentId":"a-1"}`)
	piApplySubagentEnd(sink, result, "tc-1", "title")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "a-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status, "unrecognized status stays running")
}

func TestPi_ApplySubagentEnd_FallbackRegex(t *testing.T) {
	sink := &testSink{}
	// A standalone "Agent ID: X" line matches the anchored regex. The row keys
	// off the deterministic toolCallID (not the captured prose) so a later
	// terminal event can close it. The input is a JSON-encoded string.
	piApplySubagentEnd(sink, json.RawMessage(`"Agent ID: regex-1\n"`), "tc-1", "title")
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "tc-1", tasks[0].RowKey, "row keyed off toolCallID, not the captured prose")
}

// Free-form prose that mentions "Agent ID:" mid-sentence must NOT match the
// anchored regex and must NOT create a phantom registry row.
func TestPi_ApplySubagentEnd_FallbackRegexIgnoresProse(t *testing.T) {
	sink := &testSink{}
	piApplySubagentEnd(sink, json.RawMessage(`"the model said Agent ID: something here"`), "tc-1", "title")
	assert.Empty(t, sink.BackgroundTasks(), "mid-sentence mention does not create a phantom row")
}

func TestPi_ApplySubagentNotification_Terminal(t *testing.T) {
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
// first-line truncate counts RUNES, not bytes. A byte slice at 80 on a
// multi-byte-rune prompt splits the rune and emits invalid UTF-8, which fails
// the proto broadcast marshal.
func TestPi_ExtractDescription_MultibyteTruncateNoInvalidUTF8(t *testing.T) {
	// 100 U+4E2D ("中") runes; each is 3 bytes. Byte offset 80 lands mid-rune
	// (26 full runes = 78 bytes + 2 bytes of the 27th rune).
	runes := strings.Repeat("中", 100)
	got := piExtractDescription(json.RawMessage(`{"prompt":`+strconv.Quote(runes)+`}`), "tool")
	assert.Equal(t, strings.Repeat("中", 80), got, "exactly 80 runes")
	assert.True(t, utf8.ValidString(got), "title must be valid UTF-8")
}

// Compile-time check: ensure the helpers participate in the package's sink
// interface (the testSink satisfies OutputSink).
var _ OutputSink = (*testSink)(nil)
var _ leapmuxv1.MessageSource
