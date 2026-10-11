package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestRegistration(t *testing.T) {
	t.Parallel()
	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, registration.Provider)
	assert.NotNil(t, registration.Start)
	assert.True(t, registration.Locator.Valid())
	assert.Nil(t, registration.DefaultModels, "the catalog belongs to the provider the user configured")
	assert.True(t, registration.ManagesEffort)
	assert.True(t, registration.FixedPermissionModes)
	assert.Equal(t, []string{agent.OptionIDEffort}, registration.AdditionalOptionIDs)
	assert.Equal(t, map[string]string{contracts.CodewhaleOptionMode: contracts.CodewhaleModeAgent}, registration.ProviderOptionDefaults)
	assert.Equal(t, contracts.CodewhalePostureAsk, registration.PermissionDefaults.Fallback)
	assert.Equal(t, "LEAPMUX_CODEWHALE_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_CODEWHALE_DEFAULT_EFFORT", registration.EnvEffortKey)

	require.Len(t, registration.OptionGroups, 2)
	mode, posture := registration.OptionGroups[0], registration.OptionGroups[1]
	assert.Equal(t, contracts.CodewhaleOptionMode, mode.GetId())
	assert.Equal(t, contracts.CodewhaleDefaultMode, mode.GetDefaultValue())
	assert.Equal(t, agent.OptionIDPermissionMode, posture.GetId())
	var postures []string
	for _, option := range posture.GetOptions() {
		postures = append(postures, option.GetId())
	}
	assert.Equal(t, []string{contracts.CodewhalePostureAsk, contracts.CodewhalePostureAutoReview, contracts.CodewhalePostureFullAccess}, postures)
}
