package ohmypi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// OptionGroups returns the model group, the current model's thinking-level group,
// and the approval-mode group.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	model, effort, approval := a.model, a.thinkingLevel, a.approvalMode
	models := a.availableModels
	a.Mu.Unlock()

	var groups []*leapmuxv1.AvailableOptionGroup
	if len(models) > 0 {
		groups = providerkit.ModelAndEffortGroups(models, model, effort, ThinkingLevelLabel, nil)
	} else {
		// No catalog yet: the running values read back, and nothing can be picked.
		groups = providerkit.ReadOnlyModelAndEffortGroups(model, model, effort)
	}
	return append(groups, providerkit.LiveGroup(approvalModeGroup, approval))
}

// SettingsSnapshot confirms every value the running agent reports.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies a model or thinking-level change live. An approval-mode
// change, and a move to Auto, restart the agent instead.
//
// omp applies the approval mode at launch only, and no RPC command UNSETS a thinking
// level, which is what Auto asks for. A live apply that fails also restarts: the
// caller relaunches with the requested settings as launch options, so the change
// takes effect rather than leaving the reader on a value the agent never took.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	curModel, curEffort, curApproval := a.model, a.thinkingLevel, a.approvalMode
	a.Mu.Unlock()

	if mode := options[agent.OptionIDPermissionMode]; mode != "" && mode != curApproval {
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

	a.Mu.Lock()
	model, effort, approval := a.model, a.thinkingLevel, a.approvalMode
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          model,
		agent.OptionIDEffort:         effort,
		agent.OptionIDPermissionMode: approval,
	})
	return a.SettingsSnapshot()
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
			if model := joinModelID(frame.Model.Provider, frame.Model.ID); model != a.model {
				a.model = model
				changed = true
			}
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
// The approval mode rides along unchanged: the settings pipeline takes every axis
// at once, and one left out would read as deleted.
func (a *Agent) publishSettings(apply func() bool) {
	a.Mu.Lock()
	changed := apply()
	model, effort, approval := a.model, a.thinkingLevel, a.approvalMode
	a.Mu.Unlock()
	if !changed {
		return
	}
	a.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          model,
		agent.OptionIDEffort:         effort,
		agent.OptionIDPermissionMode: approval,
	})
}

// stateRefresher runs the get_state reads that a model_changed frame asks for, one
// at a time. A frame that arrives while a read runs asks for one more read after
// it, rather than for a read of its own: omp's answer is the same either way, and
// each answer is about 100 KB.
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
		raw, err := a.sendCommand(CommandGetState, nil, a.APITimeout())
		if err != nil {
			if !a.IsStopped() {
				slog.Warn("omp get_state after a model change failed", "agent_id", a.AgentID(), "error", err)
			}
		} else {
			a.applyRefreshedState(raw)
		}
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
	a.Mu.Unlock()
	identityChanged := a.applyState(raw)
	a.publishSettings(func() bool {
		return a.model != prevModel || a.thinkingLevel != prevEffort
	})
	if identityChanged {
		a.Mu.Lock()
		handle := a.sessionHandleLocked()
		a.Mu.Unlock()
		a.sink.UpdateSessionID(handle)
	}
}
