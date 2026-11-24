package agent

import (
	"bytes"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/util/jsonfield"
)

// restoreControlResponseID reads the native ID from the persisted request.
// An answer carries the worker's string key of the request as its ID (JSONRPCResult).
func restoreControlResponseID(ctx ControlResponseContext) ControlResponseResolution {
	resolution := ControlResponseResolution{Content: ctx.ResponseContent}
	if len(ctx.RequestPayload) == 0 {
		return resolution
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(ctx.RequestPayload, &request) == nil && len(request["id"]) == 0 && len(request["method"]) == 0 {
		// Native events can use a JSON-RPC response without a JSON-RPC request envelope.
		return resolution
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(ctx.ResponseContent, &response) != nil || len(response["id"]) == 0 || len(response["method"]) > 0 {
		return resolution
	}
	if len(response["result"]) == 0 && len(response["error"]) == 0 {
		return resolution
	}
	nativeID, _, validRequest := ExtractJSONRPCID(ctx.RequestPayload)
	if !validRequest {
		resolution.Refuse(RefusalUnreadableRequest)
		return resolution
	}
	if bytes.Equal(nativeID, response["id"]) {
		return resolution
	}
	content, err := jsonfield.Set(ctx.ResponseContent, nativeID, "id")
	if err != nil {
		resolution.Refuse(RefusalUnencodableReply)
		return resolution
	}
	resolution.Content = content
	return resolution
}
