package muse

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseRegistrationDerivesStartupDefaultsFromTheContract(t *testing.T) {
	t.Parallel()
	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE, registration.Provider)
	require.NotNil(t, registration.Plugin)
	require.NotNil(t, registration.Start)
	assert.True(t, registration.ManagesEffort)
	assert.Contains(t, registration.AdditionalOptionIDs, agent.OptionIDEffort)
	for _, descriptor := range contracts.MuseStartupOptionGroups {
		var found *leapmuxv1.AvailableOptionGroup
		for _, group := range registration.OptionGroups {
			if group.Id == descriptor.ID {
				found = group
				break
			}
		}
		require.NotNil(t, found, descriptor.ID)
		assert.Equal(t, descriptor.DefaultValue, found.DefaultValue)
		assert.Equal(t, descriptor.DefaultValue, registration.PermissionDefaults.NewSession[descriptor.ID])
		assert.False(t, found.Mutable)
		assert.Equal(t, descriptor.ReadOnlyReason, found.ReadOnlyReason)
		require.Len(t, found.Options, len(descriptor.Options))
		for index, option := range descriptor.Options {
			assert.Equal(t, option.Value, found.Options[index].Id)
			assert.Equal(t, option.Label, found.Options[index].Name)
			assert.Equal(t, option.Description, found.Options[index].Description)
		}
	}
}

func TestMuseRegistrationReturnsIndependentOptionGroups(t *testing.T) {
	t.Parallel()
	first := Registration()
	second := Registration()
	require.NotEmpty(t, first.OptionGroups)
	first.OptionGroups[0].Label = "Changed by a caller"
	first.OptionGroups[0].Options[0].Name = "Changed by a caller"
	assert.Equal(t, "Permissions", second.OptionGroups[0].Label)
	assert.Equal(t, "On request", second.OptionGroups[0].Options[0].Name)
}
