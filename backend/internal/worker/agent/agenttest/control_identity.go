package agenttest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// AssertControlIdentitiesStaySeparate delivers one control request for each
// spelling of a JSON-RPC id through receive, and asserts that each one reaches
// sink as a separate request with its native bytes.
//
// A number and a string with the same digits are two ids in JSON-RPC. A
// provider that keyed both on the digits alone replaced one pending request
// with the other, and the reply for the first one then went nowhere.
func AssertControlIdentitiesStaySeparate(t *testing.T, sink *ControlSink, receive func([]byte), method string) {
	t.Helper()
	ids := []string{`7`, `"7"`, `0`, `"0"`, `9007199254740993`, `"9007199254740993"`, `""`, `"json:7"`}
	seen := make(map[string]bool)
	for _, nativeID := range ids {
		content := []byte(fmt.Sprintf(` {"jsonrpc":"2.0", "id":%s,"method":%q,"params":{"sessionId":"test-session","questions":[]}} `, nativeID, method))
		receive(content)
		require.Equal(t, len(seen)+1, sink.PublishedControlCount(), nativeID)
		request := sink.LastPublishedControl()
		require.False(t, seen[request.RequestID], "native ID %s replaced another request with worker ID %q", nativeID, request.RequestID)
		require.NotEmpty(t, request.RequestID)
		require.Equal(t, content, request.Payload)
		seen[request.RequestID] = true
	}
}
