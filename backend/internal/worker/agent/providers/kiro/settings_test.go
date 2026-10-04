package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestKiroPolicyPresetTableStartsWithTheSafeValue(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, kiroPolicyPresets)
	first := kiroPolicyPresets[0]
	assert.Equal(t, kiroPolicyAsk, first.id)
	assert.Empty(t, first.presets, "the default states no preset, so Kiro's own rules decide")

	seen := map[string]bool{}
	for _, spec := range kiroPolicyPresets {
		assert.False(t, seen[spec.id], "value %q appears twice", spec.id)
		seen[spec.id] = true
		assert.NotEmpty(t, spec.name, spec.id)
		assert.NotEmpty(t, spec.description, spec.id)
		for _, preset := range spec.presets {
			assert.NotEqual(t, kiroPolicyAsk, preset, "ask is LeapMux's own word, and Kiro has no such preset")
		}
	}
	allowAll, ok := policyPresetFor(contracts.KiroPolicyPresetAllowAll)
	require.True(t, ok, "the bypass preset of the browser must be a value of the table")
	assert.Equal(t, []string{contracts.KiroPolicyPresetAllowAll}, allowAll.presets)
}

func TestKiroInitialPolicyPresetFallsBackToAsk(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "dev-shell", initialPolicyPreset("dev-shell"))
	assert.Equal(t, kiroPolicyAsk, initialPolicyPreset(""), "a launch that states nothing asks")
	assert.Equal(t, kiroPolicyAsk, initialPolicyPreset("read-only-shell"), "a value that this build does not offer asks")
}

// An agent that no start configured yet reads the safe default, never a
// preset that nobody chose.
func TestKiroUnconfiguredAgentRunsTheAskPolicy(t *testing.T) {
	t.Parallel()
	a := &Agent{}

	assert.Equal(t, kiroPolicyAsk, a.currentPolicyPreset().id)
	params := map[string]any{}
	a.adjustSessionParams(acp.MethodSessionNew, params)
	assert.NotContains(t, params, "_meta", "the ask policy states no preset")
}

func TestKiroSessionParamsStateThePolicyAndNoReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy string
		method string
		want   any
	}{
		{name: "ask on new", policy: kiroPolicyAsk, method: acp.MethodSessionNew, want: nil},
		{name: "ask on load", policy: kiroPolicyAsk, method: acp.MethodSessionLoad, want: map[string]any{"kiro": map[string]any{"noReplay": true}}},
		{name: "preset on new", policy: "edit-workspace", method: acp.MethodSessionNew, want: map[string]any{"kiro": map[string]any{"policyPreset": []string{"edit-workspace"}}}},
		{name: "preset on load", policy: contracts.KiroPolicyPresetAllowAll, method: acp.MethodSessionLoad, want: map[string]any{"kiro": map[string]any{
			"policyPreset": []string{contracts.KiroPolicyPresetAllowAll}, "noReplay": true,
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &Agent{}
			a.configure(agent.Options{Options: map[string]string{contracts.KiroOptionPolicyPreset: tc.policy}})
			params := map[string]any{"cwd": "/w"}

			a.adjustSessionParams(tc.method, params)

			assert.Equal(t, tc.want, params["_meta"])
			assert.Equal(t, "/w", params["cwd"], "the adjustment keeps the base's own params")
		})
	}
}

func TestKiroPolicyGroupCarriesTheLiveValue(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{Options: map[string]string{contracts.KiroOptionPolicyPreset: "dev-shell"}}, nil)

	group := optionids.GroupByID(a.OptionGroups(), contracts.KiroOptionPolicyPreset)
	require.NotNil(t, group)
	assert.Equal(t, "dev-shell", group.GetCurrentValue())
	assert.Equal(t, kiroPolicyAsk, group.GetDefaultValue())
	assert.Equal(t, "Permissions", group.GetLabel())
	assert.True(t, group.GetMutable())
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	assert.Equal(t, []string{kiroPolicyAsk, "edit-workspace", "dev-shell", "read-all", contracts.KiroPolicyPresetAllowAll}, ids)
}

func TestKiroUpdateSettingsRestartsForANewPolicy(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)
	options := map[string]string{contracts.KiroOptionPolicyPreset: contracts.KiroPolicyPresetAllowAll}

	result := a.UpdateSettings(options)

	assert.Equal(t, agent.RestartRequiredSettings(options), result, "Kiro reads the preset only when a session opens")
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), acp.MethodSessionSetConfigOption))
}

func TestKiroUpdateSettingsLeavesAnUnchangedPolicyToTheBase(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)

	result := a.UpdateSettings(map[string]string{contracts.KiroOptionPolicyPreset: kiroPolicyAsk})

	settlement := result.Settlements[contracts.KiroOptionPolicyPreset]
	assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State, "the preset already runs, so no restart")
	require.NotNil(t, settlement.Value)
	assert.Equal(t, kiroPolicyAsk, *settlement.Value)
}

func TestKiroUpdateSettingsKeepsThePolicyForAnUnknownValue(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{Options: map[string]string{contracts.KiroOptionPolicyPreset: "dev-shell"}}, nil)
	options := map[string]string{contracts.KiroOptionPolicyPreset: "read-only-shell"}

	result := a.UpdateSettings(options)

	settlement := result.Settlements[contracts.KiroOptionPolicyPreset]
	assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State, "the session keeps its preset, and no restart runs")
	require.NotNil(t, settlement.Value)
	assert.Equal(t, "dev-shell", *settlement.Value, "the snapshot reports the preset that runs")
	assert.Equal(t, "read-only-shell", options[contracts.KiroOptionPolicyPreset], "the caller's map stays as it was")
	assert.Equal(t, "dev-shell", agent.CurrentOptions(a.OptionGroups())[contracts.KiroOptionPolicyPreset])
}

func TestKiroApplyLocalOptionRefusesALivePolicyChange(t *testing.T) {
	t.Parallel()
	a := &Agent{}

	handled, err := a.applyLocalOption(contracts.KiroOptionPolicyPreset, "dev-shell")
	assert.True(t, handled)
	assert.ErrorIs(t, err, errPolicyAppliesAtSessionOpen)

	handled, err = a.applyLocalOption("somethingElse", "x")
	assert.False(t, handled, "an option that the policy group does not own is not handled")
	assert.NoError(t, err)
}

func TestKiroDecorateModelStatesTheCreditRate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		description string
		meta        string
		want        string
	}{
		{name: "with a description", description: "Claude Sonnet 4.5", meta: `{"kiro":{"rateMultiplier":1.3,"rateUnit":"Credit"}}`, want: "Claude Sonnet 4.5 (1.3x credit rate)"},
		{name: "without a description", meta: `{"kiro":{"rateMultiplier":0.4,"rateUnit":"Credit"}}`, want: "0.4x credit rate"},
		{name: "without a unit", description: "m", meta: `{"kiro":{"rateMultiplier":2}}`, want: "m (2x credit rate)"},
		{name: "a lowercase unit", description: "m", meta: `{"kiro":{"rateMultiplier":1,"rateUnit":"token"}}`, want: "m (1x token rate)"},
		{name: "no meta", description: "m", meta: ``, want: "m"},
		{name: "null", description: "m", meta: `null`, want: "m"},
		{name: "meta that is not an object", description: "m", meta: `"x"`, want: "m"},
		{name: "kiro that is not an object", description: "m", meta: `{"kiro":[1.3]}`, want: "m"},
		{name: "no kiro namespace", description: "m", meta: `{"other":{"rateMultiplier":1}}`, want: "m"},
		{name: "no rate", description: "m", meta: `{"kiro":{"rateUnit":"Credit"}}`, want: "m"},
		{name: "zero rate", description: "m", meta: `{"kiro":{"rateMultiplier":0}}`, want: "m"},
		{name: "negative rate", description: "m", meta: `{"kiro":{"rateMultiplier":-1}}`, want: "m"},
		{name: "a rate that is not a number", description: "m", meta: `{"kiro":{"rateMultiplier":"high"}}`, want: "m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := &agent.ModelInfo{Id: "m", Description: tc.description}
			decorateModel(model, json.RawMessage(tc.meta))
			assert.Equal(t, tc.want, model.Description)
		})
	}
}

func TestKiroLowerFirst(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "credit", lowerFirst("Credit"))
	assert.Equal(t, "credit", lowerFirst("credit"))
	assert.Equal(t, "", lowerFirst(""))
	assert.Equal(t, "1x", lowerFirst("1x"))
}

func TestKiroRegistrationStatesTheStaticGroups(t *testing.T) {
	t.Parallel()
	registration := Registration()
	modes := optionids.GroupByID(registration.OptionGroups, agent.OptionIDPermissionMode)
	require.NotNil(t, modes)
	ids := make([]string, 0, len(modes.GetOptions()))
	for _, option := range modes.GetOptions() {
		ids = append(ids, option.GetId())
	}
	assert.Equal(t, []string{contracts.KiroModeDefault, "spec", "quick-spec", "bug-fix", contracts.KiroModePlan, "autonomous"}, ids)
	policy := optionids.GroupByID(registration.OptionGroups, contracts.KiroOptionPolicyPreset)
	require.NotNil(t, policy)
	assert.Empty(t, policy.GetCurrentValue(), "the static template states no current value")
	assert.Equal(t, kiroPolicyAsk, registration.ProviderOptionDefaults[contracts.KiroOptionPolicyPreset])
	assert.Equal(t, contracts.KiroModeDefault, registration.PermissionDefaults.Fallback)
	assert.Equal(t, contracts.KiroModeDefault, registration.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	assert.ElementsMatch(t, []string{contracts.KiroConfigEffortLevel, kiroConfigThinking, kiroConfigAutopilot, kiroConfigContentCollection}, registration.AdditionalOptionIDs)
	assert.Equal(t, "LEAPMUX_KIRO_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_KIRO_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.Empty(t, registration.DefaultModels, "the account's catalog is the only source of models")
}
