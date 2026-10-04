package ohmypi

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want agent.NotificationClassification
	}{
		{
			name: "a finished automatic compaction is the boundary",
			raw:  `{"type":"auto_compaction_end","action":"snapcompact","result":{"summary":"s"},"aborted":false,"willRetry":false}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "omp:auto_compaction_end"},
		},
		{
			name: "a compaction in progress is a status",
			raw:  `{"type":"auto_compaction_start","reason":"threshold","action":"snapcompact"}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "omp:auto_compaction_start"},
		},
		{
			name: "a retry is a retry notice",
			raw:  `{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92,"errorMessage":"400"}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "omp:auto_retry_start"},
		},
		{
			name: "a retry's end is a retry notice",
			raw:  `{"type":"auto_retry_end","success":true,"attempt":1}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "omp:auto_retry_end"},
		},
		{
			name: "the response to the worker's compact command is the boundary",
			raw:  `{"type":"response","id":"leapmux-4","command":"compact","success":true,"data":{"summary":"s","tokensBefore":60030}}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "omp:compact"},
		},
		{
			name: "a refused compact command is a status",
			raw:  `{"type":"response","id":"leapmux-4","command":"compact","success":false,"error":"Nothing to compact (session too small)"}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "omp:compact"},
		},
		{name: "a response to another command is not grouped", raw: `{"type":"response","id":"leapmux-5","command":"prompt","success":true}`},
		{name: "a notice is not grouped", raw: `{"type":"notice","level":"info","message":"x"}`},
		{name: "a frame with no type is not grouped", raw: `{"x":1}`},
		{name: "malformed JSON is not grouped", raw: `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ompProvider{}.Classify(json.RawMessage(tc.raw)))
		})
	}
}

func TestIsInterrupt(t *testing.T) {
	t.Parallel()
	assert.True(t, ompProvider{}.IsInterrupt(`{"id":"leapmux-3","type":"abort"}`), "the frame Interrupt writes")
	assert.False(t, ompProvider{}.IsInterrupt(`{"id":"leapmux-3","type":"prompt","message":"abort"}`))
	assert.False(t, ompProvider{}.IsInterrupt(`abort`))
	assert.False(t, ompProvider{}.IsInterrupt(``))
}

func TestResolveProviderData(t *testing.T) {
	t.Parallel()
	start := `{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","args":{"command":"sleep 60"}}`
	supplement := func(toolCallID, toolName, partial string) []byte {
		encoded, err := json.Marshal(contracts.OhMyPiIncompleteToolSupplement{
			ToolCallID: toolCallID, ToolName: toolName, PartialResult: json.RawMessage(partial),
		})
		require.NoError(t, err)
		return encoded
	}
	partial := `{"content":[{"type":"text","text":"so far"}],"details":{}}`

	t.Run("puts the partial result on the start frame", func(t *testing.T) {
		got := ompProvider{}.ResolveProviderData(agent.MessageContent{Original: []byte(start), Supplemental: supplement("call_1", "bash", partial)})
		var frame map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(got, &frame))
		assert.JSONEq(t, partial, string(frame[contracts.OhMyPiFrameFieldResult]))
		assert.JSONEq(t, `{"command":"sleep 60"}`, string(frame[contracts.OhMyPiFrameFieldArgs]), "the rest of the frame stays")
	})

	t.Run("a resolved frame resolves to the same bytes", func(t *testing.T) {
		once := ompProvider{}.ResolveProviderData(agent.MessageContent{Original: []byte(start), Supplemental: supplement("call_1", "bash", partial)})
		twice := ompProvider{}.ResolveProviderData(agent.MessageContent{Original: once, Supplemental: supplement("call_1", "bash", partial)})
		assert.Equal(t, once, twice)
	})

	unchanged := []struct {
		name         string
		original     string
		supplemental []byte
	}{
		{name: "no supplement", original: start},
		{name: "a malformed supplement", original: start, supplemental: []byte(`{`)},
		{name: "a supplement with no call id", original: start, supplemental: supplement("", "bash", partial)},
		{name: "a supplement with no partial result", original: start, supplemental: supplement("call_1", "bash", "")},
		{name: "another call's supplement", original: start, supplemental: supplement("call_2", "bash", partial)},
		{name: "another tool's supplement", original: start, supplemental: supplement("call_1", "read", partial)},
		{name: "a frame that is not a start frame", original: `{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash"}`, supplemental: supplement("call_1", "bash", partial)},
		{name: "a malformed frame", original: `{`, supplemental: supplement("call_1", "bash", partial)},
		{name: "a null frame", original: `null`, supplemental: supplement("call_1", "bash", partial)},
	}
	for _, tc := range unchanged {
		t.Run(tc.name+" leaves the frame unchanged", func(t *testing.T) {
			got := ompProvider{}.ResolveProviderData(agent.MessageContent{Original: []byte(tc.original), Supplemental: tc.supplemental})
			assert.Equal(t, tc.original, string(got))
		})
	}
}

func TestValidateAttachment(t *testing.T) {
	t.Parallel()
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage} {
		assert.NoError(t, ompProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "a", Kind: kind}), kind)
	}
	assert.ErrorContains(t, ompProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "a.pdf", Kind: agent.AttachmentKindPDF}), "Oh My Pi does not support PDF")
	assert.ErrorContains(t, ompProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "a.bin", Kind: agent.AttachmentKindBinary}), "Oh My Pi does not support binary")
}

func TestResolveResumeHandle(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	file := filepath.Join(home, ".omp", "agent", "sessions", "-p", "2026-09-23T18-11-57-284Z_01a0cf77.jsonl")

	got, err := ompProvider{}.ResolveResumeHandle(file, home)
	require.NoError(t, err)
	assert.Equal(t, file, got, "a session file is the handle the worker stores")

	got, err = ompProvider{}.ResolveResumeHandle("01a0cf77-9ae4-72d8-9a42-665c431d3beb", home)
	require.NoError(t, err)
	assert.Equal(t, "01a0cf77-9ae4-72d8-9a42-665c431d3beb", got, "an id is accepted too, as omp's --resume accepts it")

	got, err = ompProvider{}.ResolveResumeHandle("", home)
	require.NoError(t, err)
	assert.Empty(t, got, "no handle means no resume")

	_, err = ompProvider{}.ResolveResumeHandle("../escape.jsonl", home)
	assert.Error(t, err, "a relative path is refused")
	_, err = ompProvider{}.ResolveResumeHandle("id\x07bell", home)
	assert.Error(t, err, "an id with a control character is refused")
	_, err = ompProvider{}.ResolveResumeHandle(" 01a0cf77", home)
	assert.Error(t, err, "an id with edge whitespace is refused")
}

func TestResolveControlResponse(t *testing.T) {
	t.Parallel()

	t.Run("preserves a dialog answer", func(t *testing.T) {
		response := []byte(`{"type":"extension_ui_response","id":"dlg-1","value":"Approve"}`)
		res := ompProvider{}.ResolveControlResponse(agent.ControlResponseContext{
			RequestPayload:  []byte(`{"type":"extension_ui_request","id":"dlg-1","method":"select","title":"Allow tool: bash","options":["Approve","Deny"]}`),
			ResponseContent: response,
		})
		assert.Equal(t, response, res.Content)
		assert.False(t, res.Withhold)
		assert.Equal(t, "dlg-1", ompProvider{}.ControlResponseRequestID(response), "the dialog id finds the stored request")
	})

	t.Run("withholds the response for a malformed request", func(t *testing.T) {
		agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, ompProvider{})
	})

	t.Run("preserves the response without a request", func(t *testing.T) {
		agenttest.AssertPreservesTheResponseWithoutARequest(t, ompProvider{})
	})
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
