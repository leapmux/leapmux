package junie

import (
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// UpdateSettings restarts when the stored effort override changes. Junie's ACP
// effort write changes its session option, but model turns read launch flags or
// a separate settings store. A reported default is not a stored override.
// Restart passes the full option map through --effort without a partial write.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if options[agent.OptionIDEffort] != a.launchEffortOverride {
		return agent.RestartRequiredSettings(options)
	}
	return a.Base.UpdateSettings(options)
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
