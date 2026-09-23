package copilot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *copilotAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	groups := providerkit.ModelAndEffortGroups(a.models, a.options[agent.OptionIDModel], a.options[agent.OptionIDEffort], agent.EffortGroupLabel, nil)
	return append(groups,
		copilotSessionModeGroup(a.options[copilotOptionSessionMode]),
		copilotPermissionModeGroup(a.options[agent.OptionIDPermissionMode]),
	)
}

func (a *copilotAgent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// refreshNativeSettings reads the independent native axes before it publishes one settings snapshot.
// The caller holds sessionMu or owns startup before the agent becomes available to other callers.
//
// The three reads do not depend on each other, so they run at the same time: the
// transport serializes each write under its own lock and correlates every response by
// request ID, which makes three requests in flight one round trip rather than three.
// This function runs at startup, at each settings update, at each restore, and for
// each external change event. The error it returns is the first one in the order
// below, so a failure reads the same way on every run.
func (a *copilotAgent) refreshNativeSettings() error {
	var modelRaw, modeRaw, permissionRaw json.RawMessage
	reads := [...]struct {
		method string
		into   *json.RawMessage
	}{
		{"model.getCurrent", &modelRaw},
		{"mode.get", &modeRaw},
		{"permissions.getMode", &permissionRaw},
	}
	var failures [len(reads)]error
	var wait sync.WaitGroup
	wait.Add(len(reads))
	for index, read := range reads {
		go func() {
			defer wait.Done()
			*read.into, failures[index] = a.requestNativeSession(read.method, nil)
		}()
	}
	wait.Wait()
	for _, err := range failures {
		if err != nil {
			return err
		}
	}
	var model struct {
		ModelID         string `json:"modelId"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if err := json.Unmarshal(modelRaw, &model); err != nil {
		return fmt.Errorf("decode Copilot model settings: %w", err)
	}
	var mode string
	if err := json.Unmarshal(modeRaw, &mode); err != nil {
		return fmt.Errorf("decode Copilot session mode: %w", err)
	}
	var permission struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(permissionRaw, &permission); err != nil {
		return fmt.Errorf("decode Copilot permission mode: %w", err)
	}
	if mode == "" || permission.Mode == "" {
		return fmt.Errorf("the Copilot response omitted a required session setting")
	}
	a.stateMu.Lock()
	if model.ModelID != "" {
		a.options[agent.OptionIDModel] = model.ModelID
	}
	if model.ReasoningEffort == "" {
		delete(a.options, agent.OptionIDEffort)
	} else {
		a.options[agent.OptionIDEffort] = model.ReasoningEffort
	}
	a.options[copilotOptionSessionMode] = mode
	a.options[agent.OptionIDPermissionMode] = permission.Mode
	a.stateMu.Unlock()
	return nil
}

// refreshNativeSettingsInBackground re-reads the axes off the reader goroutine.
//
// The runtime reports `session.mode_changed`, `session.permissions_changed` and
// `session.model_change` when something OUTSIDE LeapMux moves an axis -- a slash
// command in the composer, or a mode that an approved plan switched. The event states
// that the axis moved and not the complete settlement, so the answer needs a request.
// offReader states why that request cannot run on the goroutine that handles the
// event, and it also keeps a burst of the three events to one read at a time: three
// goroutines that each published a snapshot would leave the last writer's mixed view
// standing.
func (a *copilotAgent) refreshNativeSettingsInBackground() {
	a.offReader(copilotReadSettings, func() {
		if err := a.refreshNativeSettings(); err != nil {
			slog.Debug("Read Copilot settings after a native change", "agent_id", a.AgentID(), "error", err)
			return
		}
		a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	})
}

func (a *copilotAgent) UpdateSettings(requested optionmap.Map) agent.SettingsApplyResult {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	return a.applyNativeSettings(requested)
}

func (a *copilotAgent) applyNativeSettings(requested optionmap.Map) agent.SettingsApplyResult {
	result := agent.SettingsApplyResult{AppliedLive: true, Settlements: make(agent.OptionSettlements)}
	// Mode changes can select a plan model. Apply an explicit model and effort after that change.
	order := []string{copilotOptionSessionMode, agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPermissionMode}
	keys := slices.Sorted(maps.Keys(requested))
	keys = append(order, slices.DeleteFunc(keys, func(key string) bool { return slices.Contains(order, key) })...)
	for _, key := range keys {
		value := requested[key]
		if value == "" {
			continue
		}
		result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		var method string
		var params map[string]any
		switch key {
		case agent.OptionIDModel:
			method, params = "model.switchTo", map[string]any{"modelId": value, "requireAvailable": true}
		case agent.OptionIDEffort:
			if value == agent.EffortAuto {
				// EffortAuto means "send no effort at all", so the runtime keeps the tier
				// it chose for the model. A confirmed settlement with no value removes the
				// stored effort, which is exactly what the sentinel asks for, and the
				// refreshed snapshot below reports the tier the runtime kept.
				result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed}
				continue
			}
			method, params = "model.setReasoningEffort", map[string]any{"reasoningEffort": value}
		case agent.OptionIDPermissionMode:
			method, params = "permissions.setMode", map[string]any{"mode": value}
		case copilotOptionSessionMode:
			method, params = "mode.set", map[string]any{"mode": value}
		default:
			continue
		}
		if _, err := a.requestNativeSession(method, params); err != nil {
			slog.Warn("Apply Copilot setting", "option", key, "error", err)
		}
	}
	if err := a.refreshNativeSettings(); err != nil {
		slog.Warn("Read Copilot settings after an update", "error", err)
		return result
	}
	result.SurfacedOptions = agent.CurrentOptions(a.OptionGroups())
	for key, settlement := range result.Settlements {
		if settlement.State == agent.OptionSettlementConfirmed {
			continue
		}
		value := result.SurfacedOptions[key]
		if value == requested[key] {
			result.Settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
	}
	a.sink.PersistSettingsRefresh(result.SurfacedOptions)
	return result
}
