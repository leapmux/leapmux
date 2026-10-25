package qoder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const qoderSettingsLimit = 1 << 20

// readQoderConfiguredModels reads choices that a custom provider declares.
// A native catalog replaces these choices, including when every native model is disabled.
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

// qoderModel is one enabled native choice or a configured fallback before a catalog arrives.
type qoderModel struct {
	id          string
	displayName string
	description string
}

// qoderModelsUpdate is Qoder's full model catalog and current selection.
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
	models := qoderEnabledModels(entries)
	a.mu.Lock()
	if initialize && a.modelsReported {
		a.mu.Unlock()
		return
	}
	changed := current != "" && current != a.model
	if entries != nil {
		changed = changed || !a.modelsReported || !slices.Equal(a.models, models)
		a.models = models
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
