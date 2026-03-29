package agent

import (
	"encoding/json"
	"log/slog"
)

func (a *GeminiCLIAgent) handleOutput(line *parsedLine) {
	slog.Debug("gemini HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))
	a.handleACPOutput(line, a.handleExtraSessionUpdate)
}

func (a *GeminiCLIAgent) HandleOutput(content []byte) {
	a.handleOutput(parseLine(content))
}

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

func (a *GeminiCLIAgent) cancelSession() error {
	return a.acpCancelSession()
}
