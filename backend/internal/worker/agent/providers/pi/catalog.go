package pi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// These values seed the model catalog before Pi reports its live models.
// Pi sends the provider and model as one pair to set_model at startup.
// An unknown pair makes Pi stop answering, so this pair must match Pi's catalog.
const (
	DefaultThinkingLevel = "medium"
	DefaultProvider      = "zai"
	DefaultModel         = "glm-5.3"
)

// piAutoEffort is the LeapMux-side sentinel: when selected we omit the
// set_thinking_level RPC and let Pi keep its current level (typically driven
// by ~/.pi/agent/settings.json).
var piAutoEffort = &agent.EffortInfo{
	Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Use Pi's configured default thinking level",
}

// piDefaultEfforts is the static fallback list of thinking levels surfaced to
// the UI before get_available_models populates per-model SupportedEfforts.
var piDefaultEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingXHigh),
	providerkit.EffortTier(ThinkingHigh),
	providerkit.EffortTier(ThinkingMedium),
	providerkit.EffortTier(ThinkingLow),
	providerkit.EffortTier(ThinkingMinimal),
	providerkit.EffortTier(ThinkingOff),
}

// piNonReasoningEfforts is the trimmed effort list for models that don't
// support reasoning — only Auto and Off make sense.
var piNonReasoningEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingOff),
}

// piDefaultModels is the static fallback model list used until the Pi process
// answers get_available_models. The single entry mirrors the user's configured
// default; the runtime catalog supersedes this.
var piDefaultModels = []*agent.ModelInfo{
	{
		Id:               DefaultModel,
		DisplayName:      "GLM-5.3",
		Description:      "Default Pi model (overridden once Pi reports its catalog)",
		IsDefault:        true,
		DefaultEffort:    DefaultThinkingLevel,
		SupportedEfforts: piDefaultEfforts,
	},
}

// applyAvailableModels parses a get_available_models response into the
// AvailableModel proto shape and stores it for the manager.
func (a *Agent) applyAvailableModels(raw json.RawMessage) {
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
		slog.Warn("pi get_available_models unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}

	models := make([]*agent.ModelInfo, 0, len(resp.Models))
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
		models = append(models, &agent.ModelInfo{
			Id:               m.ID,
			DisplayName:      display,
			DefaultEffort:    DefaultThinkingLevel,
			SupportedEfforts: efforts,
			ContextWindow:    m.ContextWindow,
		})
	}

	// A response that parsed but yielded no usable model (empty list, or every entry missing an
	// id) carries no information -- like the len(raw) == 0 case above -- so leave the catalog
	// untouched rather than overwriting it with an empty list, which would blank the model picker
	// until the next non-empty response. (Today the manager's static-fallback chain backstops an
	// empty a.availableModels, but keeping the guard local makes the intent self-evident.)
	if len(models) == 0 {
		return
	}

	a.Mu.Lock()
	a.availableModels = models
	a.modelProviders = providers
	a.Mu.Unlock()
}

// providerForModel returns the underlying provider for a model id, looking it
// up in the available-models catalog. Falls back to the agent's current
// provider, then to the Pi default. Caller does not need to hold a.Mu.
func (a *Agent) providerForModel(modelID string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if p := a.modelProviders[modelID]; p != "" {
		return p
	}
	if a.provider != "" {
		return a.provider
	}
	return DefaultProvider
}
