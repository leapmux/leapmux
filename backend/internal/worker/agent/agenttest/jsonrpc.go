package agenttest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// RecordedRequest captures a JSON-RPC request written to an ACP agent's stdin
// in tests.
type RecordedRequest struct {
	Method string
	Params map[string]interface{}
	// Raw is the whole frame. A RESPONSE carries no method and no params, so a test
	// that asserts one reads it here.
	Raw string
}

// JSONRPCResultsByID reads every response frame one agent wrote, keyed by its raw id.
func JSONRPCResultsByID(t *testing.T, written string) map[string]string {
	t.Helper()
	results := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(written), "\n") {
		if line == "" {
			continue
		}
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame), line)
		if frame.Method != "" {
			continue
		}
		results[string(frame.ID)] = string(frame.Result)
	}
	return results
}

// RPCReply is the body of one JSON-RPC response that a test peer sends: a
// result, or an error. Each member holds raw JSON, so a test can also send a
// malformed response on purpose.
type RPCReply struct {
	Result json.RawMessage
	Error  json.RawMessage
}

// JSONRPCResponse frames a reply for request id. It keeps the result/error
// distinction that the wire carries.
func JSONRPCResponse(id int64, payload RPCReply) json.RawMessage {
	if payload.Error != nil {
		return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":%s}`, id, payload.Error))
	}
	result := payload.Result
	if result == nil {
		result = json.RawMessage(`null`)
	}
	return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, result))
}
