package junie

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// fallbackJunieModes lists Junie's session modes in Junie's own order. The
// static fallback and every rebuilt list derive from this one value.
func fallbackJunieModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.JunieModeDefault, Name: "Default"},
		{Id: contracts.JunieModePlan, Name: "Plan"},
	}
}

// junieStaticOptionGroups holds Junie's static default/plan mode group. The
// factory registration and Start both read this one value.
var junieStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackJunieModes())

// junieLocator finds the Junie CLI on the user's PATH.
var junieLocator = launch.Binaries("junie")

// Registration states everything the worker knows about Junie before any of
// its agents runs. Junie reports its live modes, models and config options on
// the session; the static group is the fallback for a session that has not
// reported yet. The effort axis is the well-known `effort` id.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_JUNIE,
		Plugin:   junieProvider{},
		Start:    Start,
		Locator:  junieLocator,
		// Models come from Junie's custom-model profiles and its account; the
		// session reports the catalog on the `model` config option under a
		// decorated wire id, which NormalizeModelID folds back to the profile id.
		DefaultModels:    nil,
		OptionGroups:     junieStaticOptionGroups,
		NormalizeModelID: normalizeJunieModelID,
		AdditionalOptionIDs: []string{
			agent.OptionIDPermissionMode,
			agent.OptionIDEffort,
			junieConfigBraveMode,
		},
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.JunieModeDefault,
		},
		EnvModelKey:  "LEAPMUX_JUNIE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_JUNIE_DEFAULT_EFFORT",
	}
}

// junieConfigBraveMode is the config-option id of Junie's shell-safety axis.
// Junie owns the id and its three values (`brave-auto`, `on`, `off`); the
// worker lists it so an incoming settings map can carry it, and forwards the
// value without reading it.
const junieConfigBraveMode = "brave_mode"
