package qoder

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// readCustomModelCatalog reads the SDK's custom-provider catalog. The session
// catalog lists Qoder models only, so its empty result cannot replace this list.
func (a *Agent) readCustomModelCatalog() error {
	result, err := a.sendControlAndWait(`{"subtype":"daemon.list_byok_configs"}`, initializeTimeout)
	if err != nil {
		return fmt.Errorf("read the Qoder custom model catalog: %w", err)
	}
	models, err := qoderCustomModels(result.RawResponse)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.customModels = models
	a.mu.Unlock()
	return nil
}

func qoderCustomModels(raw json.RawMessage) ([]qoderModel, error) {
	var catalog struct {
		Configs *[]struct {
			ProviderID string `json:"providerId"`
			Models     []struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
		} `json:"configs"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("decode the Qoder custom model catalog: %w", err)
	}
	if catalog.Configs == nil {
		return nil, fmt.Errorf("the Qoder custom model catalog has no configs array")
	}
	configs := *catalog.Configs
	sort.SliceStable(configs, func(i, j int) bool { return configs[i].ProviderID < configs[j].ProviderID })
	var models []qoderModel
	seen := make(map[string]bool)
	for _, config := range configs {
		providerID := strings.TrimSpace(config.ProviderID)
		if providerID == "" {
			continue
		}
		for _, model := range config.Models {
			modelID := strings.TrimSpace(model.ID)
			id := providerID + "/" + modelID
			if modelID == "" || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = modelID
			}
			models = append(models, qoderModel{id: id, displayName: name})
		}
	}
	return models, nil
}

// qoderMergedModels keeps native choices authoritative for every ID they
// mention, including disabled choices. Custom providers supply the remaining IDs.
func qoderMergedModels(native []qoderModelEntry, custom []qoderModel) []qoderModel {
	models := qoderEnabledModels(native)
	seen := make(map[string]bool, len(native)+len(custom))
	for _, entry := range native {
		seen[entry.Value] = true
	}
	for _, model := range custom {
		if !seen[model.id] {
			models = append(models, model)
			seen[model.id] = true
		}
	}
	return models
}

// qoderModel is one enabled native choice or a model of a custom provider.
type qoderModel struct {
	id          string
	displayName string
	description string
}

// qoderModelsUpdate is Qoder's account model catalog and current selection.
type qoderModelsUpdate struct {
	Models       []qoderModelEntry `json:"models"`
	CurrentModel string            `json:"currentModel"`
}

type qoderModelEntry struct {
	Value       string `json:"value"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	IsEnabled   bool   `json:"isEnabled"`
}

type qoderInitializeResponse struct {
	Models []qoderModelEntry `json:"models"`
	// OutputStyle is the style that the session runs; AvailableOutputStyles
	// lists the built-in styles and the user's own styles. Both are optional,
	// so qoderOutputStyleReport reads them without failing the handshake.
	OutputStyle           json.RawMessage `json:"output_style"`
	AvailableOutputStyles json.RawMessage `json:"available_output_styles"`
}

// qoderOutputStyleReport reads the style fields of an initialize reply. A
// field of an unexpected shape reads as absent.
func qoderOutputStyleReport(response qoderInitializeResponse) (running string, styles []string, malformed bool) {
	running, ok := providerkit.RawString(response.OutputStyle)
	if !ok && providerkit.RawPresent(response.OutputStyle) {
		malformed = true
	}
	if providerkit.RawPresent(response.AvailableOutputStyles) && json.Unmarshal(response.AvailableOutputStyles, &styles) != nil {
		styles, malformed = nil, true
	}
	return running, styles, malformed
}

// applyOutputStyles records the styles that the initialize reply states, and
// refreshes the stored settings when they change. A reply without a list
// keeps the built-in styles.
func (a *Agent) applyOutputStyles(running string, styles []string) {
	a.mu.Lock()
	changed := running != a.runningOutputStyle || styles != nil && !slices.Equal(styles, a.outputStyles)
	a.runningOutputStyle = running
	if styles != nil {
		a.outputStyles = slices.Clone(styles)
	}
	a.mu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	}
}

func (a *Agent) handleAvailableModelsUpdate(raw []byte) {
	var update qoderModelsUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		slog.Warn("qoder: unreadable model catalog", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.applyModelCatalog(update.Models, update.CurrentModel, false)
}

func qoderEnabledModels(entries []qoderModelEntry) []qoderModel {
	models := make([]qoderModel, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !entry.IsEnabled || strings.TrimSpace(entry.Value) == "" || seen[entry.Value] {
			continue
		}
		seen[entry.Value] = true
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = entry.Value
		}
		models = append(models, qoderModel{id: entry.Value, displayName: name, description: entry.Description})
	}
	return models
}

// applyModelCatalog keeps a native update that arrives before the initialize caller resumes.
func (a *Agent) applyModelCatalog(entries []qoderModelEntry, current string, initialize bool) {
	a.mu.Lock()
	if initialize && a.modelsReported {
		a.mu.Unlock()
		return
	}
	changed := current != "" && current != a.model
	if entries != nil {
		changed = changed || !a.modelsReported || !slices.Equal(a.models, entries)
		a.models = slices.Clone(entries)
		a.modelsReported = true
	}
	if current != "" {
		a.model = current
	}
	a.mu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	}
}

// qoderModelGroup keeps the running model visible before Qoder sends a catalog.
func qoderModelGroup(models []qoderModel, current string, includeCurrent bool) *leapmuxv1.AvailableOptionGroup {
	defs := make([]agent.OptionDef, 0, len(models)+1)
	currentFound := false
	for _, model := range models {
		defs = append(defs, agent.OptionDef{Id: model.id, Name: model.displayName, Description: model.description})
		currentFound = currentFound || model.id == current
	}
	if includeCurrent && current != "" && !currentFound {
		defs = append(defs, agent.OptionDef{Id: current, Name: current})
	}
	if len(defs) == 0 {
		return nil
	}
	if current == "" {
		current = defs[0].Id
	}
	return agent.SelectGroup(agent.OptionIDModel, "Model", agent.OptionOrderModel, current, defs)
}
