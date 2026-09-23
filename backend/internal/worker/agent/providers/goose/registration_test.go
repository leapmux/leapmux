package goose

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestGooseRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(gooseStaticOptionGroups, acp.ModeChannelPermissionMode), fallbackGooseCLIModes(),
		Registration().OptionGroups, gooseStaticOptionGroups)
}
