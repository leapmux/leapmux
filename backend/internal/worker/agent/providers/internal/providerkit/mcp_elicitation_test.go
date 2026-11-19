package providerkit

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storedJSONRPCKey is the key under which the Worker stores a JSON-RPC request
// with the id nativeID, and which each answer to it addresses.
func storedJSONRPCKey(t *testing.T, nativeID string) string {
	t.Helper()
	identity, ok := agent.NewControlRequestIdentity(json.RawMessage(nativeID))
	require.True(t, ok, nativeID)
	return identity.Key
}

func TestResolveMCPElicitationResponse(t *testing.T) {
	for _, method := range []string{contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodCodex} {
		for _, id := range []string{`"001"`, `"request-1"`, `9007199254740993`, `0`} {
			request := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":{"mode":"form"}}`)
			requestID := storedJSONRPCKey(t, id)
			for _, action := range []string{"accept", "decline", "cancel"} {
				response, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": map[string]any{"action": action, "content": map[string]any{"count": 0, "enabled": false}}}})
				require.NoError(t, err)
				resolved, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf(request, requestID, agent.ControlAnswer{Content: response}), method, nil)
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

// Each answer addresses the stored request, as every answer that the Worker
// resolves does. A `_meta` value that is not a string fails to decode, and the
// answer is then withheld rather than forwarded without its scope.
func TestResolveMCPElicitationRejectsInvalidAnswer(t *testing.T) {
	request := json.RawMessage(`{"id":1,"method":"elicitation/create"}`)
	for _, response := range []string{
		`{"response":{"request_id":"jsonrpc:1","response":{"action":"unknown"}}}`,
		`{"response":{"request_id":"jsonrpc:1","response":{}}}`,
		`{"response":{"request_id":"jsonrpc:1","response":{"action":"accept","content":{},"_meta":{"persist":5}}}}`,
	} {
		result, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf(request, "jsonrpc:1", agent.ControlAnswer{Content: []byte(response)}), contracts.MCPElicitationMethodACP, nil)
		require.True(t, claimed, response)
		assert.True(t, result.Withhold, response)
		assert.EqualError(t, result.Refusal(), agent.RefusalUnreadableAnswer, response)
	}
	decline := []byte(`{"response":{"request_id":"jsonrpc:1","response":{"action":"decline"}}}`)
	_, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf([]byte(`{"id":1,"method":"session/request_permission"}`), "jsonrpc:1", agent.ControlAnswer{Content: decline}), contracts.MCPElicitationMethodACP, nil)
	assert.False(t, claimed)
}

// A stored elicitation that states no id gives the reply no id to answer.
func TestResolveMCPElicitationRejectsAStoredRequestWithNoID(t *testing.T) {
	decline := []byte(`{"response":{"request_id":"jsonrpc:1","response":{"action":"decline"}}}`)
	for _, request := range []string{`{"method":"elicitation/create"}`, `{"id":null,"method":"elicitation/create"}`} {
		result, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf([]byte(request), "jsonrpc:1", agent.ControlAnswer{Content: decline}), contracts.MCPElicitationMethodACP, nil)
		require.True(t, claimed, request)
		assert.True(t, result.Withhold, request)
		assert.EqualError(t, result.Refusal(), agent.RefusalUnreadableRequest, request)
	}
}

// TestResolveMCPElicitationClaimsOnlyTheCallersMethod pins that each provider
// answers the elicitation method it speaks and passes on the other one, so
// neither provider's method is a word that shared code has to know.
func TestResolveMCPElicitationClaimsOnlyTheCallersMethod(t *testing.T) {
	reply := []byte(`{"response":{"request_id":"jsonrpc:1","response":{"action":"decline"}}}`)
	for _, tc := range []struct{ requestMethod, callerMethod string }{
		{contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodCodex},
		{contracts.MCPElicitationMethodCodex, contracts.MCPElicitationMethodACP},
	} {
		request := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + tc.requestMethod + `"}`)
		_, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf(request, "jsonrpc:1", agent.ControlAnswer{Content: reply}), tc.callerMethod, nil)
		assert.False(t, claimed, "a %s resolution must not claim a %s request", tc.callerMethod, tc.requestMethod)
	}
}

// TestResolveMCPElicitationWithoutAMetaRuleRefusesAScope pins the nil rule: a
// provider that states no `_meta` rule withholds an accepted answer that
// carries a scope, rather than forwarding a scope its protocol never offered.
func TestResolveMCPElicitationWithoutAMetaRuleRefusesAScope(t *testing.T) {
	request := json.RawMessage(`{"id":"001","method":"elicitation/create"}`)
	requestID := storedJSONRPCKey(t, `"001"`)
	withScope := []byte(`{"response":{"request_id":` + strconv.Quote(requestID) + `,"response":{"action":"accept","content":{},"_meta":{"persist":"session"}}}}`)
	result, claimed := ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf(request, requestID, agent.ControlAnswer{Content: withScope}), contracts.MCPElicitationMethodACP, nil)
	require.True(t, claimed)
	assert.True(t, result.Withhold)

	withoutScope := []byte(`{"response":{"request_id":` + strconv.Quote(requestID) + `,"response":{"action":"accept","content":{}}}}`)
	result, claimed = ResolveMCPElicitationResponse(agenttest.ControlResponseContextOf(request, requestID, agent.ControlAnswer{Content: withoutScope}), contracts.MCPElicitationMethodACP, nil)
	require.True(t, claimed)
	assert.False(t, result.Withhold)
}
