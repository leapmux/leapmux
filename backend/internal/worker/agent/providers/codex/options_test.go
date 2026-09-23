package codex

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodexStaticOptionGroups_DefaultMatchesSeed guards that each Codex axis's picker
// default badge (the static group's DefaultValue) agrees with the value seeded into a fresh
// agent's launch options (codexOptionDefaults). The two are stamped from independent
// constants of equal value, so this catches a future edit that changes one without the other
// -- which would launch the agent on one value while the popover badges a different default.
func TestCodexStaticOptionGroups_DefaultMatchesSeed(t *testing.T) {
	t.Parallel()

	seeds := codexOptionDefaults()
	require.NotEmpty(t, seeds)
	groups := Registration().OptionGroups
	require.NotEmpty(t, groups)
	matched := 0
	for _, g := range groups {
		seed, ok := seeds[g.GetId()]
		if !ok {
			continue // model/effort/permission axes are not seeded via codexOptionDefaults
		}
		matched++
		assert.Equal(t, seed, g.GetDefaultValue(),
			"axis %q: static group default must match the launch seed", g.GetId())
	}
	assert.Equal(t, len(seeds), matched, "every seeded axis has a matching static group")
}

// TestCodexStaticOptionGroups_CarryOrder guards the regression where the Codex
// static-fallback templates omitted Order (0), which sorts a provider group ahead
// of the model group (order 10) in the frontend's order-based layout.
func TestCodexStaticOptionGroups_CarryOrder(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	groups := registry.StaticOptionGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NotEmpty(t, groups)
	for _, g := range groups {
		assert.NotZero(t, g.GetOrder(),
			"registered Codex group %q must carry a non-zero display order so the static fallback can't sort it ahead of the model group", g.GetId())
		assert.Greater(t, g.GetOrder(), agent.OptionOrderModel,
			"registered Codex group %q must sort after the model group", g.GetId())
	}
}

// TestStaticOptionGroupsForProvider_CodexOrdersAfterModel verifies the assembled
// static fallback never places a non-model group ahead of the model group.
func TestStaticOptionGroupsForProvider_CodexOrdersAfterModel(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	// A Codex agent that is not running and has no cached catalog is served the
	// static fallback.
	groups := agent.NewManager(registry, nil).OptionGroups("absent-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "")
	require.NotEmpty(t, groups)
	var sawModel bool
	for _, g := range groups {
		if g.GetId() == agent.OptionIDModel {
			sawModel = true
			continue
		}
		assert.GreaterOrEqual(t, g.GetOrder(), agent.OptionOrderModel,
			"static-fallback group %q must not sort before the model group", g.GetId())
	}
	assert.True(t, sawModel, "Codex static fallback includes a model group from its default catalog")
}
