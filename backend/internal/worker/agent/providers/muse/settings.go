package muse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	groups := providerkit.ModelAndEffortGroups(a.models, a.settings[agent.OptionIDModel], a.settings[agent.OptionIDEffort], "Effort", nil)
	groups = append(groups, providerkit.LiveGroup(permissionGroup(), a.settings[agent.OptionIDPermissionMode]))
	return append(groups, startupGroups(a.settings)...)
}
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return agent.ConfirmedSettings(a.settings)
}
func (a *Agent) refreshCatalog(timeout time.Duration) error {
	a.stateMu.Lock()
	id := a.sessionID
	a.stateMu.Unlock()
	raw, err := a.request(methodModelList, map[string]string{"sessionId": id}, timeout, nil)
	if err != nil {
		return err
	}
	models, err := decodeCatalog(raw)
	if err != nil {
		return err
	}
	a.stateMu.Lock()
	a.models = models
	if a.settings[agent.OptionIDEffort] == "" {
		if model := agent.FindAvailableModel(models, a.settings[agent.OptionIDModel]); model != nil {
			a.settings[agent.OptionIDEffort] = model.DefaultEffort
		}
	}
	a.stateMu.Unlock()
	return nil
}

func (a *Agent) applyOption(id, value string, timeout time.Duration) error {
	a.stateMu.Lock()
	session := a.sessionID
	a.stateMu.Unlock()
	params := map[string]any{"sessionId": session}
	method := ""
	switch id {
	case agent.OptionIDModel:
		if agent.UsesAccountDefaultModel(value) {
			return fmt.Errorf("the Muse host cannot restore its launch default model on a running session")
		}
		method = methodSetModel
		params["model"] = map[string]string{"modelId": value}
	case agent.OptionIDEffort:
		if !validEffort(value) {
			return fmt.Errorf("the Muse effort is invalid")
		}
		method = methodSetEffort
		params["reasoningEffort"] = value
	case agent.OptionIDPermissionMode:
		if !validApprovalMode(value) {
			return fmt.Errorf("the Muse approval mode is invalid")
		}
		method = methodSetApprovalMode
		params["mode"] = value
	default:
		return fmt.Errorf("the Muse option %q requires a new agent", id)
	}
	raw, err := a.command(method, params, timeout, nil)
	if err != nil {
		return err
	}
	if id == agent.OptionIDPermissionMode {
		var reply struct {
			EffectiveMode struct {
				Mode string `json:"mode"`
			} `json:"effectiveMode"`
		}
		if json.Unmarshal(raw, &reply) != nil || !validApprovalMode(reply.EffectiveMode.Mode) {
			return fmt.Errorf("the Muse approval reply supplies an invalid effective mode")
		}
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		if a.sessionID != session {
			return agent.ErrInputSessionChanged
		}
		a.settings[id] = reply.EffectiveMode.Mode
		return nil
	}
	return a.refreshNativeSettings(session, timeout)
}

// refreshNativeSettings reads applied state. Command admission supplies no model confirmation.
func (a *Agent) refreshNativeSettings(session string, timeout time.Duration) error {
	raw, err := a.request(methodSessionRead, map[string]any{"sessionId": session, "excludeItems": false}, timeout, nil)
	if err != nil {
		return err
	}
	var result sessionResult
	if json.Unmarshal(raw, &result) != nil || result.Session.ID != session {
		return fmt.Errorf("the Muse settings read returned another or invalid session")
	}
	var history struct {
		Snapshot *struct {
			State struct {
				ReasoningEffort *struct {
					Value string `json:"reasoningEffort"`
				} `json:"reasoningEffort"`
			} `json:"state"`
		} `json:"snapshot"`
	}
	if len(result.History) > 0 && json.Unmarshal(result.History, &history) != nil {
		return fmt.Errorf("the Muse settings read returned invalid history")
	}
	if result.Session.ApprovalMode != nil && !validApprovalMode(result.Session.ApprovalMode.Mode) {
		return fmt.Errorf("the Muse settings read returned an invalid approval mode")
	}
	if history.Snapshot != nil && history.Snapshot.State.ReasoningEffort != nil && !validEffort(history.Snapshot.State.ReasoningEffort.Value) {
		return fmt.Errorf("the Muse settings read returned an invalid effort")
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.sessionID != session {
		return agent.ErrInputSessionChanged
	}
	if result.Session.ModelID == nil || *result.Session.ModelID == "" {
		delete(a.settings, agent.OptionIDModel)
	} else {
		a.settings[agent.OptionIDModel] = *result.Session.ModelID
	}
	if result.Session.ApprovalMode != nil {
		a.settings[agent.OptionIDPermissionMode] = result.Session.ApprovalMode.Mode
	}
	if history.Snapshot != nil && history.Snapshot.State.ReasoningEffort != nil {
		a.settings[agent.OptionIDEffort] = history.Snapshot.State.ReasoningEffort.Value
	}
	return nil
}

func validApprovalMode(value string) bool {
	switch value {
	case contracts.MuseApprovalModeAllowAll, contracts.MuseApprovalModeOnRequest, contracts.MuseApprovalModePromptUnmatched, contracts.MuseApprovalModeDenyUnmatched:
		return true
	default:
		return false
	}
}

func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	result := a.SettingsSnapshot()
	for id, value := range options {
		if value == "" || value == result.SurfacedOptions[id] {
			continue
		}
		if err := a.applyOption(id, value, a.APITimeout()); err != nil {
			slog.Warn("apply a Muse option", "option", id, "error", err)
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
		if a.SettingsSnapshot().SurfacedOptions[id] != value {
			result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
		}
	}
	refreshed := a.SettingsSnapshot()
	for id, settlement := range result.Settlements {
		if settlement.State == agent.OptionSettlementUnresolved {
			refreshed.Settlements[id] = settlement
		}
	}
	return refreshed
}
