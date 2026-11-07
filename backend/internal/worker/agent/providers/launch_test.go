package providers

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
)

// TestEveryRegisteredProviderResolvesALaunch pins that a launch reads each
// provider's registered locator. Controlled resolution never runs an installed CLI.
func TestEveryRegisteredProviderResolvesALaunch(t *testing.T) {
	registry := Registry()
	assert.Len(t, registry.Providers(), len(agentlabels.AllProviders()))
	t.Run("every registered provider resolves", func(t *testing.T) {
		for _, provider := range registry.Providers() {
			reg, _ := registry.Registration(provider)
			require.True(t, reg.Locator.Valid(), "provider %v needs a valid registered locator", provider)
			reg.Locator = agenttest.AnsweringLocator(launch.Spec{Program: "/controlled/provider"}, launch.Found)
			spec, err := providerkit.ResolveLaunch(context.Background(), agent.Options{}, reg)
			require.NoError(t, err)
			assert.Equal(t, "/controlled/provider", spec.Program)
		}
	})
	t.Run("an unregistered provider has no locator to launch", func(t *testing.T) {
		_, ok := registry.Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
		assert.False(t, ok)
	})
}
