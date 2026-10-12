package commandcode

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(a.models, a.model, a.effort, "Effort", nil)
	permission := providerkit.LiveGroup(permissionModeGroup, a.permissionMode)
	return append(groups, permission)
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return agent.ConfirmedSettings(map[string]string{agent.OptionIDModel: a.model, agent.OptionIDEffort: a.effort, agent.OptionIDPermissionMode: a.permissionMode})
}

// UpdateSettings applies native RPC setters or requests a native session restart for permission changes.
//
// The effort `auto` is no native tier: the host refuses it as an unknown
// effort, and Start sends no `--effort` for it. So `auto` sends no setter. The
// confirmed state of the host states the real effort. The host cannot drop an
// effort that it holds, so `auto` over a held effort needs a relaunch.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	mode, busy := a.permissionMode, a.operationActiveLocked()
	a.Mu.Unlock()
	if value := options[agent.OptionIDPermissionMode]; value != "" && value != mode {
		return agent.RestartRequiredSettings(options)
	}
	if busy {
		return agent.RestartRequiredSettings(options)
	}
	for _, choice := range []struct{ id, method, field string }{
		{agent.OptionIDModel, methodSetModel, "model"}, {agent.OptionIDEffort, methodSetEffort, "effort"},
	} {
		value := options[choice.id]
		if value == "" || (choice.id == agent.OptionIDEffort && value == agent.EffortAuto) {
			continue
		}
		if _, err := a.request(choice.method, map[string]string{choice.field: value}, a.APITimeout()); err != nil {
			slog.Warn("apply a Command Code setting", "option", choice.id, "error", err)
			return agent.RestartRequiredSettings(options)
		}
	}
	raw, err := a.request(methodSessionState, nil, a.APITimeout())
	if err == nil {
		err = a.applyState(raw)
	}
	if err != nil {
		slog.Warn("confirm the Command Code settings", "error", err)
		return agent.RestartRequiredSettings(options)
	}
	a.Mu.Lock()
	heldEffort := a.effort
	a.Mu.Unlock()
	if options[agent.OptionIDEffort] == agent.EffortAuto && heldEffort != "" {
		return agent.RestartRequiredSettings(options)
	}
	return a.SettingsSnapshot()
}
