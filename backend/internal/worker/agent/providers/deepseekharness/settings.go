package deepseekharness

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	catalog, selection, mode, permissions := a.catalog, a.selection, a.mode, a.permissions
	a.Mu.Unlock()
	groups := providerkit.ModelAndEffortGroups(catalog.models, selection.id(), selection.Effort, agent.EffortGroupLabel, nil)
	return append(groups, modeGroup(mode), permissionGroup(permissions))
}

func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	a.Mu.Lock()
	catalog, current, mode, permissions, sessionID := a.catalog, a.selection, a.mode, a.permissions, a.sessionID
	a.Mu.Unlock()
	next, err := catalog.resolve(current, options[agent.OptionIDModel], options[agent.OptionIDEffort])
	if err != nil {
		slog.Warn("DeepSeek Harness refused the requested model settings", "error", err)
		return a.SettingsSnapshot()
	}
	if value := options[agent.OptionIDPermissionMode]; value != "" {
		mode = value
	}
	if value := options[contracts.DeepseekHarnessOptionPermissions]; value != "" {
		permissions = value
	}
	if err := a.applySessionSettings(sessionID, next, mode, permissions); err != nil {
		slog.Warn("DeepSeek Harness settings failed", "error", err)
		// Read the native values after a partial write. A snapshot must not claim rollback.
		var baseline struct {
			Values map[string]json.RawMessage `json:"values"`
		}
		if readErr := a.rpc.request(a.Context(), "session/projections", map[string]string{"sessionId": sessionID}, &baseline); readErr == nil {
			if projectionErr := a.applyProjections(&sessionStream{sessionID: sessionID}, baseline.Values, true); projectionErr != nil {
				slog.Warn("DeepSeek Harness could not restore native settings", "error", projectionErr)
			}
		}
		return a.SettingsSnapshot()
	}
	a.Mu.Lock()
	a.selection, a.mode, a.permissions = next, mode, permissions
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	return a.SettingsSnapshot()
}

func (a *Agent) applySessionSettings(sessionID string, selection modelSelection, mode, permissions string) error {
	if mode != contracts.DeepseekHarnessModeAct && mode != contracts.DeepseekHarnessModePlan {
		return fmt.Errorf("DeepSeek Harness mode is unavailable")
	}
	valid := false
	for _, option := range nativePermissions {
		valid = valid || option.Id == permissions
	}
	if !valid {
		return fmt.Errorf("DeepSeek Harness permission preset is unavailable")
	}
	var result struct {
		Selected modelSelection `json:"selected"`
	}
	request := map[string]string{"sessionId": sessionID, "provider": selection.Provider, "model": selection.Model}
	if selection.Effort != "" {
		request["reasoningEffort"] = selection.Effort
	}
	if err := a.rpc.request(a.Context(), "session/selectModel", request, &result); err != nil {
		return err
	}
	if result.Selected != selection {
		return fmt.Errorf("DeepSeek Harness did not confirm the selected model settings")
	}
	if err := a.executeCommand(sessionID, "/permission "+permissions); err != nil {
		return err
	}
	line := "/plan off"
	if mode == contracts.DeepseekHarnessModePlan {
		line = "/plan"
	}
	return a.executeCommand(sessionID, line)
}
