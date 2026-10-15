package codex

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// These Go-only permission modes use the native Codex approval policy values.
// The browser and worker share the other modes through the generated contracts.
// Approve for Me maps to the on-request policy with the auto-review reviewer.
// codexApprovalWire defines that mapping.
const (
	ModeAskForApproval = "on-request"
	ModeUnlessTrusted  = "untrusted"
)

// DefaultPermissionMode applies to a new Codex agent. LeapMux owns this default.
// The browser reads the other option defaults from contracts/codex-protocol.json.
const DefaultPermissionMode = ModeAskForApproval

// ProfileReadOnly is the built-in profile that no browser preset selects.
// permissionProfile/list reports built-in profiles and user-defined config.toml profiles.
const ProfileReadOnly = ":read-only"

// codexBuiltinProfiles holds the display name and the description of each built-in
// profile. The static option group offers these three, and a live catalog gives
// them the same names, so a profile reads the same before and after discovery.
var codexBuiltinProfiles = []*leapmuxv1.AvailableOption{
	{Id: contracts.CodexPermissionProfileWorkspace, Name: "Workspace", Description: "Read and edit files in the workspace, and run commands. Network access and edits outside the workspace need approval."},
	{Id: ProfileReadOnly, Name: "Read Only", Description: "Read files in the workspace. Edits and network access need approval."},
	{Id: contracts.CodexPermissionProfileDangerFullAccess, Name: "Full Access", Description: "Edit any file and use the network, with no sandbox."},
}

// codexStaticOptionGroups holds the option groups that do not depend on the model catalog.
// Registration and Agent.OptionGroups read this value and offer the same template.
// A live agent offers only the approval policies that its requirements allow.
// It replaces the permission profiles with the list that Codex reports.
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
			{Id: contracts.CodexPermissionModeApproveForMe, Name: "Approve for Me", Description: "A reviewer agent approves or denies each request that would ask you. Each review uses model tokens."},
			{Id: ModeUnlessTrusted, Name: "Ask Unless Trusted", Description: "Codex asks you before each command that no trusted rule allows."},
			{Id: contracts.CodexPermissionModeNever, Name: "Never Ask", Description: "Codex never asks. An action that its permissions do not allow fails."},
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
		// The static option groups declare the provider axes except effort.
		// The model catalog supplies effort, so declare that axis here.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// Set the provider defaults in a new agent's launch options.
		// resolveProviderDefaults applies them for every provider.
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
