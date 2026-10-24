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
	return []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(bypassPermissionsGroup, strconv.FormatBool(a.bypassPermissions))}
}

// UpdateSettings validates the launch policy before any other setting changes.
// A different policy requires a new process because Fast Agent installs its permission handler at startup.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if options.Get(contracts.FastagentOptionBypassPermissions) != "" {
		bypass, err := options.Bool(contracts.FastagentOptionBypassPermissions)
		if err != nil {
			return a.SettingsSnapshot()
		}
		if bypass != a.bypassPermissions {
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.Base.UpdateSettings(options)
}
