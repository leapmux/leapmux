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
func (a *Agent) HandleOutput(content []byte) {
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
		if !isSimpleUserTextEcho(content) {
			a.handlePersistableMessage(content, envelope.Type, role)
		}

	case "context_cleared", "interrupted", "plan_execution":
		if err := a.sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
			slog.Error("persist agent notification", "agent_id", a.agentID, "type", envelope.Type, "error", err)
		}

	case "control_request":
		a.ccHandleControlRequest(content)

	case "control_cancel_request":
		a.ccHandleControlCancel(content)

	case "control_response":
		a.ccHandleControlResponse(content)

	case "rate_limit_event":
		a.ccHandleRateLimitEvent(content)

	default:
		a.sink.BroadcastStreamChunk(content)
	}
}

// handlePersistableMessage handles assistant, system, user, and result messages.
func (a *Agent) handlePersistableMessage(content []byte, msgType string, role leapmuxv1.MessageRole) {
	if msgType == "system" {
		a.ccHandleSystemInit(content)

		if isNotificationThreadable(content, role) {
			if statusVal, ok := extractStatusValue(content); ok {
				prev := a.lastAgentStatus
				a.lastAgentStatus = statusVal
				if statusVal == "" && prev == "" {
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

	// Track plan mode tool_use and plan file paths from assistant messages.
	if msgType == "assistant" {
		a.trackPlanModeToolUse(content)
		a.trackPlanFilePath(content)
	}

	// Extract thread_id — try each extractor until one succeeds.
	threadID := extractToolUseID(content)
	if threadID == "" {
		threadID = extractToolResultID(content)
	}
	if threadID == "" {
		threadID = extractParentToolUseID(content)
	}
	if threadID == "" {
		threadID = extractSystemToolUseID(content)
	}

	// Child message with a matching parent: merge into the parent's row.
	if threadID != "" && (role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER || role == leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM) {
		if a.sink.MergeIntoThread(threadID, content) {
			if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
				a.detectPlanModeFromToolResult(content)
			}
			return
		}
	}

	// Standalone message or no matching parent — persist.
	if err := a.sink.PersistMessage(role, content, threadID); err != nil {
		slog.Error("persist agent message", "agent_id", a.agentID, "error", err)
	}
}

// ccHandleSystemInit extracts session_id from system init messages.
func (a *Agent) ccHandleSystemInit(content []byte) {
	var initMsg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(content, &initMsg); err != nil || initMsg.SessionID == "" {
		return
	}
	a.sink.UpdateSessionID(initMsg.SessionID)
	a.sink.BroadcastStatusActive(initMsg.SessionID)
}

// ccHandleControlRequest persists and broadcasts a control_request.
func (a *Agent) ccHandleControlRequest(content []byte) {
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

// ccHandleControlCancel persists and broadcasts a control_cancel_request.
func (a *Agent) ccHandleControlCancel(content []byte) {
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

// ccHandleControlResponse handles control_response from Claude Code.
func (a *Agent) ccHandleControlResponse(content []byte) {
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
}

// ccHandleRateLimitEvent broadcasts rate_limit_event and persists as notification.
func (a *Agent) ccHandleRateLimitEvent(content []byte) {
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
func (a *Agent) extractAndBroadcastUsage(content []byte, msgType string) {
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

func (a *Agent) getOrCreateUsageSnapshot() *contextUsageSnapshot {
	if a.contextUsage == nil {
		a.contextUsage = &contextUsageSnapshot{}
	}
	return a.contextUsage
}

// trackPlanModeToolUse inspects an assistant message for EnterPlanMode or
// ExitPlanMode tool_use blocks.
func (a *Agent) trackPlanModeToolUse(content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}
	for _, block := range msg.Message.Content {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		switch block.Name {
		case "EnterPlanMode":
			a.sink.StorePlanModeToolUse(block.ID, "plan")
		case "ExitPlanMode":
			a.sink.StorePlanModeToolUse(block.ID, "default")
		}
	}
}

// trackPlanFilePath inspects an assistant message for Write or Edit tool_use
// blocks whose file_path targets the agent's ~/.claude/plans/ directory.
func (a *Agent) trackPlanFilePath(content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
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
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}

	for _, block := range msg.Message.Content {
		if block.Type != "tool_use" {
			continue
		}
		if block.Name != "Write" && block.Name != "Edit" {
			continue
		}
		filePath := block.Input.FilePath
		if filePath == "" || a.homeDir == "" {
			continue
		}

		planDir := a.homeDir + "/.claude/plans/"
		if !strings.HasPrefix(filePath, planDir) {
			continue
		}

		// Resolve plan content.
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

		// Only track the first matching plan file per message.
		return
	}
}

// detectPlanModeFromToolResult inspects a user message (tool_result) for
// confirmation of a previously tracked EnterPlanMode or ExitPlanMode tool_use.
func (a *Agent) detectPlanModeFromToolResult(content []byte) {
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
		if targetMode == "plan" && strings.Contains(resultLower, "entered plan mode") {
			confirmed = true
		} else if targetMode == "default" && strings.Contains(resultLower, "approved your plan") {
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
		return msg.Type == "settings_changed" || msg.Type == "context_cleared" || msg.Type == "interrupted" || msg.Type == "rate_limit" || msg.Type == "agent_error"
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
