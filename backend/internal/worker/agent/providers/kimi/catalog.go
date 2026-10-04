package kimi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's model catalog.
//
// The catalog is the user's own: `config.toml` lists the providers and models,
// and a Kimi account adds its managed models. GET /models reports them, each with
// the thinking levels it takes, and GET /config states the configured default.
// Nothing here is static, so the registration carries no model catalog.

// kimiModelItem is one entry of GET /models.
type kimiModelItem struct {
	Model          string   `json:"model"`
	DisplayName    string   `json:"display_name"`
	MaxContextSize int64    `json:"max_context_size"`
	Capabilities   []string `json:"capabilities"`
	SupportEfforts []string `json:"support_efforts"`
	DefaultEffort  string   `json:"default_effort"`
}

// kimiCatalog is the models the server reported and the configured defaults.
type kimiCatalog struct {
	models       []*agent.ModelInfo
	defaultModel string
	// thinking is the configuration's `[thinking]` table: the level a session
	// that states none runs on.
	thinking kimiThinkingDefaults
	// imageModels holds the models that take image input.
	imageModels map[string]bool
}

// kimiThinkingDefaults is the `thinking` object of GET /config.
type kimiThinkingDefaults struct {
	Enabled *bool  `json:"enabled"`
	Effort  string `json:"effort"`
}

// takesImages reports whether model accepts image input. An unknown model reads
// as no: the server would drop an image it cannot send.
func (c kimiCatalog) takesImages(model string) bool {
	return c.imageModels[model]
}

// has reports whether the catalog lists model.
func (c kimiCatalog) has(model string) bool {
	return slices.ContainsFunc(c.models, func(m *agent.ModelInfo) bool { return m.GetId() == model })
}

// model returns the catalog entry of id, or nil.
func (c kimiCatalog) model(id string) *agent.ModelInfo {
	for _, m := range c.models {
		if m.GetId() == id {
			return m
		}
	}
	return nil
}

// kimiAutoEffort is the entry that sends no thinking level at all, which leaves
// the model on the thinking the user's Kimi configuration states.
var kimiAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use Kimi Code's configured thinking for the model",
}

// kimiModelEfforts lists the thinking levels one model takes: Auto first, then
// the model's effort ladder strongest first, or `on` for a model that has none,
// then `off` unless the model always thinks. A model that cannot think takes no
// level at all.
func kimiModelEfforts(item kimiModelItem) []*agent.EffortInfo {
	thinks := slices.Contains(item.Capabilities, kimiCapabilityThinking)
	always := slices.Contains(item.Capabilities, kimiCapabilityAlwaysThinking)
	if !thinks && !always && len(item.SupportEfforts) == 0 {
		return nil
	}
	levels := make([]*agent.EffortInfo, 0, len(item.SupportEfforts))
	for _, effort := range item.SupportEfforts {
		if effort = strings.TrimSpace(effort); effort != "" && effort != kimiThinkingOff && effort != kimiThinkingOn {
			levels = append(levels, providerkit.EffortTier(effort))
		}
	}
	providerkit.SortEffortsDescending(levels)
	out := make([]*agent.EffortInfo, 0, len(levels)+3)
	out = append(out, kimiAutoEffort)
	out = append(out, levels...)
	if len(levels) == 0 {
		out = append(out, providerkit.EffortTier(kimiThinkingOn))
	}
	if !always {
		out = append(out, providerkit.EffortTier(kimiThinkingOff))
	}
	return out
}

// buildKimiCatalog projects the server's model list.
func buildKimiCatalog(items []kimiModelItem, defaultModel string) kimiCatalog {
	catalog := kimiCatalog{imageModels: make(map[string]bool)}
	for _, item := range items {
		id := strings.TrimSpace(item.Model)
		if id == "" {
			continue
		}
		catalog.models = append(catalog.models, &agent.ModelInfo{
			Id:               id,
			DisplayName:      agent.NameOrID(strings.TrimSpace(item.DisplayName), id),
			IsDefault:        id == defaultModel,
			DefaultEffort:    agent.EffortAuto,
			SupportedEfforts: kimiModelEfforts(item),
			ContextWindow:    item.MaxContextSize,
		})
		if slices.Contains(item.Capabilities, kimiCapabilityImageIn) {
			catalog.imageModels[id] = true
		}
	}
	if catalog.has(defaultModel) {
		catalog.defaultModel = defaultModel
	}
	return catalog
}

// loadKimiCatalog reads the model list and the configured default.
func (a *Agent) loadKimiCatalog(ctx context.Context) (kimiCatalog, error) {
	var models struct {
		Items []kimiModelItem `json:"items"`
	}
	if err := a.api.get(ctx, kimiRouteModels, &models); err != nil {
		return kimiCatalog{}, err
	}
	var config struct {
		DefaultModel string          `json:"default_model"`
		Thinking     json.RawMessage `json:"thinking"`
	}
	if err := a.api.get(ctx, kimiRouteConfig, &config); err != nil {
		return kimiCatalog{}, err
	}
	catalog := buildKimiCatalog(models.Items, strings.TrimSpace(config.DefaultModel))
	if len(config.Thinking) > 0 {
		// The table is free-form in the server's schema. One the worker cannot
		// read leaves Auto on the model's own default.
		if err := json.Unmarshal(config.Thinking, &catalog.thinking); err != nil {
			catalog.thinking = kimiThinkingDefaults{}
		}
	}
	return catalog, nil
}

// launchModel chooses the model a session runs: the one the launch asked for
// when the catalog lists it, else the configured default, else the first model.
// The server binds no model to a new session, so an empty answer means the
// first prompt fails; the start refuses that case instead.
func (c kimiCatalog) launchModel(requested string) string {
	if requested != "" && c.has(requested) {
		return requested
	}
	if c.defaultModel != "" {
		return c.defaultModel
	}
	if len(c.models) > 0 {
		return c.models[0].Id
	}
	return ""
}
