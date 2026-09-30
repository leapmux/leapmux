package qoder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const qoderSettingsLimit = 1 << 20

// readQoderConfiguredModels reads choices that a custom provider declares.
// Native available_models_update replaces these choices when it lists models.
func readQoderConfiguredModels(opts agent.Options) (models []qoderModel, err error) {
	root := qoderConfigRoot(opts)
	if !filepath.IsAbs(root) {
		root = filepath.Join(opts.WorkingDir, root)
	}
	file, err := os.Open(filepath.Join(root, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read qoder settings: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(file, qoderSettingsLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read qoder settings: %w", err)
	}
	if len(raw) > qoderSettingsLimit {
		return nil, fmt.Errorf("qoder settings exceed %d bytes", qoderSettingsLimit)
	}
	var settings struct {
		Providers map[string]struct {
			Model  string `json:"model"`
			Models []struct {
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("parse qoder settings: %w", err)
	}
	providerIDs := make([]string, 0, len(settings.Providers))
	for id := range settings.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	seen := make(map[string]bool)
	for _, providerID := range providerIDs {
		if strings.TrimSpace(providerID) == "" {
			continue
		}
		provider := settings.Providers[providerID]
		for _, entry := range provider.Models {
			id := providerID + "/" + strings.TrimSpace(entry.Model)
			if strings.TrimSpace(entry.Model) == "" || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(entry.DisplayName)
			if name == "" {
				name = strings.TrimSpace(entry.Model)
			}
			models = append(models, qoderModel{id: id, displayName: name})
		}
		if implicit := strings.TrimSpace(provider.Model); implicit != "" {
			id := providerID + "/" + implicit
			if !seen[id] {
				seen[id] = true
				models = append(models, qoderModel{id: id, displayName: implicit})
			}
		}
	}
	return models, nil
}

// qoderModel is one choice that Qoder reports on available_models_update.
type qoderModel struct {
	id          string
	displayName string
	description string
}

// qoderModelsUpdate is Qoder's full model catalog and current selection.
type qoderModelsUpdate struct {
	Models []struct {
		Value       string `json:"value"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	} `json:"models"`
	CurrentModel string `json:"currentModel"`
}

func (a *Agent) handleAvailableModelsUpdate(raw []byte) {
	var update qoderModelsUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		slog.Warn("qoder: unreadable model catalog", "agent_id", a.AgentID(), "error", err)
		return
	}
	models := make([]qoderModel, 0, len(update.Models))
	seen := make(map[string]bool, len(update.Models))
	for _, entry := range update.Models {
		if entry.Value == "" || seen[entry.Value] {
			continue
		}
		seen[entry.Value] = true
		name := entry.DisplayName
		if name == "" {
			name = entry.Value
		}
		models = append(models, qoderModel{id: entry.Value, displayName: name, description: entry.Description})
	}
	a.mu.Lock()
	a.models = models
	if update.CurrentModel != "" {
		a.model = update.CurrentModel
	}
	a.mu.Unlock()
}

// qoderModelGroup keeps the running model visible before Qoder sends a catalog.
func qoderModelGroup(models []qoderModel, current string) *leapmuxv1.AvailableOptionGroup {
	defs := make([]agent.OptionDef, 0, len(models)+1)
	currentFound := false
	for _, model := range models {
		defs = append(defs, agent.OptionDef{Id: model.id, Name: model.displayName, Description: model.description})
		currentFound = currentFound || model.id == current
	}
	if current != "" && !currentFound {
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
