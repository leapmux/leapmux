package zcode

import "fmt"

// refreshZCodeModelCatalog reads the unrestricted model list of the active
// session. Native session/read and setter replies contain only the current model.
func (a *Agent) refreshZCodeModelCatalog() error {
	a.sessionLifecycleMu.Lock()
	changed, err := a.readZCodeModelCatalog()
	a.sessionLifecycleMu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(nil)
	}
	return err
}

func (a *Agent) readZCodeModelCatalog() (bool, error) {
	a.Mu.Lock()
	sessionID, stopped := a.sessionID, a.StoppedLocked()
	a.Mu.Unlock()
	if sessionID == "" || stopped {
		return false, errNoZCodeSession
	}
	// Native resume returns the existing runtime when this ID is active. The
	// lifecycle lock prevents adopting and retiring a replacement during this read.
	raw, err := a.sendZCodeRequest(MethodSessionResume, map[string]any{"sessionId": sessionID}, a.APITimeout())
	if err != nil {
		return false, err
	}
	snap, ok := a.parseStateSnapshot(raw)
	if !ok || snap.Session.SessionID != sessionID || snap.Settings == nil || snap.Settings.Model == nil || snap.Settings.Model.Available == nil {
		return false, fmt.Errorf("ZCode returned no complete model catalog for session %q", sessionID)
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.sessionID != sessionID || a.StoppedLocked() {
		return false, errNoZCodeSession
	}
	// The response's axes and event sequence can already be stale. Fold only
	// membership and metadata, using the model currently observed by the agent.
	providerID, modelID, _ := splitZCodeModelID(a.model)
	a.replaceZCodeLiveModelsLocked(snap.Settings.Model.Available, zcodeModelRef{ProviderID: providerID, ModelID: modelID})
	return true, nil
}
