package agent_test

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// testRegistry registers a synthetic provider (testRegistration) under every
// AgentProvider value. A test that needs only SOME registry -- to hold stub
// agents, or to dispatch to them -- builds its manager on it. A test about a
// provider's real registration belongs to that provider, and a test about every
// real registration belongs to the composition root.
var testRegistry = func() *agent.Registry {
	var regs []agent.Registration
	for value := range leapmuxv1.AgentProvider_name {
		provider := leapmuxv1.AgentProvider(value)
		if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			regs = append(regs, testRegistration(provider))
		}
	}
	return agenttest.MustNewRegistry(regs...)
}()
