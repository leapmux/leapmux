package letta

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// lettaLocator finds the `letta` program on the user's PATH.
var lettaLocator = launch.Binaries(lettaBinaryName)

// PermissionModeLabel is the label of LeapMux's permission-mode axis for Letta
// Code. The mode maps onto `runtime_start.mode`.
const PermissionModeLabel = "Permissions"

// permissionModeGroup is the static option group of the permission mode.
var permissionModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDPermissionMode,
	Label:        PermissionModeLabel,
	DefaultValue: contracts.LettaModeStandard,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.LettaModeStrict, Name: "Strict", Description: "Ask before every tool that changes something"},
		{Id: contracts.LettaModeStandard, Name: "Standard", Description: "Ask before the tools Letta marks as needing approval"},
		{Id: contracts.LettaModeAcceptEdits, Name: "Accept Edits", Description: "Auto-approve file edits"},
		{Id: contracts.LettaModeUnrestricted, Name: "Unrestricted", Description: "Auto-approve every tool"},
	},
}

// staticOptionGroups holds the option groups that do not depend on a running
// agent. The model catalog is discovered at startup (settings.go).
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{permissionModeGroup}

// Registration states everything the worker knows about Letta Code before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_LETTA,
		Plugin:   lettaProvider{},
		Start:    Start,
		Locator:  lettaLocator,
		// The running server reports the models its provider configuration
		// exposes (settings.go).
		DefaultModels:       defaultModels,
		OptionGroups:        staticOptionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// A new session, and a session that stored no mode, run Standard:
		// Letta's own default, which asks before the tools it marks.
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.LettaModeStandard},
			Fallback:   contracts.LettaModeStandard,
		},
		// LeapMux states the four modes completely.
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_LETTA_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_LETTA_DEFAULT_EFFORT",
	}
}
