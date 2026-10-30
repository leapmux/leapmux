package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
)

func TestKiloRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(kiloStaticOptionGroups, acp.ModeChannelPrimaryAgent), fallbackKiloPrimaryAgents(),
		Registration().OptionGroups, kiloStaticOptionGroups)
	registered := Registration()
	assert.Equal(t, kiloStaticOptionGroups, registered.OptionGroups)
	assert.Equal(t, "LEAPMUX_KILO_DEFAULT_MODEL", registered.EnvModelKey)
	assert.Equal(t, "LEAPMUX_KILO_DEFAULT_EFFORT", registered.EnvEffortKey)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, registered.Provider)
	assert.IsType(t, kiloProvider{}, registered.Plugin)
	assert.Equal(t, []string{agent.OptionIDEffort}, registered.AdditionalOptionIDs)
	assert.NotNil(t, registered.Start)
	assert.NotNil(t, registered.Locator)
}

func TestKiloRegistrationServesAutoApprove(t *testing.T) {
	t.Parallel()
	assert.Same(t, AutoApproveGroup, optionids.GroupByID(Registration().OptionGroups, contracts.ClientApprovalOptionAutoApprove))
}
