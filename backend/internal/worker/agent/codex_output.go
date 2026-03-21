package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleCodexOutput processes a single JSONL notification from the Codex app-server.
// Codex messages are stored in their native JSON-RPC format.
func handleCodexOutput(a *CodexAgent, content []byte) {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		slog.Warn("invalid codex output JSON", "agent_id", a.agentID, "error", err)
		return
	}

	slog.Debug("codex HandleOutput", "agent_id", a.agentID, "method", envelope.Method, "len", len(content))

	switch envelope.Method {
	case "turn/started":
		a.handleTurnStarted(envelope.Params)

	case "item/agentMessage/delta":
		a.handleAgentMessageDelta(envelope.Params)

	case "item/started":
		a.handleItemStarted(envelope.Params)

	case "item/completed":
		a.handleItemCompleted(envelope.Params)

	case "turn/completed":
		a.handleTurnCompleted(envelope.Params)

	case "thread/tokenUsage/updated":
		a.handleTokenUsageUpdated(envelope.Params)

	case "thread/name/updated":
		a.handleThreadNameUpdated(envelope.Params)

	case "thread/status/changed":
		// Transient status signal (e.g. waitingOnApproval). Persisted for
		// record-keeping; the frontend hides these.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, ""); err != nil {
			slog.Error("codex persist thread/status/changed", "agent_id", a.agentID, "error", err)
		}

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval":
		a.handleApprovalRequest(envelope.ID, content)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(envelope.Params)

	case "error":
		a.handleErrorNotification(envelope.Params)

	default:
		// Forward unknown notifications as stream chunks for the frontend.
		a.sink.BroadcastStreamChunk(content)
	}
}

// handleTurnStarted processes turn/started notifications.
func (a *CodexAgent) handleTurnStarted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.Turn.ID != "" {
		a.mu.Lock()
		a.turnID = notif.Turn.ID
		threadID := a.threadID
		a.mu.Unlock()

		// Broadcast the turn ID so the frontend can use it for interrupts.
		a.sink.BroadcastSessionInfo(map[string]interface{}{
			"codexTurnId": notif.Turn.ID,
		})
		a.sink.BroadcastStatusActive(threadID)
		return
	}

	a.mu.Lock()
	threadID := a.threadID
	a.mu.Unlock()
	a.sink.BroadcastStatusActive(threadID)
}

// handleAgentMessageDelta processes item/agentMessage/delta — streaming text.
func (a *CodexAgent) handleAgentMessageDelta(params json.RawMessage) {
	var delta struct {
		Delta string `json:"delta"`
	}
	if json.Unmarshal(params, &delta) == nil && delta.Delta != "" {
		a.sink.BroadcastStreamChunk([]byte(delta.Delta))
	}
}

// handleItemStarted processes item/started notifications.
func (a *CodexAgent) handleItemStarted(params json.RawMessage) {
	item, itemType, itemID := extractCodexItem(params)
	if item == nil {
		return
	}

	switch itemType {
	case "agentMessage":
		// No-op for started — wait for completed to persist.
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		// Persist with itemID as threadID so the completed item can merge.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, itemID); err != nil {
			slog.Error("codex persist item/started", "agent_id", a.agentID, "type", itemType, "error", err)
		}
	case "reasoning":
		// No-op for started — wait for completed.
	}
}

// handleItemCompleted processes item/completed notifications.
func (a *CodexAgent) handleItemCompleted(params json.RawMessage) {
	item, itemType, itemID := extractCodexItem(params)
	if item == nil {
		return
	}

	// Non-notification messages soft-clear the notification thread.
	a.sink.SoftClearNotifThread()

	switch itemType {
	case "agentMessage":
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, ""); err != nil {
			slog.Error("codex persist agentMessage", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		// Try to merge into the started item.
		if itemID != "" && a.sink.MergeIntoThread(itemID, params) {
			return
		}
		// Fallback: persist standalone.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, itemID); err != nil {
			slog.Error("codex persist item/completed", "agent_id", a.agentID, "type", itemType, "error", err)
		}
	case "reasoning":
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, ""); err != nil {
			slog.Error("codex persist reasoning", "agent_id", a.agentID, "error", err)
		}
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, ""); err != nil {
			slog.Error("codex persist unknown item", "agent_id", a.agentID, "type", itemType, "error", err)
		}
	}
}

// handleTurnCompleted processes turn/completed notifications.
func (a *CodexAgent) handleTurnCompleted(params json.RawMessage) {
	// Persist as a result divider.
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, params, ""); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.agentID, "error", err)
	}

	// Extract usage from the turn data.
	var notif struct {
		Turn struct {
			Status string `json:"status"`
			Usage  *struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			} `json:"usage"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &notif) == nil {
		if notif.Turn.Status == "failed" {
			a.sink.BroadcastNotification(map[string]interface{}{
				"type":  "agent_error",
				"error": "Codex turn failed",
			})
		}
	}

	a.mu.Lock()
	a.turnID = ""
	a.mu.Unlock()

	// Clear the turn ID in session info.
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"codexTurnId": "",
	})
}

// handleTokenUsageUpdated processes thread/tokenUsage/updated notifications.
func (a *CodexAgent) handleTokenUsageUpdated(params json.RawMessage) {
	var notif struct {
		TokenUsage struct {
			Total struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			} `json:"total"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(params, &notif) != nil {
		return
	}

	usage := map[string]interface{}{
		"inputTokens":  notif.TokenUsage.Total.InputTokens,
		"outputTokens": notif.TokenUsage.Total.OutputTokens,
	}
	if notif.TokenUsage.ModelContextWindow != nil {
		usage["contextWindow"] = *notif.TokenUsage.ModelContextWindow
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"contextUsage": usage,
	})
}

// handleThreadNameUpdated processes thread/name/updated notifications.
func (a *CodexAgent) handleThreadNameUpdated(params json.RawMessage) {
	var notif struct {
		ThreadName *string `json:"threadName"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ThreadName != nil {
		a.sink.BroadcastNotification(map[string]interface{}{
			"type": "agent_renamed",
			"name": *notif.ThreadName,
		})
	}
}

// handleApprovalRequest processes server requests (approval requests from Codex).
// These arrive as JSON-RPC requests (with an "id" field) from the server.
// The id is already extracted from the outer envelope to avoid re-parsing content.
func (a *CodexAgent) handleApprovalRequest(id *json.Number, content []byte) {
	if id == nil {
		slog.Warn("codex approval request missing id", "agent_id", a.agentID)
		return
	}
	requestID := id.String()
	a.sink.PersistControlRequest(requestID, content)
	a.sink.BroadcastControlRequest(requestID, content)
}

// handleServerRequestResolved processes serverRequest/resolved notifications.
func (a *CodexAgent) handleServerRequestResolved(params json.RawMessage) {
	var notif struct {
		RequestID json.Number `json:"requestId"`
	}
	if json.Unmarshal(params, &notif) == nil {
		requestID := notif.RequestID.String()
		a.sink.DeleteControlRequest(requestID)
		a.sink.BroadcastControlCancel(requestID)
	}
}

// handleErrorNotification processes error notifications.
func (a *CodexAgent) handleErrorNotification(params json.RawMessage) {
	var notif struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.Message != "" {
		a.sink.BroadcastNotification(map[string]interface{}{
			"type":  "agent_error",
			"error": notif.Message,
		})
	}
}

// extractCodexItem extracts the item type and ID from item/started or item/completed params.
// Two unmarshals are needed: one to extract the raw item JSON, another to read type/ID from it.
func extractCodexItem(params json.RawMessage) (json.RawMessage, string, string) {
	var wrapper struct {
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(params, &wrapper) != nil || len(wrapper.Item) == 0 {
		return nil, "", ""
	}

	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(wrapper.Item, &header) != nil {
		return nil, "", ""
	}

	return wrapper.Item, header.Type, header.ID
}
