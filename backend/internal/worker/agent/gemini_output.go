package agent

import (
	"encoding/json"
	"log/slog"
)

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
	if err := json.Unmarshal(update, &mode); err != nil {
		slog.Warn("gemini current mode update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if mode.CurrentModeID == "" {
		return
	}

	a.mu.Lock()
	a.permissionMode = mode.CurrentModeID
	a.mu.Unlock()
	a.sink.UpdatePermissionMode(mode.CurrentModeID)
}
