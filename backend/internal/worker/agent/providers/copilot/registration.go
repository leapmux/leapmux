package copilot

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// copilotBinaryName is the CLI this provider launches.
const copilotBinaryName = "copilot"

const copilotOptionSessionMode = contracts.CopilotOptionSessionMode

// copilotNativeModes are the session modes the runtime accepts. Copilot 1.0.83
// refuses every other word, and its own message lists exactly these three.
var copilotNativeModes = []agent.OptionDef{
	{Id: contracts.CopilotModeInteractive, Name: "Agent", Default: true},
	{Id: contracts.CopilotModePlan, Name: "Plan", Description: "Plan the work and ask before it runs."},
	{Id: contracts.CopilotModeAutopilot, Name: "Autopilot", Description: "Work toward the session objective without asking to continue."},
}

// copilotNativePermissionModes are the permission modes the runtime accepts.
//
// The slash command spells the first one `default`, and the remote procedure call
// spells it `manual`. Copilot 1.0.83 refuses `default` at
// `session.permissions.setMode`, so the RPC spelling is the one LeapMux stores.
var copilotNativePermissionModes = []agent.OptionDef{
	{Id: contracts.CopilotPermissionModeManual, Name: "Manual", Default: true, Description: "Ask before each tool call."},
	{Id: contracts.CopilotPermissionModeAssisted, Name: "Assisted", Description: "Approve the calls a safety check finds safe, and ask about the rest."},
	{Id: contracts.CopilotPermissionModeAllowAll, Name: "Allow All", Description: "Approve every tool call without asking."},
}

func copilotSessionModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(copilotOptionSessionMode, "Mode", agent.OptionOrderProviderFirst, current, copilotNativeModes)
}

func copilotPermissionModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, copilotNativePermissionModes)
}

// copilotLocator finds the Copilot CLI on the user's PATH.
var copilotLocator = launch.Binaries(copilotBinaryName)

// Registration states everything the worker knows about GitHub Copilot
// before any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		Plugin:   copilotProvider{},
		Start:    Start,
		Locator:  copilotLocator,
		// The account decides which models exist, so the catalog is read from the
		// open session rather than declared here.
		DefaultModels:       nil,
		OptionGroups:        []*leapmuxv1.AvailableOptionGroup{copilotSessionModeGroup(""), copilotPermissionModeGroup("")},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			// A new session asks for Assisted: it approves what a safety check finds
			// safe and asks about everything else, which is the narrowest mode that
			// does not stop at every read. A RESUMED session keeps the mode it had.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.CopilotPermissionModeAssisted},
			// A session with no stored mode runs Manual, which is the mode the runtime
			// itself starts in.
			Fallback: contracts.CopilotPermissionModeManual,
		},
		// Each model states its own reasoning-effort tiers, and the account decides which
		// models exist -- so the catalog above is nil and only the open session can report
		// them. Without this declaration a model switch would keep the PREVIOUS model's
		// tiers, and an effort the new model refuses would survive the switch.
		ManagesEffort:        true,
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_COPILOT_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_COPILOT_DEFAULT_EFFORT",
	}
}
