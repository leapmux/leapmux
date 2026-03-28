package agent

import (
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

var geminiToolCallUpdatePersistStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"cancelled": true,
}

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
		a.handleRequestPermission(envelope.ID, content)
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, SpanInfo{}); err != nil {
			slog.Error("gemini persist notification", "agent_id", a.agentID, "method", envelope.Method, "error", err)
		}
	}
}

func (a *GeminiCLIAgent) handleSessionUpdate(params json.RawMessage) {
	var wrapper struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrapper) != nil || len(wrapper.Update) == 0 {
		return
	}

	var header struct {
		SessionUpdate string `json:"sessionUpdate"`
		Role          string `json:"role"`
	}
	if json.Unmarshal(wrapper.Update, &header) != nil {
		return
	}

	if header.Role == "result" {
		return
	}

	switch header.SessionUpdate {
	case acpUpdateAgentMessageChunk:
		a.handleAgentMessageChunk(wrapper.Update)
	case acpUpdateAgentThoughtChunk:
		a.handleAgentThoughtChunk(wrapper.Update)
	case acpUpdateToolCall:
		a.handleToolCall(wrapper.Update)
	case acpUpdateToolCallUpdate:
		a.handleToolCallUpdate(wrapper.Update)
	case acpUpdatePlan:
		a.handlePlan(wrapper.Update)
	case acpUpdateUsageUpdate:
		handleACPUsageUpdate(a.sink, wrapper.Update)
	case "current_mode_update":
		a.handleCurrentModeUpdate(wrapper.Update)
	case acpUpdateAvailableCommandsUpdate, acpUpdateUserMessageChunk:
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, wrapper.Update, SpanInfo{}); err != nil {
			slog.Error("gemini persist unknown sessionUpdate", "agent_id", a.agentID, "type", header.SessionUpdate, "error", err)
		}
	}
}

func (a *GeminiCLIAgent) handleAgentMessageChunk(update json.RawMessage) {
	a.handleAgentChunk(update, &a.turnAssistantText, "agent_message_chunk")
}

func (a *GeminiCLIAgent) handleAgentThoughtChunk(update json.RawMessage) {
	a.handleAgentChunk(update, &a.turnThinkingText, "agent_thought_chunk")
}

func (a *GeminiCLIAgent) handleAgentChunk(update json.RawMessage, builder *strings.Builder, eventType string) {
	appendACPChunk(update, builder, &a.mu, a.sink, eventType)
}

func (a *GeminiCLIAgent) handleToolCall(update json.RawMessage) {
	handleACPToolCall(a.agentID, a.sink, update)
}

func (a *GeminiCLIAgent) handleToolCallUpdate(update json.RawMessage) {
	handleACPToolCallUpdate(a.agentID, a.sink, &a.mu, &a.turnToolUses, update, geminiToolCallUpdatePersistStatuses)
}

func (a *GeminiCLIAgent) handlePlan(update json.RawMessage) {
	handleACPPlan(a.agentID, a.sink, update)
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

func (a *GeminiCLIAgent) handleRequestPermission(id *json.Number, content []byte) {
	if id == nil {
		slog.Warn("gemini requestPermission missing id", "agent_id", a.agentID)
		return
	}
	requestID := id.String()
	a.sink.PersistControlRequest(requestID, content)
	a.sink.BroadcastControlRequest(requestID, content)
}
