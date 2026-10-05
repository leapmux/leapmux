package commandcode

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

var commandcodeLocator = launch.Binaries("command-code", "commandcode", "cmdc")

var permissionModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id: agent.OptionIDPermissionMode, Label: "Permissions", DefaultValue: contracts.CommandCodePermissionModeDefault,
	Mutable: true, Order: agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.CommandCodePermissionModeDefault, Name: "Default", Description: "Run headless tools with native restrictions"},
		{Id: contracts.CommandCodePermissionModePlan, Name: "Plan", Description: "Plan with the native read-only tool set"},
		{Id: contracts.CommandCodePermissionModeBypass, Name: "Bypass", Description: "Run tools without permission prompts"},
	},
}

// Registration states the native launch metadata and safe default permission mode.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_COMMAND_CODE,
		Plugin:   commandcodeProvider{}, Start: Start, Locator: commandcodeLocator,
		DefaultModels:       registrationModels(),
		OptionGroups:        []*leapmuxv1.AvailableOptionGroup{permissionModeGroup},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// The effort tiers belong to the model, and CMD_LOCAL_ONLY empties the
		// static gateway catalog, so DefaultModels cannot carry this answer.
		ManagesEffort: true,
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.CommandCodePermissionModeDefault},
			Fallback:   contracts.CommandCodePermissionModeDefault,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_COMMANDCODE_DEFAULT_MODEL", EnvEffortKey: "LEAPMUX_COMMANDCODE_DEFAULT_EFFORT",
	}
}
