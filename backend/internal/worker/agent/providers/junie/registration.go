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

// Registration states what the worker knows about Junie before an agent starts.
// Junie reports these catalogs on the session:
//   - Modes.
//   - Models.
//   - Config options.
//
// The static group supplies the fallback until the session reports its catalog.
// The effort axis uses the well-known `effort` ID.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_JUNIE,
		Plugin:   junieProvider{},
		Start:    Start,
		Locator:  junieLocator,
		// Models come from Junie's custom-model profiles and its account.
		// The session reports the catalog through the `model` config option with decorated wire IDs.
		// NormalizeModelID converts each decorated wire ID to its profile ID.
		DefaultModels:    nil,
		OptionGroups:     junieStaticOptionGroups,
		NormalizeModelID: normalizeJunieModelID,
		AdditionalOptionIDs: []string{
			agent.OptionIDPermissionMode,
			agent.OptionIDEffort,
			// The worker recognizes this ID but forwards its values without interpretation.
			contracts.JunieConfigBraveMode,
		},
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.JunieModeDefault,
		},
		EnvModelKey:  "LEAPMUX_JUNIE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_JUNIE_DEFAULT_EFFORT",
	}
}
