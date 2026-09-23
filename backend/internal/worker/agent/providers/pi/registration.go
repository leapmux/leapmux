package pi

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// piBinaryCandidates lists the executable names to probe for Pi.
var piBinaryCandidates = []string{"pi"}

// piLocator finds the Pi CLI on the user's PATH, the preferred name first.
var piLocator = launch.Binaries(piBinaryCandidates...)

// Registration states everything the worker knows about Pi before any of its
// agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		Plugin:        piProvider{},
		Start:         Start,
		Locator:       piLocator,
		DefaultModels: piDefaultModels,
		// No static option groups; thinking levels live on each model.
		OptionGroups: nil,
		// Pi's model-dependent group is its thinking level, labeled "Thinking Level"
		// rather than the default "Effort" -- so the not-running static fallback and the
		// model-switch sub_groups match the live OptionGroups (see Agent.OptionGroups).
		ModelSubGroups: agent.EffortSubGroupsLabeled(ThinkingLevelLabel),
		// model + effort (the "Thinking Level" axis). Pi has no permission-mode axis.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// pi_provider (the underlying LLM provider Pi folds into its model selection) is
		// persisted by LeapMux but never surfaced as a group, so its absence from a confirmed
		// catalog is by design -- confirmedOptions preserves it rather than reconciling it away.
		PersistedOnlyOptionIDs: []string{OptionProvider},
		EnvModelKey:            "LEAPMUX_PI_DEFAULT_MODEL",
		EnvEffortKey:           "LEAPMUX_PI_DEFAULT_EFFORT",
	}
}
