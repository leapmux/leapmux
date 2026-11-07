package antigravity

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var effortSuffixes = []string{"xhigh", "medium", "high", "low", "max"}

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
	Disabled           bool                  `json:"disabled"`
	SupportedMimeTypes map[string]bool       `json:"supportedMimeTypes"`
	QuotaInfo          *antigravityQuotaInfo `json:"quotaInfo"`
}

type antigravityQuotaInfo struct {
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime"`
}

type catalogVariant struct {
	token     string
	mimeTypes map[string]bool
}

type antigravityCatalog struct {
	models       []*agent.ModelInfo
	variants     map[string]map[string]catalogVariant
	defaultToken string
	quota        []antigravityModelConfig
}

func buildAntigravityCatalog(reply antigravityCatalogReply) antigravityCatalog {
	catalog := antigravityCatalog{
		models:   []*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the model that Antigravity selects")},
		variants: map[string]map[string]catalogVariant{},
	}
	families := map[string]*agent.ModelInfo{}
	var familyOrder []string
	for _, config := range reply.ClientModelConfigs {
		if config.Disabled || config.ModelID == "" || config.ModelOrAlias.Model == "" {
			continue
		}
		family, effort := splitModelEffort(config.ModelID)
		if catalog.variants[family] == nil {
			catalog.variants[family] = map[string]catalogVariant{}
		}
		catalog.variants[family][effort] = catalogVariant{token: config.ModelOrAlias.Model, mimeTypes: config.SupportedMimeTypes}
		if families[family] == nil {
			families[family] = &agent.ModelInfo{Id: family, DisplayName: config.Label, Description: family}
			familyOrder = append(familyOrder, family)
		}
		if effort != "" {
			families[family].SupportedEfforts = append(families[family].SupportedEfforts, providerkit.EffortTier(effort))
		}
		if config.QuotaInfo != nil {
			catalog.quota = append(catalog.quota, config)
		}
	}
	if reply.DefaultOverrideModelConfig != nil && reply.DefaultOverrideModelConfig.ModelOrAlias.Model != "" {
		catalog.defaultToken = reply.DefaultOverrideModelConfig.ModelOrAlias.Model
	}
	for _, id := range familyOrder {
		family := families[id]
		providerkit.SortEffortsDescending(family.SupportedEfforts)
		catalog.models = append(catalog.models, family)
	}
	if catalog.defaultToken == "" {
		for _, config := range reply.ClientModelConfigs {
			if !config.Disabled && config.ModelOrAlias.Model != "" {
				catalog.defaultToken = config.ModelOrAlias.Model
				break
			}
		}
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

func (c antigravityCatalog) resolveModel(model, effort string) (catalogVariant, string, error) {
	if agent.UsesAccountDefaultModel(model) {
		if c.defaultToken == "" {
			return catalogVariant{}, "", fmt.Errorf("the Antigravity catalog has no default model")
		}
		for _, family := range c.variants {
			for _, variant := range family {
				if variant.token == c.defaultToken {
					return variant, "", nil
				}
			}
		}
		return catalogVariant{token: c.defaultToken}, "", nil
	}
	family, ok := c.variants[model]
	if !ok || len(family) == 0 {
		return catalogVariant{}, "", fmt.Errorf("%s is not in the Antigravity catalog", model)
	}
	selected := settleAntigravityEffort(family, effort)
	variant := family[selected]
	if variant.token == "" {
		return catalogVariant{}, "", fmt.Errorf("%s is not in the Antigravity catalog", model)
	}
	return variant, selected, nil
}

func settleAntigravityEffort(family map[string]catalogVariant, requested string) string {
	if _, ok := family[requested]; ok {
		return requested
	}
	if len(family) == 1 {
		for effort := range family {
			return effort
		}
	}
	requestedRank, _ := providerkit.EffortRankOf(requested)
	best := ""
	bestDistance := math.MaxInt
	bestRank := math.MaxInt
	for effort := range family {
		rank, _ := providerkit.EffortRankOf(effort)
		distance := int(math.Abs(float64(rank - requestedRank)))
		if distance < bestDistance || distance == bestDistance && rank < bestRank || distance == bestDistance && rank == bestRank && effort < best {
			best, bestDistance, bestRank = effort, distance, rank
		}
	}
	return best
}

func (a *Agent) loadCatalog() error {
	reply, err := a.rpc.modelConfig(a.Context())
	if err != nil {
		return err
	}
	catalog := buildAntigravityCatalog(reply)
	a.Mu.Lock()
	a.catalog = catalog
	requestedModel, requestedEffort := a.model, a.effort
	if requestedModel == "" {
		requestedModel = agent.DefaultModelSentinel
	}
	_, effort, err := catalog.resolveModel(requestedModel, requestedEffort)
	if err == nil {
		a.effort = effort
	} else {
		a.model = agent.DefaultModelSentinel
		a.effort = ""
	}
	a.Mu.Unlock()
	return nil
}

func (a *Agent) refreshRateLimits() {
	a.Mu.Lock()
	ctx, generation := a.streamCtx, a.generation
	eligible := a.cascadeCurrentLocked(a.root) && !a.IsDiscardingOutput()
	a.Mu.Unlock()
	if !eligible || ctx.Err() != nil {
		return
	}
	reply, err := a.rpc.modelConfig(ctx)
	if err != nil {
		slog.Debug("antigravity refresh model quota", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	if generation != a.generation || !a.cascadeCurrentLocked(a.root) || ctx.Err() != nil || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	catalog := buildAntigravityCatalog(reply)
	if len(reply.ClientModelConfigs) > 0 || reply.DefaultOverrideModelConfig != nil {
		a.catalog = catalog
	} else {
		a.catalog.quota = nil
	}
	quota := catalog.quota
	a.Mu.Unlock()
	values := map[string]any{}
	for _, config := range quota {
		value := map[string]any{
			contracts.RateLimitFieldLabel:       config.Label,
			contracts.RateLimitFieldUtilization: (1 - config.QuotaInfo.RemainingFraction) * 100,
			contracts.RateLimitFieldStatus:      contracts.RateLimitStatusAllowed,
		}
		if config.QuotaInfo.RemainingFraction <= 0 {
			value[contracts.RateLimitFieldStatus] = contracts.RateLimitStatusExceeded
		}
		if config.QuotaInfo.ResetTime != "" {
			if reset, err := time.Parse(time.RFC3339, config.QuotaInfo.ResetTime); err == nil {
				value[contracts.RateLimitFieldResetsAt] = reset.Format(time.RFC3339)
			} else {
				value[contracts.RateLimitFieldResetsAt] = config.QuotaInfo.ResetTime
			}
		}
		values[config.ModelID] = value
	}
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}
