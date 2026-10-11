package cline

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Cline's model catalog.
//
// Cline sends no catalog on the hub: the daemon answers `catalog.list` with
// `unsupported_command`, and Cline compiles the provider catalogs into the
// binary. So the worker keeps a static table of the providers that most users
// sign in to, taken from Cline's own generated catalog
// (sdk/packages/llms/src/catalog/catalog.generated.ts and
// cline-recommended.generated.ts, as of Cline 3.0.64). A session always offers
// the model that the user's Cline settings select for the provider, whether the
// table lists it or not, so a provider the table does not hold still runs on
// the user's own model.
//
// A LeapMux model id is Cline's model id within the session's provider. The
// provider is the one that the user's Cline settings last used; LeapMux does not
// switch it (see settings.go).

// catalogEntry is one model of the static table.
type catalogEntry struct {
	// provider is Cline's provider id, as providers.json states it.
	provider string
	// id is Cline's model id within the provider.
	id   string
	name string
	// contextWindow is the model's context window in tokens, or 0 when Cline's
	// catalog states none.
	contextWindow int64
	// efforts are the reasoning efforts the model takes, in Cline's words. `none`
	// turns the reasoning off. Empty for a model with no effort ladder.
	efforts []string
}

// staticCatalog is the table. The rows of one provider keep the order of
// Cline's own catalog, newest first.
var staticCatalog = []catalogEntry{
	{provider: "cline", id: "spacexai/grok-4.7", name: "Grok 4.7", contextWindow: 500000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{provider: "cline", id: "openai/gpt-6-astra", name: "GPT-6 Astra", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "cline", id: "moonshotai/kimi-k3", name: "Kimi K3", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "cline", id: "anthropic/claude-opus-5", name: "Claude Opus 5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "cline", id: "stealth/space-bunny-alpha", name: "Space Bunny Alpha (free)", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "cline", id: "cline-free/mimo-v2.6-flash", name: "MiMo-V2.6-Flash (free)", contextWindow: 1048576, efforts: nil},
	{provider: "cline", id: "cline-free/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash (free)", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "cline", id: "cline-free/muse-spark-1.3-contributor", name: "Muse Spark 1.3 Contributor (free)", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
	{provider: "cline-pass", id: "cline-pass/mimo-v2.6-flash", name: "MiMo-V2.6-Flash", contextWindow: 1048576, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/mimo-v2.6-pro", name: "MiMo-V2.6-Pro", contextWindow: 1048576, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/glm-5.3", name: "GLM-5.3", contextWindow: 1310720, efforts: []string{"low", "high", "max"}},
	{provider: "cline-pass", id: "cline-pass/deepseek-v4-pro", name: "DeepSeek V4 Pro", contextWindow: 1048576, efforts: []string{"high", "xhigh"}},
	{provider: "cline-pass", id: "cline-pass/qwen3.8-max", name: "cline-pass/qwen3.8-max", contextWindow: 128000, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "cline-pass", id: "cline-pass/muse-spark-1.3-contributor", name: "Muse Spark 1.3 Contributor", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
	{provider: "cline-pass", id: "cline-pass/kimi-k3", name: "Kimi K3", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "cline-pass", id: "cline-pass/glm-5.3-flash", name: "GLM-5.3-Flash", contextWindow: 1310720, efforts: []string{"low", "high", "max"}},
	{provider: "cline-pass", id: "cline-pass/minimax-m3", name: "MiniMax-M3", contextWindow: 1048576, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/qwen3.7-max", name: "Qwen3.7 Max", contextWindow: 1000000, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/qwen3.7-plus", name: "Qwen3.7 Plus", contextWindow: 1000000, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/mimo-v2.5-pro", name: "MiMo-V2.5-Pro", contextWindow: 1050000, efforts: nil},
	{provider: "cline-pass", id: "cline-pass/mimo-v2.5", name: "MiMo-V2.5", contextWindow: 1050000, efforts: nil},
	{provider: "cline-pass", id: "stealth/space-bunny-alpha", name: "Space Bunny Alpha (free)", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "cline-pass", id: "cline-free/mimo-v2.6-flash", name: "MiMo-V2.6-Flash (free)", contextWindow: 1048576, efforts: nil},
	{provider: "cline-pass", id: "cline-free/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash (free)", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "cline-pass", id: "cline-free/muse-spark-1.3-contributor", name: "Muse Spark 1.3 Contributor (free)", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-opus-5-5", name: "Claude Opus 5.5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-fable-5-1", name: "Claude Fable 5.1", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-opus-5", name: "Claude Opus 5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-sonnet-5", name: "Claude Sonnet 5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-fable-5", name: "Claude Fable 5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-opus-4-8", name: "Claude Opus 4.8", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-opus-4-7", name: "Claude Opus 4.7", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "anthropic", id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "max"}},
	{provider: "anthropic", id: "claude-opus-4-6", name: "Claude Opus 4.6", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "max"}},
	{provider: "anthropic", id: "claude-opus-4-5", name: "Claude Opus 4.5 (latest)", contextWindow: 200000, efforts: []string{"low", "medium", "high"}},
	{provider: "anthropic", id: "claude-opus-4-5-20251101", name: "Claude Opus 4.5", contextWindow: 200000, efforts: []string{"low", "medium", "high"}},
	{provider: "anthropic", id: "claude-haiku-4-5", name: "Claude Haiku 4.5 (latest)", contextWindow: 200000, efforts: nil},
	{provider: "anthropic", id: "claude-haiku-4-5-20251001", name: "Claude Haiku 4.5", contextWindow: 200000, efforts: nil},
	{provider: "anthropic", id: "claude-sonnet-4-5", name: "Claude Sonnet 4.5 (latest)", contextWindow: 1000000, efforts: nil},
	{provider: "anthropic", id: "claude-sonnet-4-5-20250929", name: "Claude Sonnet 4.5", contextWindow: 1000000, efforts: nil},
	{provider: "openai-native", id: "gpt-6-luna", name: "GPT-6 Luna", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-6-sol", name: "GPT-6 Sol", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-6-astra", name: "GPT-6 Astra", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-5.6", name: "GPT-5.6", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-5.6-luna", name: "GPT-5.6 Luna", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-5.6-sol", name: "GPT-5.6 Sol", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{provider: "openai-native", id: "gpt-5.5", name: "GPT-5.5", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.5-pro", name: "GPT-5.5 Pro", contextWindow: 1050000, efforts: []string{"medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.4-mini", name: "GPT-5.4 mini", contextWindow: 400000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.4-nano", name: "GPT-5.4 nano", contextWindow: 400000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.4", name: "GPT-5.4", contextWindow: 1050000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.4-pro", name: "GPT-5.4 Pro", contextWindow: 1050000, efforts: []string{"medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.3-codex", name: "GPT-5.3 Codex", contextWindow: 400000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.3-codex-spark", name: "GPT-5.3 Codex Spark", contextWindow: 128000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.2", name: "GPT-5.2", contextWindow: 400000, efforts: []string{"none", "low", "medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.2-pro", name: "GPT-5.2 Pro", contextWindow: 400000, efforts: []string{"medium", "high", "xhigh"}},
	{provider: "openai-native", id: "gpt-5.1", name: "GPT-5.1", contextWindow: 400000, efforts: []string{"none", "low", "medium", "high"}},
	{provider: "openai-native", id: "gpt-5-pro", name: "GPT-5 Pro", contextWindow: 400000, efforts: []string{"high"}},
	{provider: "openai-native", id: "gpt-5", name: "GPT-5", contextWindow: 400000, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "openai-native", id: "gpt-5-mini", name: "GPT-5 Mini", contextWindow: 400000, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "openai-native", id: "gpt-5-nano", name: "GPT-5 Nano", contextWindow: 400000, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "openai-native", id: "o3-pro", name: "o3-pro", contextWindow: 200000, efforts: []string{"low", "medium", "high"}},
	{provider: "openai-native", id: "o3", name: "o3", contextWindow: 200000, efforts: []string{"low", "medium", "high"}},
	{provider: "openai-native", id: "gpt-4.1", name: "GPT-4.1", contextWindow: 1047576, efforts: nil},
	{provider: "openai-native", id: "gpt-4.1-mini", name: "GPT-4.1 mini", contextWindow: 1047576, efforts: nil},
	{provider: "openai-native", id: "gpt-4o-2024-11-20", name: "GPT-4o (2024-11-20)", contextWindow: 128000, efforts: nil},
	{provider: "openai-native", id: "gpt-4o-2024-08-06", name: "GPT-4o (2024-08-06)", contextWindow: 128000, efforts: nil},
	{provider: "openai-native", id: "gpt-4o-mini", name: "GPT-4o mini", contextWindow: 128000, efforts: nil},
	{provider: "openai-native", id: "gpt-4o", name: "GPT-4o", contextWindow: 128000, efforts: nil},
	{provider: "gemini", id: "gemini-3.8-flash", name: "Gemini 3.8 Flash", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.7-flash", name: "Gemini 3.7 Flash", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{provider: "gemini", id: "gemini-flash-latest", name: "Gemini Flash Latest", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.5-flash-lite", name: "Gemini 3.5 Flash Lite", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.6-flash", name: "Gemini 3.6 Flash", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemini-flash-lite-latest", name: "Gemini Flash-Lite Latest", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.5-flash", name: "Gemini 3.5 Flash", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.1-flash-lite", name: "Gemini 3.1 Flash Lite", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemma-4-26b-a4b-it", name: "Gemma 4 26B A4B IT", contextWindow: 262144, efforts: nil},
	{provider: "gemini", id: "gemma-4-31b-it", name: "Gemma 4 31B IT", contextWindow: 262144, efforts: nil},
	{provider: "gemini", id: "gemini-3.1-pro-preview", name: "Gemini 3.1 Pro Preview", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3.1-pro-preview-customtools", name: "Gemini 3.1 Pro Preview Custom Tools", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{provider: "gemini", id: "gemini-3-flash-preview", name: "Gemini 3 Flash Preview", contextWindow: 1048576, efforts: []string{"minimal", "low", "medium", "high"}},
	{provider: "gemini", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash", contextWindow: 1048576, efforts: nil},
	{provider: "gemini", id: "gemini-2.5-flash-lite", name: "Gemini 2.5 Flash-Lite", contextWindow: 1048576, efforts: nil},
	{provider: "gemini", id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", contextWindow: 1048576, efforts: nil},
	{provider: "deepseek", id: "deepseek-flash", name: "DeepSeek V4.1 Flash", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{provider: "deepseek", id: "deepseek-v4-pro", name: "DeepSeek V4 Pro", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{provider: "xai", id: "grok-4.7", name: "Grok 4.7", contextWindow: 500000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{provider: "xai", id: "grok-4.6", name: "Grok 4.6", contextWindow: 500000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{provider: "xai", id: "grok-4.5", name: "Grok 4.5", contextWindow: 500000, efforts: []string{"low", "medium", "high"}},
	{provider: "xai", id: "grok-4.3", name: "Grok 4.3", contextWindow: 1000000, efforts: []string{"none", "low", "medium", "high"}},
	{provider: "xai", id: "grok-build-0.1", name: "Grok Build 0.1", contextWindow: 256000, efforts: nil},
	{provider: "xai", id: "grok-4.20-0309-non-reasoning", name: "Grok 4.20 (Non-Reasoning)", contextWindow: 1000000, efforts: nil},
	{provider: "xai", id: "grok-4.20-0309-reasoning", name: "Grok 4.20 (Reasoning)", contextWindow: 1000000, efforts: nil},
	{provider: "moonshot", id: "kimi-k3", name: "Kimi K3", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{provider: "moonshot", id: "kimi-k2.7-code", name: "Kimi K2.7 Code", contextWindow: 262144, efforts: nil},
	{provider: "moonshot", id: "kimi-k2.7-code-highspeed", name: "Kimi K2.7 Code HighSpeed", contextWindow: 262144, efforts: nil},
	{provider: "moonshot", id: "kimi-k2.6", name: "Kimi K2.6", contextWindow: 262144, efforts: nil},
}

// fallbackModel is the model Cline's own CLI runs when the provider settings
// state none and the catalog lists none either.
const fallbackModel = "anthropic/claude-sonnet-4.6"

// clineAutoEffort is the entry that sends no reasoning setting at all, which
// leaves the model on the reasoning that the user's Cline settings state.
var clineAutoEffort = &agent.EffortInfo{
	Id:          agent.EffortAuto,
	Name:        providerkit.EffortLabel(agent.EffortAuto),
	Description: "Use the reasoning that your Cline settings state for the model",
}

// effortOff is Cline's word for no reasoning. The hub takes it as `thinking:
// false`, because its reasoning-effort field refuses the word.
const effortOff = "none"

// modelEfforts lists the efforts of one model: Auto first, then the model's
// ladder strongest first. A model with no ladder takes no effort at all.
func modelEfforts(efforts []string) []*agent.EffortInfo {
	if len(efforts) == 0 {
		return nil
	}
	levels := make([]*agent.EffortInfo, 0, len(efforts))
	for _, effort := range efforts {
		if effort = strings.TrimSpace(effort); effort != "" {
			levels = append(levels, providerkit.EffortTier(effort))
		}
	}
	if len(levels) == 0 {
		return nil
	}
	providerkit.SortEffortsDescending(levels)
	return append([]*agent.EffortInfo{clineAutoEffort}, levels...)
}

// modelInfo projects one table row.
func (e catalogEntry) modelInfo() *agent.ModelInfo {
	return &agent.ModelInfo{
		Id:               e.id,
		DisplayName:      agent.NameOrID(e.name, e.id),
		DefaultEffort:    agent.EffortAuto,
		SupportedEfforts: modelEfforts(e.efforts),
		ContextWindow:    e.contextWindow,
	}
}

// providerCatalog returns the table's models of one provider, or nil.
func providerCatalog(provider string) []*agent.ModelInfo {
	var models []*agent.ModelInfo
	for _, entry := range staticCatalog {
		if entry.provider == provider {
			models = append(models, entry.modelInfo())
		}
	}
	return models
}

// sessionCatalog returns the models a session of provider offers: the table's
// models of the provider, and the configured model first when the table does
// not list it. The configured model is the default.
func sessionCatalog(provider, configured string) []*agent.ModelInfo {
	models := providerCatalog(provider)
	if configured != "" && !slices.ContainsFunc(models, func(m *agent.ModelInfo) bool { return m.Id == configured }) {
		models = append([]*agent.ModelInfo{{
			Id:            configured,
			DisplayName:   configured,
			DefaultEffort: agent.EffortAuto,
		}}, models...)
	}
	for _, m := range models {
		m.IsDefault = m.Id == configured
	}
	return models
}

// defaultModelFor is the model a session of provider runs when the user's
// Cline settings state none: the table's first model of the provider, and else
// Cline's own fallback, as Cline's CLI chooses.
func defaultModelFor(provider string) string {
	for _, entry := range staticCatalog {
		if entry.provider == provider {
			return entry.id
		}
	}
	return fallbackModel
}

// defaultModels is the registration's catalog: the one entry that means "the
// provider and model that your Cline settings select". A running agent reports
// the concrete catalog of its provider.
var defaultModels = []*agent.ModelInfo{
	agent.AccountDefaultModelEntry("The provider and the model that your Cline settings select"),
}
