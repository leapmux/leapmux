package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// contextUsageSnapshot tracks token usage for debounced broadcasting.
type contextUsageSnapshot struct {
	mu                       sync.Mutex
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ContextWindow            int64
	LastBroadcast            time.Time
}

// HandleOutput processes a single NDJSON line from Claude Code.
// This is the Claude Code-specific implementation of the Provider interface.
func (a *ClaudeCodeAgent) HandleOutput(content []byte) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		slog.Warn("invalid agent output JSON", "agent_id", a.agentID, "error", err)
		return
	}

	var role leapmuxv1.MessageRole
	switch envelope.Type {
	case "user":
		role = leapmuxv1.MessageRole_MESSAGE_ROLE_USER
	case "assistant":
		role = leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT
	case "system":
		role = leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM
	case "result":
		role = leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT
	}

	slog.Debug("HandleOutput", "agent_id", a.agentID, "type", envelope.Type, "len", len(content))

	switch envelope.Type {
	case "assistant", "system", "result":
		a.handlePersistableMessage(content, envelope.Type, role)

	case "user":
		// Reset tool use counter at the start of each user turn.
		a.mu.Lock()
		a.turnToolUses = 0
		a.mu.Unlock()
		if !isSimpleUserTextEcho(content) {
			a.handlePersistableMessage(content, envelope.Type, role)
		}

	case "context_cleared", "interrupted", "plan_execution":
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
			slog.Error("persist agent notification", "agent_id", a.agentID, "type", envelope.Type, "error", err)
		}

	case "control_request":
		a.claudeCodeHandleControlRequest(content)

	case "control_cancel_request":
		a.claudeCodeHandleControlCancel(content)

	case "control_response":
		a.claudeCodeHandleControlResponse(content)

	case "rate_limit_event":
		a.claudeCodeHandleRateLimitEvent(content)

	default:
		a.sink.BroadcastStreamChunk(content)
	}
}

// enrichResultWithToolUses injects num_tool_uses into a result message so
// the frontend can determine whether the turn involved tool use.
func (a *ClaudeCodeAgent) enrichResultWithToolUses(content []byte) []byte {
	return a.enrichWithToolUses(content)
}

// processAssistantBlocks parses the message.content[] blocks of an assistant
// message once and performs plan mode tracking, plan file tracking, tool
// use counting, and scope management — replacing multiple separate parse passes.
func (a *ClaudeCodeAgent) processAssistantBlocks(content []byte) {
	var msg struct {
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         struct {
			Content []struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Input struct {
					FilePath  string `json:"file_path"`
					Content   string `json:"content"`
					OldString string `json:"old_string"`
					NewString string `json:"new_string"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(content, &msg) != nil {
		return
	}

	// Determine the parent span for any Agent tool_use blocks.
	parentSpanID := msg.ParentToolUseID

	toolUseCount := 0
	planFileProcessed := false
	for _, block := range msg.Message.Content {
		if block.Type != "tool_use" {
			continue
		}

		toolUseCount++

		// Plan mode tracking (EnterPlanMode/ExitPlanMode).
		if block.ID != "" {
			switch block.Name {
			case "EnterPlanMode":
				a.sink.StorePlanModeToolUse(block.ID, PermissionModePlan)
			case "ExitPlanMode":
				a.sink.StorePlanModeToolUse(block.ID, PermissionModeDefault)
			}

			// Open a span for every tool_use so the tool_result
			// is visually grouped under its tool_use.
			a.sink.OpenSpan(block.ID, parentSpanID)
		}

		// Plan file path tracking (Write/Edit to ~/.claude/plans/).
		if !planFileProcessed && (block.Name == "Write" || block.Name == "Edit") {
			filePath := block.Input.FilePath
			if filePath != "" && a.homeDir != "" {
				planDir := a.homeDir + "/.claude/plans/"
				if strings.HasPrefix(filePath, planDir) {
					planFileProcessed = true

					var planContentStr string
					if block.Name == "Write" && block.Input.Content != "" {
						planContentStr = block.Input.Content
					} else {
						data, readErr := os.ReadFile(filePath)
						if readErr == nil && len(data) > 0 {
							if block.Name == "Edit" {
								planContentStr = strings.Replace(string(data), block.Input.OldString, block.Input.NewString, 1)
							} else {
								planContentStr = string(data)
							}
						}
					}

					var compressed []byte
					var compression leapmuxv1.ContentCompression
					if planContentStr != "" {
						compressed, compression = msgcodec.Compress([]byte(planContentStr))
					}
					a.sink.UpdatePlan(filePath, compressed, compression, extractPlanTitle(planContentStr))
				}
			}
		}
	}

	if toolUseCount > 0 {
		a.mu.Lock()
		a.turnToolUses += toolUseCount
		a.mu.Unlock()
	}
}

// handlePersistableMessage handles assistant, system, user, and result messages.
func (a *ClaudeCodeAgent) handlePersistableMessage(content []byte, msgType string, role leapmuxv1.MessageRole) {
	if msgType == "system" {
		a.claudeCodeHandleSystemInit(content)

		if isNotificationThreadable(content, role) {
			if statusVal, ok := extractStatusValue(content); ok {
				prev := a.lastAgentStatus
				a.lastAgentStatus = statusVal
				if statusVal == "" && prev == "" {
					return
				}
				// Emit a LEAPMUX notification for compacting status so it threads with
				// other LEAPMUX notifications (context_cleared, settings_changed, etc.).
				if statusVal == "compacting" {
					leapmuxContent := []byte(`{"type":"compacting"}`)
					if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, leapmuxContent); err != nil {
						slog.Error("persist compacting notification", "agent_id", a.agentID, "error", err)
					}
					return
				}
			}
			if err := a.sink.PersistNotification(role, content); err != nil {
				slog.Error("persist notification-threaded system message", "agent_id", a.agentID, "error", err)
			}
			return
		}
	}

	// Non-notification messages soft-clear the notification thread.
	a.sink.SoftClearNotifThread()

	// Extract agent context metadata from assistant and result messages.
	if msgType == "assistant" || msgType == "result" {
		a.extractAndBroadcastUsage(content, msgType)
	}

	// Determine parent span ID for hierarchy tracking.
	parentSpanID := extractParentToolUseID(content)
	if parentSpanID == "" {
		parentSpanID = extractSystemToolUseID(content)
	}

	// Determine span ID: for tool_use messages use the block ID,
	// for tool_result messages use the tool_use_id reference.
	var spanID string
	if msgType == "assistant" {
		spanID = extractToolUseID(content)
	} else if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
		spanID = extractToolResultID(content)
	}

	// Detect plan mode from tool_result messages.
	if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
		a.detectPlanModeFromToolResult(content)
	}

	// Enrich result messages with num_tool_uses.
	if msgType == "result" {
		content = a.enrichResultWithToolUses(content)
	}

	// Pre-peek the span color for tool_use messages (assistant with a spanID)
	// so it is available at persist time, before the span is actually opened.
	var spanColor int32 = -1
	if msgType == "assistant" && spanID != "" {
		spanColor = a.sink.PeekNextSpanColor()
	}

	// Persist as a standalone message with hierarchy metadata.
	// This MUST happen before processAssistantBlocks (which opens spans)
	// so the assistant message stays at the parent depth.
	// closing is true when this is a tool_result that will close its span.
	closing := role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER && spanID != ""
	if err := a.sink.PersistMessage(role, content, SpanInfo{
		ParentSpanID: parentSpanID,
		SpanID:       spanID,
		SpanColor:    spanColor,
		Closing:      closing,
	}); err != nil {
		slog.Error("persist agent message", "agent_id", a.agentID, "error", err)
	}

	// Parse assistant message content blocks for plan mode tracking,
	// plan file tracking, tool use counting, and span management.
	// Runs after persist so spans open AFTER the tool_use message,
	// keeping it at parent depth while its tool_result is indented.
	if msgType == "assistant" {
		a.processAssistantBlocks(content)
	}

	// Close span after persisting if this is a user message (tool_result)
	// that completes a tool span.
	if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
		if spanID != "" {
			a.sink.CloseSpan(spanID)
		}
	}
}

// claudeCodeHandleSystemInit extracts session_id from system init messages.
func (a *ClaudeCodeAgent) claudeCodeHandleSystemInit(content []byte) {
	var initMsg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(content, &initMsg); err != nil || initMsg.SessionID == "" {
		return
	}
	a.sink.UpdateSessionID(initMsg.SessionID)
	a.sink.BroadcastStatusActive(initMsg.SessionID)
}

// claudeCodeHandleControlRequest persists and broadcasts a control_request.
func (a *ClaudeCodeAgent) claudeCodeHandleControlRequest(content []byte) {
	var cr struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cr); err != nil {
		slog.Warn("invalid control_request JSON", "agent_id", a.agentID, "error", err)
		return
	}
	a.sink.PersistControlRequest(cr.RequestID, content)
	a.sink.BroadcastControlRequest(cr.RequestID, content)
}

// claudeCodeHandleControlCancel persists and broadcasts a control_cancel_request.
func (a *ClaudeCodeAgent) claudeCodeHandleControlCancel(content []byte) {
	var cc struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cc); err != nil {
		slog.Warn("invalid control_cancel_request JSON", "agent_id", a.agentID, "error", err)
		return
	}
	a.sink.DeleteControlRequest(cc.RequestID)
	a.sink.BroadcastControlCancel(cc.RequestID)
}

// claudeCodeHandleControlResponse handles control_response from Claude Code.
func (a *ClaudeCodeAgent) claudeCodeHandleControlResponse(content []byte) {
	var reqID struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &reqID); err == nil && reqID.RequestID != "" {
		a.sink.DeleteControlRequest(reqID.RequestID)
	}

	var cr struct {
		Response struct {
			Subtype  string `json:"subtype"`
			Response struct {
				Mode string `json:"mode"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &cr); err == nil {
		if cr.Response.Subtype == "success" && cr.Response.Response.Mode != "" {
			a.sink.UpdatePermissionMode(cr.Response.Response.Mode)
		}
	}

	// No need to persist control_response in the timeline — they are
	// already surfaced as notification threads.
}

// claudeCodeHandleRateLimitEvent broadcasts rate_limit_event and persists as notification.
func (a *ClaudeCodeAgent) claudeCodeHandleRateLimitEvent(content []byte) {
	var rle struct {
		RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(content, &rle); err != nil || len(rle.RateLimitInfo) == 0 {
		return
	}

	var rlType struct {
		RateLimitType string `json:"rateLimitType"`
	}
	_ = json.Unmarshal(rle.RateLimitInfo, &rlType)
	if rlType.RateLimitType == "" {
		rlType.RateLimitType = "unknown"
	}

	rateLimits := map[string]json.RawMessage{
		rlType.RateLimitType: rle.RateLimitInfo,
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"rateLimits": rateLimits,
	})

	notifContent, _ := json.Marshal(map[string]interface{}{
		"type":            "rate_limit",
		"rate_limit_info": rle.RateLimitInfo,
	})
	if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, notifContent); err != nil {
		slog.Error("persist rate_limit notification", "agent_id", a.agentID, "error", err)
	}
}

// extractAndBroadcastUsage extracts token usage from assistant/result messages.
func (a *ClaudeCodeAgent) extractAndBroadcastUsage(content []byte, msgType string) {
	var infoFields struct {
		CostUSD *float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(content, &infoFields); err != nil {
		return
	}

	info := map[string]interface{}{}
	if infoFields.CostUSD != nil {
		info["total_cost_usd"] = *infoFields.CostUSD
	}

	snapshot := a.getOrCreateUsageSnapshot()

	if msgType == "assistant" {
		var assistantMsg struct {
			Message *struct {
				Usage *struct {
					InputTokens              int64 `json:"input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(content, &assistantMsg); err == nil &&
			assistantMsg.Message != nil && assistantMsg.Message.Usage != nil {
			u := assistantMsg.Message.Usage
			snapshot.mu.Lock()
			snapshot.InputTokens = u.InputTokens
			snapshot.OutputTokens = u.OutputTokens
			snapshot.CacheCreationInputTokens = u.CacheCreationInputTokens
			snapshot.CacheReadInputTokens = u.CacheReadInputTokens
			snapshot.mu.Unlock()
		}
	}

	if msgType == "result" {
		var resultMsg struct {
			ModelUsage map[string]json.RawMessage `json:"modelUsage"`
		}
		if err := json.Unmarshal(content, &resultMsg); err == nil && resultMsg.ModelUsage != nil {
			for _, raw := range resultMsg.ModelUsage {
				var mu struct {
					ContextWindow int64 `json:"contextWindow"`
				}
				if json.Unmarshal(raw, &mu) == nil && mu.ContextWindow > 0 {
					snapshot.mu.Lock()
					snapshot.ContextWindow = mu.ContextWindow
					snapshot.mu.Unlock()
					break
				}
			}
		}
	}

	snapshot.mu.Lock()
	hasUsage := snapshot.InputTokens > 0 || snapshot.OutputTokens > 0 ||
		snapshot.CacheCreationInputTokens > 0 || snapshot.CacheReadInputTokens > 0
	if hasUsage {
		now := time.Now()
		shouldBroadcast := msgType == "result" ||
			now.Sub(snapshot.LastBroadcast) >= 10*time.Second
		if shouldBroadcast {
			snapshot.LastBroadcast = now
			usageMap := map[string]interface{}{
				"inputTokens":              snapshot.InputTokens,
				"outputTokens":             snapshot.OutputTokens,
				"cacheCreationInputTokens": snapshot.CacheCreationInputTokens,
				"cacheReadInputTokens":     snapshot.CacheReadInputTokens,
			}
			if snapshot.ContextWindow > 0 {
				usageMap["contextWindow"] = snapshot.ContextWindow
			}
			info["contextUsage"] = usageMap
		}
	}
	snapshot.mu.Unlock()

	if len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}

func (a *ClaudeCodeAgent) getOrCreateUsageSnapshot() *contextUsageSnapshot {
	if a.contextUsage == nil {
		a.contextUsage = &contextUsageSnapshot{}
	}
	return a.contextUsage
}

// detectPlanModeFromToolResult inspects a user message (tool_result) for
// confirmation of a previously tracked EnterPlanMode or ExitPlanMode tool_use.
func (a *ClaudeCodeAgent) detectPlanModeFromToolResult(content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
		ToolUseResult *struct {
			Message string `json:"message"`
		} `json:"tool_use_result"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}

	for _, block := range msg.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}

		targetMode, ok := a.sink.LoadAndDeletePlanModeToolUse(block.ToolUseID)
		if !ok {
			continue
		}

		resultText := ""
		if msg.ToolUseResult != nil {
			resultText = msg.ToolUseResult.Message
		}

		resultLower := strings.ToLower(resultText)
		confirmed := false
		if targetMode == PermissionModePlan && strings.Contains(resultLower, "entered plan mode") {
			confirmed = true
		} else if targetMode == PermissionModeDefault && strings.Contains(resultLower, "approved your plan") {
			confirmed = true
		}

		if confirmed {
			slog.Info("plan mode change confirmed via tool_result",
				"agent_id", a.agentID,
				"tool_use_id", block.ToolUseID,
				"mode", targetMode)
			a.sink.UpdatePermissionMode(targetMode)
		} else {
			truncated := resultText
			if len(truncated) > 64 {
				truncated = truncated[:64]
			}
			slog.Debug("plan mode tool_result did not contain expected confirmation",
				"agent_id", a.agentID,
				"tool_use_id", block.ToolUseID,
				"expected_mode", targetMode,
				"result_text", truncated)
		}
	}
}

// --- Thread ID extraction helpers ---

func extractToolUseID(content []byte) string {
	var msg struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	for _, block := range msg.Message.Content {
		if block.Type == "tool_use" && block.ID != "" {
			return block.ID
		}
	}
	return ""
}

func extractToolResultID(content []byte) string {
	var msg struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	for _, block := range msg.Message.Content {
		if block.Type == "tool_result" && block.ToolUseID != "" {
			return block.ToolUseID
		}
	}
	return ""
}

func extractParentToolUseID(content []byte) string {
	var msg struct {
		ParentToolUseID string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	return msg.ParentToolUseID
}

func extractSystemToolUseID(content []byte) string {
	var msg struct {
		ToolUseID string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	return msg.ToolUseID
}

// --- Notification threading helpers ---

var notificationThreadableSubtypes = map[string]bool{
	"status":                true,
	"compact_boundary":      true,
	"microcompact_boundary": true,
}

func isNotificationThreadable(content []byte, role leapmuxv1.MessageRole) bool {
	switch role {
	case leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX:
		var msg struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(content, &msg) != nil {
			return false
		}
		return msg.Type == "settings_changed" || msg.Type == "context_cleared" || msg.Type == "interrupted" || msg.Type == "rate_limit" || msg.Type == "agent_error" || msg.Type == "compacting"
	case leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM:
		var msg struct {
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(content, &msg) != nil {
			return false
		}
		return notificationThreadableSubtypes[msg.Subtype]
	default:
		return false
	}
}

func extractStatusValue(content []byte) (status string, ok bool) {
	var msg struct {
		Subtype string  `json:"subtype"`
		Status  *string `json:"status"`
	}
	if json.Unmarshal(content, &msg) != nil || msg.Subtype != "status" {
		return "", false
	}
	if msg.Status != nil {
		return *msg.Status, true
	}
	return "", true
}

// isSimpleUserTextEcho returns true if the NDJSON line is a user message echo
// with string content (not a tool_result array).
func isSimpleUserTextEcho(content []byte) bool {
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(content, &msg) != nil || msg.Type != "user" {
		return false
	}
	trimmed := bytes.TrimSpace(msg.Message.Content)
	return len(trimmed) > 0 && trimmed[0] == '"'
}
