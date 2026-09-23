package copilot

import (
	"cmp"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// parseCopilotModels reads the catalogue for the native session's account and custom providers.
func parseCopilotModels(raw json.RawMessage) ([]*agent.ModelInfo, error) {
	var response struct {
		Models *[]struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Capabilities struct {
				Limits struct {
					ContextWindow int64 `json:"max_context_window_tokens"`
				} `json:"limits"`
				Supports struct {
					ReasoningEfforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Copilot models: %w", err)
	}
	if response.Models == nil {
		return nil, fmt.Errorf("the Copilot model list is absent")
	}
	models := make([]*agent.ModelInfo, 0, len(*response.Models))
	seen := make(map[string]struct{}, len(*response.Models))
	for _, entry := range *response.Models {
		if entry.ID == "" {
			return nil, fmt.Errorf("a Copilot model ID is empty")
		}
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("the Copilot model list repeats ID %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		model := &agent.ModelInfo{
			Id: entry.ID, DisplayName: cmp.Or(entry.Name, entry.ID),
			ContextWindow: max(0, entry.Capabilities.Limits.ContextWindow),
		}
		efforts := make(map[string]struct{}, len(entry.Capabilities.Supports.ReasoningEfforts))
		for _, effort := range entry.Capabilities.Supports.ReasoningEfforts {
			if effort == "" {
				continue
			}
			if _, exists := efforts[effort]; exists {
				continue
			}
			efforts[effort] = struct{}{}
			model.SupportedEfforts = append(model.SupportedEfforts, providerkit.EffortTier(effort))
		}
		providerkit.SortEffortsDescending(model.SupportedEfforts)
		models = append(models, model)
	}
	return models, nil
}
