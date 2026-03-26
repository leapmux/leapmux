package agent

import (
	"encoding/json"
	"log/slog"

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
	case "session/update":
		a.handleSessionUpdate(envelope.Params)

	case "session/request_permission":
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
	case "agent_message_chunk":
		a.handleAgentMessageChunk(wrapper.Update)

	case "agent_thought_chunk":
		a.handleAgentThoughtChunk(wrapper.Update)

	case "tool_call":
		a.handleToolCall(wrapper.Update)

	case "tool_call_update":
		a.handleToolCallUpdate(wrapper.Update)

	case "usage_update":
		a.handleUsageUpdate(wrapper.Update)

	case "plan":
		a.handlePlan(wrapper.Update)

	case "user_message_chunk", "available_commands_update":
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
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &chunk) == nil && chunk.Content.Text != "" {
		a.mu.Lock()
		a.turnAssistantText += chunk.Content.Text
		a.mu.Unlock()
		a.sink.BroadcastStreamChunk([]byte(chunk.Content.Text), "", "agent_message_chunk")
	}
}

// handleAgentThoughtChunk processes agent_thought_chunk — reasoning tokens.
// Thinking text is accumulated separately and persisted at turn end.
func (a *OpenCodeAgent) handleAgentThoughtChunk(update json.RawMessage) {
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &chunk) == nil && chunk.Content.Text != "" {
		a.mu.Lock()
		a.turnThinkingText += chunk.Content.Text
		a.mu.Unlock()
		a.sink.BroadcastStreamChunk([]byte(chunk.Content.Text), "", "agent_thought_chunk")
	}
}

// handleToolCall processes tool_call — a new tool invocation (status: pending).
func (a *OpenCodeAgent) handleToolCall(update json.RawMessage) {
	var tc struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
	}
	if json.Unmarshal(update, &tc) != nil || tc.ToolCallID == "" {
		return
	}

	a.sink.SoftClearNotifThread()

	spanType := tc.Kind
	if spanType == "" {
		spanType = "tool_call"
	}
	spanColor := a.sink.PeekNextSpanColor()

	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
		SpanID: tc.ToolCallID, SpanType: spanType, SpanColor: spanColor,
	}); err != nil {
		slog.Error("opencode persist tool_call", "agent_id", a.agentID, "kind", tc.Kind, "error", err)
	}
	a.sink.SetSpanType(tc.ToolCallID, spanType)
	a.sink.OpenSpan(tc.ToolCallID, "")
}

// handleToolCallUpdate processes tool_call_update — progress or completion.
func (a *OpenCodeAgent) handleToolCallUpdate(update json.RawMessage) {
	var tcu struct {
		ToolCallID string `json:"toolCallId"`
		Status     string `json:"status"`
	}
	if json.Unmarshal(update, &tcu) != nil || tcu.ToolCallID == "" {
		return
	}

	switch tcu.Status {
	case "in_progress":
		a.sink.BroadcastStreamChunk(update, tcu.ToolCallID, "tool_call_update")

	case "completed", "failed":
		a.sink.SoftClearNotifThread()

		a.mu.Lock()
		a.turnToolUses++
		a.mu.Unlock()

		spanType := a.sink.GetSpanType(tcu.ToolCallID)
		if spanType == "" {
			spanType = "tool_call"
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{
			SpanID: tcu.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("opencode persist tool_call_update", "agent_id", a.agentID, "status", tcu.Status, "error", err)
		}
		a.sink.BroadcastStreamEnd(tcu.ToolCallID)
		a.sink.CloseSpan(tcu.ToolCallID)
	}
}

// handleUsageUpdate processes usage_update — token/cost reporting.
func (a *OpenCodeAgent) handleUsageUpdate(update json.RawMessage) {
	var usage struct {
		Used int64 `json:"used"`
		Size int64 `json:"size"`
		Cost struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if json.Unmarshal(update, &usage) != nil {
		return
	}

	info := map[string]interface{}{
		"contextUsage": map[string]interface{}{
			"inputTokens":              usage.Used,
			"cacheCreationInputTokens": int64(0),
			"cacheReadInputTokens":     int64(0),
			"outputTokens":             int64(0),
			"contextWindow":            usage.Size,
		},
	}
	if usage.Cost.Amount > 0 {
		info["totalCostUsd"] = usage.Cost.Amount
	}
	a.sink.BroadcastSessionInfo(info)
}

// handlePlan processes plan — todo list entries.
func (a *OpenCodeAgent) handlePlan(update json.RawMessage) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, update, SpanInfo{}); err != nil {
		slog.Error("opencode persist plan", "agent_id", a.agentID, "error", err)
	}
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
