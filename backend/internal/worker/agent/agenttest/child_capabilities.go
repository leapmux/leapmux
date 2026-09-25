package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// AssertChildCapabilities pins that plugin states each child capability that
// the provider's running agent has, and no other one. agentValue is a value of
// the provider's agent type; a nil pointer serves, because only its method set
// counts.
//
// A child tab reads these capabilities from AgentInfo before its root runs, so
// the plugin states them rather than the worker deriving them from a live agent.
// This suite is what keeps the stated value equal to the implemented one: a
// provider that implements InterruptChild but states nothing would draw no
// Interrupt control on a subagent tab that it can stop.
func AssertChildCapabilities(t testing.TB, plugin agent.Provider, agentValue any) {
	t.Helper()
	_, steers := agentValue.(agent.ChildSteerer)
	assert.Equal(t, steers, plugin.SupportsChildSteering(),
		"SupportsChildSteering must state whether the agent implements agent.ChildSteerer")
	_, interrupts := agentValue.(agent.ChildInterrupter)
	assert.Equal(t, interrupts, plugin.SupportsChildInterrupt(),
		"SupportsChildInterrupt must state whether the agent implements agent.ChildInterrupter")
}
