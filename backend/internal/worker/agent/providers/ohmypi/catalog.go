package ohmypi

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// thinkingOff is omp's thinking level that turns thinking off. omp offers it for
// every model, including one that does not reason.
const thinkingOff = "off"

// autoEffort is LeapMux's sentinel for "send no thinking level": omp then keeps the
// level its own configuration sets, which can be omp's own per-prompt `auto`.
var autoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use the thinking level that Oh My Pi's configuration sets",
}

// joinModelID spells one omp model the way `--model` and the model option group
// take it: `<provider>/<id>`. omp identifies a model by the pair, and two
// providers can offer the same id.
func joinModelID(provider, id string) string {
	if provider == "" {
		return id
	}
	return provider + "/" + id
}

// splitModelID reads `<provider>/<id>` back into the pair set_model takes. The
// provider is everything before the FIRST slash: a model id can hold a slash of its
// own ("openrouter/anthropic/claude-sonnet"), and a provider id holds none.
func splitModelID(model string) (provider, id string) {
	provider, id, ok := strings.Cut(model, "/")
	if !ok {
		return "", model
	}
	return provider, id
}

// ompModel is one entry of a get_available_models response.
type ompModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Reasoning is false for a model with no thinking levels beyond "off".
	Reasoning bool `json:"reasoning"`
	Thinking  *struct {
		Efforts []string `json:"efforts"`
	} `json:"thinking"`
	ContextWindow int64 `json:"contextWindow"`
}

// modelInfos converts a get_available_models response into the catalog, or nil
// when the response holds no usable model.
func modelInfos(raw json.RawMessage) ([]*agent.ModelInfo, error) {
	var response struct {
		Models []ompModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	models := make([]*agent.ModelInfo, 0, len(response.Models))
	seen := make(map[string]bool, len(response.Models))
	for _, m := range response.Models {
		if m.ID == "" {
			continue
		}
		id := joinModelID(m.Provider, m.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		display := m.Name
		if display == "" {
			display = m.ID
		}
		models = append(models, &agent.ModelInfo{
			Id:               id,
			DisplayName:      display,
			Description:      id,
			DefaultEffort:    agent.EffortAuto,
			SupportedEfforts: modelEfforts(m),
			ContextWindow:    m.ContextWindow,
		})
	}
	return models, nil
}

// modelEfforts lists the thinking levels one model offers: Auto first, then the
// levels strongest first, with "off" last.
//
// omp states a reasoning model's levels in `thinking.efforts` and offers "off" for
// every model (get_available_thinking_levels answers `["off", ...efforts]`).
func modelEfforts(m ompModel) []*agent.EffortInfo {
	levels := []*agent.EffortInfo{providerkit.EffortTier(thinkingOff)}
	if m.Reasoning && m.Thinking != nil {
		for _, effort := range m.Thinking.Efforts {
			if effort == "" || effort == thinkingOff || effort == agent.EffortAuto {
				continue
			}
			levels = append(levels, providerkit.EffortTier(effort))
		}
	}
	providerkit.SortEffortsDescending(levels)
	out := make([]*agent.EffortInfo, 0, len(levels)+1)
	out = append(out, autoEffort)
	return append(out, levels...)
}

// applyAvailableModels stores the catalog a get_available_models response states.
// A response that yields no model leaves the catalog as it was, rather than
// blanking the model picker.
func (a *Agent) applyAvailableModels(raw json.RawMessage) {
	models, err := modelInfos(raw)
	if err != nil {
		slog.Warn("omp get_available_models decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	if len(models) == 0 {
		return
	}
	a.Mu.Lock()
	a.availableModels = models
	a.Mu.Unlock()
}

// providerForModel returns the provider of a model the caller identified by its id
// alone, from the catalog.
func (a *Agent) providerForModel(id string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, m := range a.availableModels {
		provider, modelID := splitModelID(m.Id)
		if modelID == id {
			return provider
		}
	}
	return ""
}
