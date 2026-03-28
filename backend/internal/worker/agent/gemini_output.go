package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleGeminiCLIOutput processes a single JSONL message from the Gemini CLI ACP server.
func handleGeminiCLIOutput(a *GeminiCLIAgent, content []byte) {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		slog.Warn("invalid gemini output JSON", "agent_id", a.agentID, "error", err)
		return
	}

	slog.Debug("gemini HandleOutput", "agent_id", a.agentID, "method", envelope.Method, "len", len(content))

	switch envelope.Method {
	case geminiMethodSessionUpdate, acpMethodSessionUpdate:
		a.handleSessionUpdate(envelope.Params)
	case geminiMethodRequestPermission, acpMethodSessionRequestPermission:
		handleACPRequestPermission(a.agentID, a.sink, envelope.ID, content)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, SpanInfo{}); err != nil {
			slog.Error("gemini persist notification", "agent_id", a.agentID, "method", envelope.Method, "error", err)
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
