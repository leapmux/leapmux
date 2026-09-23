package pi

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelAndEffortGroups_EffortLabel verifies the effort axis is labeled per the
// caller: Pi calls it "Thinking Level" (its CLI's set_thinking_level concept), Codex
// "Effort". The label rides on both the top-level group and each model's sub_groups so
// a model switch stays consistently named.
func TestModelAndEffortGroups_EffortLabel(t *testing.T) {
	t.Parallel()

	models := []*agent.ModelInfo{{
		Id:               "m1",
		SupportedEfforts: []*agent.EffortInfo{{Id: "low", Name: "Low"}, {Id: "high", Name: "High"}},
		DefaultEffort:    "high",
	}}

	pi := providerkit.ModelAndEffortGroups(models, "m1", "high", ThinkingLevelLabel, nil)
	piEffort := optionids.GroupByID(pi, agent.OptionIDEffort)
	require.NotNil(t, piEffort)
	assert.Equal(t, ThinkingLevelLabel, piEffort.GetLabel(), "Pi labels its effort axis 'Thinking Level'")

	// The per-model sub_groups carry the same label (used by the model-switch swap).
	piModel := optionids.GroupByID(pi, agent.OptionIDModel)
	require.NotNil(t, piModel)
	require.NotEmpty(t, piModel.GetOptions())
	piSubEffort := optionids.GroupByID(piModel.GetOptions()[0].GetSubGroups(), agent.OptionIDEffort)
	require.NotNil(t, piSubEffort)
	assert.Equal(t, ThinkingLevelLabel, piSubEffort.GetLabel(), "the model-switch sub_group label matches")

	codex := providerkit.ModelAndEffortGroups(models, "m1", "high", agent.EffortGroupLabel, nil)
	codexEffort := optionids.GroupByID(codex, agent.OptionIDEffort)
	require.NotNil(t, codexEffort)
	assert.Equal(t, "Effort", codexEffort.GetLabel(), "Codex keeps the default 'Effort' label")
}

// TestPiStaticOptionGroups_ThinkingLevelLabel verifies the not-running static fallback
// for Pi (built from the registered modelSubGroups) also labels the thinking-level
// group "Thinking Level", so the popover stays consistent while a Pi agent restarts.
func TestPiStaticOptionGroups_ThinkingLevelLabel(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(agenttest.MustNewRegistry(Registration()), nil)
	groups := m.OptionGroups("absent-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, DefaultModel)
	eg := optionids.GroupByID(groups, agent.OptionIDEffort)
	require.NotNil(t, eg, "Pi's static fallback surfaces a thinking-level group for its default model")
	assert.Equal(t, ThinkingLevelLabel, eg.GetLabel())
}
