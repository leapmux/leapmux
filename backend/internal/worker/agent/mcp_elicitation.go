package agent

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

// resolveMCPElicitationResponse restores the request's exact JSON-RPC identifier.
// The frontend uses the shared control envelope to keep wide IDs and string IDs intact.
func resolveMCPElicitationResponse(ctx ControlResponseContext) (ControlResponseResolution, bool) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			Meta struct {
				Kind    string   `json:"codex_approval_kind"`
				Persist []string `json:"persist"`
			} `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(ctx.RequestPayload, &request) != nil {
		return ControlResponseResolution{}, false
	}
	switch request.Method {
	case contracts.MCPElicitationMethodACP, contracts.MCPElicitationMethodReasonix, contracts.MCPElicitationMethodCodex:
	default:
		return ControlResponseResolution{}, false
	}
	result := defaultControlResponseResolution(ctx)
	result.Withhold = true
	var response mcpElicitationControlResponse
	id, requestID, ok := ExtractJSONRPCID(ctx.RequestPayload)
	if !ok || json.Unmarshal(ctx.ResponseContent, &response) != nil || response.Response.RequestID != storedControlRequestID(ctx, requestID) {
		return result, true
	}
	answer := response.Response.Response
	switch answer.Action {
	case contracts.MCPElicitationActionAccept:
		if len(answer.Meta) > 0 {
			persist := answer.Meta["persist"]
			if request.Method != contracts.MCPElicitationMethodCodex || (request.Params.Meta.Kind != contracts.MCPElicitationApprovalKindToolCall && request.Params.Meta.Kind != contracts.MCPElicitationApprovalKindToolSuggestion) {
				return result, true
			}
			if (persist != contracts.MCPElicitationApprovalScopeSession && persist != contracts.MCPElicitationApprovalScopeAlways) || !slices.Contains(request.Params.Meta.Persist, persist) {
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

func isClaudeMCPElicitation(payload json.RawMessage) bool {
	var root struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	return json.Unmarshal(payload, &root) == nil && root.Request.Subtype == contracts.MCPElicitationSubtypeClaude
}

type mcpElicitationControlResponse struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Action  string            `json:"action"`
			Content json.RawMessage   `json:"content,omitempty"`
			Meta    map[string]string `json:"_meta,omitempty"`
		} `json:"response"`
	} `json:"response"`
}
