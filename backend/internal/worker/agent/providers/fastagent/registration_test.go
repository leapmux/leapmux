package fastagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A session that carries no stored mode takes the one mode the default setup
// reports, so the settings panel always has a selection to show.
func TestFastagentRegistrationStatesItsPermissionFallback(t *testing.T) {
	t.Parallel()
	assert.Equal(t, contracts.FastagentModeAgent, Registration().PermissionDefaults.Fallback)
	assert.Equal(t, []string{agent.OptionIDPermissionMode}, Registration().AdditionalOptionIDs)
}

func TestFastAgentBypassRegistration(t *testing.T) {
	t.Parallel()
	groups := Registration().OptionGroups
	require.Len(t, groups, 2)
	assert.Equal(t, "bypassPermissions", groups[0].Id)
	assert.Equal(t, "false", groups[0].DefaultValue)
	assert.True(t, groups[0].Mutable)
	require.Len(t, groups[0].Options, 2)
	assert.Equal(t, "false", groups[0].Options[0].Id)
	assert.Equal(t, "true", groups[0].Options[1].Id)
	for _, choice := range groups[0].Options {
		assert.Contains(t, choice.Description, "restarts the agent")
		assert.Contains(t, choice.Description, "stops any active turn")
	}
}

func TestFastAgentNativeCommandsRegistration(t *testing.T) {
	t.Parallel()
	var found bool
	for _, group := range Registration().OptionGroups {
		if group.Id == "nativeCommands" {
			found = true
			assert.Equal(t, "false", group.DefaultValue)
			require.Len(t, group.Options, 2)
		}
	}
	require.True(t, found, "the native command route needs an explicit option")
}
