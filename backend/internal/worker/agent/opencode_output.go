package agent

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleOpenCodeOutput processes a single parsed JSONL message from the OpenCode ACP server.
func handleOpenCodeOutput(a *OpenCodeAgent, line *parsedLine) {
	slog.Debug("opencode HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))

	switch line.Method {
	case acpMethodSessionUpdate:
		a.handleACPSessionUpdate(line.Params, nil)
	case acpMethodSessionRequestPermission:
		a.handleRequestPermission(line.ID, line.Raw)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("opencode persist notification", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
	}
}
