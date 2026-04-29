package agent

import (
	"encoding/json"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Pi default option values.
const (
	PiDefaultThinkingLevel = "medium"
	PiDefaultProvider      = "openai-codex"
	PiDefaultModel         = "gpt-5.5"
)

// Pi extra-settings keys persisted in the agents table.
const (
	// PiExtraProvider stores the underlying LLM provider name (e.g.
	// "openai-codex", "anthropic") so model switches can be sent with the
	// correct {provider, modelId} pair via Pi's set_model RPC.
	PiExtraProvider = "pi_provider"
)

// Pi thinking-level values. These match Pi's set_thinking_level wire values
// and are stored as the agent's `effort`.
const (
	PiThinkingOff     = "off"
	PiThinkingMinimal = "minimal"
	PiThinkingLow     = "low"
	PiThinkingMedium  = "medium"
	PiThinkingHigh    = "high"
	PiThinkingXHigh   = "xhigh"
)

// piAutoEffort is the Leapmux-side sentinel: when selected we omit the
// set_thinking_level RPC and let Pi keep its current level (typically driven
// by ~/.pi/agent/settings.json).
var piAutoEffort = &leapmuxv1.AvailableEffort{
	Id: EffortAuto, Name: "Auto", Description: "Use Pi's configured default thinking level",
}

// piDefaultEfforts is the static fallback list of thinking levels surfaced to
// the UI before get_available_models populates per-model SupportedEfforts.
var piDefaultEfforts = []*leapmuxv1.AvailableEffort{
	piAutoEffort,
	{Id: PiThinkingXHigh, Name: "Extra High"},
	{Id: PiThinkingHigh, Name: "High"},
	{Id: PiThinkingMedium, Name: "Medium"},
	{Id: PiThinkingLow, Name: "Low"},
	{Id: PiThinkingMinimal, Name: "Minimal"},
	{Id: PiThinkingOff, Name: "Off"},
}

// piNonReasoningEfforts is the trimmed effort list for models that don't
// support reasoning — only Auto and Off make sense.
var piNonReasoningEfforts = []*leapmuxv1.AvailableEffort{
	piAutoEffort,
	{Id: PiThinkingOff, Name: "Off"},
}

// piDefaultModels is the static fallback model list used until the Pi process
// answers get_available_models. The single entry mirrors the user's configured
// default; the runtime catalog supersedes this.
var piDefaultModels = []*leapmuxv1.AvailableModel{
	{
		Id:               PiDefaultModel,
		DisplayName:      "GPT-5.5",
		Description:      "Default Pi model (overridden once Pi reports its catalog)",
		IsDefault:        true,
		DefaultEffort:    PiDefaultThinkingLevel,
		SupportedEfforts: piDefaultEfforts,
	},
}

// applyAvailableModels parses a get_available_models response into the
// AvailableModel proto shape and stores it for the manager.
func (a *PiAgent) applyAvailableModels(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var resp struct {
		Models []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Provider      string `json:"provider"`
			Reasoning     bool   `json:"reasoning"`
			ContextWindow int64  `json:"contextWindow"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		slog.Warn("pi get_available_models unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	models := make([]*leapmuxv1.AvailableModel, 0, len(resp.Models))
	providers := make(map[string]string, len(resp.Models))
	for _, m := range resp.Models {
		if m.ID == "" {
			continue
		}
		display := m.Name
		if display == "" {
			display = m.ID
		}
		efforts := piDefaultEfforts
		if !m.Reasoning {
			// Models without reasoning support only `off`; still expose Auto.
			efforts = piNonReasoningEfforts
		}
		if m.Provider != "" {
			providers[m.ID] = m.Provider
		}
		models = append(models, &leapmuxv1.AvailableModel{
			Id:               m.ID,
			DisplayName:      display,
			DefaultEffort:    PiDefaultThinkingLevel,
			SupportedEfforts: efforts,
			ContextWindow:    m.ContextWindow,
		})
	}

	a.mu.Lock()
	a.availableModels = models
	a.modelProviders = providers
	a.mu.Unlock()
}

// providerForModel returns the underlying provider for a model id, looking it
// up in the available-models catalog. Falls back to the agent's current
// provider, then to the Pi default. Caller does not need to hold a.mu.
func (a *PiAgent) providerForModel(modelID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.modelProviders[modelID]; p != "" {
		return p
	}
	if a.provider != "" {
		return a.provider
	}
	return PiDefaultProvider
}

// applyModel sends set_model and updates local state on success.
func (a *PiAgent) applyModel(modelID, providerID string, timeout time.Duration) error {
	if providerID == "" {
		providerID = a.providerForModel(modelID)
	}
	params := map[string]any{"provider": providerID, "modelId": modelID}
	if _, err := a.sendPiCommand(PiCommandSetModel, params, timeout); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = modelID
	a.provider = providerID
	a.mu.Unlock()
	return nil
}

// applyThinkingLevel sends set_thinking_level and updates local state.
func (a *PiAgent) applyThinkingLevel(level string, timeout time.Duration) error {
	params := map[string]any{"level": level}
	if _, err := a.sendPiCommand(PiCommandSetThinkingLevel, params, timeout); err != nil {
		return err
	}
	a.mu.Lock()
	a.thinkingLevel = level
	a.mu.Unlock()
	return nil
}

// CurrentSettings returns a snapshot of the agent's settings.
func (a *PiAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.thinkingLevel,
		ExtraSettings: map[string]string{
			PiExtraProvider: a.provider,
		},
	}
}

// AvailableModels returns the catalog reported by Pi.
func (a *PiAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.availableModels
}

// AvailableOptionGroups returns nil — Pi exposes thinking levels per model
// via SupportedEfforts rather than as a global option group.
func (a *PiAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return nil
}

// UpdateSettings applies model, thinking-level, and provider changes live so
// the next prompt picks them up without a restart.
func (a *PiAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	a.mu.Lock()
	curEffort := a.thinkingLevel
	curModel := a.model
	curProvider := a.provider
	a.mu.Unlock()

	// Switching effort to "auto" is a Leapmux sentinel that means "let Pi
	// pick its own default" — the wire protocol has no equivalent, so a
	// restart is required (return false to signal the caller to restart).
	if IsEffortAutoTransition(s.GetEffort(), curEffort) {
		return false
	}

	timeout := a.APITimeout()

	if model := s.GetModel(); model != "" && model != curModel {
		providerID := curProvider
		if v := s.GetExtraSettings()[PiExtraProvider]; v != "" {
			providerID = v
		}
		if err := a.applyModel(model, providerID, timeout); err != nil {
			slog.Warn("pi UpdateSettings set_model failed", "agent_id", a.agentID, "model", model, "error", err)
		}
	} else if v := s.GetExtraSettings()[PiExtraProvider]; v != "" && v != curProvider {
		// Agent changed without a model change — re-send set_model so Pi
		// switches to the new provider's instance of the same model id.
		if err := a.applyModel(curModel, v, timeout); err != nil {
			slog.Warn("pi UpdateSettings provider switch failed", "agent_id", a.agentID, "provider", v, "error", err)
		}
	}

	if effort := s.GetEffort(); effort != "" && effort != EffortAuto && effort != curEffort {
		if err := a.applyThinkingLevel(effort, timeout); err != nil {
			slog.Warn("pi UpdateSettings set_thinking_level failed", "agent_id", a.agentID, "level", effort, "error", err)
		}
	}

	a.mu.Lock()
	model, eff, prov := a.model, a.thinkingLevel, a.provider
	a.mu.Unlock()
	a.sink.PersistSettingsRefresh(model, eff, "", map[string]string{
		PiExtraProvider: prov,
	})
	return true
}
