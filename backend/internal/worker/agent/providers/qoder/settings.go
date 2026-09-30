package qoder

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const qoderSettingsTimeout = 2 * time.Second

// OptionGroups returns every configuration axis this agent currently reports.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	model := a.model
	models := a.models
	effort := a.effort
	mode := a.displayedModeLocked()
	a.mu.Unlock()
	if len(models) == 0 {
		a.configuredModelsOnce.Do(func() {
			configured, err := readQoderConfiguredModels(a.opts)
			if err != nil {
				slog.Warn("qoder: configured models unavailable", "agent_id", a.AgentID(), "error", err)
				return
			}
			a.configuredModels = configured
		})
		models = a.configuredModels
	}
	groups := []*leapmuxv1.AvailableOptionGroup{
		qoderEffortGroup(effort),
		qoderPermissionModeGroup(mode),
	}
	if modelGroup := qoderModelGroup(models, model); modelGroup != nil {
		groups = append([]*leapmuxv1.AvailableOptionGroup{modelGroup}, groups...)
	}
	return groups
}

// SettingsSnapshot returns the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := map[string]string{
		agent.OptionIDEffort:         a.effort,
		agent.OptionIDPermissionMode: a.displayedModeLocked(),
	}
	if a.model != "" {
		values[agent.OptionIDModel] = a.model
	}
	return agent.ConfirmedSettings(values)
}

// displayedModeLocked shows Plan while Qoder's separate Plan state is active.
func (a *Agent) displayedModeLocked() string {
	if a.planMode {
		return contracts.QoderModePlan
	}
	return a.permissionMode
}

// UpdateSettings sends model and mode choices to Qoder's control channel.
// A changed effort needs a restart because Qoder takes it at launch.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if effort, ok := options[agent.OptionIDEffort]; ok && effort != a.opts.Effort() {
		return agent.RestartRequiredSettings(options)
	}
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: agent.OptionSettlements{}}
	if value, ok := options[agent.OptionIDModel]; ok {
		if err := a.applyModel(value); err != nil {
			result.Settlements[agent.OptionIDModel] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		} else {
			result.Settlements[agent.OptionIDModel] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	if value, ok := options[agent.OptionIDPermissionMode]; ok {
		if err := a.applyPermissionMode(value); err != nil {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		} else {
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	for id, value := range options {
		switch id {
		case agent.OptionIDEffort:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		case agent.OptionIDModel, agent.OptionIDPermissionMode:
		default:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		}
	}
	a.mu.Lock()
	surfaced := optionmap.Map{
		agent.OptionIDEffort:         a.effort,
		agent.OptionIDPermissionMode: a.displayedModeLocked(),
	}
	if a.model != "" {
		surfaced[agent.OptionIDModel] = a.model
	}
	a.mu.Unlock()
	result.SurfacedOptions = surfaced
	return result
}

// applyModel sends set_model and records the model that Qoder acknowledged.
func (a *Agent) applyModel(model string) error {
	if model == "" {
		return errors.New("the qoder model id is empty")
	}
	body, err := json.Marshal(map[string]string{
		"subtype": contracts.QoderControlRequestSubtypeSetModel,
		"model":   model,
	})
	if err != nil {
		return err
	}
	if _, err := a.sendControlAndWait(string(body), qoderSettingsTimeout); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

// applyPermissionMode sends set_permission_mode and records the mode.
func (a *Agent) applyPermissionMode(mode string) error {
	body, err := json.Marshal(map[string]string{
		"subtype": contracts.QoderControlRequestSubtypeSetPermissionMode,
		"mode":    mode,
	})
	if err != nil {
		return err
	}
	resp, err := a.sendControlAndWait(string(body), qoderSettingsTimeout)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if resp.Mode != "" {
		a.permissionMode = resp.Mode
	} else {
		a.permissionMode = mode
	}
	a.mu.Unlock()
	return nil
}
