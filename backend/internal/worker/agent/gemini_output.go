package agent

import "encoding/json"

func (a *GeminiCLIAgent) handleExtraSessionUpdate(sessionUpdate string, update json.RawMessage) bool {
	if sessionUpdate == acpUpdateCurrentModeUpdate {
		a.handleCurrentModeUpdate(update)
		return true
	}
	return false
}

func (a *GeminiCLIAgent) handleCurrentModeUpdate(update json.RawMessage) {
	var mode struct {
		CurrentModeID string `json:"currentModeId"`
	}
	if json.Unmarshal(update, &mode) != nil || mode.CurrentModeID == "" {
		return
	}

	a.mu.Lock()
	a.permissionMode = mode.CurrentModeID
	a.mu.Unlock()
	a.sink.UpdatePermissionMode(mode.CurrentModeID)
}
