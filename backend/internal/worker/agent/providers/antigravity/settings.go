package antigravity

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var antigravityStaticModes = []agent.OptionDef{
	{Id: contracts.AntigravityModeDefault, Name: "Default", Description: "Ask before every action that Antigravity does not allow inside the workspace.", Default: true},
	{Id: contracts.AntigravityModeAcceptEdits, Name: "Accept edits", Description: "Allow writes inside the workspace and ask before every other action."},
	{Id: contracts.AntigravityModePlan, Name: "Plan", Description: "Ask Antigravity to plan before implementation. Plan mode does not block writes."},
	{Id: contracts.AntigravityModeBypass, Name: "Bypass", Description: "Allow every tool action without asking."},
}

func antigravityModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, antigravityStaticModes)
}

func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	catalog, model, effort, mode := a.catalog, a.model, a.effort, a.mode
	a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(catalog.models, model, effort, agent.EffortGroupLabel, nil)
	return append(groups, antigravityModeGroup(mode))
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Mu.Lock()
	model, effort, mode := a.model, a.effort, a.mode
	catalog := a.catalog
	if requested := options[agent.OptionIDModel]; requested != "" {
		model = requested
	}
	if requested := options[agent.OptionIDEffort]; requested != "" {
		effort = requested
	}
	if requested := options[agent.OptionIDPermissionMode]; requested != "" && validAntigravityMode(requested) {
		mode = requested
	}
	_, settledEffort, err := catalog.resolveModel(model, effort)
	if err != nil {
		model, settledEffort = agent.DefaultModelSentinel, ""
	}
	a.model, a.effort, a.mode = model, settledEffort, mode
	if mode == contracts.AntigravityModeBypass {
		for _, state := range a.cascades {
			for _, control := range state.controls {
				if control.kind == "permission" {
					control.automatic = true
				}
			}
		}
	}
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(optionmap.Map{
		agent.OptionIDModel: model, agent.OptionIDEffort: settledEffort, agent.OptionIDPermissionMode: mode,
	})
	a.queuePermissionApprovals()
	return a.SettingsSnapshot()
}

func validAntigravityMode(mode string) bool {
	for _, definition := range antigravityStaticModes {
		if definition.Id == mode {
			return true
		}
	}
	return false
}

func (a *Agent) cascadeConfigLocked() (map[string]any, error) {
	variant, _, err := a.catalog.resolveModel(a.model, a.effort)
	if err != nil {
		return nil, err
	}
	return cascadeConfigForToken(variant.token, a.workspace), nil
}

func cascadeConfigForToken(token, workspace string) map[string]any {
	return map[string]any{
		"plannerConfig": map[string]any{
			"planModel":      token,
			"conversational": map[string]any{"agenticMode": true},
			"toolConfig": map[string]any{
				"runCommand": map[string]any{
					"enableTerminalSandbox": false,
					"autoCommandConfig":     map[string]any{"autoExecutionPolicy": "CASCADE_COMMANDS_AUTO_EXECUTION_OFF"},
				},
				"permissionConfig": map[string]any{
					"enterpriseConfig": map[string]any{"enforceWorkspaceValidation": true},
					"effectiveGrants":  map[string]any{"allow": []string{"read_file(" + workspace + ")"}},
					"defaultGrants":    map[string]any{"ask": []string{"write_file(/)"}},
				},
				"autoAllowAllInteractions": false,
				"notifyUser":               map[string]any{"artifactReviewMode": "ARTIFACT_REVIEW_MODE_ALWAYS"},
			},
		},
		"conversationHistoryConfig": map[string]any{},
	}
}

func slashCommandItem(name string) map[string]any {
	return map[string]any{"item": map[string]any{"slashCommand": map[string]any{
		"info": map[string]any{"name": name, "type": "SLASH_COMMAND_TYPE_SYSTEM"},
	}}}
}

func (a *Agent) loadSlashCommands() {
	a.Mu.Lock()
	if a.root == nil {
		a.Mu.Unlock()
		return
	}
	id := a.root.id
	config, err := a.cascadeConfigLocked()
	a.Mu.Unlock()
	if err != nil {
		return
	}
	commands, err := a.rpc.slashCommands(a.Context(), id, config)
	if err == nil {
		a.Mu.Lock()
		a.slashCommands = commands
		a.Mu.Unlock()
	}
}
