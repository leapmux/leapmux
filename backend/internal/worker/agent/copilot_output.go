package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func handleCopilotCLIOutput(a *CopilotCLIAgent, line *parsedLine) {
	slog.Debug("copilot HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))

	switch line.Method {
	case acpMethodSessionUpdate:
		a.handleSessionUpdate(line.Params)
	case acpMethodSessionRequestPermission:
		a.handleRequestPermission(line.ID, line.Raw)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("copilot persist notification", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
	}
}

func (a *CopilotCLIAgent) handleSessionUpdate(params json.RawMessage) {
	a.handleACPSessionUpdate(params, func(sessionUpdate string, update json.RawMessage) bool {
		if sessionUpdate == acpUpdateConfigOptionUpdate {
			a.handleConfigOptionUpdate(update)
			return true
		}
		return false
	})
}

func (a *CopilotCLIAgent) handleConfigOptionUpdate(update json.RawMessage) {
	var payload struct {
		ConfigOptions []copilotCLIConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(update, &payload) != nil || len(payload.ConfigOptions) == 0 {
		return
	}

	if mode := a.syncConfigOptions(payload.ConfigOptions); mode != "" {
		a.sink.UpdatePermissionMode(mode)
	}
}
