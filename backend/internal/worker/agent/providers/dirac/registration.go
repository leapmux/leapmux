package dirac

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// fallbackDiracModes lists Dirac's session modes in Dirac's own order. The
// static fallback and every rebuilt list derive from this one value.
func fallbackDiracModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.DiracModePlan, Name: "Plan"},
		{Id: contracts.DiracModeAct, Name: "Act"},
	}
}

// diracStaticOptionGroups holds Dirac's static plan/act mode group. The
// factory registration and Start both read this one value.
var diracStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackDiracModes())

// diracLocator finds the Dirac CLI on the user's PATH.
var diracLocator = launch.Binaries("dirac")

// Registration states everything the worker knows about Dirac before any of
// its agents runs. Dirac discovers its live modes and config options at
// session creation; the static group is the fallback for a session that has
// not reported yet. The reasoning axis is Dirac's own `reasoning_effort`
// config option, not the well-known `effort` id.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
		Plugin:   diracProvider{},
		Start:    Start,
		Locator:  diracLocator,
		// Models come from the provider catalog the DIRAC_* environment
		// selects; Dirac reports them on the session's config options.
		DefaultModels: nil,
		OptionGroups:  diracStaticOptionGroups,
		AdditionalOptionIDs: []string{
			contracts.DiracConfigReasoningEffort,
			agent.OptionIDPermissionMode,
			agent.OptionIDEffort,
		},
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.DiracModeAct,
		},
		EnvModelKey:  "LEAPMUX_DIRAC_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_DIRAC_DEFAULT_EFFORT",
	}
}
