package antigravity

import (
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var effortSuffixes = []string{"xhigh", "medium", "high", "low", "max"}

var antigravityAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use Antigravity's default reasoning effort for the model",
}

type antigravityCatalogReply struct {
	ClientModelConfigs         []antigravityModelConfig `json:"clientModelConfigs"`
	DefaultOverrideModelConfig *antigravityModelConfig  `json:"defaultOverrideModelConfig"`
}

type antigravityModelConfig struct {
	Label        string `json:"label"`
	ModelID      string `json:"modelId"`
	ModelOrAlias struct {
		Model string `json:"model"`
	} `json:"modelOrAlias"`
	Disabled           bool            `json:"disabled"`
	SupportedMimeTypes map[string]bool `json:"supportedMimeTypes"`
}

type catalogVariant struct {
	token     string
	mimeTypes map[string]bool
}

type catalogFamily struct {
	variants      map[string]catalogVariant
	defaultEffort string
}

func (f catalogFamily) hasEfforts() bool {
	for effort := range f.variants {
		if effort != "" {
			return true
		}
	}
	return false
}

type antigravityCatalog struct {
	models          []*agent.ModelInfo
	families        map[string]catalogFamily
	defaultModel    string
	fallbackDefault catalogVariant
}

func buildAntigravityCatalog(reply antigravityCatalogReply) antigravityCatalog {
	catalog := antigravityCatalog{
		families: map[string]catalogFamily{},
	}
	familiesInfo := map[string]*agent.ModelInfo{}
	var familyOrder []string
	for _, config := range reply.ClientModelConfigs {
		if config.Disabled || config.ModelID == "" || config.ModelOrAlias.Model == "" {
			continue
		}
		family, effort := splitModelEffort(config.ModelID)
		fam, seenFamily := catalog.families[family]
		if !seenFamily {
			fam = catalogFamily{variants: map[string]catalogVariant{}, defaultEffort: effort}
			familiesInfo[family] = &agent.ModelInfo{Id: family, DisplayName: modelFamilyLabel(config.Label, effort), Description: family}
			familyOrder = append(familyOrder, family)
		}
		_, exists := fam.variants[effort]
		fam.variants[effort] = catalogVariant{token: config.ModelOrAlias.Model, mimeTypes: config.SupportedMimeTypes}
		catalog.families[family] = fam
		if effort != "" && !exists {
			familiesInfo[family].SupportedEfforts = append(familiesInfo[family].SupportedEfforts, providerkit.EffortTier(effort))
		}
	}
	for _, id := range familyOrder {
		info := familiesInfo[id]
		if len(info.SupportedEfforts) > 0 {
			providerkit.SortEffortsDescending(info.SupportedEfforts)
			info.SupportedEfforts = append([]*agent.EffortInfo{antigravityAutoEffort}, info.SupportedEfforts...)
			info.DefaultEffort = agent.EffortAuto
		}
	}
	if override := reply.DefaultOverrideModelConfig; override != nil && !override.Disabled {
		matchedFamily, matchedEffort, matched := "", "", false
		if override.ModelOrAlias.Model != "" {
			for _, config := range reply.ClientModelConfigs {
				if config.Disabled || config.ModelID == "" || config.ModelOrAlias.Model == "" {
					continue
				}
				family, effort := splitModelEffort(config.ModelID)
				if catalog.families[family].variants[effort].token == override.ModelOrAlias.Model {
					matchedFamily, matchedEffort, matched = family, effort, true
					break
				}
			}
		}
		if !matched && override.ModelID != "" {
			family, effort := splitModelEffort(override.ModelID)
			if catalog.families[family].variants[effort].token != "" {
				matchedFamily, matchedEffort, matched = family, effort, true
			}
		}
		if matched {
			catalog.defaultModel = matchedFamily
			fam := catalog.families[matchedFamily]
			fam.defaultEffort = matchedEffort
			catalog.families[matchedFamily] = fam
			familiesInfo[matchedFamily].IsDefault = true
		} else if override.ModelOrAlias.Model != "" {
			catalog.fallbackDefault = catalogVariant{token: override.ModelOrAlias.Model, mimeTypes: override.SupportedMimeTypes}
		}
	}
	if catalog.defaultModel == "" && catalog.fallbackDefault.token == "" && len(familyOrder) > 0 {
		catalog.defaultModel = familyOrder[0]
		familiesInfo[familyOrder[0]].IsDefault = true
	}
	if catalog.defaultModel == "" {
		catalog.models = append(catalog.models, agent.AccountDefaultModelEntry("Use the model that Antigravity selects"))
	}
	for _, id := range familyOrder {
		catalog.models = append(catalog.models, familiesInfo[id])
	}
	return catalog
}

func splitModelEffort(modelID string) (string, string) {
	for _, suffix := range effortSuffixes {
		if strings.HasSuffix(modelID, "-"+suffix) {
			return strings.TrimSuffix(modelID, "-"+suffix), suffix
		}
	}
	return modelID, ""
}

func modelFamilyLabel(label, effort string) string {
	if start := strings.LastIndex(label, " ("); start > 0 && effort != "" && strings.EqualFold(label[start+2:], effort+")") {
		return label[:start]
	}
	return label
}

func (c antigravityCatalog) resolveModel(model, effort string) (catalogVariant, string, error) {
	if agent.UsesAccountDefaultModel(model) {
		if c.defaultModel != "" {
			return c.resolveModel(c.defaultModel, effort)
		}
		if c.fallbackDefault.token == "" {
			return catalogVariant{}, "", fmt.Errorf("the Antigravity catalog has no default model")
		}
		return c.fallbackDefault, "", nil
	}
	family, ok := c.families[model]
	if !ok || len(family.variants) == 0 {
		return catalogVariant{}, "", fmt.Errorf("%s is not in the Antigravity catalog", model)
	}
	wireEffort, settled := settleAntigravityEffort(family, effort)
	variant := family.variants[wireEffort]
	if variant.token == "" {
		return catalogVariant{}, "", fmt.Errorf("%s is not in the Antigravity catalog", model)
	}
	return variant, settled, nil
}

func (c antigravityCatalog) settleSelection(model, effort string) (string, string) {
	if agent.UsesAccountDefaultModel(model) {
		if c.defaultModel != "" {
			model = c.defaultModel
		} else {
			model = agent.DefaultModelSentinel
		}
	}
	if _, settled, err := c.resolveModel(model, effort); err == nil {
		return model, settled
	}
	if c.defaultModel != "" {
		if _, settled, err := c.resolveModel(c.defaultModel, agent.EffortAuto); err == nil {
			return c.defaultModel, settled
		}
	}
	return agent.DefaultModelSentinel, ""
}

func settleAntigravityEffort(family catalogFamily, requested string) (string, string) {
	if !family.hasEfforts() {
		return family.defaultEffort, ""
	}
	if requested == "" || requested == agent.EffortAuto {
		return family.defaultEffort, agent.EffortAuto
	}
	if _, ok := family.variants[requested]; ok {
		return requested, requested
	}
	requestedRank, ok := providerkit.EffortRankOf(requested)
	if !ok {
		return family.defaultEffort, agent.EffortAuto
	}
	best := ""
	bestDistance := math.MaxInt
	bestRank := math.MaxInt
	for effort := range family.variants {
		rank, ranked := providerkit.EffortRankOf(effort)
		if !ranked {
			continue
		}
		distance := int(math.Abs(float64(rank - requestedRank)))
		if distance < bestDistance || distance == bestDistance && rank < bestRank || distance == bestDistance && rank == bestRank && effort < best {
			best, bestDistance, bestRank = effort, distance, rank
		}
	}
	if best == "" {
		return family.defaultEffort, agent.EffortAuto
	}
	return best, best
}

func (a *Agent) loadCatalog() error {
	reply, err := a.rpc.modelConfig(a.Context())
	if err != nil {
		return err
	}
	catalog := buildAntigravityCatalog(reply)
	a.Mu.Lock()
	a.catalog = catalog
	a.model, a.effort = catalog.settleSelection(a.model, a.effort)
	a.Mu.Unlock()
	return nil
}

func (a *Agent) refreshAccountCatalog() {
	ctx, generation, current := a.accountRefreshContext()
	if !current {
		return
	}
	reply, err := a.rpc.modelConfig(ctx)
	if err != nil {
		slog.Debug("antigravity refresh model catalog", "agent_id", a.AgentID(), "error", err)
		return
	}
	catalog := buildAntigravityCatalog(reply)
	if len(catalog.families) == 0 && catalog.fallbackDefault.token == "" {
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Mu.Lock()
	if !a.accountRefreshCurrentLocked(ctx, generation) {
		a.Mu.Unlock()
		return
	}
	model, effort := catalog.settleSelection(a.model, a.effort)
	changed := model != a.model || effort != a.effort || !agent.ModelInfosEqual(a.catalog.models, catalog.models)
	a.catalog, a.model, a.effort = catalog, model, effort
	a.Mu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(optionmap.Map{agent.OptionIDModel: model, agent.OptionIDEffort: effort})
	}
}
