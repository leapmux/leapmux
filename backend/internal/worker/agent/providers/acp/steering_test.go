package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

// TestAdvertisedACPSteerMethodDetection pins how ParseAdvertisedMethod reads
// the steer method that an initialize response advertises under a namespace.
// Each provider that steers this way pins its own namespace and method.
func TestAdvertisedACPSteerMethodDetection(t *testing.T) {
	t.Parallel()

	const namespace, method = "vendor", "_vendor/session/steer"
	assert.Equal(t, method, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{"sessionSteer":{"method":"_vendor/session/steer"}}}}}`), namespace, method))
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{}}}}`), namespace, method),
		"a namespace that advertises no steer method")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"description":"_vendor/session/steer"}`), namespace, method),
		"the method text outside the capability")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"other":{"sessionSteer":{"method":"_vendor/session/steer"}}}}}`), namespace, method),
		"the method under another namespace")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"vendor":{"sessionSteer":{"method":"_vendor/session/other"}}}}}`), namespace, method),
		"a method that this provider does not speak")
	assert.Empty(t, ParseAdvertisedMethod([]byte(`not json`), namespace, method))
}

// The base refuses a send while a prompt runs, for each ACP provider: the send
// reaches no RPC, and the base republishes the turn on demand.
func TestACPSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a, requests := newTestAgentForRPC(t)
	a.sink = agent.NewProviderServices(sink)
	a.wireTurnActive()
	a.promptActive = true
	err := a.SendInput("later turn", nil)
	assert.Empty(t, requests(), "a refused send must reach no RPC")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, err)
}
