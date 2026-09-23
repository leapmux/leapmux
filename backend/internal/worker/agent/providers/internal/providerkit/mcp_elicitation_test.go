package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMCPElicitationResponse(t *testing.T) {
	for _, method := range []string{contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodCodex} {
		for _, id := range []string{`"001"`, `"request-1"`, `9007199254740993`, `0`} {
			request := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":{"mode":"form"}}`)
			_, requestID, ok := agent.ExtractJSONRPCID(request)
			require.True(t, ok)
			for _, action := range []string{"accept", "decline", "cancel"} {
				response, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": map[string]any{"action": action, "content": map[string]any{"count": 0, "enabled": false}}}})
				require.NoError(t, err)
				resolved, claimed := ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: response}, method, nil)
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
		result, claimed := ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: []byte(response)}, contracts.MCPElicitationMethodACP, nil)
		require.True(t, claimed)
		assert.True(t, result.Withhold)
	}
	_, claimed := ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: []byte(`{"method":"session/request_permission"}`)}, contracts.MCPElicitationMethodACP, nil)
	assert.False(t, claimed)
}

// TestResolveMCPElicitationClaimsOnlyTheCallersMethod pins that each provider
// answers the elicitation method it speaks and passes on the other one, so
// neither provider's method is a word that shared code has to know.
func TestResolveMCPElicitationClaimsOnlyTheCallersMethod(t *testing.T) {
	reply := []byte(`{"response":{"request_id":"1","response":{"action":"decline"}}}`)
	for _, tc := range []struct{ requestMethod, callerMethod string }{
		{contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodCodex},
		{contracts.MCPElicitationMethodCodex, contracts.MCPElicitationMethodACP},
	} {
		request := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + tc.requestMethod + `"}`)
		_, claimed := ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: reply}, tc.callerMethod, nil)
		assert.False(t, claimed, "a %s resolution must not claim a %s request", tc.callerMethod, tc.requestMethod)
	}
}

// TestResolveMCPElicitationWithoutAMetaRuleRefusesAScope pins the nil rule: a
// provider that states no `_meta` rule withholds an accepted answer that
// carries a scope, rather than forwarding a scope its protocol never offered.
func TestResolveMCPElicitationWithoutAMetaRuleRefusesAScope(t *testing.T) {
	request := json.RawMessage(`{"id":"001","method":"elicitation/create"}`)
	withScope := []byte(`{"response":{"request_id":"001","response":{"action":"accept","content":{},"_meta":{"persist":"session"}}}}`)
	result, claimed := ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: withScope}, contracts.MCPElicitationMethodACP, nil)
	require.True(t, claimed)
	assert.True(t, result.Withhold)

	withoutScope := []byte(`{"response":{"request_id":"001","response":{"action":"accept","content":{}}}}`)
	result, claimed = ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: withoutScope}, contracts.MCPElicitationMethodACP, nil)
	require.True(t, claimed)
	assert.False(t, result.Withhold)
}
