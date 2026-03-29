package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleGeminiCLIOutput processes a single parsed JSONL message from the Gemini CLI ACP server.
func handleGeminiCLIOutput(a *GeminiCLIAgent, line *parsedLine) {
	slog.Debug("gemini HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))

	switch line.Method {
	case acpMethodSessionUpdate:
		a.handleSessionUpdate(line.Params)
	case acpMethodSessionRequestPermission:
		a.handleRequestPermission(line.ID, line.Raw)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("gemini persist notification", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
	}
}

func (a *GeminiCLIAgent) handleSessionUpdate(params json.RawMessage) {
	a.handleACPSessionUpdate(params, func(sessionUpdate string, update json.RawMessage) bool {
		if sessionUpdate == acpUpdateCurrentModeUpdate {
			a.handleCurrentModeUpdate(update)
			return true
		}
		return false
	})
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
