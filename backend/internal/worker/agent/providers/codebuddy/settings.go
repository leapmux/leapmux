package codebuddy

import (
	"encoding/json"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// OptionGroups returns every configuration axis this agent currently reports.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	model := a.model
	models := a.models
	effort := a.effort
	mode := a.permissionMode
	a.mu.Unlock()
	groups := []*leapmuxv1.AvailableOptionGroup{
		codebuddyEffortGroup(effort),
		codebuddyPermissionModeGroup(mode),
	}
	if modelGroup := codebuddyModelGroup(models, model); modelGroup != nil {
		groups = append([]*leapmuxv1.AvailableOptionGroup{modelGroup}, groups...)
	}
	return groups
}

// SettingsSnapshot returns the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := optionmap.Map{
		agent.OptionIDEffort:         a.effort,
		agent.OptionIDPermissionMode: a.permissionMode,
	}
	if a.model != "" {
		values[agent.OptionIDModel] = a.model
	}
	return agent.ConfirmedSettings(values)
}

// UpdateSettings applies model and permission changes live. CodeBuddy takes
// effort from the launch arguments on every prompt, so a changed effort needs
// a session-preserving process restart before any other option changes.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if effort, ok := options[agent.OptionIDEffort]; ok && effort != a.opts.Effort() {
		return agent.RestartRequiredSettings(options)
	}
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: agent.OptionSettlements{}}
	if model, ok := options[agent.OptionIDModel]; ok {
		if err := a.applyModel(model); err != nil {
			result.Settlements[agent.OptionIDModel] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		} else {
			result.Settlements[agent.OptionIDModel] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &model}
		}
	}
	if mode, ok := options[agent.OptionIDPermissionMode]; ok {
		if err := a.applyPermissionMode(mode); err != nil {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		} else {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &mode}
		}
	}
	if effort, ok := options[agent.OptionIDEffort]; ok {
		result.Settlements[agent.OptionIDEffort] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &effort}
	}
	for id := range options {
		if id != agent.OptionIDModel && id != agent.OptionIDPermissionMode && id != agent.OptionIDEffort {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
	}
	result.SurfacedOptions = a.SettingsSnapshot().SurfacedOptions
	return result
}

// applyModel changes the current native session. The native response must
// confirm the exact model that the user chose.
func (a *Agent) applyModel(model string) error {
	a.mu.Lock()
	current := a.model
	models := a.models
	sessionID := a.sessionID
	a.mu.Unlock()
	if model == "" || (!codebuddyModelOffered(models, model) && model != current) {
		return fmt.Errorf("CodeBuddy does not offer model %q", model)
	}
	if model == current {
		return nil
	}
	body, err := json.Marshal(struct {
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
	}{Subtype: "set_model", SessionID: sessionID, Model: model})
	if err != nil {
		return err
	}
	result, err := a.sendControlAndWait(string(body), 2*time.Second)
	if err != nil {
		return err
	}
	if result.Model != model {
		return fmt.Errorf("CodeBuddy confirmed model %q instead of %q", result.Model, model)
	}
	a.mu.Lock()
	a.model = result.Model
	a.mu.Unlock()
	return nil
}
