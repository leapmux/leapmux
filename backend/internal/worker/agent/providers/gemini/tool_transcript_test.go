package gemini

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
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
