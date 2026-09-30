package junie

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func junieAgentWithEffort(t *testing.T, currentEffort, launchEffort string) (*Agent, func() int) {
	t.Helper()
	a, requests := newJunieAgentForRPC(t)
	a.launchEffortOverride = launchEffort
	a.SetModelForTest("custom:mock-model")
	a.Mu.Lock()
	a.ApplyOptionGroupsLockedForTest([]acp.ConfigOption{{
		ID: agent.OptionIDEffort, Name: "Effort", CurrentValue: currentEffort,
		Options: []acp.ConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}},
	}})
	a.Mu.Unlock()
	return a, func() int { return len(requests()) }
}

func TestJunieUpdateSettingsRestartsBeforeChangingEffort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options optionmap.Map
	}{
		{name: "effort only", options: optionmap.Map{agent.OptionIDEffort: "low"}},
		{name: "model and effort", options: optionmap.Map{agent.OptionIDModel: "custom:mock-effort", agent.OptionIDEffort: "low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, requestCount := junieAgentWithEffort(t, "high", "high")

			result := a.UpdateSettings(tc.options)

			assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
			assert.Zero(t, requestCount(), "the restart must apply the full option map before any ACP write")
			assert.Equal(t, "custom:mock-model", a.ModelForTest())
			assert.Equal(t, "high", agent.CurrentOptions(a.OptionGroups())[agent.OptionIDEffort])
		})
	}
}

func TestJunieUpdateSettingsKeepsUnchangedEffortLive(t *testing.T) {
	t.Parallel()
	a, requestCount := junieAgentWithEffort(t, "high", "high")

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
	assert.Zero(t, requestCount())
}

// UpdateSettings receives the full merged map. A missing effort means the next
// launch must omit --effort; it does not mean an unchanged partial update.
func TestJunieUpdateSettingsRestartsWhenEffortClears(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options optionmap.Map
	}{
		{name: "missing effort", options: optionmap.Map{}},
		{name: "explicit empty effort", options: optionmap.Map{agent.OptionIDEffort: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, requestCount := junieAgentWithEffort(t, "high", "high")

			result := a.UpdateSettings(tc.options)

			assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
			assert.Zero(t, requestCount())
			assert.Equal(t, "high", agent.CurrentOptions(a.OptionGroups())[agent.OptionIDEffort])
		})
	}
}

func TestJunieUpdateSettingsKeepsAbsentEffortLiveWhenNoneRuns(t *testing.T) {
	t.Parallel()
	a, requestCount := junieAgentWithEffort(t, "", "")

	result := a.UpdateSettings(optionmap.Map{})

	require.True(t, result.AppliedLive)
	assert.Zero(t, requestCount())
}

func TestJunieUpdateSettingsKeepsDefaultEffortOnModeChange(t *testing.T) {
	t.Parallel()
	a, requestCount := junieAgentWithEffort(t, "high", "")
	options := optionmap.Map{agent.OptionIDPermissionMode: "plan"}

	result := a.UpdateSettings(options)

	require.True(t, result.AppliedLive, "a provider default is not a stored effort override")
	assert.Equal(t, "plan", result.SurfacedOptions[agent.OptionIDPermissionMode])
	assert.Equal(t, 1, requestCount(), "only the mode change reaches Junie's ACP session")
}

func TestJunieUpdateSettingsRestartsForExplicitDefaultEffort(t *testing.T) {
	t.Parallel()
	a, requestCount := junieAgentWithEffort(t, "high", "")
	options := optionmap.Map{agent.OptionIDEffort: "high"}

	result := a.UpdateSettings(options)

	assert.Equal(t, agent.RestartRequiredSettings(options), result,
		"an explicit High override must reach Junie's next launch")
	assert.Zero(t, requestCount(), "the provider cannot apply the launch override through ACP")
}
