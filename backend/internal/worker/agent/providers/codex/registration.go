package codex

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// DefaultApprovalPolicy applies to a new Codex agent. LeapMux owns this default.
// The browser reads the other option defaults from contracts/codex-protocol.json.
const DefaultApprovalPolicy = "on-request"

// codexBinaryCandidates lists the executable names to probe for Codex, in
// preference order. The second entry is the full Rust host triple produced
// by `cargo install` on Windows when a shorter `codex` shim is absent.
var codexBinaryCandidates = []string{"codex", "codex-x86_64-pc-windows-msvc"}

// codexStaticOptionGroups holds Codex's option groups that do not depend on the
// model catalog: fast mode, workflow, approval policy, sandbox policy and network
// access. The factory registration and Agent.OptionGroups both read this one
// value, so the static fallback and a running agent offer the same template.
var codexStaticOptionGroups = []*leapmuxv1.AvailableOptionGroup{
	{
		Id:           contracts.CodexOptionServiceTier,
		Label:        "Fast Mode",
		DefaultValue: contracts.CodexOptionDefaultServiceTier,
		Mutable:      true,
		Order:        agent.OptionOrderProviderFirst,
		Options: []*leapmuxv1.AvailableOption{
			{Id: ServiceTierFast, Name: "On", Description: "Use Codex fast mode for future turns"},
			{Id: contracts.CodexOptionDefaultServiceTier, Name: "Off", Description: "Use the normal/default service tier"},
		},
	},
	{
		Id:           contracts.CodexOptionCollaborationMode,
		Label:        "Workflow",
		DefaultValue: contracts.CodexOptionDefaultCollaborationMode,
		Mutable:      true,
		Order:        agent.OptionOrderProviderSecond,
		Options: []*leapmuxv1.AvailableOption{
			{Id: CollaborationDefault, Name: "Default"},
			{Id: CollaborationPlan, Name: "Plan Mode"},
		},
	},
	{
		Id:           agent.OptionIDPermissionMode,
		Label:        "Approval Policy",
		DefaultValue: DefaultApprovalPolicy,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options: []*leapmuxv1.AvailableOption{
			{Id: "never", Name: "Full Auto"},
			{Id: DefaultApprovalPolicy, Name: "Suggest & Approve"},
			{Id: "untrusted", Name: "Auto-edit"},
		},
	},
	{
		Id:           contracts.CodexOptionSandboxPolicy,
		Label:        "Sandbox Policy",
		DefaultValue: contracts.CodexOptionDefaultSandboxPolicy,
		Mutable:      true,
		Order:        agent.OptionOrderProviderFourth,
		Options: []*leapmuxv1.AvailableOption{
			{Id: SandboxDangerFullAccess, Name: "Full Access", Description: "No filesystem restrictions"},
			{Id: SandboxWorkspaceWrite, Name: "Workspace Write", Description: "Write only within the working directory"},
			{Id: SandboxReadOnly, Name: "Read Only", Description: "No write access to the filesystem"},
		},
	},
	{
		Id:           contracts.CodexOptionNetworkAccess,
		Label:        "Network Access",
		DefaultValue: contracts.CodexOptionDefaultNetworkAccess,
		Mutable:      true,
		Order:        agent.OptionOrderProviderThird,
		Options: []*leapmuxv1.AvailableOption{
			{Id: NetworkRestricted, Name: "Restricted", Description: "No network access from the sandbox"},
			{Id: NetworkEnabled, Name: "Enabled", Description: "Allow network access from the sandbox"},
		},
	},
}

// codexLocator finds the Codex CLI on the user's PATH, the preferred name first.
var codexLocator = launch.Binaries(codexBinaryCandidates...)

// Registration states everything the worker knows about Codex before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Plugin:        codexProvider{},
		Start:         Start,
		Locator:       codexLocator,
		DefaultModels: codexDefaultModels,
		OptionGroups:  codexStaticOptionGroups,
		// model + the provider options above (static groups) + effort. The sandbox/network/
		// collaboration/service-tier axes are already static OptionGroups, so only effort
		// (built from the model catalog) needs declaring here.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// Seed the sandbox/network/collaboration/service-tier defaults into a fresh agent's
		// launch options; resolveProviderDefaults applies these for every provider uniformly.
		ProviderOptionDefaults: codexOptionDefaults(),
		// Codex has no new-session safe default: Suggest & Approve already asks.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: DefaultApprovalPolicy,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CODEX_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CODEX_DEFAULT_EFFORT",
	}
}
