package codex

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// The Codex permission modes, the values of the permissionMode axis. LeapMux owns
// this vocabulary. Each mode except ModeApproveForMe is also the native approval
// policy that Codex receives. ModeApproveForMe is the native on-request policy with
// the auto-review reviewer; codexApprovalWire states that mapping. Upstream Codex
// calls ModeApproveForMe "Approve for me".
const (
	ModeAskForApproval = "on-request"
	ModeApproveForMe   = "auto-review"
	ModeUnlessTrusted  = "untrusted"
	ModeNever          = "never"
)

// DefaultPermissionMode applies to a new Codex agent. LeapMux owns this default.
// The browser reads the other option defaults from contracts/codex-protocol.json.
const DefaultPermissionMode = ModeAskForApproval

// The built-in Codex permission profiles. permissionProfile/list reports them
// together with the profiles that the user defines in config.toml.
const (
	ProfileWorkspace        = ":workspace"
	ProfileReadOnly         = ":read-only"
	ProfileDangerFullAccess = ":danger-full-access"
)

// codexBuiltinProfiles holds the display name and the description of each built-in
// profile. The static option group offers these three, and a live catalog gives
// them the same names, so a profile reads the same before and after discovery.
var codexBuiltinProfiles = []*leapmuxv1.AvailableOption{
	{Id: ProfileWorkspace, Name: "Workspace", Description: "Read and edit files in the workspace, and run commands. Network access and edits outside the workspace need approval."},
	{Id: ProfileReadOnly, Name: "Read Only", Description: "Read files in the workspace. Edits and network access need approval."},
	{Id: ProfileDangerFullAccess, Name: "Full Access", Description: "Edit any file and use the network, with no sandbox."},
}

// codexStaticOptionGroups holds Codex's option groups that do not depend on the
// model catalog: fast mode, workflow, approval policy, and permissions. The factory
// registration and Agent.OptionGroups both read this one value, so the static
// fallback and a running agent offer the same template. A running agent narrows
// the approval policy to the modes that the requirements allow, and replaces the
// permission profiles with the list that Codex reports.
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
		DefaultValue: DefaultPermissionMode,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options: []*leapmuxv1.AvailableOption{
			{Id: ModeAskForApproval, Name: "Ask for Approval", Description: "Codex asks you before an action that its permissions do not allow."},
			{Id: ModeApproveForMe, Name: "Approve for Me", Description: "A reviewer agent approves or denies each request that would ask you. Each review uses model tokens."},
			{Id: ModeUnlessTrusted, Name: "Ask Unless Trusted", Description: "Codex asks you before each command that no trusted rule allows."},
			{Id: ModeNever, Name: "Never Ask", Description: "Codex never asks. An action that its permissions do not allow fails."},
		},
	},
	{
		Id:           contracts.CodexOptionPermissionProfile,
		Label:        "Permissions",
		DefaultValue: contracts.CodexOptionDefaultPermissionProfile,
		Mutable:      true,
		Order:        agent.OptionOrderProviderThird,
		Options:      codexBuiltinProfiles,
	},
}

// codexLocator finds the Codex CLI on the user's PATH, the preferred name first.
// contracts/codex-protocol.json lists the names and states why each one exists.
// The E2E suite reads the same list to decide whether the worker can start Codex.
var codexLocator = launch.Binaries(contracts.CodexExecutableKeys...)

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
		// model + the provider options above (static groups) + effort. The permission-profile,
		// collaboration and service-tier axes are already static OptionGroups, so only effort
		// (built from the model catalog) needs declaring here.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// Seed the permission-profile, collaboration and service-tier defaults into a fresh
		// agent's launch options; resolveProviderDefaults applies these for every provider
		// uniformly.
		ProviderOptionDefaults: codexOptionDefaults(),
		// Codex has no new-session safe default: Ask for Approval already asks.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: DefaultPermissionMode,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CODEX_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CODEX_DEFAULT_EFFORT",
	}
}
