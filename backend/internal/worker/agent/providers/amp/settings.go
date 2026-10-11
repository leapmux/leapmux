package amp

import (
	"slices"

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

// OptionGroups returns the agent-mode group and the permission-mode group.
//
// The mode group is mutable until the thread gets its first message. From then
// on it shows the thread's mode alone, read-only, with its read-only reason
// saying why.
// Amp cannot change the mode of a thread, and the worker never starts a new
// thread in silence to change it.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	mode, locked, permission := a.agentMode, a.modeLocked, a.permissionMode
	a.mu.Unlock()
	return []*leapmuxv1.AvailableOptionGroup{
		modeGroup(mode, locked),
		providerkit.LiveGroup(permissionModeGroup, permission),
	}
}

// modeGroup projects the agent mode: the mutable template before the first
// message, and a read-only group that holds the current mode after it.
func modeGroup(mode string, locked bool) *leapmuxv1.AvailableOptionGroup {
	if !locked {
		return providerkit.LiveGroup(agentModeGroup, mode)
	}
	option := &leapmuxv1.AvailableOption{Id: mode, Name: mode}
	for _, candidate := range agentModeGroup.GetOptions() {
		if candidate.GetId() == mode {
			option = candidate
		}
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:             agentModeGroup.GetId(),
		Label:          agentModeGroup.GetLabel(),
		Options:        []*leapmuxv1.AvailableOption{option},
		CurrentValue:   mode,
		DefaultValue:   mode,
		Mutable:        false,
		Order:          agentModeGroup.GetOrder(),
		ReadOnlyReason: lockedModeNote,
	}
}

// SettingsSnapshot confirms every value the agent holds.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

// UpdateSettings applies a change live, with no restart.
//
//   - The permission mode takes effect at the very next permission request.
//     The settings file delegates every call to the helper in both modes, and
//     the agent decides each request from its current mode (see
//     decidePermission), so a change writes no file.
//   - The agent mode takes effect at the next process that starts a thread,
//     which is the first message's. After the first message the thread keeps
//     its mode, and a request for another one changes nothing: the snapshot
//     then confirms the mode the thread keeps.
//
// A value that is not one of the axis's options changes nothing either.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.mu.Lock()
	if mode := options.Get(agent.OptionIDPermissionMode); slices.Contains(permissionModes, mode) {
		a.permissionMode = mode
	}
	if mode := options.Get(contracts.AmpOptionAgentMode); !a.modeLocked && slices.Contains(agentModes, mode) {
		a.agentMode = mode
	}
	mode, permission := a.agentMode, a.permissionMode
	a.mu.Unlock()
	a.sink.PersistSettingsRefresh(map[string]string{
		contracts.AmpOptionAgentMode: mode,
		agent.OptionIDPermissionMode: permission,
	})
	return a.SettingsSnapshot()
}
