package cursor

func (a *Agent) setCursorModel(model string) error {
	// Send the wire id but store the normalized (display) id, so b.model never
	// transiently holds the wire form "default[]" (see SetModelViaConfigOption).
	if err := a.SetModelViaConfigOption(cursorModelIDForWire(model)); err != nil {
		return err
	}
	a.SetCurrentModel(model)
	return nil
}
