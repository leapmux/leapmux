package mimo

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistration(t *testing.T) {
	t.Parallel()

	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, registration.Provider)
	assert.NotNil(t, registration.Start)
	assert.NotNil(t, registration.Locator)
	assert.IsType(t, mimoProvider{}, registration.Plugin)
	assert.Empty(t, registration.DefaultModels, "the running server states the models")
	assert.True(t, registration.ManagesEffort, "each model states its own variants")
	assert.False(t, registration.FixedPermissionModes, "the server can offer agents beyond the static seed")
	assert.Equal(t, "LEAPMUX_MIMO_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_MIMO_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.Equal(t, []string{agent.OptionIDEffort}, registration.AdditionalOptionIDs)
	assert.Equal(t, contracts.MiMoModeBuild, registration.PermissionDefaults.Fallback)
}

func TestRegistrationOptionGroups(t *testing.T) {
	t.Parallel()

	groups := Registration().OptionGroups
	modes := optionids.GroupByID(groups, agent.OptionIDPermissionMode)
	require.NotNil(t, modes)
	assert.Equal(t, ModeLabel, modes.GetLabel())
	assert.Equal(t, []string{contracts.MiMoModeBuild, contracts.MiMoModePlan}, optionIDs(modes))
	assert.Equal(t, contracts.MiMoDefaultMode, modes.GetDefaultValue())

	policies := optionids.GroupByID(groups, contracts.MiMoOptionPermissionPolicy)
	require.NotNil(t, policies)
	assert.Equal(t, PermissionPolicyLabel, policies.GetLabel())
	assert.Equal(t, []string{
		contracts.MiMoPermissionPolicyAsk, contracts.MiMoPermissionPolicySkip, contracts.MiMoPermissionPolicyBypass,
	}, optionIDs(policies))
	assert.Equal(t, contracts.MiMoPermissionPolicyAsk, policies.GetDefaultValue())
}

// A new agent must never start with MiMo's permission checks off. The
// default is the policy that asks, whatever order the table lists the
// policies in.
func TestRegistrationStartsOnThePolicyThatAsks(t *testing.T) {
	t.Parallel()

	defaults := Registration().ProviderOptionDefaults
	assert.Equal(t, contracts.MiMoPermissionPolicyAsk, defaults[contracts.MiMoOptionPermissionPolicy])
	for _, policy := range mimoPermissionPolicies {
		assert.Equal(t, policy.Id == contracts.MiMoPermissionPolicyAsk, policy.Default,
			"only Ask is the default policy, and %s is not it", policy.Id)
	}
}

func TestResolveResumeHandleKeepsTheTokenRule(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

func optionIDs(group *leapmuxv1.AvailableOptionGroup) []string {
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}
