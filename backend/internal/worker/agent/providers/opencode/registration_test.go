package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
)

func TestOpenCodeRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(opencodeStaticOptionGroups, acp.ModeChannelPrimaryAgent), fallbackOpenCodePrimaryAgents(),
		Registration().OptionGroups, opencodeStaticOptionGroups)
	spec := FamilyRegistrationSpec{
		OptionGroups: opencodeStaticOptionGroups,
		EnvModelKey:  "LEAPMUX_OPENCODE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_OPENCODE_DEFAULT_EFFORT",
	}
	fromSpec := FamilyRegistration(spec)
	registered := Registration()
	assert.Equal(t, spec.OptionGroups, fromSpec.OptionGroups)
	assert.Equal(t, spec.EnvModelKey, fromSpec.EnvModelKey)
	assert.Equal(t, spec.EnvEffortKey, fromSpec.EnvEffortKey)
	assert.Equal(t, fromSpec.OptionGroups, registered.OptionGroups)
	assert.Equal(t, fromSpec.EnvModelKey, registered.EnvModelKey)
	assert.Equal(t, fromSpec.EnvEffortKey, registered.EnvEffortKey)
}
