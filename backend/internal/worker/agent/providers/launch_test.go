package providers

import (
	"context"
	"os/exec"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
)

// TestEveryRegisteredProviderResolvesALaunch pins that a launch reads each
// provider's own registered locator, the SAME one the availability scan reads,
// and that each one resolves.
func TestEveryRegisteredProviderResolvesALaunch(t *testing.T) {
	registry := Registry()
	assert.Len(t, registry.Providers(), len(agentlabels.AllProviders()))
	// Every provider must resolve to SOMETHING, or its Start returns an error before it
	// spawns anything. NewRegistry already refuses a locator that states no way to find
	// the program; this is where a locator that states one but resolves nothing surfaces.
	t.Run("every registered provider resolves", func(t *testing.T) {
		shell, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("no POSIX shell on this machine")
		}
		for _, provider := range registry.Providers() {
			reg, _ := registry.Registration(provider)
			spec, err := providerkit.ResolveLaunch(context.Background(), agent.Options{Shell: shell}, reg)
			// ZCode and Codewhale resolve against the real machine, so each may
			// legitimately report that it is not installed. Every other provider must
			// name a program.
			if err != nil {
				assert.Contains(t, []leapmuxv1.AgentProvider{
					leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
					leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE,
				}, provider, "provider %v has neither a resolver nor a registered binary name", provider)
				continue
			}
			assert.NotEmptyf(t, spec.Program, "provider %v resolved to an empty program", provider)
		}
	})
	t.Run("an unregistered provider has no locator to launch", func(t *testing.T) {
		_, ok := registry.Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
		assert.False(t, ok)
	})
}
