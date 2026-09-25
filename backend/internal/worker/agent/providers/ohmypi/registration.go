package ohmypi

import (
	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ompLocator finds the omp CLI on the user's PATH.
var ompLocator = launch.Binaries("omp")

// ThinkingLevelLabel is the label of omp's effort axis. omp calls it a thinking
// level (`--thinking`, `set_thinking_level`), so the settings popover uses that
// word.
const ThinkingLevelLabel = "Thinking Level"

// ApprovalModeLabel is the label of omp's permission-mode axis, which carries the
// tool approval mode.
const ApprovalModeLabel = "Approval Mode"

// approvalModeGroup is the static option group of omp's tool approval mode.
//
// It is the one home of that template: Registration and Agent.OptionGroups both
// read it, so the list the static fallback offers and the list a running agent
// offers cannot drift apart.
//
// omp fixes the three modes itself (`tools.approvalMode`), and it applies a mode
// only at launch: no RPC command changes it for a running session. A change
// therefore restarts the agent with `--approval-mode`.
var approvalModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDPermissionMode,
	Label:        ApprovalModeLabel,
	DefaultValue: contracts.OhMyPiApprovalModeWrite,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.OhMyPiApprovalModeAlwaysAsk, Name: "Always Ask", Description: "Ask before every file change and every command"},
		{Id: contracts.OhMyPiApprovalModeWrite, Name: "Write", Description: "Change files freely; ask before a command, a subagent or a script runs"},
		{Id: contracts.OhMyPiApprovalModeYolo, Name: "Yolo", Description: "Run everything without asking"},
	},
}

// staticOptionGroups holds the option groups that do not depend on a running
// agent. omp reports its models and their thinking levels only at runtime, so
// the approval mode is the one static axis.
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{approvalModeGroup}

// Registration states everything the worker knows about Oh My Pi before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI,
		Plugin:   ompProvider{},
		Start:    Start,
		Locator:  ompLocator,
		// No static catalog. omp's models come from its own configuration and its
		// provider discovery, so a hard-coded entry could identify a model that this
		// installation does not have. The catalog appears once the agent reports it.
		DefaultModels:  nil,
		OptionGroups:   staticOptionGroups,
		ModelSubGroups: agent.EffortSubGroupsLabeled(ThinkingLevelLabel),
		// model + effort (the "Thinking Level" axis) + permissionMode (the approval
		// mode, a static group).
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// A session with no stored mode takes Write, which asks before a command
		// runs. omp's own default is Yolo, which is the mode the bypass preset
		// selects, so a resumed session must never fall back to it.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.OhMyPiApprovalModeWrite,
		},
		// LeapMux states omp's three modes completely, so a launch option that
		// specifies another value is refused rather than handed to omp. omp logs an
		// unknown `--approval-mode` value and runs its configured mode instead,
		// which is Yolo unless the user changed it -- so an unchecked value would
		// silently turn every approval prompt off.
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_OHMYPI_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_OHMYPI_DEFAULT_EFFORT",
	}
}
