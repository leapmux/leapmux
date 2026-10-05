package gemini

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiToolSupplementPreservesTheNativeRecord(t *testing.T) {
	t.Parallel()
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"run_shell_command__c1","status":"completed","kind":"execute","content":[]}`)
	record := []byte(`{"id":"run_shell_command__c1","name":"run_shell_command","args":{"command":"exit 7"},"status":"success","result":[{"functionResponse":{"response":{"output":"Output: value\nExit Code: 7\nProcess Group PGID: 123"}}}],"resultDisplay":[[{"text":"value","bold":false}]]}`)
	encoded, err := geminiToolSupplement(original, record)
	require.NoError(t, err)
	var supplement acp.ToolSupplement
	require.NoError(t, json.Unmarshal(encoded, &supplement))
	var frame map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original, &frame))
	assert.True(t, supplement.IdentityMatches(frame))
	var output map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(supplement[contracts.ACPSupplementRawOutput], &output))
	assert.JSONEq(t, string(record), string(output[contracts.GeminiSupplementStoredToolRecord]))
}

// resolveWithRecord resolves one frame with the supplement that the transcript
// stores for record beside the frame stored.
func resolveWithRecord(t *testing.T, stored, original, record string) map[string]json.RawMessage {
	t.Helper()
	supplemental, err := geminiToolSupplement([]byte(stored), []byte(record))
	require.NoError(t, err)
	resolved := Registration().Plugin.ResolveProviderData(agent.MessageContent{Original: []byte(original), Supplemental: supplemental})
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolved, &fields))
	return fields
}

func TestGeminiResolveProviderDataJoinsTheStoredRecord(t *testing.T) {
	t.Parallel()
	frame := `{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c1","status":"completed","title":"Set 1 todo(s)","content":[],"kind":"other"}`
	record := `{"id":"write_todos__c1","name":"write_todos","status":"success","resultDisplay":{"todos":[]}}`

	fields := resolveWithRecord(t, frame, frame, record)

	assert.JSONEq(t, `{"`+contracts.GeminiSupplementStoredToolRecord+`":`+record+`}`, string(fields[contracts.ACPSupplementRawOutput]))
	delete(fields, contracts.ACPSupplementRawOutput)
	remaining, err := json.Marshal(fields)
	require.NoError(t, err)
	assert.JSONEq(t, frame, string(remaining), "every field of the frame stays as the agent sent it")
}

func TestGeminiResolveProviderDataKeepsTheFrameOwnRawOutput(t *testing.T) {
	t.Parallel()
	record := `{"id":"read_file__c1","name":"read_file","status":"success"}`
	for name, test := range map[string]struct {
		rawOutput string
		want      string
	}{
		"an object takes the record beside its own keys": {
			rawOutput: `{"native":1}`,
			want:      `{"native":1,"` + contracts.GeminiSupplementStoredToolRecord + `":` + record + `}`,
		},
		"null takes the record": {
			rawOutput: `null`,
			want:      `{"` + contracts.GeminiSupplementStoredToolRecord + `":` + record + `}`,
		},
		"a value that is not an object stays": {rawOutput: `"native text"`, want: `"native text"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frame := `{"sessionUpdate":"tool_call_update","toolCallId":"read_file__c1","status":"completed","rawOutput":` + test.rawOutput + `}`
			fields := resolveWithRecord(t, frame, frame, record)
			assert.JSONEq(t, test.want, string(fields[contracts.ACPSupplementRawOutput]))
		})
	}
}

// A record stored beside one frame never reaches another frame: the same
// identity gate as the shared resolve and the browser plugin.
func TestGeminiResolveProviderDataRefusesTheRecordOfAnotherFrame(t *testing.T) {
	t.Parallel()
	stored := `{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c1","status":"completed","content":[]}`
	record := `{"id":"write_todos__c1","name":"write_todos","status":"success","resultDisplay":{"todos":[]}}`
	for name, original := range map[string]string{
		"another call":           `{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c2","status":"completed","content":[]}`,
		"another status":         `{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c1","status":"failed","content":[]}`,
		"another session update": `{"sessionUpdate":"tool_call","toolCallId":"write_todos__c1","status":"completed","content":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fields := resolveWithRecord(t, stored, original, record)
			assert.NotContains(t, fields, contracts.ACPSupplementRawOutput)
		})
	}
}

func TestGeminiResolveProviderDataLeavesAFrameWithoutAStoredRecord(t *testing.T) {
	t.Parallel()
	frame := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c1","status":"completed","content":[]}`)
	identity := `"sessionUpdate":"tool_call_update","toolCallId":"write_todos__c1","status":"completed"`
	for name, supplemental := range map[string]string{
		"no supplement":           ``,
		"an unreadable one":       `{`,
		"one without a record":    `{` + identity + `}`,
		"one with a null record":  `{` + identity + `,"rawOutput":null}`,
		"one with an empty one":   `{` + identity + `,"rawOutput":{}}`,
		"one that is not a map":   `[]`,
		"one with a record array": `{` + identity + `,"rawOutput":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolved := Registration().Plugin.ResolveProviderData(agent.MessageContent{Original: frame, Supplemental: []byte(supplemental)})
			assert.JSONEq(t, string(frame), string(resolved))
		})
	}
}

func TestGeminiToolSupplementRejectsAnUnrelatedOrInvalidRecord(t *testing.T) {
	t.Parallel()
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"run_shell_command__c1","status":"completed"}`)
	for _, record := range []string{
		`{`, `null`, `{}`, `{"id":"run_shell_command__c2","name":"run_shell_command"}`, `{"id":"run_shell_command__c1"}`,
	} {
		_, err := geminiToolSupplement(original, []byte(record))
		assert.Error(t, err, "record %q", record)
	}
}
