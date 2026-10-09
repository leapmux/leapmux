package muse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func decodeCatalog(raw []byte) ([]*agent.ModelInfo, error) {
	var result struct {
		Models *[]struct {
			ID          string          `json:"modelId"`
			Label       string          `json:"displayLabel"`
			Description string          `json:"description"`
			IsDefault   bool            `json:"isDefault"`
			Context     *int64          `json:"contextLimit"`
			Variants    json.RawMessage `json:"variants"`
			Described   []struct {
				Tier        string `json:"tier"`
				Description string `json:"description"`
			} `json:"reasoningEffortVariants"`
			DefaultEffort string `json:"defaultReasoningEffort"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Models == nil {
		return nil, fmt.Errorf("the Muse catalog contains no model list")
	}
	models := make([]*agent.ModelInfo, 0, len(*result.Models))
	ids := make(map[string]bool)
	for _, row := range *result.Models {
		if strings.TrimSpace(row.ID) == "" || ids[row.ID] || (row.Context != nil && *row.Context < 0) {
			return nil, fmt.Errorf("the Muse catalog contains an invalid or repeated model")
		}
		ids[row.ID] = true
		label := row.Label
		if label == "" {
			label = row.ID
		}
		model := &agent.ModelInfo{Id: row.ID, DisplayName: label, Description: row.Description, IsDefault: row.IsDefault, DefaultEffort: row.DefaultEffort}
		if row.Context != nil {
			model.ContextWindow = *row.Context
		}
		var variants []string
		if len(row.Variants) != 0 && string(row.Variants) != `"unknown"` {
			if err := json.Unmarshal(row.Variants, &variants); err != nil {
				return nil, fmt.Errorf("decode the Muse effort catalog: %w", err)
			}
		}
		seen := make(map[string]bool)
		for _, tier := range variants {
			if !validEffort(tier) || seen[tier] {
				return nil, fmt.Errorf("the Muse catalog contains an invalid or repeated effort")
			}
			seen[tier] = true
			effort := &agent.EffortInfo{Id: tier, Name: strings.ToUpper(tier[:1]) + tier[1:]}
			for _, described := range row.Described {
				if described.Tier == tier {
					effort.Description = described.Description
				}
			}
			model.SupportedEfforts = append(model.SupportedEfforts, effort)
		}
		if model.DefaultEffort != "" && !seen[model.DefaultEffort] {
			return nil, fmt.Errorf("the Muse default effort is outside its catalog")
		}
		models = append(models, model)
	}
	return models, nil
}

func validEffort(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}
