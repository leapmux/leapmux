package junie

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// The worker persists the effort that Junie reports (confirmedOptions at the
// startup handoff), so the merged map of every later edit carries it back. With
// no --effort at launch, that value is Junie's own default and the effort that
// the running process uses already. An edit of another axis must not restart the
// agent. The probe of a real Junie 26.9.22 showed a live mode write and a live
// model write that the next prompt follows at once.
func TestJunieUpdateSettingsKeepsTheReportedDefaultEffortLive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		options      optionmap.Map
		wantRequests int
		check        func(t *testing.T, a *Agent, result agent.SettingsApplyResult)
	}{
		{
			name:         "a mode change",
			options:      optionmap.Map{agent.OptionIDPermissionMode: "plan", agent.OptionIDEffort: "high"},
			wantRequests: 1,
			check: func(t *testing.T, _ *Agent, result agent.SettingsApplyResult) {
				assert.Equal(t, "plan", result.SurfacedOptions[agent.OptionIDPermissionMode])
			},
		},
		{
			name:         "a model change",
			options:      optionmap.Map{agent.OptionIDModel: "custom:mock-effort", agent.OptionIDEffort: "high"},
			wantRequests: 1,
			check: func(t *testing.T, a *Agent, _ agent.SettingsApplyResult) {
				assert.Equal(t, "custom:mock-effort", a.ModelForTest())
			},
		},
		{
			name:    "the default restated",
			options: optionmap.Map{agent.OptionIDEffort: "high"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, requestCount := junieAgentWithEffort(t, "high", "")

			result := a.UpdateSettings(tc.options)

			require.True(t, result.AppliedLive, "the running process already uses the reported effort")
			assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
			assert.Equal(t, tc.wantRequests, requestCount())
			if tc.check != nil {
				tc.check(t, a, result)
			}
		})
	}
}

// A session that reports no effort axis has no effort that the process uses
// that the request could match, so a requested effort restarts the agent. The
// restart carries --effort to the next launch.
func TestJunieUpdateSettingsRestartsForAnEffortWhenNoneIsReported(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)
	options := optionmap.Map{agent.OptionIDEffort: "high"}

	result := a.UpdateSettings(options)

	assert.Equal(t, agent.RestartRequiredSettings(options), result)
	assert.Empty(t, requests())
}

// An effort that differs from the one that the running process uses needs a
// launch with --effort, because the ACP effort write changes only the session
// option and model turns read the launch flag.
func TestJunieUpdateSettingsRestartsWhenEffortDiffersFromTheReportedDefault(t *testing.T) {
	t.Parallel()
	a, requestCount := junieAgentWithEffort(t, "high", "")
	options := optionmap.Map{agent.OptionIDEffort: "low"}

	result := a.UpdateSettings(options)

	assert.Equal(t, agent.RestartRequiredSettings(options), result)
	assert.Zero(t, requestCount(), "the provider cannot apply the launch override through ACP")
}
