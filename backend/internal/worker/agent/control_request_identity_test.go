package agent

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	for _, provider := range []string{"codex", "opencode", "cursor", "reasonix"} {
		t.Run(provider, func(t *testing.T) {
			sink := &recordingControlSink{}
			var receive func([]byte)
			method := "session/request_permission"
			switch provider {
			case "codex":
				a := newCodexAgentWithSink(sink)
				receive = func(content []byte) { handleCodexOutput(a, parseLine(content)) }
				method = "item/commandExecution/requestApproval"
			case "opencode":
				receive = newOpenCodeAgentWithSink(sink).HandleOutput
			case "cursor":
				receive = newCursorAgentWithSink(sink).HandleOutput
				method = contracts.CursorMethodAskQuestion
			case "reasonix":
				// Reasonix sends the STANDARD Agent Client Protocol elicitation, so its
				// requests reach the shared dispatcher rather than its own extra-method
				// hook. Its identity rule is therefore the shared one, exercised here
				// through the same path a live request takes.
				a := &ReasonixAgent{}
				a.sink = sink
				receive = a.HandleOutput
				method = contracts.MCPElicitationMethodACP
			}
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
		})
	}
}
