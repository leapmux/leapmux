package qoder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func outputStyleOptions(t *testing.T, a *Agent) (ids []string, names []string, current string) {
	t.Helper()
	group := qoderOptionGroup(a.OptionGroups(), OptionOutputStyle)
	require.NotNil(t, group)
	for _, option := range group.Options {
		ids = append(ids, option.Id)
		names = append(names, option.Name)
	}
	return ids, names, group.CurrentValue
}

// Qoder stores no style in the session, so Auto sends no flag on any launch.
func TestQoderOutputStyleArgs(t *testing.T) {
	t.Parallel()
	assert.Nil(t, qoderOutputStyleArgs(""))
	assert.Nil(t, qoderOutputStyleArgs("  "))
	assert.Nil(t, qoderOutputStyleArgs(qoderOutputStyleAuto))
	assert.Equal(t, []string{"--output-style", "Explanatory"}, qoderOutputStyleArgs(qoderOutputStyleExplanatory))
	assert.Equal(t, []string{"--output-style", "default"}, qoderOutputStyleArgs(qoderOutputStyleDefault))
	assert.Equal(t, []string{"--output-style", "Custom Probe"}, qoderOutputStyleArgs("Custom Probe"))
}

func TestQoderRegistrationOffersTheBuiltinOutputStyles(t *testing.T) {
	t.Parallel()
	group := qoderOptionGroup(Registration().OptionGroups, OptionOutputStyle)
	require.NotNil(t, group, "the settings allowlist must accept the output style axis")
	assert.Equal(t, "Output Style", group.Label)
	assert.Equal(t, qoderOutputStyleAuto, group.CurrentValue)
	var ids, names []string
	for _, option := range group.Options {
		ids = append(ids, option.Id)
		names = append(names, option.Name)
	}
	assert.Equal(t, []string{"auto", "default", "Explanatory", "Learning"}, ids)
	assert.Equal(t, []string{"Auto", "Default", "Explanatory", "Learning"}, names)
}

func TestQoderOutputStyleGroupListsTheReportedStylesOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.outputStyle = qoderOutputStyleExplanatory
	a.applyOutputStyles(qoderOutputStyleExplanatory, []string{"default", "Explanatory", "Learning", "Custom Probe", "Custom Probe", "", "auto"})
	ids, names, current := outputStyleOptions(t, a)
	assert.Equal(t, []string{"auto", "default", "Explanatory", "Learning", "Custom Probe"}, ids)
	assert.Equal(t, []string{"Auto", "Default", "Explanatory", "Learning", "Custom Probe"}, names)
	assert.Equal(t, qoderOutputStyleExplanatory, current)
	require.Equal(t, 1, sink.SettingsRefreshCount(), "a new style catalog refreshes the stored settings")
	assert.Equal(t, qoderOutputStyleExplanatory, sink.LastSettingsRefresh().Options[OptionOutputStyle])

	a.applyOutputStyles(qoderOutputStyleExplanatory, []string{"default", "Explanatory", "Learning", "Custom Probe", "Custom Probe", "", "auto"})
	assert.Equal(t, 1, sink.SettingsRefreshCount(), "an unchanged report refreshes nothing")
}

func TestQoderOutputStyleShowsTheStyleThatRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		launched string
		running  string
		styles   []string
		want     string
	}{
		{name: "auto shows auto whatever the settings style is", launched: "", running: "Explanatory", styles: []string{"default", "Explanatory"}, want: "auto"},
		{name: "explicit auto shows auto", launched: "auto", running: "default", styles: []string{"default"}, want: "auto"},
		{name: "an unknown name shows the default that runs", launched: "Nonexistent", running: "default", styles: []string{"default", "Explanatory"}, want: "default"},
		{name: "a known name shows itself", launched: "Learning", running: "Learning", styles: []string{"default", "Learning"}, want: "Learning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.outputStyle = tc.launched
			a.applyOutputStyles(tc.running, tc.styles)
			_, _, current := outputStyleOptions(t, a)
			assert.Equal(t, tc.want, current)
			assert.Equal(t, tc.want, a.SettingsSnapshot().SurfacedOptions[OptionOutputStyle])
		})
	}
}

// Before the initialize reply, the group shows the built-in styles and keeps a
// launched user style visible.
func TestQoderOutputStyleGroupBeforeTheReport(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.outputStyle = "Custom Probe"
	ids, _, current := outputStyleOptions(t, a)
	assert.Equal(t, []string{"auto", "default", "Explanatory", "Learning", "Custom Probe"}, ids)
	assert.Equal(t, "Custom Probe", current)
	assert.Equal(t, []string{"default", "Explanatory", "Learning"}, qoderBuiltinOutputStyles, "the group must not grow the shared built-in list")

	a.applyOutputStyles("", nil)
	ids, _, _ = outputStyleOptions(t, a)
	assert.Equal(t, []string{"auto", "default", "Explanatory", "Learning", "Custom Probe"}, ids, "a reply without a list keeps the built-in styles")
}

func TestQoderInitializeRecordsTheOutputStyles(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	a.outputStyle = qoderOutputStyleLearning
	a.SetStdinForTest(&qoderInitializeStdin{agent: a, reply: json.RawMessage(`{"models":[],"output_style":"Learning","available_output_styles":["default","Explanatory","Learning","Team Voice"]}`)})
	require.NoError(t, a.initializeStream())
	ids, _, current := outputStyleOptions(t, a)
	assert.Equal(t, []string{"auto", "default", "Explanatory", "Learning", "Team Voice"}, ids)
	assert.Equal(t, qoderOutputStyleLearning, current)
}

// The style fields are optional. A shape that Qoder does not send keeps the
// built-in styles and does not stop the agent.
func TestQoderInitializeIgnoresMalformedOutputStyles(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]string{
		"a list that is a string": `{"models":[],"output_style":"Learning","available_output_styles":"default"}`,
		"a style that is a list":  `{"models":[],"output_style":["Learning"],"available_output_styles":["default","Learning"]}`,
		"no style fields":         `{"models":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newGoalAgent(t)
			a.opts.HomeDir = t.TempDir()
			a.SetStdinForTest(&qoderInitializeStdin{agent: a, reply: json.RawMessage(reply)})
			require.NoError(t, a.initializeStream())
			ids, _, current := outputStyleOptions(t, a)
			assert.Contains(t, ids, "Learning")
			assert.Equal(t, qoderOutputStyleAuto, current)
		})
	}
}

// Qoder reads the style only at launch, so a change settles after a restart.
func TestQoderChangedOutputStyleRequiresRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		launched string
		running  string
		options  optionmap.Map
	}{
		{name: "auto to a style", launched: "", options: optionmap.Map{OptionOutputStyle: qoderOutputStyleExplanatory}},
		{name: "a style to auto", launched: qoderOutputStyleLearning, running: qoderOutputStyleLearning, options: optionmap.Map{OptionOutputStyle: qoderOutputStyleAuto}},
		{name: "a style to another", launched: qoderOutputStyleLearning, running: qoderOutputStyleLearning, options: optionmap.Map{OptionOutputStyle: "Custom Probe"}},
		{name: "with a live model change", launched: "", options: optionmap.Map{OptionOutputStyle: qoderOutputStyleDefault, agent.OptionIDModel: "mockprov/alternate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.outputStyle = tc.launched
			a.runningOutputStyle = tc.running
			a.model = "mockprov/primary"
			assert.Equal(t, agent.RestartRequiredSettings(tc.options), a.UpdateSettings(tc.options))
			assert.Equal(t, "mockprov/primary", a.model, "a restart request must not change the live model first")
		})
	}
}

func TestQoderUnchangedOutputStyleConfirmsLive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		launched string
		running  string
		chosen   string
	}{
		{launched: "", chosen: qoderOutputStyleAuto},
		{launched: qoderOutputStyleExplanatory, running: qoderOutputStyleExplanatory, chosen: qoderOutputStyleExplanatory},
		// Qoder ran `default` for an unknown name, so choosing `default` changes nothing.
		{launched: "Nonexistent", running: qoderOutputStyleDefault, chosen: qoderOutputStyleDefault},
	} {
		t.Run(tc.chosen, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.outputStyle = tc.launched
			a.runningOutputStyle = tc.running
			result := a.UpdateSettings(optionmap.Map{OptionOutputStyle: tc.chosen})
			assert.True(t, result.AppliedLive)
			settlement := result.Settlements[OptionOutputStyle]
			assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State)
			require.NotNil(t, settlement.Value)
			assert.Equal(t, tc.chosen, *settlement.Value)
			assert.Equal(t, tc.chosen, result.SurfacedOptions[OptionOutputStyle])
		})
	}
}
