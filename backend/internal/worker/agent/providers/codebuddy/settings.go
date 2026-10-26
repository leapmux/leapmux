package codebuddy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// OptionGroups returns every configuration axis this agent currently reports.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	model := a.model
	models := a.models
	effort := a.effort
	mode := a.permissionMode
	style := a.outputStyle
	styles := a.outputStyles
	thinking := a.thinkingChoice
	a.mu.Unlock()
	groups := []*leapmuxv1.AvailableOptionGroup{
		codebuddyEffortGroup(effort),
		codebuddyPermissionModeGroup(mode),
		codebuddyThinkingGroup(thinking),
		providerkit.LiveGroup(codebuddySwarmGroup, strconv.FormatBool(a.swarmMode)),
	}
	if modelGroup := codebuddyModelGroup(models, model); modelGroup != nil {
		groups = append([]*leapmuxv1.AvailableOptionGroup{modelGroup}, groups...)
	}
	if styleGroup := codebuddyOutputStyleGroup(styles, style); styleGroup != nil {
		groups = append(groups, styleGroup)
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
		OptionThinking:               codebuddyThinkingValue(a.thinkingChoice),
		OptionSwarmMode:              strconv.FormatBool(a.swarmMode),
	}
	if a.model != "" {
		values[agent.OptionIDModel] = a.model
	}
	if a.outputStyle != "" {
		values[OptionOutputStyle] = a.outputStyle
	}
	return agent.ConfirmedSettings(values)
}

// UpdateSettings applies model, permission and explicit thinking changes live. CodeBuddy takes
// effort and Swarm mode from the launch arguments on every prompt. Its session
// also retains the initial output style, so these changes require a process
// restart before any live option changes.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	thinking, thinkingRequested := options[OptionThinking]
	swarmValue, swarmRequested := options[OptionSwarmMode]
	swarm := a.swarmMode
	if swarmValue != "" {
		var err error
		swarm, err = options.Bool(OptionSwarmMode)
		if err != nil {
			return a.rejectSettings(options)
		}
	}
	var thinkingOverride *bool
	if thinkingRequested && thinking != "" {
		var err error
		thinkingOverride, err = codebuddyThinkingOverride(thinking)
		if err != nil {
			return a.rejectSettings(options)
		}
	}
	a.mu.Lock()
	currentThinking := codebuddyThinkingValue(a.thinkingChoice)
	a.mu.Unlock()
	if style, requested := options[OptionOutputStyle]; requested {
		a.mu.Lock()
		current := a.outputStyle
		offered := slices.Contains(a.outputStyles, style) || style == current
		a.mu.Unlock()
		if style != "" && (strings.TrimSpace(style) == "" || !offered) {
			// AppliedLive:false requests a restart with the original input.
			// Confirm the unchanged snapshot to reject this invalid batch instead.
			return a.rejectSettings(options)
		}
		if style != "" && codebuddyOutputStyleKey(style) != codebuddyOutputStyleKey(current) {
			return agent.RestartRequiredSettings(options)
		}
	}
	if effort, ok := options[agent.OptionIDEffort]; ok && effort != a.opts.Effort() {
		return agent.RestartRequiredSettings(options)
	}
	if swarm != a.swarmMode {
		return agent.RestartRequiredSettings(options)
	}
	if thinkingRequested && thinking == codebuddyThinkingAuto && currentThinking != codebuddyThinkingAuto {
		// A null native setting does not restore the profile. Auto requires
		// a new process without the explicit thinking override.
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
			confirmed := a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode]
			result.Settlements[agent.OptionIDPermissionMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &confirmed}
		}
	}
	if effort, ok := options[agent.OptionIDEffort]; ok {
		result.Settlements[agent.OptionIDEffort] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &effort}
	}
	if _, requested := options[OptionOutputStyle]; requested {
		current := a.SettingsSnapshot().SurfacedOptions[OptionOutputStyle]
		settlement := agent.OptionSettlement{State: agent.OptionSettlementConfirmed}
		if current != "" {
			settlement.Value = &current
		}
		result.Settlements[OptionOutputStyle] = settlement
	}
	if thinkingRequested {
		if thinkingOverride != nil && thinking != currentThinking {
			if err := a.applyThinking(*thinkingOverride); err != nil {
				result.Settlements[OptionThinking] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
				result.AppliedLive = false
			}
		}
		if _, settled := result.Settlements[OptionThinking]; !settled {
			confirmed := a.SettingsSnapshot().SurfacedOptions[OptionThinking]
			result.Settlements[OptionThinking] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &confirmed}
		}
	}
	if swarmRequested {
		confirmed := strconv.FormatBool(a.swarmMode)
		result.Settlements[OptionSwarmMode] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &confirmed}
	}
	for id := range options {
		if id != agent.OptionIDModel && id != agent.OptionIDPermissionMode && id != agent.OptionIDEffort && id != OptionOutputStyle && id != OptionThinking && id != OptionSwarmMode {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
	}
	result.SurfacedOptions = a.SettingsSnapshot().SurfacedOptions
	return result
}

func codebuddyThinkingValue(value string) string {
	if value == "" {
		return codebuddyThinkingAuto
	}
	return value
}

func codebuddyThinkingOverride(value string) (*bool, error) {
	switch codebuddyThinkingValue(value) {
	case codebuddyThinkingAuto:
		return nil, nil
	case codebuddyThinkingOff, codebuddyThinkingOn:
		enabled := value == codebuddyThinkingOn
		return &enabled, nil
	default:
		return nil, fmt.Errorf("CodeBuddy thinking must be auto, false or true, got %q", value)
	}
}

// Confirm the unchanged snapshot to undo an invalid optimistic edit without
// requesting a restart with the invalid values.
func (a *Agent) rejectSettings(options optionmap.Map) agent.SettingsApplyResult {
	result := a.SettingsSnapshot()
	for id := range options {
		if _, present := result.Settlements[id]; !present {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed}
		}
	}
	return result
}

func (a *Agent) applyThinking(enabled bool) error {
	body, err := json.Marshal(struct {
		Subtype string                  `json:"subtype"`
		Config  codebuddyNativeSettings `json:"config"`
	}{Subtype: "set_config", Config: codebuddyNativeSettings{AlwaysThinkingEnabled: &enabled}})
	if err != nil {
		return err
	}
	result, err := a.sendControlAndWait(string(body), a.opts.EffectiveAPITimeout())
	if err != nil {
		return err
	}
	if len(result.ConfigErrors) != 0 {
		return fmt.Errorf("CodeBuddy rejected the thinking setting: %v", result.ConfigErrors)
	}
	if result.ConfigUpdated == nil || result.ConfigUpdated.AlwaysThinkingEnabled == nil || *result.ConfigUpdated.AlwaysThinkingEnabled != enabled {
		return fmt.Errorf("CodeBuddy did not confirm the thinking setting")
	}
	if a.IsStopped() || a.Context().Err() != nil {
		return fmt.Errorf("CodeBuddy stopped before the thinking setting was confirmed")
	}
	choice := codebuddyThinkingOff
	if enabled {
		choice = codebuddyThinkingOn
	}
	a.mu.Lock()
	a.thinkingChoice = choice
	a.mu.Unlock()
	return nil
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
