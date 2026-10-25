package ohmypi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// OptionGroups returns the model group, the current model's thinking-level group,
// the approval-mode group, the output-style group, and the fast-mode group.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	model, effort, approval := a.model, a.thinkingLevel, a.approvalMode
	style := a.outputStyle
	fastMode := strconv.FormatBool(a.fastMode)
	models := a.availableModels
	a.Mu.Unlock()

	var groups []*leapmuxv1.AvailableOptionGroup
	if len(models) > 0 {
		groups = providerkit.ModelAndEffortGroups(models, model, effort, ThinkingLevelLabel, nil)
	} else {
		// No catalog yet: the running values read back, and nothing can be picked.
		groups = providerkit.ReadOnlyModelAndEffortGroups(model, model, effort)
	}
	return append(groups, providerkit.LiveGroup(approvalModeGroup, approval), providerkit.LiveGroup(outputStyleGroup, style), providerkit.LiveGroup(fastModeGroup, fastMode))
}

// SettingsSnapshot confirms the settings applied to the running agent.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies model, thinking-level, fast-mode, and Yolo changes live.
// Other approval-mode changes, output-style changes, and a move to Auto restart the agent.
//
// omp applies its native approval mode at launch. The Worker grants Yolo tool approvals itself.
// No RPC command removes a thinking level, which is what Auto requires.
// A failed model or thinking-level change requires a restart. A refused fast-mode choice stays unresolved.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	defer a.approvePendingDialogs()
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	curModel, curEffort, curApproval := a.model, a.thinkingLevel, a.approvalMode
	if style := options[OptionOutputStyle]; style != "" && style != a.outputStyle {
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}
	if options[agent.OptionIDPermissionMode] == contracts.OhMyPiApprovalModeYolo {
		a.approvalMode = contracts.OhMyPiApprovalModeYolo
	}
	a.Mu.Unlock()

	if mode := options[agent.OptionIDPermissionMode]; mode != "" && mode != curApproval && mode != contracts.OhMyPiApprovalModeYolo {
		return agent.RestartRequiredSettings(options)
	}
	if agent.IsEffortAutoTransition(options[agent.OptionIDEffort], curEffort) {
		return agent.RestartRequiredSettings(options)
	}

	timeout := a.APITimeout()
	applied := true
	if model := options[agent.OptionIDModel]; model != "" && model != curModel {
		if err := a.applyModel(model, timeout); err != nil {
			slog.Warn("omp set_model failed; restarting to apply", "agent_id", a.AgentID(), "model", model, "error", err)
			applied = false
		}
	}
	if applied {
		if effort := options[agent.OptionIDEffort]; effort != "" && effort != agent.EffortAuto && effort != curEffort {
			if err := a.applyThinkingLevel(effort, timeout); err != nil {
				slog.Warn("omp set_thinking_level failed; restarting to apply", "agent_id", a.AgentID(), "level", effort, "error", err)
				applied = false
			}
		}
	}
	if !applied {
		// A partial apply (the model landed, the level did not) leaves a mix that
		// no one asked for. Restore the prior values, so nothing observes the mix
		// before the restart applies the whole request at launch.
		a.Mu.Lock()
		a.model, a.thinkingLevel = curModel, curEffort
		a.Mu.Unlock()
		return agent.RestartRequiredSettings(options)
	}

	var fastModeErr error
	if value := options[OptionFastMode]; value != "" {
		a.Mu.Lock()
		current := strconv.FormatBool(a.fastMode)
		a.Mu.Unlock()
		// A different model can select a different native service-tier family.
		modelChanged := options[agent.OptionIDModel] != "" && options[agent.OptionIDModel] != curModel
		if value != current || modelChanged {
			fastModeErr = a.applyFastMode(value, timeout)
			if fastModeErr != nil {
				slog.Warn("omp fast-mode change failed", "agent_id", a.AgentID(), "error", fastModeErr)
				a.stateRefresh.schedule(a)
			}
		}
	}
	a.Mu.Lock()
	settings := a.currentSettingsLocked()
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(settings)
	result := a.SettingsSnapshot()
	if value := options[OptionFastMode]; value != "" && (fastModeErr != nil || result.SurfacedOptions[OptionFastMode] != value) {
		result.Settlements[OptionFastMode] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	return result
}

// applyFastMode records the state that the native command acknowledges.
// The caller holds sessionMu so an older state read cannot replace this reply.
func (a *Agent) applyFastMode(value string, timeout time.Duration) error {
	if value != "true" && value != "false" {
		return fmt.Errorf("unsupported Oh My Pi fast-mode value %q", value)
	}
	raw, err := a.sendCommand(CommandSetFastMode, map[string]any{"enabled": value == "true"}, timeout)
	if err != nil {
		return err
	}
	var result struct {
		Enabled *bool `json:"enabled"`
		Active  *bool `json:"active"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode the Oh My Pi fast-mode response: %w", err)
	}
	if result.Enabled == nil || result.Active == nil {
		return fmt.Errorf("the Oh My Pi fast-mode response has no enabled or active state")
	}
	a.Mu.Lock()
	a.fastMode = *result.Enabled
	a.Mu.Unlock()
	return nil
}

// currentSettingsLocked retains every option when a native event changes only one.
// The caller holds Process.Mu.
func (a *Agent) currentSettingsLocked() map[string]string {
	return map[string]string{
		agent.OptionIDModel:          a.model,
		agent.OptionIDEffort:         a.thinkingLevel,
		agent.OptionIDPermissionMode: a.approvalMode,
		OptionOutputStyle:            a.outputStyle,
		OptionFastMode:               strconv.FormatBool(a.fastMode),
	}
}

// applyModel switches the model with set_model. omp answers with the model it
// settled on, which is the value the agent records: an alias or a provider omp
// resolved differently is omp's answer, not the request.
func (a *Agent) applyModel(model string, timeout time.Duration) error {
	provider, id := splitModelID(model)
	if provider == "" {
		provider = a.providerForModel(id)
	}
	if provider == "" {
		return fmt.Errorf("the model %q states no provider, and the catalog holds none for it", model)
	}
	raw, err := a.sendCommand(CommandSetModel, map[string]any{"provider": provider, "modelId": id}, timeout)
	if err != nil {
		return err
	}
	settled := joinModelID(provider, id)
	var response struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}
	if json.Unmarshal(raw, &response) == nil && response.ID != "" {
		settled = joinModelID(response.Provider, response.ID)
	}
	a.Mu.Lock()
	a.model = settled
	a.effectiveModel = settled
	a.fallbackModel = ""
	a.Mu.Unlock()
	return nil
}

// applyThinkingLevel sets the thinking level with set_thinking_level, and records
// the level omp settled on.
//
// omp clamps a level the model does not offer ("max" becomes "xhigh", anything
// becomes "off" on a model that does not reason), and it states a level only
// when the level MOVED, in a thinking_level_changed frame that arrives before the
// command's response. So the settled level is effectiveThinking once the response
// arrives: the frame's level, or the unchanged level when no frame came.
func (a *Agent) applyThinkingLevel(level string, timeout time.Duration) error {
	if _, err := a.sendCommand(CommandSetThinkingLevel, map[string]any{"level": level}, timeout); err != nil {
		return err
	}
	a.Mu.Lock()
	settled := a.effectiveThinking
	if settled == "" {
		settled = level
	}
	a.thinkingLevel = settled
	a.Mu.Unlock()
	return nil
}

// handleThinkingLevelChanged folds the thinking level omp settled on back into the
// settings.
//
// omp CLAMPS a level to what the model offers, so a model switch alone can move
// it, and it announces the level it settled on. The frame states the effective
// level, and `configured` when the reader's selector differs from it -- omp's own
// `auto`. A model with no thinking states neither, and runs "off".
//
// While the worker runs on Auto -- it sent no level at all -- the frame reports
// omp's own default, and the reader's choice stays Auto.
func (a *Agent) handleThinkingLevelChanged(raw []byte) {
	var frame struct {
		ThinkingLevel string `json:"thinkingLevel"`
		Configured    string `json:"configured"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		slog.Warn("omp thinking_level_changed decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	effective := frame.ThinkingLevel
	if effective == "" {
		effective = thinkingOff
	}
	level := frame.Configured
	if level == "" {
		level = effective
	}
	a.publishSettings(func() bool {
		a.effectiveThinking = effective
		if a.thinkingLevel == agent.EffortAuto || a.thinkingLevel == level {
			return false
		}
		a.thinkingLevel = level
		return true
	})
}

// handleConfigUpdate folds the model and the level that a slash command such as
// `/model` or `/switch` set back into the settings.
func (a *Agent) handleConfigUpdate(raw []byte) {
	var frame struct {
		Model *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		ThinkingLevel string `json:"thinkingLevel"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		slog.Warn("omp config_update decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.publishSettings(func() bool {
		changed := false
		if frame.Model != nil && frame.Model.ID != "" {
			changed = a.applyReportedModelLocked(joinModelID(frame.Model.Provider, frame.Model.ID))
		}
		if frame.ThinkingLevel != "" {
			a.effectiveThinking = frame.ThinkingLevel
			if a.thinkingLevel != agent.EffortAuto && frame.ThinkingLevel != a.thinkingLevel {
				a.thinkingLevel = frame.ThinkingLevel
				changed = true
			}
		}
		return changed
	})
}

// publishSettings states the settings the running agent settled on, whenever a
// value it reports differs from the one this worker holds. `apply` runs under
// a.Mu and reports whether anything moved; nothing publishes when nothing moved.
//
// Every unchanged setting stays in the snapshot: the settings pipeline takes every axis
// at once, and one left out would read as deleted.
func (a *Agent) publishSettings(apply func() bool) {
	a.Mu.Lock()
	changed := apply()
	settings := a.currentSettingsLocked()
	a.Mu.Unlock()
	if !changed {
		return
	}
	a.sink.PersistSettingsRefresh(settings)
}

// stateRefresher coalesces reads requested by native events, local commands, and failed settings writes.
// A request during a read schedules one more read after it. Each native response is about 100 KB.
type stateRefresher struct {
	mu      sync.Mutex
	running bool
	again   bool
	stopped bool
}

// schedule starts a read, or asks the running one for one more.
func (r *stateRefresher) schedule(a *Agent) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	if r.running {
		r.again = true
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go r.run(a)
}

func (r *stateRefresher) run(a *Agent) {
	for {
		a.sessionMu.Lock()
		raw, err := a.sendCommand(CommandGetState, nil, a.APITimeout())
		if err != nil {
			if !a.IsStopped() {
				slog.Warn("omp state refresh failed", "agent_id", a.AgentID(), "error", err)
			}
		} else {
			a.applyRefreshedState(raw)
		}
		a.sessionMu.Unlock()
		r.mu.Lock()
		if r.again && !r.stopped {
			r.again = false
			r.mu.Unlock()
			continue
		}
		r.running = false
		r.mu.Unlock()
		return
	}
}

// stop ends the refresher. A read that runs finishes; none starts after it.
func (r *stateRefresher) stop() {
	r.mu.Lock()
	r.stopped = true
	r.again = false
	r.mu.Unlock()
}

// applyRefreshedState folds a get_state response into the agent and publishes
// what moved.
func (a *Agent) applyRefreshedState(raw json.RawMessage) {
	a.Mu.Lock()
	prevModel, prevEffort := a.model, a.thinkingLevel
	prevFastMode := a.fastMode
	a.Mu.Unlock()
	identityChanged := a.applyState(raw)
	a.publishSettings(func() bool {
		return a.model != prevModel || a.thinkingLevel != prevEffort || a.fastMode != prevFastMode
	})
	if identityChanged {
		a.Mu.Lock()
		handle := a.sessionHandleLocked()
		a.Mu.Unlock()
		a.sink.UpdateSessionID(handle)
	}
}

// handleRetryFallback records a temporary model without changing the selected primary.
func (a *Agent) handleRetryFallback(raw []byte) {
	model := providerkit.StringField(raw, "to")
	if model == "" {
		return
	}
	provider, id := splitModelID(model)
	a.Mu.Lock()
	a.fallbackModel = joinModelID(provider, id)
	a.Mu.Unlock()
}

// applyReportedModelLocked records native state while a temporary fallback runs.
// The caller holds a.Mu. The result states whether the selected primary changed.
func (a *Agent) applyReportedModelLocked(model string) bool {
	a.effectiveModel = model
	if model == a.fallbackModel {
		return false
	}
	a.fallbackModel = ""
	changed := a.model != model
	a.model = model
	return changed
}
