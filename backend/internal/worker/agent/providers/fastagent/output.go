package fastagent

func (a *Agent) clearProviderState() {
	a.clearChildState()
	a.rootMu.Lock()
	a.rootSession = ""
	a.rootFresh = true
	a.rootMu.Unlock()
}

func (a *Agent) initializeRootArchive(resumed bool) error {
	sessionID := a.CurrentSessionID()
	a.rootMu.Lock()
	a.rootSession = sessionID
	a.rootFresh = !resumed
	a.rootMu.Unlock()
	return nil
}

func (a *Agent) finishChildArchiveReads() {
	a.rootFailureWork.CloseAndWait()
	a.archiveWG.Wait()
}

func (a *Agent) finishArchiveReads() {
	a.rootMu.Lock()
	a.rootClosing = true
	a.rootMu.Unlock()
	a.rootArchiveWG.Wait()
}
