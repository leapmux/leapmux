package codebuddy

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// codebuddyLocator finds the CodeBuddy Code CLI on the user's PATH.
// `cbc` is the short alias and resolves to the same executable.
var codebuddyLocator = launch.Binaries("codebuddy", "cbc")

// codebuddyPermissionModes are the four modes `get_available_modes` advertises.
//
// The CLI's --permission-mode flag accepts six words (the same set as
// contracts/claude-protocol.json), but the runtime advertises only these four
// and its plan description is a copy-paste from Claude Code. LeapMux offers the
// advertised set until a live probe proves dontAsk and auto are enterable.
var codebuddyPermissionModes = []agent.OptionDef{
	{Id: contracts.CodebuddyModeDefault, Name: "Default", Default: true, Description: "Prompts for dangerous operations."},
	{Id: contracts.CodebuddyModeAcceptEdits, Name: "Accept Edits", Description: "Auto-accept file edit operations."},
	{Id: contracts.CodebuddyModePlan, Name: "Plan", Description: "Plan the work without modifying files."},
	{Id: contracts.CodebuddyModeBypassPermissions, Name: "Bypass Permissions", Description: "Bypass all permission checks."},
}

// codebuddyEffortLevels are the --effort values CodeBuddy owns. They are not
// Claude's low/medium/high/ultra set; map, do not pass through.
var codebuddyEffortLevels = []agent.OptionDef{
	{Id: contracts.CodebuddyEffortLevelMinimal, Name: "Minimal"},
	{Id: contracts.CodebuddyEffortLevelLow, Name: "Low"},
	{Id: contracts.CodebuddyEffortLevelMedium, Name: "Medium", Default: true},
	{Id: contracts.CodebuddyEffortLevelHigh, Name: "High"},
	{Id: contracts.CodebuddyEffortLevelXhigh, Name: "XHigh"},
	{Id: contracts.CodebuddyEffortLevelMax, Name: "Max"},
}

func codebuddyPermissionModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, codebuddyPermissionModes)
}

func codebuddyEffortGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDEffort, "Effort", agent.OptionOrderEffort, current, codebuddyEffortLevels)
}

// Registration states everything the worker knows about CodeBuddy Code before
// any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY,
		Plugin:   codebuddyProvider{},
		Start:    Start,
		Locator:  codebuddyLocator,
		// CodeBuddy reports no static model catalog: the account and the
		// models.json custom-local tier decide which models exist, so the running
		// session reports them through get_available_models.
		DefaultModels: nil,
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			codebuddyEffortGroup(""),
			codebuddyPermissionModeGroup(""),
		},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			// A new session asks for Default: prompt for dangerous operations,
			// which is the narrowest mode that still lets work proceed.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModeDefault},
			Fallback:   contracts.CodebuddyModeDefault,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CODEBUDDY_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CODEBUDDY_DEFAULT_EFFORT",
	}
}
