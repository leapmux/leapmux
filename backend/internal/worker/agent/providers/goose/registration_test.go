package goose

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestGooseRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(gooseStaticOptionGroups, acp.ModeChannelPermissionMode), fallbackGooseCLIModes(),
		Registration().OptionGroups, gooseStaticOptionGroups)
}

// Goose exposes its reasoning axis and its LLM provider as server-driven
// config options. The registration states both ids, so a stored value of each
// is an option that the worker keeps before the daemon starts.
func TestGooseRegistrationStatesItsServerDrivenOptionIDs(t *testing.T) {
	t.Parallel()
	assert.ElementsMatch(t, []string{contracts.GooseConfigThinkingEffort, ConfigProvider}, Registration().AdditionalOptionIDs)
}
