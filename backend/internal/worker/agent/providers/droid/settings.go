package droid

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// OptionGroups returns the live configuration axes.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	settings := a.settings
	catalog := a.catalog
	a.Mu.Unlock()

	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, 3)
	if len(catalog.models) > 0 {
		groups = append(groups, droidModelGroup(catalog.models, settings.model))
	}
	effort := droidEffortGroup(settings.reasoningEffort, catalog.models, settings.model)
	if effort != nil {
		groups = append(groups, effort)
	}
	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Id:           agent.OptionIDPermissionMode,
		Label:        PermissionModeLabel,
		CurrentValue: settings.permissionMode,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options:      permissionModeGroup.GetOptions(),
	})
	return groups
}

// SettingsSnapshot reports the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	groups := a.OptionGroups()
	values := map[string]string{}
	for _, g := range groups {
		if g.GetId() != "" && g.GetCurrentValue() != "" {
			values[g.GetId()] = g.GetCurrentValue()
		}
	}
	return agent.ConfirmedSettings(values)
}

// UpdateSettings applies each included option and reads Droid's settled values.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.settingsApplyMu.Lock()
	defer a.settingsApplyMu.Unlock()

	a.Mu.Lock()
	sessionID := a.sessionID
	stopped := a.stopped
	current := a.settings
	a.Mu.Unlock()
	if sessionID == "" || stopped {
		return agent.RestartRequiredSettings(options)
	}

	params := updateSettingsParams{}
	if model := options[agent.OptionIDModel]; model != "" && model != current.model {
		params.ModelID = model
	}
	if effort := options[agent.OptionIDEffort]; effort != "" && effort != current.reasoningEffort {
		params.ReasoningEffort = effort
	}
	if mode := options[agent.OptionIDPermissionMode]; mode != "" && mode != current.permissionMode {
		params.InteractionMode, params.AutonomyLevel = droidWireMode(mode)
		if params.InteractionMode == "" {
			return agent.RestartRequiredSettings(options)
		}
	}
	if params == (updateSettingsParams{}) {
		return agent.ConfirmedSettings(nil)
	}

	id := a.nextRequestID()
	reply := a.registerReply(id)
	updated := a.registerSettingsUpdate(id)
	defer a.unregisterReply(id)
	defer a.unregisterSettingsUpdate(id)
	if err := a.writeRequest(id, droidMethodUpdateSessionSettings, params); err != nil {
		slog.Warn("droid settings request failed", "agent_id", a.AgentID(), "error", err)
		return agent.RestartRequiredSettings(options)
	}
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, a.APITimeout())
	defer cancel()
	for reply != nil || updated != nil {
		select {
		case response := <-reply:
			if response.Error != nil {
				slog.Warn("droid settings reply failed", "agent_id", a.AgentID(), "error", response.Error)
				return agent.RestartRequiredSettings(options)
			}
			reply = nil
		case event := <-updated:
			if !event.confirms(params) {
				slog.Warn("droid settings did not match the request", "agent_id", a.AgentID(), "request_id", id)
				return agent.RestartRequiredSettings(options)
			}
			updated = nil
		case <-a.ProcessDone():
			return agent.RestartRequiredSettings(options)
		case <-ctx.Done():
			slog.Warn("droid settings reply timed out", "agent_id", a.AgentID(), "error", ctx.Err())
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.SettingsSnapshot()
}

// droidWireMode maps a LeapMux mode to Droid's two live settings axes.
func droidWireMode(mode string) (string, string) {
	switch mode {
	case contracts.DroidModeDefault:
		return "auto", "off"
	case contracts.DroidModeSpec:
		return "spec", "off"
	case contracts.DroidModeAutoLow:
		return "auto", "low"
	case contracts.DroidModeAutoMedium:
		return "auto", "medium"
	case contracts.DroidModeAutoHigh:
		return "auto", "high"
	default:
		return "", ""
	}
}

// droidModeFromSettings reads the current mode from the native pair.
func droidModeFromSettings(settings droidNativeSettings) string {
	switch settings.InteractionMode {
	case "spec":
		return contracts.DroidModeSpec
	case "auto":
		switch settings.AutonomyLevel {
		case "off":
			return contracts.DroidModeDefault
		case "low":
			return contracts.DroidModeAutoLow
		case "medium":
			return contracts.DroidModeAutoMedium
		case "high":
			return contracts.DroidModeAutoHigh
		}
		return ""
	case "":
		return droidModeForAutonomy(settings.AutonomyMode)
	default:
		return ""
	}
}

// droidAutonomyForMode maps LeapMux's permission mode onto Droid's autonomy axis.
func droidAutonomyForMode(mode string) string {
	switch mode {
	case contracts.DroidModeAutoLow:
		return droidAutonomyAutoLow
	case contracts.DroidModeAutoMedium:
		return droidAutonomyAutoMedium
	case contracts.DroidModeAutoHigh:
		return droidAutonomyAutoHigh
	case contracts.DroidModeSpec:
		return droidAutonomySpec
	default:
		return droidAutonomyNormal
	}
}

// droidModeForAutonomy maps Droid's autonomy axis back onto LeapMux's mode.
func droidModeForAutonomy(autonomy string) string {
	switch autonomy {
	case droidAutonomyAutoLow:
		return contracts.DroidModeAutoLow
	case droidAutonomyAutoMedium:
		return contracts.DroidModeAutoMedium
	case droidAutonomyAutoHigh:
		return contracts.DroidModeAutoHigh
	case droidAutonomyNormal:
		return contracts.DroidModeDefault
	case droidAutonomySpec:
		return contracts.DroidModeSpec
	default:
		return ""
	}
}

// droidNativeSettings is the part of a native snapshot that LeapMux reads.
type droidNativeSettings struct {
	ModelID         string `json:"modelId"`
	ReasoningEffort string `json:"reasoningEffort"`
	InteractionMode string `json:"interactionMode"`
	AutonomyLevel   string `json:"autonomyLevel"`
	AutonomyMode    string `json:"autonomyMode"`
}

// droidSettingsUpdated identifies a native snapshot caused by one request.
type droidSettingsUpdated struct {
	Type      string              `json:"type"`
	RequestID string              `json:"requestId"`
	Settings  droidNativeSettings `json:"settings"`
}

func (n droidSettingsUpdated) confirms(request updateSettingsParams) bool {
	if request.ModelID != "" && n.Settings.ModelID != request.ModelID {
		return false
	}
	if request.ReasoningEffort != "" && n.Settings.ReasoningEffort != request.ReasoningEffort {
		return false
	}
	if request.InteractionMode != "" && n.Settings.InteractionMode != request.InteractionMode {
		return false
	}
	if request.AutonomyLevel != "" && n.Settings.AutonomyLevel != request.AutonomyLevel {
		return false
	}
	return true
}

func (a *Agent) registerSettingsUpdate(id string) chan droidSettingsUpdated {
	a.rpcMu.Lock()
	defer a.rpcMu.Unlock()
	if a.pendingSettings == nil {
		a.pendingSettings = make(map[string]chan droidSettingsUpdated)
	}
	updated := make(chan droidSettingsUpdated, 1)
	a.pendingSettings[id] = updated
	return updated
}

func (a *Agent) unregisterSettingsUpdate(id string) {
	a.rpcMu.Lock()
	delete(a.pendingSettings, id)
	a.rpcMu.Unlock()
}

// onSettingsUpdated stores the native snapshot, persists changed choices, and
// settles its matching request. ExitSpecMode has no request ID but still moves
// the permission mode, so its event must reach the Worker settings row.
func (a *Agent) onSettingsUpdated(payload []byte) {
	var n droidSettingsUpdated
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	a.Mu.Lock()
	previous := a.settings
	if n.Settings.ModelID != "" {
		a.settings.model = n.Settings.ModelID
	}
	if n.Settings.ReasoningEffort != "" {
		a.settings.reasoningEffort = n.Settings.ReasoningEffort
	}
	if n.Settings.InteractionMode != "" {
		a.settings.interactionMode = n.Settings.InteractionMode
	}
	if n.Settings.AutonomyLevel != "" {
		a.settings.autonomyLevel = n.Settings.AutonomyLevel
	}
	if mode := droidModeFromSettings(n.Settings); mode != "" {
		a.settings.permissionMode = mode
	}
	current := a.settings
	sessionID := a.sessionID
	a.Mu.Unlock()
	if n.RequestID != "" {
		a.rpcMu.Lock()
		updated := a.pendingSettings[n.RequestID]
		a.rpcMu.Unlock()
		if updated != nil {
			select {
			case updated <- n:
			default:
			}
		}
	}
	if sessionID != "" && current != previous {
		refresh := optionmap.Map{}
		if current.model != "" {
			refresh[agent.OptionIDModel] = current.model
		}
		if current.reasoningEffort != "" {
			refresh[agent.OptionIDEffort] = current.reasoningEffort
		}
		if current.permissionMode != "" {
			refresh[agent.OptionIDPermissionMode] = current.permissionMode
		}
		a.sink.PersistSettingsRefresh(refresh)
	}
	target, ok := a.outputTargetFor("")
	if ok {
		a.persistNotification(payload, target)
	}
}
