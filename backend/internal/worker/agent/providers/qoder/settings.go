package qoder

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
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
	modelsReported := a.modelsReported
	effort := a.runningEffortLocked()
	thinking := qoderThinkingValue(a.thinking)
	outputStyleGroup := a.outputStyleGroupLocked()
	mode := a.displayedModeLocked()
	a.mu.Unlock()
	if !modelsReported {
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
		qoderThinkingGroup(thinking),
		outputStyleGroup,
		qoderPermissionModeGroup(mode),
	}
	if modelGroup := qoderModelGroup(models, model, !modelsReported); modelGroup != nil {
		groups = append([]*leapmuxv1.AvailableOptionGroup{modelGroup}, groups...)
	}
	return groups
}

// SettingsSnapshot returns the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := map[string]string{
		agent.OptionIDEffort:         a.runningEffortLocked(),
		OptionThinking:               qoderThinkingValue(a.thinking),
		OptionOutputStyle:            a.displayedOutputStyleLocked(),
		agent.OptionIDPermissionMode: a.displayedModeLocked(),
	}
	if a.model != "" {
		values[agent.OptionIDModel] = a.model
	}
	return agent.ConfirmedSettings(values)
}

// displayedOutputStyleLocked shows Auto for an agent that launched with Auto.
// Otherwise it shows the style that the initialize reply reports, so a name
// that Qoder does not know shows `default`, the style that runs. Before the
// reply, it shows the launch value.
func (a *Agent) displayedOutputStyleLocked() string {
	if strings.TrimSpace(a.outputStyle) == "" || a.outputStyle == qoderOutputStyleAuto {
		return qoderOutputStyleAuto
	}
	if a.runningOutputStyle != "" {
		return a.runningOutputStyle
	}
	return a.outputStyle
}

// outputStyleGroupLocked lists the styles that the session reports, or the
// built-in styles before a report. It keeps the current style visible when the
// list lacks it.
func (a *Agent) outputStyleGroupLocked() *leapmuxv1.AvailableOptionGroup {
	current := a.displayedOutputStyleLocked()
	styles := qoderBuiltinOutputStyles
	if a.outputStyles != nil {
		styles = a.outputStyles
	}
	if current != qoderOutputStyleAuto && !slices.Contains(styles, current) {
		styles = append(slices.Clone(styles), current)
	}
	return qoderOutputStyleGroup(current, styles)
}

// runningEffortLocked reports the effort that the process runs at. An agent
// that opened without an effort runs at Auto, because the launch then sends
// Qoder's own `--reasoning-effort auto` (see qoderReasoningArgs). An empty
// value would tell the picker that the catalog is absent. Qoder reads the level
// only at launch, so the value never changes while the process runs.
func (a *Agent) runningEffortLocked() string {
	if a.effort == "" {
		return agent.EffortAuto
	}
	return a.effort
}

// displayedModeLocked shows Plan while Qoder's separate Plan state is active.
func (a *Agent) displayedModeLocked() string {
	if a.planMode {
		return contracts.QoderModePlan
	}
	return a.permissionMode
}

// UpdateSettings sends model and mode choices to Qoder's control channel.
// A changed effort, Extended Thinking or Output Style value needs a restart
// because Qoder takes each at launch. A choice of the running value needs none.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.mu.Lock()
	running := map[string]string{
		agent.OptionIDEffort: a.runningEffortLocked(),
		OptionThinking:       qoderThinkingValue(a.thinking),
		OptionOutputStyle:    a.displayedOutputStyleLocked(),
	}
	a.mu.Unlock()
	for id, value := range running {
		if requested, ok := options[id]; ok && requested != value {
			return agent.RestartRequiredSettings(options)
		}
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
		case agent.OptionIDEffort, OptionThinking, OptionOutputStyle:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		case agent.OptionIDModel, agent.OptionIDPermissionMode:
		default:
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
			result.AppliedLive = false
		}
	}
	a.mu.Lock()
	surfaced := optionmap.Map{
		agent.OptionIDEffort:         a.runningEffortLocked(),
		OptionThinking:               qoderThinkingValue(a.thinking),
		OptionOutputStyle:            a.displayedOutputStyleLocked(),
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
