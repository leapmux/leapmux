package gemini

// ClearContext records the user's intent before replacing the session. A failed
// replacement must not retain the automatic retry of an earlier long wait.
func (a *Agent) ClearContext() (string, error) {
	a.noteUserStop()
	return a.Base.ClearContext()
}
