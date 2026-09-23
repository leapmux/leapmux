package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
)

func TestKiloRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(kiloStaticOptionGroups, acp.ModeChannelPrimaryAgent), fallbackKiloPrimaryAgents(),
		Registration().OptionGroups, kiloStaticOptionGroups)
	spec := opencode.FamilyRegistrationSpec{
		OptionGroups: kiloStaticOptionGroups,
		EnvModelKey:  "LEAPMUX_KILO_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_KILO_DEFAULT_EFFORT",
	}
	fromSpec := opencode.FamilyRegistration(spec)
	registered := Registration()
	assert.Equal(t, spec.OptionGroups, fromSpec.OptionGroups)
	assert.Equal(t, spec.EnvModelKey, fromSpec.EnvModelKey)
	assert.Equal(t, spec.EnvEffortKey, fromSpec.EnvEffortKey)
	assert.Equal(t, fromSpec.OptionGroups, registered.OptionGroups)
	assert.Equal(t, fromSpec.EnvModelKey, registered.EnvModelKey)
	assert.Equal(t, fromSpec.EnvEffortKey, registered.EnvEffortKey)
}
