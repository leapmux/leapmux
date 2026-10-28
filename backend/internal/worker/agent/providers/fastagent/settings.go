package fastagent

import (
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// localOptionGroups reports the immutable native policy used by this process.
func (a *Agent) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	bypass := providerkit.LiveGroup(bypassPermissionsGroup, strconv.FormatBool(a.bypassPermissions))
	if a.nativeCommands {
		bypass.Mutable = false
		bypass.ReadOnlyReason = "Disable Native Commands first. Native commands do not request approval."
	}
	native := providerkit.LiveGroup(nativeCommandsGroup, strconv.FormatBool(a.nativeCommands))
	if !a.bypassPermissions {
		native.Mutable = false
		native.ReadOnlyReason = "Enable Bypass Permissions first. Native commands do not request approval."
	}
	return []*leapmuxv1.AvailableOptionGroup{bypass, native}
}

// UpdateSettings validates the launch policy before any other setting changes.
// A different policy requires a new process because Fast Agent installs its permission handler at startup.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	bypass, native := a.bypassPermissions, a.nativeCommands
	if options.Get(contracts.FastagentOptionBypassPermissions) != "" {
		requested, err := options.Bool(contracts.FastagentOptionBypassPermissions)
		if err != nil {
			return a.SettingsSnapshot()
		}
		bypass = requested
	}
	if options.Get(OptionNativeCommands) != "" {
		requested, err := options.Bool(OptionNativeCommands)
		if err != nil {
			return a.SettingsSnapshot()
		}
		native = requested
	}
	if native && !bypass {
		return a.SettingsSnapshot()
	}
	if bypass != a.bypassPermissions || native != a.nativeCommands {
		return agent.RestartRequiredSettings(options)
	}
	return a.Base.UpdateSettings(options)
}
