package goose

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsEffortConfigOption covers the category-then-id matching that lets the effort override
// and the strongest-first sort fire whether or not the daemon supplies the thought_level
// category -- mirroring the model/mode channels' well-known-id fallback. The only
// provider-convention id it accepts is the one the running provider declares.
func TestIsEffortConfigOption(t *testing.T) {
	t.Parallel()

	assert.True(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "x", Category: "thought_level"}, ""), "category match")
	assert.True(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: agent.OptionIDEffort}, ""), "OpenCode/Kilo effort id (no category)")
	assert.True(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "thinking_effort"}, contracts.GooseConfigThinkingEffort), "Goose's declared effort id (no category)")
	assert.False(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "thinking_effort"}, ""),
		"another provider's convention id is not an effort axis for a provider that declares none")
	assert.False(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "reasoning_effort"}, contracts.GooseConfigThinkingEffort),
		"an id the running provider does not declare is not matched")
	assert.False(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "allow_all"}, ""), "a non-effort config option is not matched")
	assert.False(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: "model", Category: "model"}, ""), "the model channel is not effort")
	assert.False(t, acp.IsEffortConfigOptionForTest(acp.ConfigOption{ID: ""}, ""),
		"an empty declared id never matches an option with an empty id")
}

// TestBuildOptionGroup_EffortSortedByKnownIDWithoutCategory guards that an effort axis is
// reordered strongest-first even when the daemon omits the thought_level category, as long as
// its id is the running provider's effort id (isEffortConfigOption). Servers report effort
// weakest-first. The same option under a provider that declares no such id keeps the server's
// order, because it is not that provider's effort axis.
func TestBuildOptionGroup_EffortSortedByKnownIDWithoutCategory(t *testing.T) {
	t.Parallel()

	option := acp.ConfigOption{
		ID: contracts.GooseConfigThinkingEffort, Name: "Thinking Effort", // no Category
		Options: []acp.ConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}
	ids := func(grp *leapmuxv1.AvailableOptionGroup) []string {
		var order []string
		for _, o := range grp.GetOptions() {
			order = append(order, o.GetId())
		}
		return order
	}
	assert.Equal(t, []string{"high", "medium", "low"}, ids(acp.BuildOptionGroupForTest(option, "high", contracts.GooseConfigThinkingEffort)),
		"effort options are reordered strongest-first by id even without a thought_level category")
	assert.Equal(t, []string{"low", "medium", "high"}, ids(acp.BuildOptionGroupForTest(option, "high", "")),
		"an undeclared convention id keeps the server's order")
}

// TestValidateLaunchOptions_ACPProviderSkipsPermissionMode guards [S1]: an ACP provider discovers its
// permission modes from the daemon (its static group is only a seed), so a mode NOT in the seed must
// NOT be rejected at spawn -- the running session validates the real value.
func TestValidateLaunchOptions_ACPProviderSkipsPermissionMode(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	goose := leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE
	require.NoError(t, registry.ValidateLaunchOptions(goose, optionmap.Map{agent.OptionIDPermissionMode: "a-dynamic-daemon-mode"}),
		"an ACP provider's daemon-discovered permission mode is not rejected against the static seed")
}
