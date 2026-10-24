package amp

import (
	"slices"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// agentModes lists the modes agentModeGroup offers.
var agentModes = []string{agentModeLow, agentModeMedium, agentModeHigh, agentModeUltra}

// permissionModes lists the modes permissionModeGroup offers.
var permissionModes = []string{contracts.AmpPermissionModeAsk, contracts.AmpPermissionModeAllowAll}

// launchAgentMode is the mode an agent starts with: the one its options state,
// else Amp's default.
func launchAgentMode(opts agent.Options) string {
	if mode := opts.Get(contracts.AmpOptionAgentMode); slices.Contains(agentModes, mode) {
		return mode
	}
	return agentModeGroup.GetDefaultValue()
}

// launchPermissionMode is the permission mode an agent starts with: the one its
// options state, else the fallback, which asks.
func launchPermissionMode(opts agent.Options) string {
	if mode := opts.PermissionMode(); slices.Contains(permissionModes, mode) {
		return mode
	}
	return Registration().PermissionDefaults.Fallback
}

// lockedModeNote is what the mode group states once the thread keeps its mode.
const lockedModeNote = "Amp keeps the mode that a thread's first message used. Start a new session for another mode."

// lockedFastModeNote explains why the speed cannot change on an existing thread.
const lockedFastModeNote = "Amp keeps a thread's speed. Start a new session to change Fast Mode."

// OptionGroups returns the thread's native settings and its live permission setting.
// Native mode and speed become read-only after the first message.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	return []*leapmuxv1.AvailableOptionGroup{
		threadOptionGroup(agentModeGroup, a.agentMode, a.modeLocked, lockedModeNote),
		providerkit.LiveGroup(permissionModeGroup, a.permissionMode),
		threadOptionGroup(fastModeGroup, a.fastMode, a.modeLocked, lockedFastModeNote),
	}
}

// threadOptionGroup keeps the native choice visible when it cannot change.
// An unreadable resumed value stays unknown instead of claiming a default.
func threadOptionGroup(template *leapmuxv1.AvailableOptionGroup, value string, locked bool, reason string) *leapmuxv1.AvailableOptionGroup {
	if !locked {
		return providerkit.LiveGroup(template, value)
	}
	group := &leapmuxv1.AvailableOptionGroup{
		Id: template.GetId(), Label: template.GetLabel(),
		CurrentValue: value, DefaultValue: value,
		Order: template.GetOrder(), ReadOnlyReason: reason,
	}
	if value != "" {
		option := &leapmuxv1.AvailableOption{Id: value, Name: value}
		for _, candidate := range template.GetOptions() {
			if candidate.GetId() == value {
				option = candidate
				break
			}
		}
		group.Options = []*leapmuxv1.AvailableOption{option}
	} else {
		group.ReadOnlyReason = "Amp did not report this thread's setting. " + reason
	}
	return group
}

// SettingsSnapshot confirms every value the agent holds.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies a change live, with no restart.
//
//   - Allow All approves pending requests and takes effect for every later request.
//     The settings file delegates every call to the helper in both modes, and
//     the agent decides each request from its current mode (see
//     decidePermission), so a change writes no file.
//   - The agent mode takes effect at the next process that starts a thread,
//     which is the first message's. After the first message the thread keeps
//     its mode and speed, and a request for another choice changes nothing: the snapshot
//     then confirms the mode the thread keeps.
//
// A value that is not one of the axis's options changes nothing either.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.mu.Lock()
	if mode := options.Get(agent.OptionIDPermissionMode); slices.Contains(permissionModes, mode) {
		a.permissionMode = mode
	}
	if mode := options.Get(contracts.AmpOptionAgentMode); !a.modeLocked && slices.Contains(agentModes, mode) {
		a.agentMode = mode
	}
	if fast := options.Get(OptionFastMode); !a.modeLocked && (fast == "true" || fast == "false") {
		a.fastMode = fast
	}
	mode, fast, permission := a.agentMode, a.fastMode, a.permissionMode
	a.mu.Unlock()
	a.approvePendingPermissions()
	a.sink.PersistSettingsRefresh(map[string]string{
		contracts.AmpOptionAgentMode: mode,
		OptionFastMode:               fast,
		agent.OptionIDPermissionMode: permission,
	})
	return a.SettingsSnapshot()
}

// launchFastMode selects the native flag only for an explicit On choice.
func launchFastMode(opts agent.Options) string {
	return strconv.FormatBool(opts.Get(OptionFastMode) == "true")
}
