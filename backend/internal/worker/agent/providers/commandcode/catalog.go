package commandcode

import (
	"cmp"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const providerConfigMaximum = 4 << 20

type declaredModel struct {
	Name          string  `json:"name"`
	ContextWindow float64 `json:"contextWindow"`
	Limit         struct {
		Context float64 `json:"context"`
	} `json:"limit"`
	Reasoning        *bool    `json:"reasoning"`
	ReasoningEfforts []string `json:"reasoningEfforts"`
}

// loadModels reads the native declarative catalog without reading or exposing credentials.
func loadModels(home, current string, localOnly bool) []*agent.ModelInfo {
	var config struct {
		Provider  map[string]json.RawMessage `json:"provider"`
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if home != "" {
		if err := sessionstore.ReadSidecarFile(filepath.Join(home, ".commandcode", "providers.json"), providerConfigMaximum, func(raw []byte) error { return json.Unmarshal(raw, &config) }); err != nil {
			config.Provider, config.Providers = nil, nil
		}
	}
	entries := config.Provider
	if entries == nil {
		entries = config.Providers
	}
	var models []*agent.ModelInfo
	if !localOnly {
		models = gatewayModels()
	}
	for providerID, raw := range entries {
		var provider struct {
			Disabled bool                       `json:"disabled"`
			Enabled  *bool                      `json:"enabled"`
			Models   map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal(raw, &provider) != nil || provider.Disabled || provider.Enabled != nil && !*provider.Enabled {
			continue
		}
		for id, raw := range provider.Models {
			if id == "" || providerID == "" || strings.TrimSpace(string(raw)) == "null" {
				continue
			}
			var declared declaredModel
			if json.Unmarshal(raw, &declared) != nil {
				continue
			}
			model := &agent.ModelInfo{Id: providerID + "/" + id, DisplayName: cmp.Or(declared.Name, id)}
			window := declared.ContextWindow
			if window == 0 {
				window = declared.Limit.Context
			}
			if window > 0 && window <= math.MaxInt64 && window == math.Trunc(window) {
				model.ContextWindow = int64(window)
			}
			if model.ContextWindow == 0 {
				model.ContextWindow = 200000
			}
			efforts := declared.ReasoningEfforts
			if efforts == nil && declared.Reasoning != nil && *declared.Reasoning {
				efforts = []string{"low", "medium", "high"}
			}
			seen := make(map[string]bool)
			for _, effort := range efforts {
				if !validEffort(effort) || effort == "off" || seen[effort] {
					continue
				}
				seen[effort] = true
				model.SupportedEfforts = append(model.SupportedEfforts, providerkit.EffortTier(effort))
			}
			providerkit.SortEffortsDescending(model.SupportedEfforts)
			models = append(models, model)
		}
	}
	slices.SortFunc(models, func(a, b *agent.ModelInfo) int { return strings.Compare(a.Id, b.Id) })
	if current != "" && agent.FindAvailableModel(models, current) == nil {
		model := &agent.ModelInfo{Id: current, DisplayName: current}
		if localOnly && agent.FindAvailableModel(gatewayModels(), current) != nil {
			model.Hidden = true
		}
		models = append(models, model)
	}
	return models
}

func validEffort(value string) bool {
	switch value {
	case "off", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func registrationModels() []*agent.ModelInfo {
	if value := os.Getenv("CMD_LOCAL_ONLY"); value == "1" || value == "true" {
		return nil
	}
	return append([]*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the model from Command Code settings")}, gatewayModels()...)
}

// userHome resolves the same home that the native session store uses.
func userHome(query agent.StoredSessionQuery) string {
	if query.HomeDir != "" {
		return query.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}
