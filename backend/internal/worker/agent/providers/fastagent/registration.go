package fastagent

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// fastagentLocator finds the fast-agent CLI on the user's PATH. The `acp`
// subcommand is what this package drives, of the `fast-agent` entry point; a
// dedicated `fast-agent-acp` console script serves the same surface but takes
// the subcommand's flags directly, so the launch stays on the one binary whose
// argument shape this package states.
var fastagentLocator = launch.Binaries("fast-agent")

const bypassRestartNote = "Changing this setting restarts the agent and stops any active turn."

// OptionNativeCommands selects the native shell instead of the ACP host terminal.
const OptionNativeCommands = "nativeCommands"

var nativeCommandsGroup = agent.SelectGroup(OptionNativeCommands, "Native Commands", agent.OptionOrderProviderSecond, "false", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: bypassRestartNote + " Use the client terminal.", Default: true},
	{Id: "true", Name: "On", Description: bypassRestartNote + " Requires Bypass Permissions because native commands do not request approval. Tool results appear when the turn ends."},
})

var bypassPermissionsGroup = agent.SelectGroup(contracts.FastagentOptionBypassPermissions, "Bypass Permissions", agent.OptionOrderProviderFirst, "false", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: bypassRestartNote + " Use native tool permission rules and prompts.", Default: true},
	{Id: "true", Name: "On", Description: bypassRestartNote + " Skip native tool permission rules and prompts."},
})

// Registration states everything the worker knows about fast-agent before any of
// its agents runs. fast-agent discovers its modes and its one model at session
// creation, so the session supplies the catalog. Its modes are its configured
// agents, and the default setup has one -- the permission-mode axis carries that
// single `agent` mode, reported on the native modes channel. There is no
// live approval switch to pair with it. Bypass uses a native process flag and
// therefore restarts the agent; the mode axis still selects configured agents.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT,
		Plugin:   fastagentProvider{},
		Start:    Start,
		Locator:  fastagentLocator,
		// Models are whatever the launch `--model` names; fast-agent routes the
		// string through its own model database and reports no catalog.
		DefaultModels: nil,
		// The session supplies modes and config options; this group describes the native launch policy.
		OptionGroups:        []*leapmuxv1.AvailableOptionGroup{bypassPermissionsGroup, nativeCommandsGroup},
		AdditionalOptionIDs: []string{agent.OptionIDPermissionMode},
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.FastagentModeAgent,
		},
		EnvModelKey:  "LEAPMUX_FAST_AGENT_DEFAULT_MODEL",
		EnvEffortKey: "",
	}
}
