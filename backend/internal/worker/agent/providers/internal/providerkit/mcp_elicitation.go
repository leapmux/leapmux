package providerkit

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// MCPElicitationMetaRule decides whether an accepted elicitation answer may
// carry the `_meta.persist` scope the frontend sent. requestPayload is the
// stored request, so a provider can check the scopes that request offered.
type MCPElicitationMetaRule func(requestPayload json.RawMessage, persist string) bool

// ResolveMCPElicitationResponse restores the request's exact JSON-RPC identifier.
// The frontend uses the shared control envelope to keep wide IDs and string IDs intact.
//
// method is the elicitation method the calling provider speaks: the resolution
// answers a stored request of that method and passes on anything else. acceptMeta
// is the provider's rule for an accepted answer's `_meta` scope; nil refuses
// every answer that carries one.
func ResolveMCPElicitationResponse(ctx agent.ControlResponseContext, method string, acceptMeta MCPElicitationMetaRule) (agent.ControlResponseResolution, bool) {
	var request struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(ctx.RequestPayload, &request) != nil || request.Method != method {
		return agent.ControlResponseResolution{}, false
	}
	result := agent.DefaultControlResponseResolution(ctx)
	result.Withhold = true
	var response MCPElicitationControlResponse
	id, requestID, ok := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !ok || json.Unmarshal(ctx.ResponseContent, &response) != nil || response.Response.RequestID != agent.StoredControlRequestID(ctx, requestID) {
		return result, true
	}
	answer := response.Response.Response
	switch answer.Action {
	case contracts.MCPElicitationActionAccept:
		if len(answer.Meta) > 0 {
			persist := answer.Meta["persist"]
			if acceptMeta == nil || !acceptMeta(ctx.RequestPayload, persist) {
				return result, true
			}
			answer.Meta = map[string]string{"persist": persist}
		}
	case contracts.MCPElicitationActionDecline, contracts.MCPElicitationActionCancel:
		answer.Content = nil
		answer.Meta = nil
	default:
		return result, true
	}
	content, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: answer})
	if err != nil {
		return result, true
	}
	result.Content = content
	result.Withhold = false
	return result, true
}

// MCPElicitationControlResponse is the control envelope in which the frontend
// answers an MCP elicitation. Pi reads the same envelope for its MCP approval.
type MCPElicitationControlResponse struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Action  string            `json:"action"`
			Content json.RawMessage   `json:"content,omitempty"`
			Meta    map[string]string `json:"_meta,omitempty"`
		} `json:"response"`
	} `json:"response"`
}
