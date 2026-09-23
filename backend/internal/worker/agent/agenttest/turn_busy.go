package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertBusyRefusalRepublishesTheTurn pins what a provider that already runs a
// turn does with more input: SendInput returned err, and provider publishes
// through sink.
//
// The provider reports ErrAgentBusy, never ErrNoActiveTurn. The two sentinels
// state opposite conditions, and the queue reads them differently: ErrAgentBusy
// is transient, so the item waits for the turn to end, while ErrNoActiveTurn says
// that steering has no target.
//
// Manager.SendInput turns that refusal into a republish of the turn flag, and
// TestManagerSendInputRepublishesTheTurnARefusalDisproves pins that half. This
// assertion pins the sentinel, and that the provider can republish on demand --
// PublishTurnActive is the interface method that the Manager calls.
func AssertBusyRefusalRepublishesTheTurn(t *testing.T, sink *Sink, provider agent.Agent, err error) {
	t.Helper()
	assert.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.NotErrorIs(t, err, agent.ErrNoActiveTurn,
		"a busy agent has a turn; the no-active-turn sentinel states the opposite")

	// Every provider must answer the republish with the turn that the refusal
	// proves is in flight. The LAST value is what matters, not the count: Claude's
	// sendInput already publishes from a defer that also covers its error paths
	// and its steer, so it answers twice and the others answer once.
	provider.PublishTurnActive()
	last, published := sink.LastTurnActive()
	require.True(t, published, "the refused provider republishes on demand")
	assert.True(t, last, "and what it republishes is the turn that caused the refusal")
}
