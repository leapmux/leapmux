package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Copilot issues the ordering token of its turn flag from the same critical
// section that reads the flag, so each publish carries a token that rises.
func TestCopilotTurnActiveTokensRise(t *testing.T) {
	t.Parallel()

	a, sink := newNativeCopilotForEvents(t)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

// A Copilot agent that already runs a turn refuses more input as busy, and
// republishes that turn on demand.
func TestCopilotSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	a, sink := newNativeCopilotForEvents(t)
	a.stateMu.Lock()
	a.active = true
	a.stateMu.Unlock()
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later turn", nil))
}
