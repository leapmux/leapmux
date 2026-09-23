package pi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestTurnSignalWhitelist_Pi(t *testing.T) {
	t.Parallel()

	// Pi's run bookends are agent_start and agent_end. Its turn_start/turn_end
	// pair is the INNER loop -- one assistant response inside the run -- so the
	// two events whose names match "turn" are the two that must move nothing.
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "agent start", Line: `{"type":"agent_start"}`, Moves: true},
		{Name: "agent end", Line: `{"type":"agent_end","willRetry":false}`, Moves: true},

		{Name: "turn start", Line: `{"type":"turn_start"}`},
		{Name: "turn end", Line: `{"type":"turn_end","message":{"role":"assistant","content":[]}}`},
		{Name: "message start", Line: `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{Name: "message end", Line: `{"type":"message_end","message":{"role":"assistant","content":[]}}`},
		{Name: "tool execution start", Line: `{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash"}`},
		{Name: "tool execution end", Line: `{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","isError":false}`},

		{Name: "an event from a later release", Line: `{"type":"agent_future_event"}`},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		handlePiOutput(a, providerkit.ParseLine([]byte(tc.Line)))
		return sink.TurnActives()
	})
}
