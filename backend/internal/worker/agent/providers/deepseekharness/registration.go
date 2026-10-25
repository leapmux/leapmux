package deepseekharness

import (
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

var nativeLocator = launch.Binaries("dsh")

var nativeModes = []agent.OptionDef{
	{Id: contracts.DeepseekHarnessModeAct, Name: "Act", Description: "Run the native agent outside plan mode.", Default: true},
	{Id: contracts.DeepseekHarnessModePlan, Name: "Plan", Description: "Plan the work before implementation."},
}

var nativePermissions = []agent.OptionDef{
	{Id: contracts.DeepseekHarnessPermissionPresetReadOnly, Name: "Read only", Description: "Keep file operations in the native read-only sandbox."},
	{Id: contracts.DeepseekHarnessPermissionPresetWorkspaceWrite, Name: "Workspace write", Description: "Write inside the workspace. Ask before wider access.", Default: true},
	{Id: contracts.DeepseekHarnessPermissionPresetDangerFullAccess, Name: "Full access", Description: "Run tools without a sandbox or permission questions."},
	// The launch overlay loads the Auto review layer that offers this preset (auto_review.go).
	{Id: contracts.DeepseekHarnessPermissionPresetAuto, Name: "Auto review", Description: "Experimental. The session's model reviews each tool call. An allowed call runs without a sandbox, and a denied call asks you."},
}

func modeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Mode", agent.OptionOrderPermissionMode, current, nativeModes)
}

// permissionOptions lists the presets that a session can select. Auto exists
// only while the Auto review layer is live, so it is listed only when the
// native permission catalog offers it.
func permissionOptions(autoReview bool) []agent.OptionDef {
	if autoReview {
		return nativePermissions
	}
	return slices.DeleteFunc(slices.Clone(nativePermissions), func(option agent.OptionDef) bool {
		return option.Id == contracts.DeepseekHarnessPermissionPresetAuto
	})
}

func permissionOffered(preset string, autoReview bool) bool {
	return slices.ContainsFunc(permissionOptions(autoReview), func(option agent.OptionDef) bool { return option.Id == preset })
}

func permissionGroup(current string, autoReview bool) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(contracts.DeepseekHarnessOptionPermissions, "Permissions", agent.OptionOrderProviderFirst, current, permissionOptions(autoReview))
}

// Registration keeps launch and fallback settings in one provider-owned source.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DEEPSEEK_HARNESS,
		Plugin:   deepseekHarnessProvider{}, Start: Start, Locator: nativeLocator,
		DefaultModels: nil,
		// The launch overlay loads the Auto review layer, so the static groups offer Auto.
		OptionGroups:        []*leapmuxv1.AvailableOptionGroup{modeGroup(""), permissionGroup("", true)},
		AdditionalOptionIDs: []string{agent.OptionIDEffort}, ManagesEffort: true,
		ProviderOptionDefaults: map[string]string{contracts.DeepseekHarnessOptionPermissions: contracts.DeepseekHarnessPermissionPresetWorkspaceWrite},
		PermissionDefaults:     agent.PermissionDefaults{Fallback: contracts.DeepseekHarnessModeAct, NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.DeepseekHarnessModeAct}},
		EnvModelKey:            "LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_MODEL", EnvEffortKey: "LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_EFFORT",
		AgentDir: ptrconv.Ptr(agentDirSpec()),
	}
}
