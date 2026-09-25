package mimo

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// mimoConfigProviders is the reply of GET /config/providers: every provider the
// user's configuration enables, with its models.
type mimoConfigProviders struct {
	Providers []mimoProviderInfo `json:"providers"`
	// Default maps a provider id to the model id MiMo ranks first for it.
	Default map[string]string `json:"default"`
}

type mimoProviderInfo struct {
	ID     string                   `json:"id"`
	Name   string                   `json:"name"`
	Models map[string]mimoModelInfo `json:"models"`
}

// mimoModelInfo is one model of GET /config/providers.
type mimoModelInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Limit  struct {
		Context int64 `json:"context"`
	} `json:"limit"`
	// Variants maps a reasoning variant to the request options it sets. The keys
	// are what a prompt's `variant` selects; LeapMux reads nothing else.
	Variants map[string]json.RawMessage `json:"variants"`
}

// mimoModelStatusDeprecated marks a model that MiMo still serves and no longer
// recommends. The catalog keeps it resolvable for a session that runs it, and
// hides it from the picker.
const mimoModelStatusDeprecated = "deprecated"

// mimoConfig is the part of GET /config that the catalog reads: the configured
// default model, as `<provider>/<model>`.
type mimoConfig struct {
	Model string `json:"model"`
}

// mimoAgentInfo is one entry of GET /agent.
type mimoAgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Mode is `primary`, `subagent` or `all`. A primary agent is one a prompt
	// can run on.
	Mode   string `json:"mode"`
	Hidden bool   `json:"hidden"`
}

// Agent modes of GET /agent that can run a prompt.
const (
	agentModePrimary = "primary"
	agentModeAll     = "all"
)

// mimoAutoEffort is the entry that sends no variant, which leaves the model on
// the request options its configuration sets without one.
var mimoAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use MiMo's default for the model; send no variant",
}

// mimoCatalog is what the server states about its models and agents. It is
// built once at startup and replaced as a whole, so a reader holds it without
// copying it.
type mimoCatalog struct {
	// models is the picker's catalog. Each id is `<provider>/<model>`.
	models []*agent.ModelInfo
	// defaultModel is the model a session runs when LeapMux asks for none.
	defaultModel string
	// modes are the primary agents a prompt can run on.
	modes []agent.OptionDef
}

// buildMiMoCatalog projects the server's answers into the catalog.
//
// The providers keep the server's order. Within one provider the models sort by
// display name and then by id, because the reply carries them in a JSON object,
// whose key order Go does not keep.
func buildMiMoCatalog(providers mimoConfigProviders, config mimoConfig, agents []mimoAgentInfo) mimoCatalog {
	var catalog mimoCatalog
	for _, provider := range providers.Providers {
		if provider.ID == "" {
			continue
		}
		models := make([]*agent.ModelInfo, 0, len(provider.Models))
		for key, model := range provider.Models {
			modelID := model.ID
			if modelID == "" {
				modelID = key
			}
			id := joinModelID(mimoModelRef{ProviderID: provider.ID, ModelID: modelID})
			if id == "" {
				continue
			}
			models = append(models, &agent.ModelInfo{
				Id:               id,
				DisplayName:      mimoModelDisplayName(provider, model, modelID),
				DefaultEffort:    mimoDefaultEffort(model.Variants),
				SupportedEfforts: mimoEfforts(model.Variants),
				ContextWindow:    model.Limit.Context,
				Hidden:           model.Status == mimoModelStatusDeprecated,
			})
		}
		slices.SortFunc(models, func(a, b *agent.ModelInfo) int {
			return cmp.Or(
				cmp.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)),
				cmp.Compare(a.Id, b.Id),
			)
		})
		catalog.models = append(catalog.models, models...)
	}
	catalog.defaultModel = mimoDefaultModel(catalog.models, providers, config)
	if model := agent.FindAvailableModel(catalog.models, catalog.defaultModel); model != nil {
		model.IsDefault = true
	}
	catalog.modes = mimoModes(agents)
	return catalog
}

// mimoModelDisplayName labels a model with its provider, as MiMo's own picker
// does: two providers can serve models of one name.
func mimoModelDisplayName(provider mimoProviderInfo, model mimoModelInfo, modelID string) string {
	name := agent.NameOrID(model.Name, modelID)
	providerName := agent.NameOrID(provider.Name, provider.ID)
	return providerName + " / " + name
}

// mimoEfforts lists a model's variants for the effort menu: Auto first, then the
// variants strongest first. A model with no variant has no effort axis at all.
func mimoEfforts(variants map[string]json.RawMessage) []*agent.EffortInfo {
	if len(variants) == 0 {
		return nil
	}
	levels := make([]*agent.EffortInfo, 0, len(variants))
	keys := make([]string, 0, len(variants))
	for key := range variants {
		// `default` names the model's own request options, which is what Auto
		// already sends, so it would be the same entry twice. MiMo's ACP mode
		// drops it for the same reason.
		if key == "" || key == mimoDefaultVariant {
			continue
		}
		keys = append(keys, key)
	}
	// The map order is random; a stable base order keeps two unranked variants in
	// the same place on every build of the menu.
	slices.Sort(keys)
	for _, key := range keys {
		levels = append(levels, providerkit.EffortTier(key))
	}
	if len(levels) == 0 {
		return nil
	}
	providerkit.SortEffortsDescending(levels)
	return append([]*agent.EffortInfo{mimoAutoEffort}, levels...)
}

// mimoDefaultVariant is the variant name MiMo reserves for "no variant".
const mimoDefaultVariant = "default"

// mimoDefaultEffort is Auto for a model with variants: MiMo applies no variant
// unless a prompt names one.
func mimoDefaultEffort(variants map[string]json.RawMessage) string {
	if len(mimoEfforts(variants)) == 0 {
		return ""
	}
	return agent.EffortAuto
}

// mimoDefaultModel resolves the model a prompt runs when LeapMux names none, in
// MiMo's own order: the configured model, then the first provider's top-ranked
// model, then the first model of the catalog.
func mimoDefaultModel(models []*agent.ModelInfo, providers mimoConfigProviders, config mimoConfig) string {
	if model := strings.TrimSpace(config.Model); model != "" && agent.FindAvailableModel(models, model) != nil {
		return model
	}
	for _, provider := range providers.Providers {
		modelID := providers.Default[provider.ID]
		if id := joinModelID(mimoModelRef{ProviderID: provider.ID, ModelID: modelID}); id != "" && agent.FindAvailableModel(models, id) != nil {
			return id
		}
	}
	for _, model := range models {
		if !model.Hidden {
			return model.Id
		}
	}
	return ""
}

// mimoModes lists the primary agents a prompt can run on, in the server's
// order. build is the default. A server that lists none of them, or an answer
// that failed, leaves the static seed in place.
func mimoModes(agents []mimoAgentInfo) []agent.OptionDef {
	modes := make([]agent.OptionDef, 0, len(agents))
	for _, info := range agents {
		if info.Hidden || info.Name == "" || (info.Mode != agentModePrimary && info.Mode != agentModeAll) {
			continue
		}
		modes = append(modes, agent.OptionDef{
			Id:          info.Name,
			Name:        providerkit.TitleCaseID(info.Name, ""),
			Description: strings.TrimSpace(info.Description),
			Default:     info.Name == contracts.MiMoDefaultMode,
		})
	}
	if len(modes) == 0 {
		return mimoStaticModes
	}
	return modes
}

// hasMode reports whether the catalog offers a primary agent.
func (c mimoCatalog) hasMode(mode string) bool {
	for _, def := range c.modes {
		if def.Id == mode {
			return true
		}
	}
	return false
}

// resolveModel returns the catalog id for a requested model, or "" when the
// catalog does not hold it.
func (c mimoCatalog) resolveModel(model string) string {
	if model == "" || agent.UsesAccountDefaultModel(model) {
		return ""
	}
	if agent.FindAvailableModel(c.models, model) != nil {
		return model
	}
	return ""
}

// resolveEffort returns the variant a prompt sends for model at effort: the
// effort itself when the model offers it, else "". Auto and an unknown effort
// both send no variant.
func (c mimoCatalog) resolveEffort(model, effort string) string {
	if effort == "" || effort == agent.EffortAuto {
		return ""
	}
	info := agent.FindAvailableModel(c.models, model)
	if info == nil {
		return ""
	}
	for _, level := range info.SupportedEfforts {
		if level.GetId() == effort {
			return effort
		}
	}
	return ""
}

// contextWindow returns the context window of a model, or 0 when unknown.
func (c mimoCatalog) contextWindow(model string) int64 {
	return agent.FindAvailableModel(c.models, model).GetContextWindow()
}
