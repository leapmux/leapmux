package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryCoversEveryProvider pins the composition root's one job: every
// AgentProvider value but UNSPECIFIED has exactly one registration, so no
// provider the proto declares can be missing from the worker.
func TestRegistryCoversEveryProvider(t *testing.T) {
	t.Parallel()

	r := Registry()
	var want []leapmuxv1.AgentProvider
	for value := range leapmuxv1.AgentProvider_name {
		if p := leapmuxv1.AgentProvider(value); p != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			want = append(want, p)
		}
	}
	assert.ElementsMatch(t, want, r.Providers())
	assert.Empty(t, unregistered(r))
}

// TestRegistrationsAreInEnumOrder pins the order of the explicit list, so a
// reader finds each provider where the enum puts it and a duplicate stands out.
func TestRegistrationsAreInEnumOrder(t *testing.T) {
	t.Parallel()

	regs := Registrations()
	for i := 1; i < len(regs); i++ {
		assert.Less(t, regs[i-1].Provider, regs[i].Provider, "entry %d", i)
	}
}

// TestUnregisteredReportsAMissingProvider pins the check Registry panics on: a
// registry without one provider reports exactly that provider.
func TestUnregisteredReportsAMissingProvider(t *testing.T) {
	t.Parallel()

	var regs []agent.Registration
	for _, reg := range Registrations() {
		if reg.Provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_PI {
			regs = append(regs, reg)
		}
	}
	r, err := agent.NewRegistry(regs...)
	require.NoError(t, err)
	assert.Equal(t, []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI}, unregistered(r))
}
