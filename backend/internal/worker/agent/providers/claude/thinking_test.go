package claude

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThinkingNativeCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, capability string
		off              bool
	}{
		{"required", `,"supportsThinkingOff":false`, false},
		{"optional", `,"supportsThinkingOff":true`, true},
		{"absent", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newTestAgentWithControlProtocolEnv(t, `GO_HELPER_MODELS=[{"value":"opus[1m]","displayName":"Opus"`+tc.capability+`}]`)
			defer stopTestAgent(a)
			require.NoError(t, a.runStartupHandshake(context.Background(), agent.Options{Options: map[string]string{OptionAlwaysThinking: AlwaysThinkingOff}}))
			group := optionids.GroupByID(a.OptionGroups(), OptionAlwaysThinking)
			require.NotNil(t, group)
			expected := AlwaysThinkingOn
			if tc.off {
				expected = AlwaysThinkingOff
			}
			assert.Equal(t, expected, group.GetCurrentValue())
			assert.Equal(t, tc.off, group.GetMutable())
			assert.Equal(t, tc.off, agenttest.OptionByID(group, AlwaysThinkingOff) != nil)
			models := optionids.GroupByID(a.OptionGroups(), agent.OptionIDModel)
			require.NotNil(t, models)
			model := agenttest.OptionByID(models, "opus[1m]")
			require.NotNil(t, model)
			sub := optionids.GroupByID(model.SubGroups, OptionAlwaysThinking)
			require.NotNil(t, sub)
			assert.Equal(t, tc.off, agenttest.OptionByID(sub, AlwaysThinkingOff) != nil)
			result := a.UpdateSettings(map[string]string{OptionAlwaysThinking: AlwaysThinkingOff})
			assert.Equal(t, expected, result.SurfacedOptions[OptionAlwaysThinking])
		})
	}
}

func TestThinkingDefaultCapabilityAndModelSwitch(t *testing.T) {
	t.Parallel()
	a := newTestAgentWithControlProtocolEnv(t, `GO_HELPER_MODELS=[{"value":"default","supportsThinkingOff":false},{"value":"haiku","supportsThinkingOff":true}]`)
	defer stopTestAgent(a)
	require.NoError(t, a.runStartupHandshake(context.Background(), agent.Options{Options: map[string]string{agent.OptionIDModel: agent.DefaultModelSentinel, OptionAlwaysThinking: AlwaysThinkingOff}}))
	require.Equal(t, "opus[1m]", a.model)
	require.Equal(t, AlwaysThinkingOn, a.alwaysThinking)
	require.False(t, optionids.GroupByID(a.OptionGroups(), OptionAlwaysThinking).GetMutable())
	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "haiku", OptionAlwaysThinking: AlwaysThinkingOff})
	require.True(t, result.AppliedLive)
	require.Equal(t, AlwaysThinkingOff, result.SurfacedOptions[OptionAlwaysThinking])
	require.True(t, optionids.GroupByID(a.OptionGroups(), OptionAlwaysThinking).GetMutable())
	result = a.UpdateSettings(map[string]string{agent.OptionIDModel: "opus[1m]", OptionAlwaysThinking: AlwaysThinkingOff})
	require.True(t, result.AppliedLive)
	require.Equal(t, AlwaysThinkingOn, result.SurfacedOptions[OptionAlwaysThinking])
}

func TestThinkingCapabilitiesKeepCatalogIdentity(t *testing.T) {
	t.Parallel()
	enabled, disabled := true, false
	got := thinkingOffCapabilities([]claudeCodeModelInfo{
		{Value: "haiku", SupportsThinkingOff: &disabled},
		{Value: "claude-haiku-5-5", SupportsThinkingOff: &enabled},
		{Value: "default5", SupportsThinkingOff: &disabled},
		{Value: "Default", SupportsThinkingOff: &enabled},
		{Value: "sonnet", Disabled: true, SupportsThinkingOff: &disabled},
		{Value: "sonnet", SupportsThinkingOff: &enabled},
		{Value: "unknown"},
		{Value: "unknown", SupportsThinkingOff: &disabled},
		{Value: "", SupportsThinkingOff: &disabled},
	})
	assert.Equal(t, map[string]bool{"haiku": false, "default": true, "sonnet": true}, got)
}
