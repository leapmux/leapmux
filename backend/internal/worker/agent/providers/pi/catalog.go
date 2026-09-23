package pi

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piAutoEffort is the LeapMux-side sentinel: when selected we omit the
// set_thinking_level RPC and let Pi keep its current level (typically driven
// by ~/.pi/agent/settings.json).
var piAutoEffort = &agent.EffortInfo{
	Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Use Pi's configured default thinking level",
}

// piDefaultEfforts is the static fallback list of thinking levels surfaced to
// the UI before get_available_models populates per-model SupportedEfforts.
var piDefaultEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingXHigh),
	providerkit.EffortTier(ThinkingHigh),
	providerkit.EffortTier(ThinkingMedium),
	providerkit.EffortTier(ThinkingLow),
	providerkit.EffortTier(ThinkingMinimal),
	providerkit.EffortTier(ThinkingOff),
}

// piNonReasoningEfforts is the trimmed effort list for models that don't
// support reasoning — only Auto and Off make sense.
var piNonReasoningEfforts = []*agent.EffortInfo{
	piAutoEffort,
	providerkit.EffortTier(ThinkingOff),
}

// piDefaultModels is the static fallback model list used until the Pi process
// answers get_available_models. The single entry mirrors the user's configured
// default; the runtime catalog supersedes this.
var piDefaultModels = []*agent.ModelInfo{
	{
		Id:               DefaultModel,
		DisplayName:      "GLM-5.3",
		Description:      "Default Pi model (overridden once Pi reports its catalog)",
		IsDefault:        true,
		DefaultEffort:    DefaultThinkingLevel,
		SupportedEfforts: piDefaultEfforts,
	},
}
