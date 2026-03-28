package agent

import (
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleOpenCodeOutput processes a single JSONL message from the OpenCode ACP server.
// Messages are stored in their native ACP JSON-RPC format.
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
		a.handleSessionUpdate(envelope.Params)

	case acpMethodSessionRequestPermission:
		a.handleRequestPermission(envelope.ID, content)

	default:
		// Persist unknown notifications so the frontend can decide how to render them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, SpanInfo{}); err != nil {
			slog.Error("opencode persist notification", "agent_id", a.agentID, "method", envelope.Method, "error", err)
		}
	}
}

// handleSessionUpdate dispatches sessionUpdate notifications by their type.
func (a *OpenCodeAgent) handleSessionUpdate(params json.RawMessage) {
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

	// Turn-end messages with role "result" are handled by handlePromptResponse
	// when the session/prompt RPC completes — skip them here to avoid duplicates.
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

	case acpUpdateUsageUpdate:
		a.handleUsageUpdate(wrapper.Update)

	case acpUpdatePlan:
		a.handlePlan(wrapper.Update)

	case acpUpdateUserMessageChunk, acpUpdateAvailableCommandsUpdate:
		// No-op: user_message_chunk is replay, available_commands_update is informational.

	default:
		// Persist unknown session updates.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, wrapper.Update, SpanInfo{}); err != nil {
			slog.Error("opencode persist unknown sessionUpdate", "agent_id", a.agentID, "type", header.SessionUpdate, "error", err)
		}
	}
}

// handleAgentMessageChunk processes agent_message_chunk — streaming text.
func (a *OpenCodeAgent) handleAgentMessageChunk(update json.RawMessage) {
	a.handleAgentChunk(update, &a.turnAssistantText, "agent_message_chunk")
}

// handleAgentThoughtChunk processes agent_thought_chunk — reasoning tokens.
// Thinking text is accumulated separately and persisted at turn end.
func (a *OpenCodeAgent) handleAgentThoughtChunk(update json.RawMessage) {
	a.handleAgentChunk(update, &a.turnThinkingText, "agent_thought_chunk")
}

// handleAgentChunk extracts text from a chunk update, appends it to the
// given builder (under lock), and broadcasts it as a stream chunk.
func (a *OpenCodeAgent) handleAgentChunk(update json.RawMessage, builder *strings.Builder, eventType string) {
	a.appendACPChunk(update, builder, a.sink, eventType)
}

// handleToolCall processes tool_call — a new tool invocation (status: pending).
func (a *OpenCodeAgent) handleToolCall(update json.RawMessage) {
	handleACPToolCall(a.agentID, a.sink, update)
}

// handleToolCallUpdate processes tool_call_update — progress or completion.
func (a *OpenCodeAgent) handleToolCallUpdate(update json.RawMessage) {
	a.handleACPToolCallUpdate(a.sink, update)
}

// handleUsageUpdate processes usage_update — token/cost reporting.
func (a *OpenCodeAgent) handleUsageUpdate(update json.RawMessage) {
	handleACPUsageUpdate(a.sink, update)
}

// handlePlan processes plan — todo list entries.
func (a *OpenCodeAgent) handlePlan(update json.RawMessage) {
	handleACPPlan(a.agentID, a.sink, update)
}

// handleRequestPermission processes requestPermission server requests.
// These are JSON-RPC requests from the server (have "id" + "method").
func (a *OpenCodeAgent) handleRequestPermission(id *json.Number, content []byte) {
	if id == nil {
		slog.Warn("opencode requestPermission missing id", "agent_id", a.agentID)
		return
	}
	requestID := id.String()
	a.sink.PersistControlRequest(requestID, content)
	a.sink.BroadcastControlRequest(requestID, content)
}
