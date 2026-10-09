package muse

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

func permissionGroup() *leapmuxv1.AvailableOptionGroup {
	return &leapmuxv1.AvailableOptionGroup{Id: agent.OptionIDPermissionMode, Label: "Permissions", DefaultValue: contracts.MuseApprovalModeOnRequest, Mutable: true, Order: agent.OptionOrderPermissionMode,
		Options: []*leapmuxv1.AvailableOption{
			{Id: contracts.MuseApprovalModeOnRequest, Name: "On request", Description: "Let Muse request approval when its policy requires it"},
			{Id: contracts.MuseApprovalModePromptUnmatched, Name: "Ask", Description: "Ask for each action without an existing policy decision"},
			{Id: contracts.MuseApprovalModeDenyUnmatched, Name: "Restrict", Description: "Refuse each action without an existing policy decision"},
			{Id: contracts.MuseApprovalModeAllowAll, Name: "Bypass", Description: "Allow tools without approval prompts"},
		}}
}

// startupGroups derives the launch defaults and read-only options from the shared descriptors.
func startupGroups(values map[string]string) []*leapmuxv1.AvailableOptionGroup {
	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, len(contracts.MuseStartupOptionGroups))
	for index, descriptor := range contracts.MuseStartupOptionGroups {
		current := values[descriptor.ID]
		if current == "" {
			current = descriptor.DefaultValue
		}
		group := &leapmuxv1.AvailableOptionGroup{Id: descriptor.ID, Label: descriptor.Label, DefaultValue: descriptor.DefaultValue, CurrentValue: current, Mutable: false, Order: int32(100 + index), ReadOnlyReason: descriptor.ReadOnlyReason}
		for _, option := range descriptor.Options {
			group.Options = append(group.Options, &leapmuxv1.AvailableOption{Id: option.Value, Name: option.Label, Description: option.Description})
		}
		groups = append(groups, group)
	}
	return groups
}

// Registration supplies the native host and launch axes.
func Registration() agent.Registration {
	groups := append([]*leapmuxv1.AvailableOptionGroup{permissionGroup()}, startupGroups(nil)...)
	defaults := map[string]string{agent.OptionIDPermissionMode: contracts.MuseApprovalModeOnRequest}
	for _, group := range groups[1:] {
		defaults[group.Id] = group.DefaultValue
	}
	return agent.Registration{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE, Plugin: museProvider{}, Start: Start, Locator: launch.Binaries("muse"),
		DefaultModels: []*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the model that Muse selects")}, OptionGroups: groups, AdditionalOptionIDs: []string{agent.OptionIDEffort}, ManagesEffort: true, FixedPermissionModes: true,
		PermissionDefaults: agent.PermissionDefaults{NewSession: defaults, Fallback: contracts.MuseApprovalModeOnRequest}, EnvModelKey: "LEAPMUX_MUSE_DEFAULT_MODEL", EnvEffortKey: "LEAPMUX_MUSE_DEFAULT_EFFORT"}
}
