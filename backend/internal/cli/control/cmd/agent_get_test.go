package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

func TestRunAgentGet_SeparatesCurrentSettingsFromTheCatalog(t *testing.T) {
	id := fullTestID("agent-options")
	model := &leapmuxv1.AvailableOptionGroup{
		Id: optionids.Model, Label: "Model", CurrentValue: "current-model", Mutable: true,
		Options: []*leapmuxv1.AvailableOption{{
			Id: "current-model", Name: "Current model",
			SubGroups: []*leapmuxv1.AvailableOptionGroup{{
				Id: optionids.Effort, CurrentValue: "catalog-default",
				Options: []*leapmuxv1.AvailableOption{{Id: "catalog-default", Name: "Catalog default"}},
			}},
		}, {
			Id: "inactive-model", Name: "Inactive model",
			SubGroups: []*leapmuxv1.AvailableOptionGroup{{
				Id: "inactive-setting", CurrentValue: "unused-choice",
				Options: []*leapmuxv1.AvailableOption{{Id: "unused-choice", Name: "Unused choice"}},
			}},
		}},
	}
	fastMode := &leapmuxv1.AvailableOptionGroup{
		Id: "fastMode", Label: "Fast mode", CurrentValue: "off", DefaultValue: "on",
		ReadOnlyReason: "The current runtime controls this setting.",
		Options: []*leapmuxv1.AvailableOption{{
			Id: "on", Name: "On", Description: "Use the faster service tier.",
			Clears: []*leapmuxv1.OptionSideEffect{{GroupId: "another-setting", Value: "off"}},
		}, {Id: "off", Name: "Off"}},
	}
	agent := &leapmuxv1.AgentInfo{
		Id: id, Title: "Configuration", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			model,
			{Id: optionids.Effort, CurrentValue: "live-effort"},
			{Id: optionids.PermissionMode, CurrentValue: "live-permission"},
			fastMode,
			{Id: "unset-setting", DefaultValue: "unset-default"},
		},
	}
	startAgentWorker(t, id, &tabMetadataDispatcher{agents: []*leapmuxv1.AgentInfo{agent}})

	current := runControlText(t, "agent", "get", id)
	require.Equal(t, cli.ExitOK, current.Code, current.Stderr)
	for _, shown := range []string{"current-model", "live-effort", "live-permission", "fastMode", "off"} {
		assert.Contains(t, current.Stdout, shown)
	}
	for _, hidden := range []string{"catalog-default", "inactive-model", "unused-choice", "unset-setting"} {
		assert.NotContains(t, current.Stdout, hidden)
	}

	catalog := runControlText(t, "agent", "get", id, "--options")
	require.Equal(t, cli.ExitOK, catalog.Code, catalog.Stderr)
	for _, shown := range []string{
		"live-effort", "catalog-default", "inactive-model", "unused-choice",
		fastMode.GetReadOnlyReason(), "Use the faster service tier.", "another-setting",
	} {
		assert.Contains(t, catalog.Stdout, shown)
	}

	var envelope struct {
		Data struct {
			Settings []struct {
				ID      string `json:"id"`
				Current string `json:"current"`
			} `json:"settings"`
			Options []struct {
				ID             string `json:"id"`
				ReadOnlyReason string `json:"read_only_reason"`
				Choices        []struct {
					Clears []struct {
						GroupID string `json:"group_id"`
						Value   string `json:"value"`
					} `json:"clears"`
				} `json:"choices"`
			} `json:"options"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(runControl(t, "agent", "get", id).ok(t), &envelope))
	if assert.Len(t, envelope.Data.Settings, 1) {
		assert.Equal(t, "fastMode", envelope.Data.Settings[0].ID)
		assert.Equal(t, "off", envelope.Data.Settings[0].Current)
	}
	require.Len(t, envelope.Data.Options, 5)
	fastModeJSON := envelope.Data.Options[3]
	assert.Equal(t, "fastMode", fastModeJSON.ID)
	assert.Equal(t, fastMode.GetReadOnlyReason(), fastModeJSON.ReadOnlyReason)
	require.Len(t, fastModeJSON.Choices, 2)
	if assert.Len(t, fastModeJSON.Choices[0].Clears, 1) {
		assert.Equal(t, "another-setting", fastModeJSON.Choices[0].Clears[0].GroupID)
		assert.Equal(t, "off", fastModeJSON.Choices[0].Clears[0].Value)
	}
}
