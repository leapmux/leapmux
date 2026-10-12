package junie

// Stop ends native child readers before the ACP base closes their transcripts.
func (a *Agent) Stop() {
	a.stopChildTails()
	a.Base.Stop()
}

// Wait joins native child readers before the ACP base closes their transcripts.
func (a *Agent) Wait() error {
	// Process.Wait only waits for the process. Base.Wait then closes open rows.
	_ = a.Process.Wait()
	a.stopChildTails()
	return a.Base.Wait()
}
