package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestKiloRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(kiloStaticOptionGroups, acp.ModeChannelPrimaryAgent), fallbackKiloPrimaryAgents(),
		Registration().OptionGroups, kiloStaticOptionGroups)
}
