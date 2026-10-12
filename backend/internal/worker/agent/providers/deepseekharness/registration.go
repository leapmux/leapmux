package deepseekharness

import (
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
}

func modeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Mode", agent.OptionOrderPermissionMode, current, nativeModes)
}
func permissionGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(contracts.DeepseekHarnessOptionPermissions, "Permissions", agent.OptionOrderProviderFirst, current, nativePermissions)
}

// Registration keeps launch and fallback settings in one provider-owned source.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DEEPSEEK_HARNESS,
		Plugin:   deepseekHarnessProvider{}, Start: Start, Locator: nativeLocator,
		DefaultModels:       nil,
		OptionGroups:        []*leapmuxv1.AvailableOptionGroup{modeGroup(""), permissionGroup("")},
		AdditionalOptionIDs: []string{agent.OptionIDEffort}, ManagesEffort: true,
		ProviderOptionDefaults: map[string]string{contracts.DeepseekHarnessOptionPermissions: contracts.DeepseekHarnessPermissionPresetWorkspaceWrite},
		PermissionDefaults:     agent.PermissionDefaults{Fallback: contracts.DeepseekHarnessModeAct, NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.DeepseekHarnessModeAct}},
		EnvModelKey:            "LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_MODEL", EnvEffortKey: "LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_EFFORT",
		AgentDir: ptrconv.Ptr(agentDirSpec()),
	}
}
