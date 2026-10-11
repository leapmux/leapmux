package ohmypi

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationWiresThePlugin(t *testing.T) {
	t.Parallel()
	_, ok := Registration().Plugin.(ompProvider)
	assert.True(t, ok, "the registration hands out this package's plugin")
}

func TestTheRegistrationStatesTheProvider(t *testing.T) {
	t.Parallel()
	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, registration.Provider)
	assert.Equal(t, "LEAPMUX_OHMYPI_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_OHMYPI_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.Equal(t, "write", registration.PermissionDefaults.Fallback)
	assert.True(t, registration.FixedPermissionModes)
	assert.Nil(t, registration.DefaultModels, "omp's models come from its own configuration")
	assert.Equal(t, []string{agent.OptionIDEffort}, registration.AdditionalOptionIDs)
	require.Len(t, registration.OptionGroups, 1)
	approval := registration.OptionGroups[0]
	assert.Equal(t, agent.OptionIDPermissionMode, approval.GetId())
	assert.Equal(t, approvalModes, optionIDs(approval), "the launch vocabulary and the offered modes are one list")
	assert.Equal(t, registration.PermissionDefaults.Fallback, approval.GetDefaultValue(),
		"a session with no stored mode runs the mode that the picker shows as the default")
	assert.True(t, approval.GetMutable(), "a change restarts the agent with the new mode")
}
