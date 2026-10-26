package cursor

import (
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const lockedBypassNote = "Cursor keeps Bypass enabled for this session. Start a new session to turn it off."

// localOptionGroups reports the effective native policy, including stored state.
func (a *Agent) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	group := providerkit.LiveGroup(bypassPermissionsGroup, strconv.FormatBool(a.bypassPermissions))
	if a.bypassPermissions {
		group.Mutable = false
		group.ReadOnlyReason = lockedBypassNote
		group.DefaultValue = "true"
		for _, option := range group.Options {
			if option.GetId() == "true" {
				group.Options = []*leapmuxv1.AvailableOption{option}
				break
			}
		}
	}
	return []*leapmuxv1.AvailableOptionGroup{group}
}

// UpdateSettings enables Bypass through a restart and refuses to disable stored Bypass.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if options.Get(contracts.CursorOptionBypassPermissions) != "" {
		bypass, err := options.Bool(contracts.CursorOptionBypassPermissions)
		if err != nil {
			return a.SettingsSnapshot()
		}
		if a.bypassPermissions && !bypass {
			return a.SettingsSnapshot()
		}
		if bypass != a.bypassPermissions {
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.Base.UpdateSettings(options)
}

func (a *Agent) setCursorModel(model string) error {
	// Keep the canonical ID in the acknowledgment, even when the native ID is default.
	return a.SetModelViaConfigOption(cursorModelIDForWire(model), model)
}
