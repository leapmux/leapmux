package providerkit

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// MCPElicitationMetaRule decides whether an accepted elicitation answer may
// carry the `_meta.persist` scope that the answer states. requestPayload is the
// stored request, so a provider can check the scopes that request offered.
type MCPElicitationMetaRule func(requestPayload json.RawMessage, persist string) bool

// ResolveMCPElicitationResponse restores the request's exact JSON-RPC identifier.
// agent.AnswerElicitation answers in the shared control envelope, which keeps wide IDs
// and string IDs intact.
//
// method is the elicitation method the calling provider speaks: the resolution
// answers a stored request of that method and passes on anything else. acceptMeta
// is the provider's rule for an accepted answer's `_meta` scope; nil refuses
// every answer that carries one.
func ResolveMCPElicitationResponse(ctx agent.ControlResponseContext, method string, acceptMeta MCPElicitationMetaRule) (agent.ControlResponseResolution, bool) {
	if method == "" || StringField(ctx.RequestPayload, "method") != method {
		return agent.ControlResponseResolution{}, false
	}
	result := agent.DefaultControlResponseResolution(ctx)
	result.Withhold = true
	var response MCPElicitationControlResponse
	id, _, ok := agent.ExtractJSONRPCID(ctx.RequestPayload)
	switch {
	case !ok:
		result.Refuse(agent.RefusalUnreadableRequest)
		return result, true
	case json.Unmarshal(ctx.ResponseContent, &response) != nil:
		// The Worker resolves only JSON that addresses the request, so this
		// fails only for a field of a type that the envelope does not take,
		// such as a `_meta` value that is not a string.
		result.Refuse(agent.RefusalUnreadableAnswer)
		return result, true
	}
	answer := response.Response.Response
	switch answer.Action {
	case contracts.MCPElicitationActionAccept:
		if len(answer.Meta) > 0 {
			persist := answer.Meta["persist"]
			if acceptMeta == nil || !acceptMeta(ctx.RequestPayload, persist) {
				result.Refuse("the answer persists a choice the request did not offer")
				return result, true
			}
			answer.Meta = map[string]string{"persist": persist}
		}
	case contracts.MCPElicitationActionDecline, contracts.MCPElicitationActionCancel:
		answer.Content = nil
		answer.Meta = nil
	default:
		result.Refuse(agent.RefusalUnreadableAnswer)
		return result, true
	}
	content, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: answer})
	if err != nil {
		result.Refuse(agent.RefusalUnencodableReply)
		return result, true
	}
	result.Content = content
	result.Withhold = false
	return result, true
}

// MCPElicitationControlResponse is the control envelope in which
// agent.AnswerElicitation answers an MCP elicitation. Pi reads the same envelope for its MCP approval.
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
