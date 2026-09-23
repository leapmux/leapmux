package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Goose answers a permission request LeapMux never answered with the JSON-RPC
// cancel notification, and it arrives the moment the reader stops the turn. The
// shared dispatcher had no case for it, so the raw frame reached the transcript
// as a message and the request it withdraws stayed in storage.
//
// Captured from `.tmp/provider-parity/interrupt5` (RL-015).
const acpGooseCancelNotification = `{"jsonrpc":"2.0","method":"$/cancel_request",` +
	`"params":{"requestId":"33d8e110-393a-4abb-935e-022e86e4e347"}}`

func TestACPCancelNotificationWithdrawsTheRequestAndWritesNoRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	base := Base{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "agent"})}, sink: agent.NewProviderServices(sink)}

	// The request the cancel withdraws, published under the same wire id.
	base.handleACPOutput(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":"33d8e110-393a-4abb-935e-022e86e4e347",` +
		`"method":"session/request_permission","params":{}}`)))
	published := sink.LastPublishedControl()

	base.handleACPOutput(providerkit.ParseLine([]byte(acpGooseCancelNotification)))

	assert.Equal(t, 0, sink.MessageCount(), "a protocol notification is not transcript content")
	require.Equal(t, []string{published.RequestID}, sink.CanceledControls(),
		"the cancel must withdraw the request the agent published, by its own identity")
}

// The LSP spelling of the same notification. Both names identify one request by
// `params.requestId`, and an ACP agent may send either.
func TestACPCancelNotificationAcceptsTheLSPSpelling(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	base := Base{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "agent"})}, sink: agent.NewProviderServices(sink)}

	base.handleACPOutput(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"requestId":42}}`)))

	assert.Equal(t, 0, sink.MessageCount())
	require.Len(t, sink.CanceledControls(), 1)
}

// A cancel with no usable id withdraws nothing, and still writes no row: the
// frame is a protocol notification either way.
func TestACPCancelNotificationWithoutAnIDWithdrawsNothing(t *testing.T) {
	t.Parallel()

	for _, params := range []string{`{}`, `{"requestId":null}`, `{"requestId":{}}`, `{"requestId":false}`} {
		sink := &agenttest.ControlSink{}
		base := Base{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "agent"})}, sink: agent.NewProviderServices(sink)}

		base.handleACPOutput(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"$/cancel_request","params":` + params + `}`)))

		assert.Equal(t, 0, sink.MessageCount(), params)
		assert.Empty(t, sink.CanceledControls(), params)
	}
}
