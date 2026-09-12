package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeMessageContentConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../../testdata/zcode_message_content_conformance.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name         string          `json:"name"`
			Original     json.RawMessage `json:"original"`
			Supplemental json.RawMessage `json:"supplemental"`
			Expected     json.RawMessage `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			original := append([]byte(nil), tc.Original...)
			supplemental := append([]byte(nil), tc.Supplemental...)
			resolved := zcodeProvider{}.ResolveProviderData(MessageContent{Original: tc.Original, Supplemental: tc.Supplemental})
			assert.JSONEq(t, string(tc.Expected), string(resolved))
			assert.Equal(t, original, []byte(tc.Original))
			assert.Equal(t, supplemental, []byte(tc.Supplemental))
		})
	}
}

func TestZCodeToolInputSupplement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		existing string
		input    string
		want     string
	}{
		{"existing input", `{"command":"original"}`, `{"command":"cached"}`, ""},
		{"partial input", `{"command":"original"}`, `{"command":"cached","plan":"# Plan"}`, `{"plan":"# Plan"}`},
		{"explicit null field", `{"plan":null}`, `{"plan":"# Plan","count":0}`, `{"count":0}`},
		{"absent cache", "", "", ""},
		{"invalid cache", "", `{invalid`, ""},
		{"array cache", "", `[1,2]`, ""},
		{"empty cache", "", `{ }`, ""},
		{"null cache", "", `null`, ""},
		{"null input", `null`, `{"a":1}`, `{"a":1}`},
		{"empty input with whitespace", "{ \n }", `{"a":1}`, `{"a":1}`},
		{"invalid original input", `[1,2]`, `{"a":1}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := zcodeToolUpdated{Kind: contracts.ZCodeToolKindScheduled, ToolCallID: "call", Input: json.RawMessage(tc.existing)}
			got, err := zcodeToolInputSupplement(payload, json.RawMessage(tc.input))
			require.NoError(t, err)
			assert.Equal(t, tc.existing, string(payload.Input))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.JSONEq(t, `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","input":`+tc.want+`}}`, string(got))
		})
	}
}

func TestZCodeToolInputSupplementRequiresRequestIdentity(t *testing.T) {
	t.Parallel()
	for _, payload := range []zcodeToolUpdated{{Kind: contracts.ZCodeToolKindScheduled}, {Kind: contracts.ZCodeToolKindResult, ToolCallID: "call"}} {
		got, err := zcodeToolInputSupplement(payload, json.RawMessage(`{"a":1}`))
		require.NoError(t, err)
		assert.Empty(t, got)
	}
}

func TestZCodeControlInputPreservesEarlierSupplement(t *testing.T) {
	t.Parallel()
	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming, `{"kind":"tool_call","toolCallId":"call","toolName":"ExitPlanMode","input":{"allowedPrompts":[],"future":9007199254740993}}`))
	original := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","toolName":"ExitPlanMode"}}`)
	event, ok := parseZCodeEvent(original)
	require.True(t, ok)
	a.dispatchZCodeEvent(event)
	matched, err := a.supplementZCodeControlInput("call", "ExitPlanMode", json.RawMessage(`{"plan":"# Plan"}`))
	require.NoError(t, err)
	require.True(t, matched)
	message := sink.Messages()[0]
	assert.Equal(t, original, message.Content)
	resolved := zcodeProvider{}.ResolveProviderData(MessageContent{Original: message.Content, Supplemental: message.SupplementalContent})
	assert.Contains(t, string(resolved), `"plan":"# Plan"`)
	assert.Contains(t, string(resolved), `"allowedPrompts":[]`)
	assert.Contains(t, string(resolved), `9007199254740993`)
	matched, err = a.supplementZCodeControlInput("call", "ExitPlanMode", json.RawMessage(`{"plan":"# Plan"}`))
	require.NoError(t, err)
	assert.True(t, matched)
	assert.Equal(t, message.SupplementalRevision, sink.Messages()[0].SupplementalRevision)
}

type zcodeRejectedEnrichmentSink struct {
	recordingControlSink
}

func (*zcodeRejectedEnrichmentSink) EnrichMessage(MessageEnrichment) (bool, error) {
	return false, nil
}

func TestZCodePlanPersistsFallbackWhenEnrichmentLosesRevision(t *testing.T) {
	t.Parallel()
	sink := &zcodeRejectedEnrichmentSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","toolCallId":"call","toolName":"ExitPlanMode"}`))
	original := []byte(` {"id":"server-1","method":"interaction/requestUserInput","params":{"requestId":"approval","toolCallId":"call","toolName":"ExitPlanMode","input":{"plan":"# Retain the plan"},"schema":{"interaction":"plan_approval"}}} `)
	a.HandleOutput(original)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, original, messages[1].Content)
	assert.True(t, messages[1].NoSpan)
	assert.Len(t, sink.PublishedControls(), 1)
}
