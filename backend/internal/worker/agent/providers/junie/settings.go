package junie

// settings.go holds the live writes of one Junie agent.

// setJunieModel writes the model through session/set_config_option with the
// decorated wire id, and stores the plain profile id locally, so b.model never
// transiently holds the wire form (see SetModelViaConfigOption).
func (a *Agent) setJunieModel(model string) error {
	if err := a.SetModelViaConfigOption(junieModelIDForWire(model)); err != nil {
		return err
	}
	a.SetCurrentModel(model)
	return nil
}
