package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func qoderOptionGroup(groups []*leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOptionGroup {
	for _, group := range groups {
		if group.Id == id {
			return group
		}
	}
	return nil
}

// Every launch states an effort, because on --resume Qoder CLI 1.1.78 keeps
// the stored reasoning settings of the session unless the launch states one.
// Qoder turns thinking back on for an explicit effort, so Off sends `none`.
// `--thinking enabled` needs a token budget, so On sends `adaptive`. Auto
// sends no thinking flag.
func TestQoderReasoningArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		effort   string
		thinking string
		want     []string
	}{
		{name: "auto thinking and auto effort", effort: agent.EffortAuto, thinking: qoderThinkingAuto, want: []string{"--reasoning-effort", "auto"}},
		{name: "auto thinking and an unset effort", effort: "", thinking: "", want: []string{"--reasoning-effort", "auto"}},
		{name: "auto thinking keeps the effort", effort: contracts.QoderEffortLevelLow, thinking: qoderThinkingAuto, want: []string{"--reasoning-effort", "low"}},
		{name: "off replaces a concrete effort with none", effort: contracts.QoderEffortLevelHigh, thinking: qoderThinkingOff, want: []string{"--thinking", "disabled", "--reasoning-effort", "none"}},
		{name: "off with auto effort", effort: agent.EffortAuto, thinking: qoderThinkingOff, want: []string{"--thinking", "disabled", "--reasoning-effort", "none"}},
		{name: "on with a concrete effort", effort: contracts.QoderEffortLevelMax, thinking: qoderThinkingOn, want: []string{"--thinking", "adaptive", "--reasoning-effort", "max"}},
		{name: "on with auto effort", effort: agent.EffortAuto, thinking: qoderThinkingOn, want: []string{"--thinking", "adaptive", "--reasoning-effort", "auto"}},
		{name: "on with an unset effort", effort: "", thinking: qoderThinkingOn, want: []string{"--thinking", "adaptive", "--reasoning-effort", "auto"}},
		{name: "an unknown thinking value is auto", effort: contracts.QoderEffortLevelMedium, thinking: "sometimes", want: []string{"--reasoning-effort", "medium"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, qoderReasoningArgs(tc.effort, tc.thinking))
		})
	}
}

func TestQoderThinkingValue(t *testing.T) {
	t.Parallel()
	for _, value := range []string{qoderThinkingAuto, qoderThinkingOff, qoderThinkingOn} {
		assert.Equal(t, value, qoderThinkingValue(value))
	}
	for _, value := range []string{"", "disabled", "enabled", "TRUE", "on"} {
		assert.Equal(t, qoderThinkingAuto, qoderThinkingValue(value), "%q reads as Auto", value)
	}
}

func TestQoderRegistrationOffersExtendedThinking(t *testing.T) {
	t.Parallel()
	registration := Registration()
	group := qoderOptionGroup(registration.OptionGroups, OptionThinking)
	require.NotNil(t, group, "the settings allowlist must accept the thinking axis")
	assert.Equal(t, "Extended Thinking", group.Label)
	var ids []string
	for _, option := range group.Options {
		ids = append(ids, option.Id)
	}
	assert.Equal(t, []string{qoderThinkingAuto, qoderThinkingOff, qoderThinkingOn}, ids)
	assert.Equal(t, qoderThinkingAuto, group.CurrentValue)
}

func TestQoderThinkingGroupAndSnapshotReportTheLaunchValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stored string
		want   string
	}{
		{stored: "", want: qoderThinkingAuto},
		{stored: qoderThinkingOff, want: qoderThinkingOff},
		{stored: qoderThinkingOn, want: qoderThinkingOn},
	} {
		t.Run(tc.want+"/"+tc.stored, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.thinking = tc.stored
			group := qoderOptionGroup(a.OptionGroups(), OptionThinking)
			require.NotNil(t, group)
			assert.Equal(t, tc.want, group.CurrentValue)
			assert.Equal(t, tc.want, a.SettingsSnapshot().SurfacedOptions[OptionThinking])
		})
	}
}

// Qoder reads the thinking flag only at launch, so a change settles after a
// restart, and the running agent keeps the value it started with.
func TestQoderChangedThinkingRequiresRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		running string
		options optionmap.Map
	}{
		{name: "auto to off", running: "", options: optionmap.Map{OptionThinking: qoderThinkingOff}},
		{name: "off to on", running: qoderThinkingOff, options: optionmap.Map{OptionThinking: qoderThinkingOn}},
		{name: "on to auto", running: qoderThinkingOn, options: optionmap.Map{OptionThinking: qoderThinkingAuto}},
		{name: "with a live model change", running: qoderThinkingOn, options: optionmap.Map{OptionThinking: qoderThinkingOff, agent.OptionIDModel: "mockprov/alternate"}},
		{name: "with the running effort", running: "", options: optionmap.Map{OptionThinking: qoderThinkingOn, agent.OptionIDEffort: agent.EffortAuto}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.thinking = tc.running
			a.model = "mockprov/primary"
			assert.Equal(t, agent.RestartRequiredSettings(tc.options), a.UpdateSettings(tc.options))
			assert.Equal(t, tc.running, a.thinking, "a restart request must not change the running value")
			assert.Equal(t, "mockprov/primary", a.model, "a restart request must not change the live model first")
		})
	}
}

func TestQoderUnchangedThinkingConfirmsLive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		running string
		chosen  string
	}{
		{running: "", chosen: qoderThinkingAuto},
		{running: qoderThinkingOff, chosen: qoderThinkingOff},
	} {
		t.Run(tc.chosen, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.thinking = tc.running
			result := a.UpdateSettings(optionmap.Map{OptionThinking: tc.chosen})
			assert.True(t, result.AppliedLive)
			settlement := result.Settlements[OptionThinking]
			assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State)
			require.NotNil(t, settlement.Value)
			assert.Equal(t, tc.chosen, *settlement.Value)
			assert.Equal(t, tc.chosen, result.SurfacedOptions[OptionThinking])
		})
	}
}

// An effort chosen while thinking is Off still restarts, so the launch keeps
// the new effort in the options; the Off launch then sends the effort `none`.
func TestQoderEffortChangeWhileThinkingIsOffRestarts(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.thinking = qoderThinkingOff
	options := optionmap.Map{agent.OptionIDEffort: contracts.QoderEffortLevelLow}
	assert.Equal(t, agent.RestartRequiredSettings(options), a.UpdateSettings(options))
	assert.Equal(t, []string{"--thinking", "disabled", "--reasoning-effort", "none"}, qoderReasoningArgs(contracts.QoderEffortLevelLow, qoderThinkingOff))
}
