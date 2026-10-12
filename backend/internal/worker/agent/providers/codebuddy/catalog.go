package codebuddy

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// loadModelCatalog reads the live models before Start publishes the first
// option groups. CodeBuddy accepts this control request before the first prompt.
//
// CodeBuddy answers from its own process. A late answer therefore means a slow
// start, and a cold start on a loaded machine can take longer than any short
// fixed wait. A wait that ends early leaves the agent with no live catalog
// until its next restart, so the wait lasts as long as the startup handshake
// of the agent allows. A process exit ends it sooner.
func (a *Agent) loadModelCatalog() error {
	result, err := a.sendControlAndWait(`{"subtype":"get_available_models"}`, a.opts.EffectiveStartupTimeout())
	if err != nil {
		return err
	}
	if !result.HasModelCatalog {
		return fmt.Errorf("CodeBuddy returned no availableModels list")
	}
	a.mu.Lock()
	a.models = normalizeCodebuddyModels(result.Models)
	a.mu.Unlock()
	return nil
}

func normalizeCodebuddyModels(available []codebuddyModelInfo) []codebuddyModelInfo {
	seen := make(map[string]bool, len(available))
	models := make([]codebuddyModelInfo, 0, len(available))
	for _, model := range available {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		models = append(models, model)
	}
	return models
}

// codebuddyModelGroup lists the live catalog and preserves a selected model
// that the catalog no longer offers, so its current value stays visible.
func codebuddyModelGroup(models []codebuddyModelInfo, current string) *leapmuxv1.AvailableOptionGroup {
	defs := make([]agent.OptionDef, 0, len(models)+1)
	seenCurrent := false
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		defs = append(defs, agent.OptionDef{
			Id: model.ID, Name: agent.NameOrID(model.Name, model.ID),
			Description: model.Description, Default: model.ID == current,
		})
		seenCurrent = seenCurrent || model.ID == current
	}
	if current != "" && !seenCurrent {
		defs = append([]agent.OptionDef{{Id: current, Name: current, Default: true}}, defs...)
	}
	if len(defs) == 0 {
		return nil
	}
	return agent.SelectGroup(agent.OptionIDModel, agent.ModelGroupLabel, agent.OptionOrderModel, current, defs)
}

func codebuddyModelOffered(models []codebuddyModelInfo, model string) bool {
	for _, available := range models {
		if available.ID == model {
			return true
		}
	}
	return false
}
