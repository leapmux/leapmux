package qoder

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderLocator finds the Qoder CLI on the user's PATH.
var qoderLocator = launch.Binaries("qodercli")

// qoderPermissionModes are the modes the wire emits in camelCase. The flag
// vocabulary is snake_case and is normalized before emission.
var qoderPermissionModes = []agent.OptionDef{
	{Id: contracts.QoderModeDefault, Name: "Default", Default: true, Description: "Prompt for dangerous operations."},
	{Id: contracts.QoderModeAcceptEdits, Name: "Accept Edits", Description: "Auto-accept file edit operations."},
	{Id: contracts.QoderModeAuto, Name: "Auto", Description: "Approve what a safety check finds safe, ask about the rest."},
	{Id: contracts.QoderModeDontAsk, Name: "Don't Ask", Description: "Do not prompt; deny what is not pre-approved."},
	{Id: contracts.QoderModePlan, Name: "Plan", Description: "Plan the work without modifying files."},
}

func qoderPermissionModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, qoderPermissionModes)
}

// qoderEffortLevels are the reasoning-effort values Qoder takes, strongest
// first, with LeapMux's Auto sentinel ahead of them.
//
// Auto is not a strength: it sends no `--reasoning-effort` at all, so the CLI
// keeps whatever default the model resolves. The six levels below are the words
// Qoder's own effort validator admits; its aliases `disabled` and `off` mean
// the same as `none` and are never offered as their own rows.
var qoderEffortLevels = []agent.OptionDef{
	{Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Let Qoder pick the effort for the model", Default: true},
	{Id: contracts.QoderEffortLevelMax, Name: providerkit.EffortLabel(contracts.QoderEffortLevelMax)},
	{Id: contracts.QoderEffortLevelXhigh, Name: providerkit.EffortLabel(contracts.QoderEffortLevelXhigh)},
	{Id: contracts.QoderEffortLevelHigh, Name: providerkit.EffortLabel(contracts.QoderEffortLevelHigh)},
	{Id: contracts.QoderEffortLevelMedium, Name: providerkit.EffortLabel(contracts.QoderEffortLevelMedium)},
	{Id: contracts.QoderEffortLevelLow, Name: providerkit.EffortLabel(contracts.QoderEffortLevelLow)},
	{Id: contracts.QoderEffortLevelNone, Name: providerkit.EffortLabel(contracts.QoderEffortLevelNone)},
}

func qoderEffortGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDEffort, "Effort", agent.OptionOrderEffort, current, qoderEffortLevels)
}

// Registration states everything the worker knows about Qoder CLI before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_QODER,
		Plugin:   qoderProvider{},
		Start:    Start,
		Locator:  qoderLocator,
		// The account decides which models exist, so the catalog is read from the
		// open session rather than declared here.
		DefaultModels: nil,
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			qoderEffortGroup(""),
			qoderPermissionModeGroup(""),
		},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.QoderModeDefault},
			Fallback:   contracts.QoderModeDefault,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_QODER_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_QODER_DEFAULT_EFFORT",
	}
}
