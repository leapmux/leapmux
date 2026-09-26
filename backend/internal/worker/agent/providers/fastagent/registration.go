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

// Registration states everything the worker knows about fast-agent before any of
// its agents runs. fast-agent discovers its modes and its one model at session
// creation, so the session supplies the catalog. Its modes are its configured
// agents, and the default setup has one -- the permission-mode axis carries that
// single `agent` mode, reported on the native modes channel. There is no
// per-session approval switch to pair with it: the launch passes
// `--no-permissions`, and the local filesystem tools bypass the permission
// system entirely.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT,
		Plugin:   fastagentProvider{},
		Start:    Start,
		Locator:  fastagentLocator,
		// Models are whatever the launch `--model` names; fast-agent routes the
		// string through its own model database and reports no catalog.
		DefaultModels: nil,
		// The session supplies available modes and config options.
		OptionGroups:        nil,
		AdditionalOptionIDs: []string{agent.OptionIDPermissionMode},
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.FastagentModeAgent,
		},
		EnvModelKey:  "LEAPMUX_FAST_AGENT_DEFAULT_MODEL",
		EnvEffortKey: "",
	}
}
