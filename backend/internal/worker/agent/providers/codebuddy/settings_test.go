package codebuddy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodebuddySettingsOfferTheNativeModelCatalog(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.model = "custom-local:primary"
	a.models = []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}
	group := optionids.GroupByID(a.OptionGroups(), agent.OptionIDModel)
	require.NotNil(t, group)
	assert.Equal(t, "custom-local:primary", group.GetCurrentValue())
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "custom-local:primary", group.GetOptions()[0].GetId())
	assert.Equal(t, "custom-local:alternate", group.GetOptions()[1].GetId())
	assert.Equal(t, "custom-local:primary", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
}

func TestCodebuddyModelGroupKeepsTheCurrentModelOutsideTheCatalog(t *testing.T) {
	t.Parallel()
	group := codebuddyModelGroup([]codebuddyModelInfo{{ID: "custom-local:alternate", Name: "Alternate"}}, "custom-local:primary")
	require.NotNil(t, group)
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "custom-local:primary", group.GetOptions()[0].GetId())
	assert.Equal(t, "custom-local:alternate", group.GetOptions()[1].GetId())
	assert.Equal(t, "custom-local:primary", group.GetDefaultValue())
	assert.Nil(t, codebuddyModelGroup(nil, ""))
}

func TestCodebuddyModelCatalogDropsEmptyAndDuplicateIds(t *testing.T) {
	t.Parallel()
	models := normalizeCodebuddyModels([]codebuddyModelInfo{
		{ID: " ", Name: "Blank"},
		{ID: " custom-local:primary ", Name: "Primary"},
		{ID: "custom-local:primary", Name: "Duplicate"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	})
	assert.Equal(t, []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}, models)
}

func TestCodebuddyModelChangeRejectsAnUnavailableModel(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.model = "custom-local:primary"
	a.models = []codebuddyModelInfo{{ID: "custom-local:primary"}, {ID: "custom-local:alternate"}}

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "custom-local:missing"})
	assert.False(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, "custom-local:primary", result.SurfacedOptions[agent.OptionIDModel])
}

func TestCodebuddyEffortRequiresResumeRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options map[string]string
	}{
		{name: "reasoning effort", options: map[string]string{agent.OptionIDEffort: "low"}},
		{name: "effort and model", options: map[string]string{agent.OptionIDEffort: "low", agent.OptionIDModel: "custom-local:alternate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.model = "custom-local:primary"
			a.models = []codebuddyModelInfo{{ID: "custom-local:primary"}, {ID: "custom-local:alternate"}}
			a.opts = agent.Options{Options: map[string]string{
				agent.OptionIDModel:          "custom-local:primary",
				agent.OptionIDPermissionMode: contracts.CodebuddyModeBypassPermissions,
				agent.OptionIDEffort:         "high",
			}}
			a.permissionMode = contracts.CodebuddyModeBypassPermissions
			a.effort = "high"

			result := a.UpdateSettings(tc.options)
			assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
		})
	}
}

func TestCodebuddyUnchangedEffortIsConfirmedLive(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.opts = agent.Options{Options: map[string]string{agent.OptionIDEffort: "high"}}
	a.effort = "high"

	result := a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "high", *result.Settlements[agent.OptionIDEffort].Value)
}
