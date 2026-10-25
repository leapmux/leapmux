package ohmypi

import (
	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
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

// OptionOutputStyle selects the native personality through the private launch configuration.
const OptionOutputStyle = "outputStyle"

// OptionFastMode selects the native priority service tier for supported models.
const OptionFastMode = "fastMode"

var fastModeGroup = agent.SelectGroup(OptionFastMode, "Fast Mode", agent.OptionOrderProviderSecond, "false", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: "Use the model's standard service tier", Default: true},
	{Id: "true", Name: "On", Description: "Use native fast serving when the selected model supports it"},
})

var outputStyleGroup = agent.SelectGroup(OptionOutputStyle, "Output Style", agent.OptionOrderProviderFirst, "auto", []agent.OptionDef{
	{Id: "auto", Name: "Auto", Description: "Use the native personality configuration", Default: true},
	{Id: "default", Name: "Default", Description: "Use concise, evidence-focused responses"},
	{Id: "friendly", Name: "Friendly", Description: "Use a warm, supportive style"},
	{Id: "pragmatic", Name: "Pragmatic", Description: "Use direct, practical responses"},
	{Id: "none", Name: "None", Description: "Omit the native personality instructions"},
})

// approvalModeGroup is the static option group of omp's tool approval mode.
//
// It is the one home of that template: Registration and Agent.OptionGroups both
// read it, so the list the static fallback offers and the list a running agent
// offers cannot drift apart.
//
// omp applies its native mode at launch. No RPC command changes it during a session.
// The Worker grants Yolo tool approvals immediately. A narrower mode requires a restart.
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
// approval mode, output style, and fast mode are static axes.
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{approvalModeGroup, outputStyleGroup, fastModeGroup}

// Registration states everything the worker knows about Oh My Pi before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI,
		Plugin:   ompProvider{},
		Start:    Start,
		Locator:  ompLocator,
		AgentDir: &agentdir.Spec{Prefix: "ohmypi"},
		// No static catalog. omp's models come from its own configuration and its
		// provider discovery, so a hard-coded entry could identify a model that this
		// installation does not have. The catalog appears once the agent reports it.
		DefaultModels:  nil,
		OptionGroups:   staticOptionGroups,
		ModelSubGroups: agent.EffortSubGroupsLabeled(ThinkingLevelLabel),
		// Model and the static groups register their own IDs. Effort is supplied by the live catalog.
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
