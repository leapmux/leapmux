package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turnPublisher is the part of a provider that AssertRisingTurnTokens drives.
type turnPublisher interface {
	PublishTurnActive() agent.TurnState
}

// AssertRisingTurnTokens publishes the turn flag of provider three times and
// asserts that each publish reaches sink with a token that rises.
//
// The token is what lets the Worker tell a publish it overtook from a current
// one, and it only does that if it RISES with each publish and comes from the
// same critical section that reads the flag. A provider that reused a token, or
// took one outside that section, would order nothing -- and a stale value would
// latch a turn that is over.
func AssertRisingTurnTokens(t *testing.T, sink *Sink, provider turnPublisher) {
	t.Helper()
	provider.PublishTurnActive()
	provider.PublishTurnActive()
	provider.PublishTurnActive()

	seqs := sink.TurnSeqs()
	require.Len(t, seqs, 3, "each publish carries a token")
	assert.Greater(t, seqs[1], seqs[0], "the token rises with each publish")
	assert.Greater(t, seqs[2], seqs[1])
}
