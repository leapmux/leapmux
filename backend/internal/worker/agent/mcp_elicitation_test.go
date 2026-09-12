package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMCPElicitationResponse(t *testing.T) {
	for _, method := range []string{contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodReasonix, contracts.MCPElicitationMethodCodex} {
		for _, id := range []string{`"001"`, `"request-1"`, `9007199254740993`, `0`} {
			request := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":{"mode":"form"}}`)
			_, requestID, ok := ExtractJSONRPCID(request)
			require.True(t, ok)
			for _, action := range []string{"accept", "decline", "cancel"} {
				response, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": map[string]any{"action": action, "content": map[string]any{"count": 0, "enabled": false}}}})
				require.NoError(t, err)
				resolved, claimed := resolveMCPElicitationResponse(ControlResponseContext{RequestPayload: request, ResponseContent: response})
				require.True(t, claimed)
				require.False(t, resolved.Withhold)
				var output struct {
					ID     json.RawMessage `json:"id"`
					Result struct {
						Action  string         `json:"action"`
						Content map[string]any `json:"content"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(resolved.Content, &output))
				assert.Equal(t, id, string(output.ID))
				assert.Equal(t, action, output.Result.Action)
				if action == "accept" {
					assert.Equal(t, map[string]any{"count": float64(0), "enabled": false}, output.Result.Content)
				} else {
					assert.Nil(t, output.Result.Content)
				}
			}
		}
	}
}

func TestResolveMCPElicitationRejectsInvalidAnswer(t *testing.T) {
	request := json.RawMessage(`{"id":1,"method":"elicitation/create"}`)
	for _, response := range []string{`{}`, `{"response":{"request_id":"other","response":{"action":"accept"}}}`, `{"response":{"request_id":"1","response":{"action":"unknown"}}}`, `broken`} {
		result, claimed := resolveMCPElicitationResponse(ControlResponseContext{RequestPayload: request, ResponseContent: []byte(response)})
		require.True(t, claimed)
		assert.True(t, result.Withhold)
	}
	_, claimed := resolveMCPElicitationResponse(ControlResponseContext{RequestPayload: []byte(`{"method":"session/request_permission"}`)})
	assert.False(t, claimed)
}

func TestMCPElicitationContextPreservesDisplayData(t *testing.T) {
	for _, payload := range []string{
		`{"id":1,"method":"elicitation/create","params":{"sessionId":"session","mode":"form","message":"Choose","requestedSchema":{"type":"object","properties":{"count":{"type":"integer","title":"Count"}}}}}`,
		`{"type":"control_request","request_id":"request","request":{"subtype":"elicitation","mcp_server_name":"probe","mode":"form","message":"Choose","requested_schema":{"type":"object","properties":{"count":{"type":"integer","title":"Count"}}}}}`,
	} {
		context := mcpElicitationRequestContext([]byte(payload))
		assert.Contains(t, string(context), `"title":"Count"`)
		assert.NotContains(t, string(context), `"sessionId"`)
		assert.NotContains(t, string(context), `"request_id"`)
	}
}

func TestMCPElicitationApprovalScope(t *testing.T) {
	request := json.RawMessage(`{"id":"001","method":"mcpServer/elicitation/request","params":{"_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session"]}}}`)
	for _, scope := range []string{"session", "always", "unknown"} {
		reply, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": "001", "response": map[string]any{"action": "accept", "content": map[string]any{}, "_meta": map[string]string{"persist": scope}}}})
		require.NoError(t, err)
		result, claimed := resolveMCPElicitationResponse(ControlResponseContext{RequestPayload: request, ResponseContent: reply})
		require.True(t, claimed)
		assert.Equal(t, scope != "session", result.Withhold)
		if !result.Withhold {
			assert.Contains(t, string(result.Content), `"_meta":{"persist":"session"}`)
		}
	}
	reply := []byte(`{"response":{"request_id":"001","response":{"action":"decline","content":{"private":"unused"},"_meta":{"persist":"always"}}}}`)
	result, claimed := resolveMCPElicitationResponse(ControlResponseContext{RequestPayload: request, ResponseContent: reply})
	require.True(t, claimed)
	require.False(t, result.Withhold)
	assert.NotContains(t, string(result.Content), "private")
	assert.NotContains(t, string(result.Content), "_meta")
}

func TestCodexPublishesZeroIDMCPElicitation(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)
	raw := []byte(`{"method":"mcpServer/elicitation/request","id":0,"params":{"threadId":"thread","mode":"form","message":"Allow this tool?","requestedSchema":{"type":"object","properties":{}}}}`)
	agent.HandleOutput(raw)
	require.Len(t, sink.publishedControls, 1)
	assert.Equal(t, "0", sink.publishedControls[0].RequestID)
	assert.Equal(t, raw, sink.publishedControls[0].Payload)
}
