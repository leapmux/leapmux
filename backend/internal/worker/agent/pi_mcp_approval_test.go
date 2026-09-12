package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiMCPApprovalResponse(t *testing.T) {
	request := []byte(`{"type":"extension_ui_request","id":"permission","method":"select","title":"MCP: probe wants to run write\n\nArguments:\n{}","options":["Allow once","Allow for session","Deny"]}`)
	for _, tc := range []struct {
		action, scope, value string
		cancelled, withheld  bool
	}{
		{action: "accept", value: "Allow once"},
		{action: "accept", scope: "session", value: "Allow for session"},
		{action: "accept", scope: "always", withheld: true},
		{action: "decline", scope: "session", value: "Deny"},
		{action: "cancel", cancelled: true},
		{action: "invalid", withheld: true},
	} {
		response, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": "permission", "response": map[string]any{"action": tc.action, "_meta": map[string]string{"persist": tc.scope}}}})
		require.NoError(t, err)
		result, matched := resolvePiMCPApprovalResponse(ControlResponseContext{RequestPayload: request, ResponseContent: response})
		require.True(t, matched)
		require.Equal(t, tc.withheld, result.Withhold)
		if tc.withheld {
			continue
		}
		var reply map[string]any
		require.NoError(t, json.Unmarshal(result.Content, &reply))
		assert.Equal(t, "extension_ui_response", reply["type"])
		assert.Equal(t, "permission", reply["id"])
		if tc.cancelled {
			assert.Equal(t, true, reply["cancelled"])
		} else {
			assert.Equal(t, tc.value, reply["value"])
		}
		assert.NotContains(t, reply, "_meta")
		assert.Contains(t, string(result.RequestContext), "MCP: probe")
	}
	result, matched := resolvePiMCPApprovalResponse(ControlResponseContext{RequestPayload: request, ResponseContent: []byte(`{"response":{"request_id":"other","response":{"action":"accept"}}}`)})
	require.True(t, matched)
	assert.True(t, result.Withhold)
	_, matched = resolvePiMCPApprovalResponse(ControlResponseContext{RequestPayload: []byte(`{"type":"extension_ui_request","id":"question","method":"select","title":"Choose","options":["Allow once","Allow for session","Deny"]}`)})
	assert.False(t, matched)
}

func TestPiControlSourceRejectsAmbiguousAndChangedTools(t *testing.T) {
	for _, change := range []string{"second tool", "completed", "changed arguments"} {
		t.Run(change, func(t *testing.T) {
			a := newPiAgentWithSink(&recordingControlSink{})
			handlePiOutput(a, parseLine([]byte(`{"type":"tool_execution_start","toolCallId":"mcp","toolName":"mcp","args":{"tool":"probe_read","args":{}}}`)))
			switch change {
			case "second tool":
				handlePiOutput(a, parseLine([]byte(`{"type":"tool_execution_start","toolCallId":"other","toolName":"read","args":{"path":"sample.py"}}`)))
			case "completed":
				handlePiOutput(a, parseLine([]byte(`{"type":"tool_execution_end","toolCallId":"mcp","toolName":"mcp","result":{"content":[]}}`)))
			case "changed arguments":
				a.toolStates["mcp"].Args = json.RawMessage(`{"tool":"different"}`)
			}
			assert.Zero(t, a.piControlSourceSeq(nil))
		})
	}
}
