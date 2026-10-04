package commandcode

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationUsesNativeSafeDefaults(t *testing.T) {
	t.Setenv("CMD_LOCAL_ONLY", "")
	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_COMMAND_CODE, registration.Provider)
	assert.Equal(t, contracts.CommandCodePermissionModeDefault, registration.PermissionDefaults.Fallback)
	assert.Equal(t, contracts.CommandCodePermissionModeDefault, registration.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	assert.True(t, registration.FixedPermissionModes)
	require.Len(t, registration.OptionGroups, 1)
	assert.Equal(t, agent.OptionIDPermissionMode, registration.OptionGroups[0].Id)
	assert.Equal(t, []string{agent.OptionIDEffort}, registration.AdditionalOptionIDs)
	require.Len(t, registration.DefaultModels, 87)
	assert.Equal(t, agent.DefaultModelSentinel, registration.DefaultModels[0].Id)
	assert.True(t, registration.DefaultModels[0].IsDefault)
}
