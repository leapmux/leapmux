package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

const codexRetryableDisconnectPrefix = "stream disconnected before completion:"

// handleCodexOutput processes a single parsed JSONL notification from the Codex app-server.
// Codex messages are stored in their native JSON-RPC format.
func handleCodexOutput(a *CodexAgent, line *parsedLine) {
	slog.Debug("codex HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))

	switch line.Method {
	case "turn/started":
		a.handleTurnStarted(line.Params)

	case "item/agentMessage/delta":
		a.handleAgentMessageDelta(line.Params)

	case "item/plan/delta":
		a.handlePlanDelta(line.Params)

	case "item/reasoning/summaryTextDelta":
		a.handleReasoningSummaryTextDelta(line.Params)

	case "item/reasoning/summaryPartAdded":
		a.handleReasoningSummaryPartAdded(line.Params)

	case "item/reasoning/textDelta":
		a.handleReasoningTextDelta(line.Params)

	case "item/commandExecution/outputDelta":
		a.handleCommandExecutionOutputDelta(line.Params)

	case "item/commandExecution/terminalInteraction":
		a.handleCommandExecutionTerminalInteraction(line.Params)

	case "item/fileChange/outputDelta":
		a.handleFileChangeOutputDelta(line.Params)

	case "item/started":
		a.handleItemStarted(line.Params)

	case "item/completed":
		a.handleItemCompleted(line.Params)

	case "thread/compacted":
		a.handleThreadCompacted(line.Params)

	case "turn/completed":
		a.handleTurnCompleted(line.Params)

	case "thread/tokenUsage/updated":
		a.handleTokenUsageUpdated(line.Raw, line.Params)

	case "thread/name/updated":
		a.handleThreadNameUpdated(line.Params)

	// Server requests (approval requests) — the server sends these as JSON-RPC
	// requests with an "id" field, but we detect them here by method name when
	// they arrive as notifications in the output stream.
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"item/tool/requestUserInput":
		a.handleApprovalRequest(line.ID, line.Raw)

	case "serverRequest/resolved":
		a.handleServerRequestResolved(line.Params)

	case "account/rateLimits/updated":
		a.handleRateLimitsUpdated(line.Raw, line.Params)

	case "mcpServer/startupStatus/updated":
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, line.Raw); err != nil {
			slog.Error("codex persist startup status notification", "agent_id", a.agentID, "error", err)
		}

	case "error":
		a.handleErrorNotification(line.Params)

	default:
		// Persist unknown notifications so the frontend can decide how to render them.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("codex persist notification", "agent_id", a.agentID, "method", line.Method, "error", err)
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
		a.sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/agentMessage/delta")
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
		a.sink.BroadcastStreamChunk([]byte(delta.Delta), "", "item/plan/delta")
	}
}

func (a *CodexAgent) handleReasoningSummaryTextDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/summaryTextDelta")
	}
}

func (a *CodexAgent) handleReasoningSummaryPartAdded(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" {
		a.sink.BroadcastStreamChunk(nil, notif.ItemID, "item/reasoning/summaryPartAdded")
	}
}

func (a *CodexAgent) handleReasoningTextDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/reasoning/textDelta")
	}
}

func (a *CodexAgent) handleCommandExecutionOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/commandExecution/outputDelta")
	}
}

func (a *CodexAgent) handleCommandExecutionTerminalInteraction(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Stdin  string `json:"stdin"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Stdin != "" {
		a.sink.BroadcastStreamChunk([]byte(notif.Stdin), notif.ItemID, "item/commandExecution/terminalInteraction")
	}
}

func (a *CodexAgent) handleFileChangeOutputDelta(params json.RawMessage) {
	var notif struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &notif) == nil && notif.ItemID != "" && notif.Delta != "" {
		a.sink.BroadcastStreamChunk([]byte(notif.Delta), notif.ItemID, "item/fileChange/outputDelta")
	}
}

// handleItemStarted processes item/started notifications.
func (a *CodexAgent) handleItemStarted(params json.RawMessage) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return
	}

	parentSpanID := a.codexVisibleParentSpanID(threadID)
	var collab *codexCollabAgentToolCall
	if itemType == "collabAgentToolCall" {
		collab = parseCollabToolCall(item)
		parentSpanID = a.codexCollabParentSpanID(parentSpanID, collab, itemID, false)
	}

	switch itemType {
	case "agentMessage":
		// No-op for started — wait for completed to persist.
	case "contextCompaction":
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, []byte(`{"type":"compacting"}`)); err != nil {
			slog.Error("codex persist compacting notification", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		// Pre-peek the span color before persisting so it is recorded with the message.
		spanColor := a.sink.PeekNextSpanColor()
		// Persist first at parent depth, then open span so the
		// completed message is indented under the started message.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType, SpanColor: spanColor,
		}); err != nil {
			slog.Error("codex persist item/started", "agent_id", a.agentID, "type", itemType, "error", err)
		}
		a.sink.SetSpanType(itemID, itemType)
		a.sink.OpenSpan(itemID, parentSpanID)
	case "collabAgentToolCall":
		spanColor := a.sink.PeekNextSpanColor()
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType, SpanColor: spanColor,
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/started", "agent_id", a.agentID, "error", err)
		}
		a.sink.SetSpanType(itemID, itemType)
		a.handleCollabAgentSpan(collab, itemID, parentSpanID, false)
	case "reasoning":
		// No-op for started — wait for completed.
	}
}

// handleItemCompleted processes item/completed notifications.
func (a *CodexAgent) handleItemCompleted(params json.RawMessage) {
	item, itemType, itemID, threadID := extractCodexItem(params)
	if item == nil {
		return
	}

	parentSpanID := a.codexVisibleParentSpanID(threadID)
	var collab *codexCollabAgentToolCall
	if itemType == "collabAgentToolCall" {
		collab = parseCollabToolCall(item)
		parentSpanID = a.codexCollabParentSpanID(parentSpanID, collab, itemID, true)
	}

	switch itemType {
	case "agentMessage":
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist agentMessage", "agent_id", a.agentID, "error", err)
		}
	case "plan":
		a.mu.Lock()
		a.turnSawPlan = true
		wasStreamingPlan := a.streamingPlan
		a.streamingPlan = false
		a.mu.Unlock()
		if wasStreamingPlan {
			a.sink.BroadcastSessionInfo(map[string]interface{}{
				"streamingType": "",
			})
		}

		var planItem struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &planItem) == nil && planItem.Text != "" {
			a.mu.Lock()
			a.turnPlanText = planItem.Text
			a.mu.Unlock()
		}
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist plan", "agent_id", a.agentID, "error", err)
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
		a.mu.Lock()
		a.turnToolUses++
		a.mu.Unlock()
		// Persist inside the span (at child depth), then close it.
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType, Closing: true,
		}); err != nil {
			slog.Error("codex persist item/completed", "agent_id", a.agentID, "type", itemType, "error", err)
		}
		if itemType == "commandExecution" || itemType == "fileChange" || itemType == "reasoning" {
			a.sink.BroadcastStreamEnd(itemID)
		}
		a.sink.CloseSpan(itemID)
	case "collabAgentToolCall":
		closingParentSpanID := a.closingCollabParentSpanID(collab, parentSpanID, true)
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, ConnectorSpanID: closingParentSpanID, SpanID: itemID, SpanType: itemType, Closing: closingParentSpanID != "",
		}); err != nil {
			slog.Error("codex persist collabAgentToolCall/completed", "agent_id", a.agentID, "error", err)
		}
		a.handleCollabAgentSpan(collab, itemID, parentSpanID, true)
	case "reasoning":
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist reasoning", "agent_id", a.agentID, "error", err)
		}
		a.sink.BroadcastStreamEnd(itemID)
	case "contextCompaction":
		// No-op: completion is represented by thread/compacted, which is emitted as
		// a LEAPMUX notification-thread boundary instead of an assistant message.
	default:
		if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, params, SpanInfo{
			ParentSpanID: parentSpanID, SpanID: itemID, SpanType: itemType,
		}); err != nil {
			slog.Error("codex persist unknown item", "agent_id", a.agentID, "type", itemType, "error", err)
		}
	}
}

func (a *CodexAgent) handleThreadCompacted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex thread_compacted unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	content, err := json.Marshal(map[string]interface{}{
		"type":     "system",
		"subtype":  "compact_boundary",
		"threadId": notif.ThreadID,
		"turnId":   notif.TurnID,
	})
	if err != nil {
		return
	}
	if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
		slog.Error("codex persist compacted notification", "agent_id", a.agentID, "error", err)
	}
}

// handleTurnCompleted processes turn/completed notifications.
func (a *CodexAgent) handleTurnCompleted(params json.RawMessage) {
	var notif struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &notif) == nil && !a.isMainThreadID(notif.ThreadID) {
		return
	}

	// Enrich the params with num_tool_uses so the frontend can distinguish
	// simple text-only exchanges from complex multi-tool turns.
	a.mu.Lock()
	numToolUses := a.turnToolUses
	sawPlan := a.turnSawPlan
	planText := a.turnPlanText
	collaborationMode := a.collaborationMode
	a.mu.Unlock()

	// Parse once: enrich with num_tool_uses and extract turn data
	// from the same map to avoid a second json.Unmarshal.
	var turnStatus, turnID, turnErrorMessage string
	parsed := make(map[string]json.RawMessage)
	if err := json.Unmarshal(params, &parsed); err == nil {
		if b, err := json.Marshal(numToolUses); err == nil {
			parsed["num_tool_uses"] = b
		}
		if turnRaw, ok := parsed["turn"]; ok {
			var turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(turnRaw, &turn) == nil {
				turnStatus = turn.Status
				turnID = turn.ID
				turnErrorMessage = turn.Error.Message
			}
		}
		if b, err := json.Marshal(parsed); err == nil {
			params = json.RawMessage(b)
		}
	}

	// Persist as a result divider.
	if err := a.sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, params, SpanInfo{}); err != nil {
		slog.Error("codex persist turn/completed", "agent_id", a.agentID, "error", err)
	}

	// Reset all span tracking at turn-end so the next turn starts clean.
	a.sink.ResetSpans()
	a.resetCollabReceivers()

	if turnStatus != "" {
		if turnStatus == "failed" {
			if isRetryableCodexTurnFailure(turnErrorMessage) {
				a.sink.ScheduleAutoContinue(AutoContinueSchedule{
					Reason:        AutoContinueReasonAPIError,
					DueAt:         time.Now().UTC(),
					SourcePayload: append([]byte(nil), params...),
				})
			} else {
				a.sink.CancelAutoContinue(AutoContinueReasonAPIError)
			}
		} else {
			a.sink.CancelAutoContinue(AutoContinueReasonAPIError)
		}
		if turnStatus == "completed" && collaborationMode == CodexCollaborationPlan && sawPlan && planText != "" {
			// Persist plan content so initiatePlanExecution can use it.
			compressed, compression := msgcodec.Compress([]byte(planText))
			a.sink.UpdatePlan("", compressed, compression, extractPlanTitle(planText))
			requestID := fmt.Sprintf("codex-plan-prompt-%s", turnID)
			payload, err := json.Marshal(map[string]interface{}{
				"type":       "control_request",
				"request_id": requestID,
				"request": map[string]interface{}{
					"tool_name": ToolNameCodexPlanModePrompt,
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
	a.mu.Unlock()

	// Clear the turn ID in session info.
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"codexTurnId": "",
	})
}

func isRetryableCodexTurnFailure(message string) bool {
	return strings.HasPrefix(message, codexRetryableDisconnectPrefix)
}

// handleTokenUsageUpdated processes thread/tokenUsage/updated notifications.
func (a *CodexAgent) handleTokenUsageUpdated(content []byte, params json.RawMessage) {
	var notif struct {
		ThreadID   string `json:"threadId"`
		TurnID     string `json:"turnId"`
		TokenUsage struct {
			Last struct {
				InputTokens       int64 `json:"inputTokens"`
				CachedInputTokens int64 `json:"cachedInputTokens"`
				OutputTokens      int64 `json:"outputTokens"`
			} `json:"last"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex token_usage_updated unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if !a.isMainThreadID(notif.ThreadID) {
		return
	}

	// Persist the raw Codex notification so reconnect/catch-up can rehydrate
	// context usage from history.
	if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
		slog.Error("codex persist tokenUsage", "agent_id", a.agentID, "error", err)
	}

	usage := map[string]interface{}{
		"inputTokens":              max(notif.TokenUsage.Last.InputTokens-notif.TokenUsage.Last.CachedInputTokens, 0),
		"cacheCreationInputTokens": int64(0),
		"cacheReadInputTokens":     notif.TokenUsage.Last.CachedInputTokens,
		"outputTokens":             notif.TokenUsage.Last.OutputTokens,
	}
	if notif.TokenUsage.ModelContextWindow != nil {
		usage["contextWindow"] = *notif.TokenUsage.ModelContextWindow
	} else if cw := modelContextWindow(a.availableModels, a.model); cw > 0 {
		usage["contextWindow"] = cw
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"contextUsage": usage,
	})
}

func (a *CodexAgent) isMainThreadID(threadID string) bool {
	if threadID == "" {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return threadID == a.threadID
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
// For user-initiated responses the control request is already deleted by the
// SendControlResponse handler, but this also covers agent-initiated
// resolutions (e.g. the agent moves on without waiting for user input).
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
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex rate limit unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	rateLimits := map[string]interface{}{}
	var latestExceededReset *time.Time
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
			if status == "exceeded" {
				resetAt := time.Unix(*tier.ResetsAt, 0).UTC()
				if latestExceededReset == nil || resetAt.After(*latestExceededReset) {
					latestExceededReset = &resetAt
				}
			}
		}
		rateLimits[rlType] = info
	}
	if len(rateLimits) > 0 {
		a.sink.BroadcastSessionInfo(map[string]interface{}{
			"rateLimits": rateLimits,
		})
	}

	if latestExceededReset != nil {
		a.sink.ScheduleAutoContinue(AutoContinueSchedule{
			Reason:        AutoContinueReasonRateLimit,
			DueAt:         *latestExceededReset,
			SourcePayload: append([]byte(nil), content...),
		})
	} else {
		a.sink.CancelAutoContinue(AutoContinueReasonRateLimit)
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

type codexCollabAgentState struct {
	Status string `json:"status"`
}

type codexCollabAgentToolCall struct {
	Tool              string                           `json:"tool"`
	Status            string                           `json:"status"`
	ReceiverThreadIds []string                         `json:"receiverThreadIds"`
	AgentsStates      map[string]codexCollabAgentState `json:"agentsStates"`
}

// handleCollabAgentSpan updates span lifecycle for CollabAgentToolCall items.
// Spawned subagent spans stay open after spawn completion and only close when a
// later terminal lifecycle event proves the agent is done.
func (a *CodexAgent) handleCollabAgentSpan(collab *codexCollabAgentToolCall, itemID, parentSpanID string, isCompleted bool) {
	if collab == nil {
		return
	}

	switch collab.Tool {
	case "spawnAgent":
		if !isCompleted {
			a.sink.OpenSpan(itemID, parentSpanID)
		}
		// Register receiver thread IDs as soon as they are available. Some
		// spawnAgent started messages do not include them yet, and they only
		// appear on completion.
		for _, receiverID := range collab.ReceiverThreadIds {
			a.registerCollabReceiver(receiverID, itemID)
		}
		// Do not close spans here. spawnAgent completion only means the child
		// thread was launched successfully, not that it finished its work.
	case "wait":
		if !isCompleted {
			return
		}
		for _, receiverID := range collab.ReceiverThreadIds {
			state, ok := collab.AgentsStates[receiverID]
			if !ok || !isTerminalCollabAgentStatus(state.Status) {
				continue
			}
			a.unregisterCollabReceiver(receiverID)
		}
		if parentSpanID != "" && !a.hasCollabReceivers(parentSpanID) {
			a.sink.CloseSpan(parentSpanID)
		}
	case "closeAgent":
		if !isCompleted || collab.Status != "completed" {
			return
		}
		for _, receiverID := range collab.ReceiverThreadIds {
			a.unregisterCollabReceiver(receiverID)
		}
		if parentSpanID != "" && !a.hasCollabReceivers(parentSpanID) {
			a.sink.CloseSpan(parentSpanID)
		}
	}
}

func (a *CodexAgent) closingCollabParentSpanID(collab *codexCollabAgentToolCall, parentSpanID string, isCompleted bool) string {
	if collab == nil || !isCompleted || parentSpanID == "" {
		return ""
	}

	switch collab.Tool {
	case "wait":
		if a.willDrainCollabParent(parentSpanID, collab.ReceiverThreadIds, collab.AgentsStates, false) {
			return parentSpanID
		}
	case "closeAgent":
		if collab.Status == "completed" && a.willDrainCollabParent(parentSpanID, collab.ReceiverThreadIds, collab.AgentsStates, true) {
			return parentSpanID
		}
	}

	return ""
}

func isTerminalCollabAgentStatus(status string) bool {
	switch status {
	case "completed", "errored", "shutdown", "notFound":
		return true
	default:
		return false
	}
}

func parseCollabToolCall(item json.RawMessage) *codexCollabAgentToolCall {
	var collab codexCollabAgentToolCall
	if err := json.Unmarshal(item, &collab); err != nil {
		slog.Warn("codex collab tool call unmarshal failed", "error", err)
		return nil
	}
	return &collab
}

// codexVisibleParentSpanID resolves the parent span for a message. It maps
// child thread IDs to their owning collab span. Main-thread or empty thread
// IDs return "" (root scope).
func (a *CodexAgent) codexVisibleParentSpanID(threadID string) string {
	if threadID == "" {
		return ""
	}
	a.mu.Lock()
	mainThreadID := a.threadID
	spanID := a.collabThreadSpans[threadID]
	a.mu.Unlock()
	if threadID == mainThreadID {
		return ""
	}
	return spanID
}

func (a *CodexAgent) codexCollabParentSpanID(defaultParentSpanID string, collab *codexCollabAgentToolCall, itemID string, isCompleted bool) string {
	if collab == nil {
		return defaultParentSpanID
	}

	switch collab.Tool {
	case "spawnAgent":
		if isCompleted {
			if spanID := a.collabSpanIDForReceivers(collab.ReceiverThreadIds); spanID != "" {
				return spanID
			}
			return itemID
		}
	case "wait", "closeAgent":
		if spanID := a.collabSpanIDForReceivers(collab.ReceiverThreadIds); spanID != "" {
			return spanID
		}
	}

	return defaultParentSpanID
}

func (a *CodexAgent) registerCollabReceiver(threadID, spanID string) bool {
	if threadID == "" || spanID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans == nil {
		a.collabThreadSpans = make(map[string]string)
	}
	if a.collabSpanThreads == nil {
		a.collabSpanThreads = make(map[string]int)
	}
	if prev := a.collabThreadSpans[threadID]; prev != "" && prev != spanID {
		if a.collabSpanThreads[prev] > 0 {
			a.collabSpanThreads[prev]--
			if a.collabSpanThreads[prev] == 0 {
				delete(a.collabSpanThreads, prev)
			}
		}
	}
	if a.collabThreadSpans[threadID] == spanID {
		return false
	}
	a.collabThreadSpans[threadID] = spanID
	a.collabSpanThreads[spanID]++
	return true
}

func (a *CodexAgent) unregisterCollabReceiver(threadID string) {
	if threadID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans == nil {
		return
	}
	spanID := a.collabThreadSpans[threadID]
	if spanID == "" {
		return
	}
	delete(a.collabThreadSpans, threadID)
	if a.collabSpanThreads != nil && a.collabSpanThreads[spanID] > 0 {
		a.collabSpanThreads[spanID]--
		if a.collabSpanThreads[spanID] == 0 {
			delete(a.collabSpanThreads, spanID)
		}
	}
}

func (a *CodexAgent) resetCollabReceivers() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.collabThreadSpans)
	clear(a.collabSpanThreads)
}

func (a *CodexAgent) hasCollabReceivers(spanID string) bool {
	if spanID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.collabSpanThreads != nil && a.collabSpanThreads[spanID] > 0
}

func (a *CodexAgent) willDrainCollabParent(parentSpanID string, receiverIDs []string, agentStates map[string]codexCollabAgentState, removeAll bool) bool {
	if parentSpanID == "" {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.collabSpanThreads == nil || a.collabSpanThreads[parentSpanID] == 0 {
		return false
	}

	remaining := a.collabSpanThreads[parentSpanID]
	seen := make(map[string]struct{}, len(receiverIDs))
	for _, receiverID := range receiverIDs {
		if receiverID == "" {
			continue
		}
		if _, ok := seen[receiverID]; ok {
			continue
		}
		seen[receiverID] = struct{}{}
		if a.collabThreadSpans == nil || a.collabThreadSpans[receiverID] != parentSpanID {
			continue
		}
		if !removeAll {
			state, ok := agentStates[receiverID]
			if !ok || !isTerminalCollabAgentStatus(state.Status) {
				continue
			}
		}
		remaining--
	}

	return remaining == 0
}

func (a *CodexAgent) collabSpanIDForReceivers(receiverIDs []string) string {
	if len(receiverIDs) == 0 {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collabThreadSpans == nil {
		return ""
	}
	var spanID string
	for _, receiverID := range receiverIDs {
		current := a.collabThreadSpans[receiverID]
		if current == "" {
			return ""
		}
		if spanID == "" {
			spanID = current
			continue
		}
		if current != spanID {
			return ""
		}
	}
	return spanID
}

// extractCodexItem extracts the item type, ID, and threadId from item/started
// or item/completed params. The threadId is returned alongside the item to
// avoid a redundant unmarshal in codexParentSpanID.
func extractCodexItem(params json.RawMessage) (item json.RawMessage, itemType, itemID, threadID string) {
	var wrapper struct {
		Item     json.RawMessage `json:"item"`
		ThreadID string          `json:"threadId"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("codex extract item wrapper unmarshal failed", "error", err)
		return nil, "", "", ""
	}
	if len(wrapper.Item) == 0 {
		return nil, "", "", ""
	}

	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(wrapper.Item, &header); err != nil {
		slog.Warn("codex extract item header unmarshal failed", "error", err)
		return nil, "", "", ""
	}

	return wrapper.Item, header.Type, header.ID, wrapper.ThreadID
}
