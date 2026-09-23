package acp

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRegistryACPBase gives a Base whose stdin a test can read back.
func newRegistryACPBase(sink agent.ProviderServices) (*Base, *bytes.Buffer) {
	var stdin bytes.Buffer
	b := &Base{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "agent", Stdin: agenttest.NopStdin(&stdin)})},
		sink:           sink,
	}
	b.sessionID = "session-1"
	return b, &stdin
}

// An ACP elicitation and a permission request each carry their own cancel answer, and
// one stop answers both.
func TestACPInterruptAnswersEveryOpenControlRequestByKind(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	b, stdin := newRegistryACPBase(agent.NewProviderServices(sink))
	b.promptActive = true
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"e-1","method":"` + contracts.MCPElicitationMethodACP + `","params":{}}`))
	require.Len(t, sink.PublishedControls(), 2)
	require.NoError(t, b.Interrupt())
	answers := agenttest.JSONRPCResultsByID(t, stdin.String())
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers[`11`])
	assert.JSONEq(t, `{"action":"cancel"}`, answers[`"e-1"`])
	assert.ElementsMatch(t, []string{"jsonrpc:11", `jsonrpc:"e-1"`}, sink.CanceledControls())
}

// A cancel notification from the agent prunes the record. Without that the map grew by
// one entry for every withdrawn request, and the next stop answered a request nobody
// waited on.
func TestACPCancelNotificationPrunesTheOutstandingRecord(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	b, stdin := newRegistryACPBase(agent.NewProviderServices(sink))
	b.promptActive = true
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"` + acpMethodCancelRequestCamel + `","params":{"requestId":11}}`))
	assert.Equal(t, []string{"jsonrpc:11"}, sink.CanceledControls())
	assert.Zero(t, b.OutstandingControlCountForTest())
	stdin.Reset()
	require.NoError(t, b.Interrupt())
	assert.Empty(t, agenttest.JSONRPCResultsByID(t, stdin.String()), "a withdrawn request must not receive a cancel answer")
}

// A withdrawal must find the record the publisher wrote, whichever JSON spelling each
// frame used for the same number.
func TestACPCancelNotificationMatchesEveryNumericSpelling(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{`12`, `12.0`, `1.2e1`} {
		t.Run(spelling, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			b, _ := newRegistryACPBase(agent.NewProviderServices(sink))
			b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":12,"method":"session/request_permission","params":{}}`))
			b.HandleOutput([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"requestId":%s}}`, acpMethodCancelRequestCamel, spelling)))
			assert.Equal(t, []string{"jsonrpc:12"}, sink.CanceledControls())
		})
	}
}
