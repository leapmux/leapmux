package acp

import (
	"io"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestTurnSignalWhitelist_ACP(t *testing.T) {
	t.Parallel()

	// The six ACP providers share one rule, and it needs no vocabulary at all:
	// the turn is the lifetime of the session/prompt request this Worker sent,
	// so NO notification moves the flag. These are the updates that arrive with
	// no prompt in flight, which is what makes the rule load-bearing rather than
	// incidental.
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "agent message chunk", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`},
		{Name: "agent thought chunk", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}}}`},
		{Name: "tool call", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"bash","status":"pending"}}}`},
		{Name: "tool call update", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}}}`},
		{Name: "plan", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"plan","entries":[]}}}`},
		{Name: "available commands update", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}}`},
		{Name: "current mode update", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"ask"}}}`},
		{Name: "session info update", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update"}}}`},
		{Name: "usage update", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"usage_update","usage":{}}}}`},
		{Name: "user message chunk", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"}}}}`},

		{Name: "an update from a later release", Line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_future_update"}}}`},
		{Name: "a method from a later release", Line: `{"jsonrpc":"2.0","method":"session/futureSignal","params":{"sessionId":"session-1"}}`},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		b, sink := newACPTurnBase(t, agenttest.NopStdin(io.Discard))
		b.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
}
