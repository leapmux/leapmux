package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestTurnSignalWhitelist_Codex(t *testing.T) {
	t.Parallel()

	// Codex states both edges itself, so every other notification of its ~90 is
	// inert -- including the hook and item bookends, whose names read like turn
	// boundaries and are not.
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "turn started", Line: `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`, Moves: true},
		{Name: "turn completed", Line: `{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`, Moves: true},

		{Name: "item started", Line: `{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"main-thread","item":{"id":"i1","itemType":"agentMessage"}}}`},
		{Name: "item completed", Line: `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"main-thread","item":{"id":"i1","itemType":"agentMessage","text":"hi"}}}`},
		{Name: "agent message delta", Line: `{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"i1","delta":"hi"}}`},
		{Name: "hook started", Line: `{"jsonrpc":"2.0","method":"hook/started","params":{"threadId":"main-thread"}}`},
		{Name: "hook completed", Line: `{"jsonrpc":"2.0","method":"hook/completed","params":{"threadId":"main-thread"}}`},
		{Name: "turn diff updated", Line: `{"jsonrpc":"2.0","method":"turn/diff/updated","params":{"threadId":"main-thread"}}`},
		{Name: "turn plan updated", Line: `{"jsonrpc":"2.0","method":"turn/plan/updated","params":{"threadId":"main-thread"}}`},
		{Name: "token usage updated", Line: `{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"main-thread","usage":{}}}`},
		{Name: "rate limits updated", Line: `{"jsonrpc":"2.0","method":"account/rateLimits/updated","params":{}}`},
		{Name: "thread compacted", Line: `{"jsonrpc":"2.0","method":"thread/compacted","params":{"threadId":"main-thread"}}`},
		{Name: "fs changed", Line: `{"jsonrpc":"2.0","method":"fs/changed","params":{}}`},
		{Name: "warning", Line: `{"jsonrpc":"2.0","method":"warning","params":{"message":"careful"}}`},
		{Name: "error", Line: `{"jsonrpc":"2.0","method":"error","params":{"message":"boom"}}`},

		{Name: "a method from a later release", Line: `{"jsonrpc":"2.0","method":"turn/futureSignal","params":{"threadId":"main-thread"}}`},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.ControlSink{}
		a := newCodexAgentWithSink(agent.NewProviderServices(sink))
		handleCodexOutput(a, providerkit.ParseLine([]byte(tc.Line)))
		return sink.TurnActives()
	})
}
