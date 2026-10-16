package claude

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestClaudeReplayDoesNotStartANativeTurn(t *testing.T) {
	t.Parallel()
	frames := map[string]string{
		"model command": `{"type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to haiku</local-command-stdout>"}}`,
		"assistant":     `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"saved answer"}]}}`,
		"tool result":   `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"old-call","content":"saved result"}]}}`,
		"thinking":      `{"type":"system","subtype":"thinking_tokens","thinking_tokens":120}`,
		"session state": `{"type":"system","subtype":"session_state_changed","state":"running"}`,
	}
	for name, frame := range frames {
		for _, replay := range []string{"true", "false", "null", `"true"`} {
			t.Run(name+"/"+replay, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
				line := strings.Replace(frame, "{", `{"isReplay":`+replay+",", 1)
				a.HandleOutput([]byte(line))
				if replay == "false" {
					assert.Equal(t, []bool{true}, sink.TurnActives(), "a live root frame can start a turn")
				} else {
					assert.Empty(t, sink.TurnActives(), "a replay or invalid replay flag cannot describe live work")
				}
				if name == "assistant" {
					assert.NotEmpty(t, sink.Messages(), "replay classification must preserve transcript content")
				}
			})
		}
	}
}
