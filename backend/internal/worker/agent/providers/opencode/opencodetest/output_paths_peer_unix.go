//go:build unix

package opencodetest

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const outputFileHelperEnv = "GO_WANT_HELPER_PROCESS_FAMILY_OUTPUT_FILE"
const outputFilePathEnv = "LEAPMUX_TEST_FAMILY_OUTPUT_FILE_PATH"

type outputFileTurnSink struct {
	*agenttest.Sink
	finished chan struct{}
}

func (s *outputFileTurnSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if err := s.Sink.PersistTurnEnd(content, span); err != nil {
		return err
	}
	close(s.finished)
	return nil
}

// ServeCompleteToolOutputFileRPC uses the captured native truncation metadata.
func ServeCompleteToolOutputFileRPC() {
	agenttest.ServeFakeJSONRPC(outputFileHelperEnv, func(method string) (string, bool, bool) {
		switch method {
		case acp.MethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"promptCapabilities":{"image":false}}}`, false, true
		case acp.MethodSessionNew:
			return `{"sessionId":"full-output-session","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"code","name":"Code"},{"id":"plan","name":"Plan"}]}}`, false, true
		case acp.MethodSessionSetMode:
			return `{}`, false, true
		case acp.MethodSessionPrompt:
			path := os.Getenv(outputFilePathEnv)
			for _, update := range []map[string]any{
				{"sessionUpdate": "tool_call", "toolCallId": "native-full-output", "kind": "execute", "status": "in_progress", "rawInput": map[string]string{"command": "native command"}},
				{"sessionUpdate": "tool_call_update", "toolCallId": "native-full-output", "kind": "execute", "status": "completed", "rawOutput": map[string]any{"output": "first\n...output truncated...\nlast42", "metadata": map[string]any{"exit": 0, "truncated": true, "outputPath": path}}},
			} {
				frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "full-output-session", "update": update}})
				if err != nil {
					return `{"code":-32603,"message":"The fake update could not be encoded."}`, true, true
				}
				if _, err := fmt.Fprintln(os.Stdout, string(frame)); err != nil {
					return `{"code":-32603,"message":"The fake update could not be written."}`, true, true
				}
			}
			return `{"stopReason":"end_turn"}`, false, true
		default:
			return "", false, false
		}
	})
}
