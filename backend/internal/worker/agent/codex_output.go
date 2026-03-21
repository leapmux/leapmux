package agent

import (
	"encoding/json"
	"fmt"
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

	case "item/plan/delta":
		a.handlePlanDelta(envelope.Params)

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

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"item/tool/requestUserInput":
		a.handleApprovalRequest(envelope.ID, content)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(envelope.Params)

	case "account/rateLimits/updated":
		a.handleRateLimitsUpdated(content, envelope.Params)

	case "error":
		a.handleErrorNotification(envelope.Params)

	default:
		// Persist unknown notifications so the frontend can decide how to render them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, content, ""); err != nil {
			slog.Error("codex persist notification", "agent_id", a.agentID, "method", envelope.Method, "error", err)
		}
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
		a.turnToolUses = 0
		a.turnSawPlan = false
		a.turnPlanText = ""
		a.turnAssistantText = ""
		a.streamingPlan = false
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

// handlePlanDelta processes item/plan/delta — streaming plan text.
func (a *CodexAgent) handlePlanDelta(params json.RawMessage) {
	var delta struct {
		Delta string `json:"delta"`
	}
	if json.Unmarshal(params, &delta) == nil && delta.Delta != "" {
		a.mu.Lock()
		if !a.streamingPlan {
			a.streamingPlan = true
			a.mu.Unlock()
			a.sink.BroadcastSessionInfo(map[string]interface{}{
				"streamingType": "plan",
			})
		} else {
			a.mu.Unlock()
		}
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
		var messageItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &messageItem) == nil && messageItem.Text != "" {
			a.mu.Lock()
			a.turnAssistantText = messageItem.Text
			a.mu.Unlock()
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, ""); err != nil {
			slog.Error("codex persist agentMessage", "agent_id", a.agentID, "error", err)
		}
	case "plan":
		var planItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &planItem) == nil && planItem.Text != "" {
			a.mu.Lock()
			a.turnSawPlan = true
			a.turnPlanText = planItem.Text
			wasStreamingPlan := a.streamingPlan
			a.streamingPlan = false
			a.mu.Unlock()
			if wasStreamingPlan {
				a.sink.BroadcastSessionInfo(map[string]interface{}{
					"streamingType": "",
				})
			}
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, ""); err != nil {
			slog.Error("codex persist plan", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		a.mu.Lock()
		a.turnToolUses++
		a.mu.Unlock()
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
	// Enrich the params with num_tool_uses so the frontend can distinguish
	// simple text-only exchanges from complex multi-tool turns.
	a.mu.Lock()
	numToolUses := a.turnToolUses
	sawPlan := a.turnSawPlan
	planText := a.turnPlanText
	assistantText := a.turnAssistantText
	collaborationMode := a.collaborationMode
	a.mu.Unlock()

	enriched := make(map[string]json.RawMessage)
	if err := json.Unmarshal(params, &enriched); err == nil {
		if b, err := json.Marshal(numToolUses); err == nil {
			enriched["num_tool_uses"] = b
		}
		if b, err := json.Marshal(enriched); err == nil {
			params = b
		}
	}

	// Persist as a result divider.
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, params, ""); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.agentID, "error", err)
	}

	// Extract usage from the turn data.
	var notif struct {
		Turn struct {
			ID     string `json:"id"`
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
		promptText := planText
		if promptText == "" {
			promptText = assistantText
		}
		if notif.Turn.Status == "completed" && collaborationMode == "plan" && (sawPlan || promptText != "") {
			requestID := fmt.Sprintf("codex-plan-prompt-%s", notif.Turn.ID)
			payload, err := json.Marshal(map[string]interface{}{
				"type":       "control_request",
				"request_id": requestID,
				"request": map[string]interface{}{
					"tool_name": "CodexPlanModePrompt",
					"input":     map[string]interface{}{},
				},
			})
			if err == nil {
				a.sink.PersistControlRequest(requestID, payload)
				a.sink.BroadcastControlRequest(requestID, payload)
			}
		}
	}

	a.mu.Lock()
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
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

// handleRateLimitsUpdated processes account/rateLimits/updated notifications.
// The raw content is persisted as-is via PersistNotification, and converted
// rate limit info is broadcast via BroadcastSessionInfo for the live popover.
func (a *CodexAgent) handleRateLimitsUpdated(content []byte, params json.RawMessage) {
	// Persist the raw Codex notification in the notification thread.
	if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
		slog.Error("codex persist rateLimits", "agent_id", a.agentID, "error", err)
	}

	// Extract and convert tiers for live session info broadcast.
	var notif struct {
		RateLimits struct {
			Primary   *codexRateLimitTier `json:"primary"`
			Secondary *codexRateLimitTier `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(params, &notif) != nil {
		return
	}

	rateLimits := map[string]interface{}{}
	for _, tier := range []*codexRateLimitTier{notif.RateLimits.Primary, notif.RateLimits.Secondary} {
		if tier == nil {
			continue
		}
		rlType := codexWindowToType(tier.WindowDurationMins)
		status := "allowed"
		if tier.UsedPercent >= 100 {
			status = "exceeded"
		} else if tier.UsedPercent >= 80 {
			status = "allowed_warning"
		}
		info := map[string]interface{}{
			"rateLimitType": rlType,
			"utilization":   float64(tier.UsedPercent) / 100,
			"status":        status,
		}
		if tier.ResetsAt != nil {
			info["resetsAt"] = *tier.ResetsAt
		}
		rateLimits[rlType] = info
	}
	if len(rateLimits) > 0 {
		a.sink.BroadcastSessionInfo(map[string]interface{}{
			"rateLimits": rateLimits,
		})
	}
}

// codexRateLimitTier represents a single tier from Codex rate limit data.
type codexRateLimitTier struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// codexWindowToType maps a Codex window duration (minutes) to a rate limit type string.
func codexWindowToType(mins int) string {
	switch mins {
	case 300:
		return "five_hour"
	case 10080:
		return "seven_day"
	default:
		if mins >= 1440 {
			days := (mins + 720) / 1440 // round
			return fmt.Sprintf("%d_day", days)
		}
		hours := (mins + 30) / 60 // round
		return fmt.Sprintf("%d_hour", hours)
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
