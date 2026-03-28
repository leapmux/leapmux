package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleOpenCodeOutput processes a single JSONL message from the OpenCode ACP server.
func handleOpenCodeOutput(a *OpenCodeAgent, content []byte) {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		slog.Warn("invalid opencode output JSON", "agent_id", a.agentID, "error", err)
		return
	}

	slog.Debug("opencode HandleOutput", "agent_id", a.agentID, "method", envelope.Method, "len", len(content))

	switch envelope.Method {
	case acpMethodSessionUpdate:
		a.handleACPSessionUpdate(envelope.Params, nil)
	case acpMethodSessionRequestPermission:
		handleACPRequestPermission(a.agentID, a.sink, envelope.ID, content)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, SpanInfo{}); err != nil {
			slog.Error("opencode persist notification", "agent_id", a.agentID, "method", envelope.Method, "error", err)
		}
	}
}
