package mimo

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// mimoBinaryName is the CLI this provider launches. The Homebrew and npm
// installations both put a Node script under this name, which runs the real
// binary as a child. See Agent.Stop for what that means at shutdown.
const mimoBinaryName = "mimo"

// mimoLocator finds MiMo Code on the user's PATH.
var mimoLocator = launch.Binaries(mimoBinaryName)

// ModeLabel labels the mode axis, which holds MiMo's primary agents.
const ModeLabel = "Mode"

// PermissionPolicyLabel labels the permission-policy axis.
const PermissionPolicyLabel = "Permissions"

// mimoStaticModes are the primary agents that every MiMo server offers. MiMo
// calls them agents; LeapMux carries them on its permission-mode axis, as it
// carries ZCode's plan and build modes, so that plan mode, its Shift+Tab toggle
// and plan approval work through the same machinery as for every provider.
//
// The live list comes from the server (GET /agent), which can add agents, such
// as the deprecated `compose` or the configured `max`. This list is the seed a
// new agent shows before the server answers, so the registration does not set
// FixedPermissionModes: a launch that asks for an agent the server offers must
// not be refused because the seed omits it.
var mimoStaticModes = []agent.OptionDef{
	{Id: contracts.MiMoModeBuild, Name: "Build", Description: "Executes tools based on configured permissions.", Default: true},
	{Id: contracts.MiMoModePlan, Name: "Plan", Description: "Plan mode. Disallows all edit tools."},
}

// mimoPermissionPolicies are LeapMux's names for the pairs of MiMo's two
// permission switches. MiMo has no permission-mode enumeration: it has a
// skip-all switch, which approves every ask except an irreversible delete, and
// an auto-approve-delete switch for those deletes.
var mimoPermissionPolicies = []agent.OptionDef{
	{Id: contracts.MiMoPermissionPolicyAsk, Name: "Ask", Description: "Follow MiMo's permission rules and ask when a rule says to ask.", Default: true},
	{Id: contracts.MiMoPermissionPolicySkip, Name: "Skip", Description: "Approve every tool call without asking, except an irreversible delete."},
	{Id: contracts.MiMoPermissionPolicyBypass, Name: "Bypass", Description: "Approve every tool call without asking, deletes included."},
}

// mimoModeGroup builds the mode group from options and the current value. The
// static fallback and the live group share this builder.
func mimoModeGroup(current string, options []agent.OptionDef) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, ModeLabel, agent.OptionOrderPermissionMode, current, options)
}

// mimoPermissionPolicyGroup builds the permission-policy group. The static
// fallback and the live group share this builder.
func mimoPermissionPolicyGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(contracts.MiMoOptionPermissionPolicy, PermissionPolicyLabel, agent.OptionOrderProviderFirst, current, mimoPermissionPolicies)
}

// Registration states everything the worker knows about MiMo Code before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE,
		Plugin:   mimoProvider{},
		Start:    Start,
		Locator:  mimoLocator,
		// The user's MiMo configuration decides which models exist, so the catalog
		// comes from the running server (GET /config/providers) and not from here.
		DefaultModels: nil,
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			mimoModeGroup("", mimoStaticModes),
			mimoPermissionPolicyGroup(""),
		},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// A new agent follows MiMo's own permission rules, which is how MiMo
		// itself starts. The policy is not a permission mode, so it is seeded here
		// rather than in PermissionDefaults.
		ProviderOptionDefaults: map[string]string{
			contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyAsk,
		},
		PermissionDefaults: agent.PermissionDefaults{
			// A session with no stored mode runs the build agent, which is MiMo's
			// own default agent.
			Fallback: contracts.MiMoDefaultMode,
		},
		// Each model states its own reasoning variants, and the catalog comes from
		// the server. Without this a model switch would keep the previous model's
		// effort tiers.
		ManagesEffort: true,
		EnvModelKey:   "LEAPMUX_MIMO_DEFAULT_MODEL",
		EnvEffortKey:  "LEAPMUX_MIMO_DEFAULT_EFFORT",
	}
}
