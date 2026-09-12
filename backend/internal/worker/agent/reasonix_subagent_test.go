package agent

import (
	"encoding/json"
	"testing"

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
		update := decodeToolCallUpdateUpdate(t, `{"toolCallId":"task","status":"completed","content":[{"type":"content","content":{"type":"text","text":`+string(text)+`}}]}`)
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
		update := decodeToolCallUpdateUpdate(t, `{"toolCallId":"task","status":"completed","title":"read_only_task","rawInput":{"prompt":"Read an example"},"content":[{"type":"content","content":{"type":"text","text":`+string(text)+`}}]}`)
		observation := reasonixSubagentFromToolCallUpdate(update)
		require.NotNil(t, observation)
		assert.Equal(t, bgtask.StatusCompleted, observation.Status)
	}
}

func TestReasonixSubagentCancellationSurvivesACPFailureStatus(t *testing.T) {
	t.Parallel()
	update := decodeToolCallUpdateUpdate(t, `{"toolCallId":"task","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Subagent outcome: status=cancelled retryable=false"}}]}`)
	observation := reasonixSubagentFromToolCallUpdate(update)
	require.NotNil(t, observation)
	assert.Equal(t, bgtask.StatusStopped, observation.Status)
}

func TestReasonixSubagentRequiresTaskIdentity(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"", "web_search", "read_file"} {
		assert.Nil(t, reasonixSubagentFromToolCall(acpToolCallEnvelope{
			ToolCallID: "call", Title: title, RawInput: json.RawMessage(`{"prompt":"Search for the answer"}`),
		}), "a prompt does not identify the tool %q", title)
	}
}

func TestReasonixTaskArgumentsDoNotChangeLaunchLayout(t *testing.T) {
	t.Parallel()
	for _, toolName := range []string{"task", "read_only_task"} {
		for _, arguments := range []string{`null`, `{}`, `[]`, `{"prompt":0}`, `{"prompt":false}`, `{"prompt":null}`, `{"prompt":""}`, `{"prompt":" \n\t"}`} {
			sink := &testSink{}
			base := &acpBase{sink: sink, subagentFromToolCall: reasonixSubagentFromToolCall, subagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate}
			request, err := json.Marshal(map[string]any{"toolCallId": "call", "title": toolName, "kind": "other", "status": "pending", "rawInput": json.RawMessage(arguments)})
			require.NoError(t, err)
			base.handleToolCall(request)
			assert.Empty(t, sink.OpenSpans(), arguments)
			assert.Empty(t, sink.ReservedColorSpans(), arguments)
			require.Len(t, sink.Messages(), 1)
			assert.True(t, sink.Messages()[0].NoSpan, arguments)
			base.handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call","status":"failed"}`))
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
		tc := acpToolCallEnvelope{ToolCallID: "call", Title: "read_only_task", RawInput: json.RawMessage(tt.input)}
		observation := reasonixSubagentFromToolCall(tc)
		require.NotNil(t, observation)
		assert.True(t, observation.Spawns)
		assert.Equal(t, tt.want, observation.Title)
		assert.Equal(t, tt.input, string(tc.RawInput))
	}
}
