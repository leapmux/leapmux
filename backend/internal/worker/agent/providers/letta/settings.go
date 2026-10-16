package letta

import (
	"encoding/json"
	"fmt"
	"log/slog"

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

	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, 2)
	// The model group is always present: the settings menu offers it even when
	// the session reported no catalog yet. The option list must HOLD the
	// current value, because the chip resolves its label by looking that value
	// up in the options -- a list that omits it draws the group label instead
	// of the model the session is running.
	models := append([]lettaModel(nil), catalog.models...)
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		seen[model.id] = true
	}
	if settings.model != "" && !seen[settings.model] {
		models = append(models, lettaModel{id: settings.model, displayName: settings.model})
	}
	if len(models) == 0 {
		models = []lettaModel{{id: defaultModels[0].Id, displayName: defaultModels[0].DisplayName}}
	}
	groups = append(groups, lettaModelGroup(models, settings.model))
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

// UpdateSettings applies model, effort and permission changes to the native runtime.
// An empty effort restores the native default.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	defer a.approvePendingPermissions()
	a.Mu.Lock()
	agentID := a.agentID
	previous := a.settings
	a.Mu.Unlock()
	mode, modeRequested := options[agent.OptionIDPermissionMode]
	if agentID == "" {
		return agent.RestartRequiredSettings(options)
	}
	if modeRequested && mode != previous.permissionMode {
		if err := a.applyPermissionMode(mode); err != nil {
			slog.Warn("letta permission mode did not settle", "agent_id", a.AgentID(), "error", err)
			return a.SettingsSnapshot()
		}
		a.Mu.Lock()
		previous = a.settings
		a.Mu.Unlock()
	}
	model, modelPresent := options[agent.OptionIDModel]
	effort, effortPresent := options[agent.OptionIDEffort]
	if !modelPresent && !effortPresent {
		if modeRequested {
			return agent.ConfirmedSettings(map[string]string{agent.OptionIDPermissionMode: previous.permissionMode})
		}
		return agent.ConfirmedSettings(nil)
	}
	if modelPresent && model == "" && !effortPresent {
		confirmed := map[string]string{agent.OptionIDModel: previous.model}
		if modeRequested {
			confirmed[agent.OptionIDPermissionMode] = previous.permissionMode
		}
		return agent.ConfirmedSettings(confirmed)
	}
	if model == "" {
		model = previous.model
	}
	if model == "" {
		return agent.RestartRequiredSettings(options)
	}
	patch := map[string]any{"model_handle": model}
	if effortPresent {
		if effort == "" {
			patch["reasoning_effort"] = nil
		} else {
			patch["reasoning_effort"] = effort
		}
	}
	cmd := newLettaCommand("update_model", a.nextRequestID())
	scope := a.runtime()
	cmd.Runtime = &scope
	cmd.Payload = patch
	pending := &lettaModelUpdatePending{requestID: cmd.RequestID, model: model, effort: effort, effortPresent: effortPresent, waiter: newRuntimeWaiter()}
	a.Mu.Lock()
	a.modelPending = pending
	a.Mu.Unlock()
	if err := a.sendCommand(cmd); err != nil {
		a.clearModelPending(pending)
		return agent.RestartRequiredSettings(options)
	}
	if err := pending.waiter.wait(a.Context(), a.ProcessDone(), lettaSendWait); err != nil {
		a.clearModelPending(pending)
		a.Mu.Lock()
		current := a.settings
		a.Mu.Unlock()
		confirmed := map[string]string{}
		if _, ok := options[agent.OptionIDModel]; ok {
			confirmed[agent.OptionIDModel] = current.model
		}
		if _, ok := options[agent.OptionIDEffort]; ok {
			confirmed[agent.OptionIDEffort] = current.reasoningLevel
		}
		if modeRequested {
			confirmed[agent.OptionIDPermissionMode] = current.permissionMode
		}
		return agent.ConfirmedSettings(confirmed)
	}
	a.Mu.Lock()
	current := a.settings
	a.Mu.Unlock()
	confirmed := map[string]string{}
	if _, ok := options[agent.OptionIDModel]; ok {
		confirmed[agent.OptionIDModel] = current.model
	}
	if _, ok := options[agent.OptionIDEffort]; ok {
		confirmed[agent.OptionIDEffort] = current.reasoningLevel
	}
	if modeRequested {
		confirmed[agent.OptionIDPermissionMode] = current.permissionMode
	}
	return agent.ConfirmedSettings(confirmed)
}

type lettaModePending struct {
	requestID string
	scope     runtimeScope
	observed  bool
	waiter    *runtimeWaiter
}

type lettaSettingsSync struct {
	lettaCommand
	RecoverApprovals      bool `json:"recover_approvals"`
	ResumeInterruptedTurn bool `json:"resume_interrupted_turn"`
	ForceDeviceStatus     bool `json:"force_device_status"`
}

func validPermissionMode(mode string) bool {
	for _, option := range permissionModeGroup.GetOptions() {
		if option.GetId() == mode {
			return true
		}
	}
	return false
}

// applyPermissionMode confirms the native mode through a scoped status snapshot.
// The sync reply follows that snapshot and carries a command correlation ID.
func (a *Agent) applyPermissionMode(mode string) error {
	if !validPermissionMode(mode) {
		return fmt.Errorf("the Letta permission mode %q is unavailable", mode)
	}
	scope := a.runtime()
	if scope.AgentID == "" || scope.ConversationID == "" {
		return fmt.Errorf("the Letta runtime identity is unavailable")
	}
	change := newLettaCommand("change_device_state", a.nextRequestID())
	change.Runtime = &scope
	change.Payload = map[string]any{"mode": mode}
	syncCommand := lettaSettingsSync{lettaCommand: newLettaCommand("sync", a.nextRequestID()), ForceDeviceStatus: true}
	syncCommand.Runtime = &scope
	pending := &lettaModePending{requestID: syncCommand.RequestID, scope: scope, waiter: newRuntimeWaiter()}
	a.Mu.Lock()
	a.modePending = pending
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		if a.modePending == pending {
			a.modePending = nil
		}
		a.Mu.Unlock()
	}()
	if err := a.sendCommand(change); err != nil {
		return err
	}
	if err := a.sendCommand(syncCommand); err != nil {
		return err
	}
	if err := pending.waiter.wait(a.Context(), a.ProcessDone(), lettaSendWait); err != nil {
		return err
	}
	a.Mu.Lock()
	current := a.settings.permissionMode
	a.Mu.Unlock()
	if current != mode {
		return fmt.Errorf("the Letta runtime reported permission mode %q after the request for %q", current, mode)
	}
	return nil
}

func (a *Agent) handleDeviceStatus(raw []byte) {
	var frame struct {
		Runtime runtimeScope `json:"runtime"`
		Status  struct {
			Mode string `json:"current_permission_mode"`
		} `json:"device_status"`
	}
	if json.Unmarshal(raw, &frame) != nil || !validPermissionMode(frame.Status.Mode) {
		return
	}
	a.Mu.Lock()
	if frame.Runtime.AgentID == "" || frame.Runtime.ConversationID == "" || frame.Runtime.AgentID != a.agentID || frame.Runtime.ConversationID != a.conversationID {
		a.Mu.Unlock()
		return
	}
	changed := a.settings.permissionMode != frame.Status.Mode
	a.settings.permissionMode = frame.Status.Mode
	if pending := a.modePending; pending != nil && pending.scope == frame.Runtime {
		pending.observed = true
	}
	a.Mu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(map[string]string{agent.OptionIDPermissionMode: frame.Status.Mode})
		a.approvalWork.Run("permissions", a.approvePendingPermissions)
	}
}

func (a *Agent) handleSettingsSyncResponse(raw []byte) {
	var frame struct {
		RequestID string       `json:"request_id"`
		Runtime   runtimeScope `json:"runtime"`
		Success   *bool        `json:"success"`
		Error     string       `json:"error"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	a.Mu.Lock()
	pending := a.modePending
	if pending == nil || pending.requestID != frame.RequestID {
		a.Mu.Unlock()
		return
	}
	observed := pending.observed
	a.Mu.Unlock()
	var err error
	switch {
	case frame.Runtime != pending.scope:
		err = fmt.Errorf("the Letta settings reply belongs to another runtime")
	case frame.Success == nil || !*frame.Success:
		err = fmt.Errorf("the Letta runtime refused the settings snapshot: %s", frame.Error)
	case !observed:
		err = fmt.Errorf("the Letta runtime returned no permission-mode snapshot")
	}
	pending.waiter.settle(err)
}
