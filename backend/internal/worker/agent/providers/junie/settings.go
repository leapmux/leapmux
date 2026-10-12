package junie

import (
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// UpdateSettings restarts when the requested effort differs from the effort
// that the running process uses. Junie's ACP effort write changes its session
// option, but model turns read launch flags or a separate settings store.
// Restart passes the full option map through --effort without a partial write.
//
// The worker stores the effort that Junie reports, so the merged map of every
// edit carries it. That value is not a change request, and an edit of another
// axis keeps the agent running.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if a.effortNeedsRestart(options[agent.OptionIDEffort]) {
		return agent.RestartRequiredSettings(options)
	}
	return a.Base.UpdateSettings(options)
}

// effortNeedsRestart reports whether a launch with --effort must apply the
// requested effort. An empty request means that the next launch omits the
// flag, and only a launch that passed it differs from that.
func (a *Agent) effortNeedsRestart(requested string) bool {
	if requested == "" {
		return a.launchEffortOverride != ""
	}
	return requested != a.runningEffort()
}

// runningEffort returns the effort that the process uses for its model turns.
// A launch flag wins. Without it, the process uses the default that Junie
// reports in its session. No ACP effort write follows a launch, because a
// different effort restarts the agent, so the reported value stays the one that
// the process uses.
func (a *Agent) runningEffort() string {
	if a.launchEffortOverride != "" {
		return a.launchEffortOverride
	}
	return agent.CurrentOptions(a.OptionGroups())[agent.OptionIDEffort]
}

// setJunieModel writes the selected config-option ID. Custom profiles need a
// decorated wire ID; proxy models already carry one. The local model keeps the
// caller's canonical ID.
func (a *Agent) setJunieModel(model string) error {
	if err := a.SetModelViaConfigOption(junieModelIDForWire(model)); err != nil {
		return err
	}
	a.SetCurrentModel(model)
	return nil
}
