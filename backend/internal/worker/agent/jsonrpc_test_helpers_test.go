package agent

import (
	"encoding/json"
	"fmt"
)

// Test replies retain the result/error distinction that the wire carries.
// Raw insertion also permits tests to supply a malformed response deliberately.
func jsonrpcTestResponse(id int64, payload jsonrpcResponsePayload) json.RawMessage {
	if payload.Error != nil {
		return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":%s}`, id, payload.Error))
	}
	result := payload.Result
	if result == nil {
		result = json.RawMessage(`null`)
	}
	return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, result))
}
