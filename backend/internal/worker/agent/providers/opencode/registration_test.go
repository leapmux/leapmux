package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestOpenCodeRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(opencodeStaticOptionGroups, acp.ModeChannelPrimaryAgent), fallbackOpenCodePrimaryAgents(),
		Registration().OptionGroups, opencodeStaticOptionGroups)
}
