package deepseekharness

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// modelSelection preserves the native provider route and exact model identity.
type modelSelection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"reasoningEffort,omitempty"`
}

func (s modelSelection) id() string {
	if s.Provider == "" || s.Model == "" {
		return ""
	}
	return s.Provider + "/" + s.Model
}

func splitModelID(id string) (modelSelection, error) {
	provider, model, ok := strings.Cut(id, "/")
	if !ok || provider == "" || model == "" || strings.ContainsAny(id, "\x00\r\n") {
		return modelSelection{}, fmt.Errorf("DeepSeek Harness model must identify its provider and model")
	}
	return modelSelection{Provider: provider, Model: model}, nil
}

type nativeModelCatalog struct {
	Default modelSelection `json:"default"`
	Groups  []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Models []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Reasoning   *struct {
				DefaultEffort string `json:"defaultEffort"`
				Efforts       []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"efforts"`
			} `json:"reasoning"`
		} `json:"models"`
	} `json:"groups"`
}

type modelCatalog struct {
	models           []*agent.ModelInfo
	defaultSelection modelSelection
}

func convertModelCatalog(native nativeModelCatalog) (modelCatalog, error) {
	result := modelCatalog{defaultSelection: native.Default}
	seen := map[string]bool{}
	for _, group := range native.Groups {
		if group.ID == "" || strings.Contains(group.ID, "/") {
			return modelCatalog{}, fmt.Errorf("DeepSeek Harness model provider is invalid")
		}
		for _, model := range group.Models {
			selection := modelSelection{Provider: group.ID, Model: model.ID}
			id := selection.id()
			if _, err := splitModelID(id); err != nil {
				return modelCatalog{}, err
			}
			if seen[id] {
				return modelCatalog{}, fmt.Errorf("DeepSeek Harness model catalog repeats a model")
			}
			seen[id] = true
			item := &agent.ModelInfo{Id: id, DisplayName: agent.NameOrID(model.Name, model.ID), Description: model.Description, IsDefault: id == native.Default.id()}
			if model.Reasoning != nil {
				levels := map[string]bool{}
				for _, level := range model.Reasoning.Efforts {
					if level.ID == "" || levels[level.ID] {
						return modelCatalog{}, fmt.Errorf("DeepSeek Harness reasoning catalog is invalid")
					}
					levels[level.ID] = true
					item.SupportedEfforts = append(item.SupportedEfforts, &agent.EffortInfo{Id: level.ID, Name: agent.NameOrID(level.Name, level.ID), Description: level.Description})
				}
				if model.Reasoning.DefaultEffort != "" && !levels[model.Reasoning.DefaultEffort] {
					return modelCatalog{}, fmt.Errorf("DeepSeek Harness reasoning default is unavailable")
				}
				item.DefaultEffort = model.Reasoning.DefaultEffort
			}
			result.models = append(result.models, item)
		}
	}
	if len(result.models) == 0 || !seen[native.Default.id()] {
		return modelCatalog{}, fmt.Errorf("DeepSeek Harness has no available default model")
	}
	return result, nil
}

func (c modelCatalog) resolve(current modelSelection, model, effort string) (modelSelection, error) {
	next := current
	if model != "" {
		parsed, err := splitModelID(model)
		if err != nil {
			return modelSelection{}, err
		}
		next = parsed
	}
	item := agent.FindAvailableModel(c.models, next.id())
	if item == nil {
		return modelSelection{}, fmt.Errorf("DeepSeek Harness does not offer the requested model")
	}
	// The Worker service sends agent.EffortAuto when a model switch makes the old effort invalid,
	// because Auto is valid for every model. For this provider Auto means the effort that the
	// selected model declares as its default.
	switch effort {
	case "":
	case agent.EffortAuto:
		next.Effort = ""
	default:
		next.Effort = effort
	}
	if next.Effort == "" {
		next.Effort = item.DefaultEffort
	}
	if next.Effort != "" {
		allowed := false
		for _, level := range item.SupportedEfforts {
			allowed = allowed || level.Id == next.Effort
		}
		if !allowed {
			return modelSelection{}, fmt.Errorf("DeepSeek Harness does not offer the requested reasoning effort")
		}
	}
	return next, nil
}
