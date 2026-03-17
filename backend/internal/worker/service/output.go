// Package service output provides agent output NDJSON processing.
// It parses Claude Code NDJSON lines, persists messages, and broadcasts
// events to watching E2EE channels via the WatcherManager.
package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// notifThreadGracePeriod is how long a soft-cleared notification thread
// remains eligible for merging.
const notifThreadGracePeriod = time.Second

// threadWrapper is the content envelope stored in the DB for every message.
// It tracks the original seq values of the message before thread merges
// and holds the raw Claude Code JSON for each message in the thread.
type threadWrapper struct {
	OldSeqs  []int64           `json:"old_seqs"`
	Messages []json.RawMessage `json:"messages"`
}

// wrapContent wraps a single raw message JSON into a threadWrapper envelope.
func wrapContent(rawJSON []byte) []byte {
	w := threadWrapper{
		OldSeqs:  []int64{},
		Messages: []json.RawMessage{rawJSON},
	}
	data, _ := json.Marshal(w)
	return data
}

// unwrapContent parses a threadWrapper from content bytes.
func unwrapContent(data []byte) (*threadWrapper, error) {
	var w threadWrapper
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// appendToThread appends a child message to an existing thread wrapper,
// recording the parent's current seq in old_seqs before the seq bump.
func appendToThread(wrapper *threadWrapper, parentSeq int64, childRawJSON []byte) []byte {
	wrapper.OldSeqs = append(wrapper.OldSeqs, parentSeq)
	wrapper.Messages = append(wrapper.Messages, childRawJSON)
	data, _ := json.Marshal(wrapper)
	return data
}

// notifThreadRef tracks the current notification thread for an agent.
type notifThreadRef struct {
	msgID     string
	seq       int64
	softClear time.Time // Zero = not soft-cleared
}

// OutputHandler processes agent NDJSON output, persists messages,
// and broadcasts events to watching E2EE channels.
type OutputHandler struct {
	queries *db.Queries
	watcher *WatcherManager
	agents  *agent.Manager

	// Per-agent state (concurrent access from agent goroutines).
	notifMu         sync.Map // agentID -> *sync.Mutex
	lastNotifThread sync.Map // agentID -> *notifThreadRef
	lastAgentStatus sync.Map // agentID -> string
	contextUsage    sync.Map // agentID -> *contextUsageSnapshot
	planModeToolUse sync.Map // tool_use_id -> target mode string ("plan" or "default")
}

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

// NewOutputHandler creates a new OutputHandler.
func NewOutputHandler(queries *db.Queries, watcher *WatcherManager, agents *agent.Manager) *OutputHandler {
	return &OutputHandler{
		queries: queries,
		watcher: watcher,
		agents:  agents,
	}
}

// notifMutex returns a per-agent mutex for notification threading.
func (h *OutputHandler) notifMutex(agentID string) *sync.Mutex {
	v, _ := h.notifMu.LoadOrStore(agentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// softClearNotifThread marks the current notification thread as soft-cleared.
func (h *OutputHandler) softClearNotifThread(agentID string) {
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()
	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if threadRef.softClear.IsZero() {
			threadRef.softClear = time.Now()
		}
	}
}

// HandleAgentOutput processes a single NDJSON line from the agent.
func (h *OutputHandler) HandleAgentOutput(agentID, workspaceID, workingDir string, content []byte) {
	// Parse just the type field to decide how to handle it.
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		slog.Warn("invalid agent output JSON", "agent_id", agentID, "error", err)
		return
	}

	// Map the JSON type field to a proto MessageRole.
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

	slog.Debug("HandleAgentOutput", "agent_id", agentID, "type", envelope.Type, "len", len(content))

	switch envelope.Type {
	case "assistant", "system", "result":
		h.handlePersistableMessage(agentID, workspaceID, workingDir, content, envelope.Type, role)

	case "user":
		// Simple user text echoes (message.content is a string) are already
		// persisted by the SendAgentMessage handler — skip them to avoid
		// duplicates. Tool-result messages (message.content is an array)
		// still need OutputHandler processing for thread merging.
		if !isSimpleUserTextEcho(content) {
			h.handlePersistableMessage(agentID, workspaceID, workingDir, content, envelope.Type, role)
		}

	case "context_cleared", "interrupted", "plan_execution":
		// Notification events from Claude Code — persist as LEAPMUX notifications
		// so they appear in the chat view (e.g. "Context cleared" bubble).
		if err := h.persistNotificationThreaded(agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, content); err != nil {
			slog.Error("persist agent notification", "agent_id", agentID, "type", envelope.Type, "error", err)
		}

	case "control_request":
		h.handleControlRequest(agentID, content)

	case "control_cancel_request":
		h.handleControlCancel(agentID, content)

	case "control_response":
		h.handleControlResponse(agentID, content)

	case "rate_limit_event":
		h.handleRateLimitEvent(agentID, content)

	default:
		// Streaming chunk or other event -- forward to watchers without persisting.
		h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_StreamChunk{
				StreamChunk: &leapmuxv1.AgentStreamChunk{
					MessageId: agentID,
					Delta:     content,
				},
			},
		})
	}
}

// handlePersistableMessage handles assistant, system, user, and result messages.
func (h *OutputHandler) handlePersistableMessage(agentID, workspaceID, workingDir string, content []byte, msgType string, role leapmuxv1.MessageRole) {
	// For "system" messages, extract session_id from the init message.
	if msgType == "system" {
		h.handleSystemInit(agentID, content)

		// Thread notification-eligible system messages.
		if isNotificationThreadable(content, role) {
			if statusVal, ok := extractStatusValue(content); ok {
				prev, hasPrev := h.lastAgentStatus.Swap(agentID, statusVal)
				if statusVal == "" && (!hasPrev || prev.(string) == "") {
					return
				}
			}
			if err := h.persistNotificationThreaded(agentID, role, content); err != nil {
				slog.Error("persist notification-threaded system message", "agent_id", agentID, "error", err)
			}
			return
		}
	}

	// Non-notification messages soft-clear the notification thread.
	h.softClearNotifThread(agentID)

	// Extract agent context metadata from assistant and result messages.
	if msgType == "assistant" || msgType == "result" {
		h.extractAndBroadcastUsage(agentID, content, msgType)
	}

	// Track plan mode tool_use and plan file paths from assistant messages.
	if msgType == "assistant" {
		h.trackPlanModeToolUse(content)
		h.trackPlanFilePath(agentID, content)
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
		if h.mergeIntoThread(agentID, threadID, content) {
			if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
				// Detect plan mode changes from tool_result.
				h.detectPlanModeFromToolResult(agentID, content)
			}
			return
		}
		// Parent not found or merge failed — fall through to standalone insert.
	}

	// Standalone message or no matching parent — wrap content and insert.
	if err := h.persistAndBroadcast(agentID, role, content, threadID); err != nil {
		slog.Error("persist agent message", "agent_id", agentID, "error", err)
		return
	}

}

// handleSystemInit extracts session_id from system init messages.
func (h *OutputHandler) handleSystemInit(agentID string, content []byte) {
	var initMsg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(content, &initMsg); err != nil || initMsg.SessionID == "" {
		return
	}

	existingAgent, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Error("failed to fetch agent for session ID comparison",
			"agent_id", agentID, "error", err)
		return
	}

	// Only update DB session ID when it actually changed.
	if existingAgent.AgentSessionID != initMsg.SessionID {
		if err := h.queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: initMsg.SessionID,
			ID:             agentID,
		}); err != nil {
			slog.Error("failed to store agent session ID",
				"agent_id", agentID, "error", err)
			return
		}

		slog.Info("agent session ID updated",
			"agent_id", agentID, "session_id", initMsg.SessionID)
	}

	// Always broadcast ACTIVE so the frontend learns the agent is running
	// (even on resume where the session ID hasn't changed).
	sc := &leapmuxv1.AgentStatusChange{
		AgentId:             agentID,
		Status:              leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		AgentSessionId:      initMsg.SessionID,
		WorkerOnline:        true,
		PermissionMode:      existingAgent.PermissionMode,
		Model:               existingAgent.Model,
		Effort:              existingAgent.Effort,
		GitStatus:           gitStatusToProto(gitutil.GetGitStatus(existingAgent.WorkingDir)),
		SupportsModelEffort: h.agents.SupportsModelEffort(agentID),
	}
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

// handleControlRequest persists and broadcasts a control_request.
func (h *OutputHandler) handleControlRequest(agentID string, content []byte) {
	var cr struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cr); err != nil {
		slog.Warn("invalid control_request JSON", "agent_id", agentID, "error", err)
		return
	}
	if err := h.queries.CreateControlRequest(bgCtx(), db.CreateControlRequestParams{
		AgentID:   agentID,
		RequestID: cr.RequestID,
		Payload:   content,
	}); err != nil {
		slog.Error("persist control request", "agent_id", agentID, "request_id", cr.RequestID, "error", err)
	}
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ControlRequest{
			ControlRequest: &leapmuxv1.AgentControlRequest{
				AgentId:   agentID,
				RequestId: cr.RequestID,
				Payload:   content,
			},
		},
	})
}

// handleControlCancel persists and broadcasts a control_cancel_request.
func (h *OutputHandler) handleControlCancel(agentID string, content []byte) {
	var cc struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cc); err != nil {
		slog.Warn("invalid control_cancel_request JSON", "agent_id", agentID, "error", err)
		return
	}
	if err := h.queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   agentID,
		RequestID: cc.RequestID,
	}); err != nil {
		slog.Error("delete control request on cancel", "agent_id", agentID, "request_id", cc.RequestID, "error", err)
	}
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{
			ControlCancel: &leapmuxv1.AgentControlCancelRequest{
				AgentId:   agentID,
				RequestId: cc.RequestID,
			},
		},
	})
}

// handleControlResponse handles control_response from Claude Code.
func (h *OutputHandler) handleControlResponse(agentID string, content []byte) {
	// Delete the answered control request so it is not replayed to new watchers.
	var reqID struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &reqID); err == nil && reqID.RequestID != "" {
		_ = h.queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
			AgentID:   agentID,
			RequestID: reqID.RequestID,
		})
	}

	// Extract permission mode from the response.
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
			newMode := cr.Response.Response.Mode

			// Fetch agent before updating to capture the old permission mode.
			dbAgent, fetchErr := h.queries.GetAgentByID(bgCtx(), agentID)
			oldMode := ""
			if fetchErr == nil {
				oldMode = dbAgent.PermissionMode
			}

			_ = h.queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
				PermissionMode: newMode,
				ID:             agentID,
			})

			// Broadcast statusChange so frontends update their permission mode display.
			if fetchErr == nil {
				h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event: &leapmuxv1.AgentEvent_StatusChange{
						StatusChange: &leapmuxv1.AgentStatusChange{
							AgentId:             agentID,
							Status:              leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
							AgentSessionId:      dbAgent.AgentSessionID,
							WorkerOnline:        true,
							PermissionMode:      newMode,
							Model:               dbAgent.Model,
							Effort:              dbAgent.Effort,
							GitStatus:           gitStatusToProto(gitutil.GetGitStatus(dbAgent.WorkingDir)),
							SupportsModelEffort: h.agents.SupportsModelEffort(agentID),
						},
					},
				})
			}

			// Broadcast settings_changed notification for the chat view.
			if oldMode != "" && oldMode != newMode {
				h.BroadcastNotification(agentID, map[string]interface{}{
					"type": "settings_changed",
					"changes": map[string]interface{}{
						"permissionMode": map[string]string{"old": oldMode, "new": newMode},
					},
				})
			}
		}
	}
}

// handleRateLimitEvent broadcasts rate_limit_event and persists as notification.
func (h *OutputHandler) handleRateLimitEvent(agentID string, content []byte) {
	var rle struct {
		RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(content, &rle); err != nil || len(rle.RateLimitInfo) == 0 {
		return
	}

	// Broadcast ephemeral agent_session_info.
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
	h.broadcastAgentSessionInfo(agentID, map[string]interface{}{
		"rateLimits": rateLimits,
	})

	// Persist as a LEAPMUX notification for the chat bubble.
	notifContent, _ := json.Marshal(map[string]interface{}{
		"type":            "rate_limit",
		"rate_limit_info": rle.RateLimitInfo,
	})
	if err := h.persistNotificationThreaded(agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, notifContent); err != nil {
		slog.Error("persist rate_limit notification", "agent_id", agentID, "error", err)
	}
}

// extractAndBroadcastUsage extracts token usage from assistant/result messages.
func (h *OutputHandler) extractAndBroadcastUsage(agentID string, content []byte, msgType string) {
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

	snapshot := h.getOrCreateUsageSnapshot(agentID)

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
		h.broadcastAgentSessionInfo(agentID, info)
	}
}

// persistAndBroadcast persists a message and broadcasts it to watchers.
func (h *OutputHandler) persistAndBroadcast(agentID string, role leapmuxv1.MessageRole, contentJSON []byte, threadID string) error {
	msgID := id.Generate()
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := h.queries.CreateMessage(bgCtx(), db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           threadID,
		CreatedAt:          now,
	})
	if err != nil {
		return err
	}

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})
	return nil
}

// mergeIntoThread appends a child message to an existing thread parent.
func (h *OutputHandler) mergeIntoThread(agentID, threadID string, childJSON []byte) bool {
	parentRow, err := h.queries.GetMessageByAgentAndThreadID(bgCtx(), db.GetMessageByAgentAndThreadIDParams{
		AgentID:  agentID,
		ThreadID: threadID,
	})
	if err != nil {
		return false
	}

	parentData, err := msgcodec.Decompress(parentRow.Content, parentRow.ContentCompression)
	if err != nil {
		slog.Error("decompress parent for thread merge", "agent_id", agentID, "thread_id", threadID, "error", err)
		return false
	}
	wrapper, err := unwrapContent(parentData)
	if err != nil {
		slog.Error("parse parent wrapper for thread merge", "agent_id", agentID, "thread_id", threadID, "error", err)
		return false
	}

	merged := appendToThread(wrapper, parentRow.Seq, childJSON)

	now := time.Now()
	mergedCompressed, mergedCompType := msgcodec.Compress(merged)
	newSeq, err := h.queries.UpdateMessageThread(bgCtx(), db.UpdateMessageThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		UpdatedAt:          sql.NullTime{Time: now, Valid: true},
		ID:                 parentRow.ID,
		AgentID:            agentID,
	})
	if err != nil {
		slog.Error("update parent thread", "agent_id", agentID, "thread_id", threadID, "error", err)
		return false
	}

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 parentRow.ID,
		Role:               parentRow.Role,
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		Seq:                newSeq,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
		UpdatedAt:          timefmt.Format(now),
	})
	return true
}

// persistNotificationThreaded persists a notification message, appending it
// to the current notification thread if one exists.
func (h *OutputHandler) persistNotificationThreaded(agentID string, role leapmuxv1.MessageRole, contentJSON []byte) error {
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if threadRef.softClear.IsZero() || time.Since(threadRef.softClear) < notifThreadGracePeriod {
			if err := h.appendToNotificationThread(agentID, threadRef, role, contentJSON); err == nil {
				return nil
			}
		}
	}

	return h.createNotificationStandalone(agentID, role, contentJSON)
}

// appendToNotificationThread appends a message to an existing notification thread.
func (h *OutputHandler) appendToNotificationThread(agentID string, threadRef *notifThreadRef, role leapmuxv1.MessageRole, contentJSON []byte) error {
	parentRow, err := h.queries.GetMessageByAgentAndID(bgCtx(), db.GetMessageByAgentAndIDParams{
		ID:      threadRef.msgID,
		AgentID: agentID,
	})
	if err != nil {
		return err
	}

	parentData, err := msgcodec.Decompress(parentRow.Content, parentRow.ContentCompression)
	if err != nil {
		return err
	}

	wrapper, err := unwrapContent(parentData)
	if err != nil {
		return err
	}

	wrapper.OldSeqs = append(wrapper.OldSeqs, parentRow.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}
	wrapper.Messages = append(wrapper.Messages, contentJSON)
	wrapper.Messages = consolidateNotificationThread(wrapper.Messages)

	merged, _ := json.Marshal(wrapper)

	now := time.Now()
	mergedCompressed, mergedCompType := msgcodec.Compress(merged)
	newSeq, err := h.queries.UpdateMessageThread(bgCtx(), db.UpdateMessageThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		UpdatedAt:          sql.NullTime{Time: now, Valid: true},
		ID:                 parentRow.ID,
		AgentID:            agentID,
	})
	if err != nil {
		return err
	}

	threadRef.seq = newSeq
	h.lastNotifThread.Store(agentID, threadRef)

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 parentRow.ID,
		Role:               parentRow.Role,
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		Seq:                newSeq,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
		UpdatedAt:          timefmt.Format(now),
	})

	return nil
}

// createNotificationStandalone creates a new standalone notification message.
func (h *OutputHandler) createNotificationStandalone(agentID string, role leapmuxv1.MessageRole, contentJSON []byte) error {
	msgID := id.Generate()
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := h.queries.CreateMessage(bgCtx(), db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		return err
	}

	h.lastNotifThread.Store(agentID, &notifThreadRef{
		msgID: msgID,
		seq:   seq,
	})

	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})
	return nil
}

// broadcastMessage broadcasts a single agent message event to all watchers.
func (h *OutputHandler) broadcastMessage(agentID string, msg *leapmuxv1.AgentChatMessage) {
	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_AgentMessage{
			AgentMessage: msg,
		},
	})
}

// broadcastAgentSessionInfo broadcasts ephemeral agent session metadata.
func (h *OutputHandler) broadcastAgentSessionInfo(agentID string, info map[string]interface{}) {
	content := map[string]interface{}{
		"type": "agent_session_info",
		"info": info,
	}
	contentJSON, _ := json.Marshal(content)
	compressed, compressionType := msgcodec.Compress(contentJSON)
	h.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 id.Generate(),
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                -1, // Ephemeral sentinel
	})
}

// BroadcastNotification persists and broadcasts a LEAPMUX notification.
func (h *OutputHandler) BroadcastNotification(agentID string, content map[string]interface{}) {
	contentJSON, _ := json.Marshal(content)
	if err := h.persistNotificationThreaded(agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// getOrCreateUsageSnapshot returns the token usage snapshot for the given agent.
func (h *OutputHandler) getOrCreateUsageSnapshot(agentID string) *contextUsageSnapshot {
	if v, ok := h.contextUsage.Load(agentID); ok {
		return v.(*contextUsageSnapshot)
	}
	snap := &contextUsageSnapshot{}
	actual, _ := h.contextUsage.LoadOrStore(agentID, snap)
	return actual.(*contextUsageSnapshot)
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

// consolidateNotificationThread consolidates a notification thread's messages.
// Each message type appears at most once in the output (except compaction
// boundaries and unknown types, which are always kept). When duplicates exist,
// the last occurrence's data wins. Output is ordered by the position of each
// type's last occurrence in the input.
func consolidateNotificationThread(messages []json.RawMessage) []json.RawMessage {
	type settingsChange struct {
		Old string `json:"old"`
		New string `json:"new"`
	}

	// Unified envelope — decoded once per message.
	type envelope struct {
		Type    string                    `json:"type"`
		Subtype string                    `json:"subtype"`
		Changes map[string]settingsChange `json:"changes,omitempty"`
		RLInfo  *struct {
			RateLimitType string `json:"rateLimitType"`
		} `json:"rate_limit_info,omitempty"`
	}

	// Deduplication state — track the last-seen index for ordering.
	mergedChanges := map[string]settingsChange{}
	settingsLastIdx := -1

	contextClearedRaw := json.RawMessage(nil)
	contextClearedLastIdx := -1

	interruptedRaw := json.RawMessage(nil)
	interruptedLastIdx := -1

	planExecRaw := json.RawMessage(nil)
	planExecLastIdx := -1

	rateLimitByType := map[string]json.RawMessage{}
	rateLimitLastIdx := -1

	var latestStatusRaw json.RawMessage
	statusLastIdx := -1

	// Compaction boundaries and unknown types: always kept, in order.
	type indexedRaw struct {
		idx int
		raw json.RawMessage
	}
	var keepAll []indexedRaw

	for i, raw := range messages {
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			keepAll = append(keepAll, indexedRaw{i, raw})
			continue
		}

		switch {
		case env.Type == "settings_changed":
			for key, val := range env.Changes {
				if existing, ok := mergedChanges[key]; ok {
					mergedChanges[key] = settingsChange{Old: existing.Old, New: val.New}
				} else {
					mergedChanges[key] = val
				}
			}
			settingsLastIdx = i

		case env.Type == "context_cleared":
			contextClearedRaw = raw
			contextClearedLastIdx = i

		case env.Type == "plan_execution":
			planExecRaw = raw
			planExecLastIdx = i

		case env.Type == "interrupted":
			interruptedRaw = raw
			interruptedLastIdx = i

		case env.Type == "rate_limit":
			key := "unknown"
			if env.RLInfo != nil && env.RLInfo.RateLimitType != "" {
				key = env.RLInfo.RateLimitType
			}
			rateLimitByType[key] = raw
			rateLimitLastIdx = i

		case env.Type == "system" && env.Subtype == "status":
			latestStatusRaw = raw
			statusLastIdx = i

		case env.Type == "system" && (env.Subtype == "compact_boundary" || env.Subtype == "microcompact_boundary"):
			keepAll = append(keepAll, indexedRaw{i, raw})

		default:
			keepAll = append(keepAll, indexedRaw{i, raw})
		}
	}

	// Build deduped entries with their ordering index.
	var entries []indexedRaw

	// settings_changed: merge all changes, drop if old==new for all keys.
	if settingsLastIdx >= 0 {
		effective := map[string]settingsChange{}
		for key, val := range mergedChanges {
			if val.Old != val.New {
				effective[key] = val
			}
		}
		if len(effective) > 0 {
			entry := map[string]interface{}{
				"type":    "settings_changed",
				"changes": effective,
			}
			if data, err := json.Marshal(entry); err == nil {
				entries = append(entries, indexedRaw{settingsLastIdx, data})
			}
		}
	}

	if contextClearedLastIdx >= 0 {
		entries = append(entries, indexedRaw{contextClearedLastIdx, contextClearedRaw})
	}

	if planExecLastIdx >= 0 {
		entries = append(entries, indexedRaw{planExecLastIdx, planExecRaw})
	}

	if interruptedLastIdx >= 0 {
		entries = append(entries, indexedRaw{interruptedLastIdx, interruptedRaw})
	}

	for _, raw := range rateLimitByType {
		entries = append(entries, indexedRaw{rateLimitLastIdx, raw})
	}

	if statusLastIdx >= 0 {
		entries = append(entries, indexedRaw{statusLastIdx, latestStatusRaw})
	}

	// Merge keepAll entries.
	entries = append(entries, keepAll...)

	// Sort by input index (ascending) to preserve chronological order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].idx < entries[j].idx
	})

	result := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.raw)
	}

	if len(result) == 0 {
		return []json.RawMessage{}
	}

	return result
}

// isSimpleUserTextEcho returns true if the NDJSON line is a user message echo
// with string content (not a tool_result array). These echoes are already
// persisted by the SendAgentMessage handler.
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
	// String content starts with '"', array content starts with '['.
	trimmed := bytes.TrimSpace(msg.Message.Content)
	return len(trimmed) > 0 && trimmed[0] == '"'
}
