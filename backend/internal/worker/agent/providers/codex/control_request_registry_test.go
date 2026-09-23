package codex

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex is the one publisher that registers a REAL cancel answer: it retires its
// own approval requests, but it defines no outcome for an MCP elicitation the
// client withdraws, so the elicitation blocks inside the CLI until somebody
// answers it. Without a drain on Interrupt that answer could never be delivered
// and the state was dead.
func TestCodexInterruptAnswersAnOutstandingElicitation(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	stdin := &agenttest.Stdin{}
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "agent", Stdin: agenttest.NopStdin(stdin)})},
		sink:           agent.NewProviderServices(sink),
	}
	a.PublishControlRequest(a.sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"`+contracts.MCPElicitationMethodCodex+`"}`), providerkit.MCPElicitationCancelAnswer())

	// No thread or turn, so Interrupt has nothing to cancel and returns early --
	// the drain must still run, because a request the runtime waits on outlives
	// the turn that raised it.
	require.NoError(t, a.Interrupt())

	assert.Contains(t, stdin.String(), `{"action":"cancel"}`,
		"the elicitation's own cancel answer releases the blocked MCP call")
	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls(), "and the browser card goes with it")
}

func TestCodexControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, func(content []byte) { handleCodexOutput(a, providerkit.ParseLine(content)) },
		"item/commandExecution/requestApproval")
}
