//go:build unix

package goose

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_CachedGenericGroupsSurviveModelChangeForACPProvider verifies the C19 fix:
// a provider with no model-dependent groups (the ACP permission-mode / primary-agent
// providers, whose reasoning/effort axes are model-independent server-driven config options)
// keeps serving its cached catalog across a since-changed model, instead of falling
// through to a degenerate static fallback that would drop the option groups. The Claude
// model-stamp fall-through (above) must NOT apply here.
//
// Goose stands for that camp. Native Copilot does NOT belong to it any more: each of its
// models states its own effort tiers, so a model change there must rebuild them.
func TestManager_CachedGenericGroupsSurviveModelChangeForACPProvider(t *testing.T) {
	registry := agenttest.MustNewRegistry(Registration())
	m := agent.NewManager(registry, nil)
	const goose = leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE
	require.False(t, registry.ManagesEffort(goose),
		"precondition: Goose has no model-dependent groups")

	// The running agent reported a model group (stamps the cache at "gpt-5") plus a
	// server-driven thinking_effort config option that the Goose static fallback never
	// reproduces (its static groups are only the permission-mode group).
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "gpt-5", Options: []*leapmuxv1.AvailableOption{{Id: "gpt-5"}, {Id: "gpt-4"}}},
		{Id: ConfigThinkingEffort, Label: "Thinking Effort", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}},
	}
	m.PreloadCache("a1", groups)

	// An offline model edit changes the requested model away from the stamp. The cache
	// (with thinking_effort) must still be served -- the model-independent option
	// can't be rebuilt from the static fallback.
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", goose, "gpt-4"), ConfigThinkingEffort),
		"a since-changed model still serves the cached option group for a provider with no model-dependent groups")
}
