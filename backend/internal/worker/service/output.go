// Package service output provides agent output persistence and broadcasting.
// It implements the agent.OutputSink interface, backing the generic primitives
// with DB queries, notification threading, and WatcherManager fan-out.
package service

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"
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

// OutputHandler manages agent output persistence and broadcasting.
// It holds shared state accessed by per-agent OutputSink instances.
type OutputHandler struct {
	queries *db.Queries
	watcher *WatcherManager
	agents  *agent.Manager

	// Per-agent notification threading state (concurrent access).
	notifMu         sync.Map // agentID -> *sync.Mutex
	lastNotifThread sync.Map // agentID -> *notifThreadRef

	// Plan mode tool_use tracking (shared across agents).
	planModeToolUse sync.Map // tool_use_id -> target mode string ("plan" or "default")
}

// NewOutputHandler creates a new OutputHandler.
func NewOutputHandler(queries *db.Queries, watcher *WatcherManager, agents *agent.Manager) *OutputHandler {
	return &OutputHandler{
		queries: queries,
		watcher: watcher,
		agents:  agents,
	}
}

// NewSink creates a per-agent OutputSink backed by this OutputHandler.
func (h *OutputHandler) NewSink(agentID string, agentProvider leapmuxv1.AgentProvider) agent.OutputSink {
	return &agentOutputSink{
		h:             h,
		agentID:       agentID,
		agentProvider: agentProvider,
	}
}

// agentOutputSink implements agent.OutputSink for a single agent.
type agentOutputSink struct {
	h             *OutputHandler
	agentID       string
	agentProvider leapmuxv1.AgentProvider
}

// --- OutputSink interface implementation ---

func (s *agentOutputSink) PersistMessage(role leapmuxv1.MessageRole, content []byte, threadID string) error {
	return s.h.persistAndBroadcast(s.agentID, s.agentProvider, role, content, threadID)
}

func (s *agentOutputSink) MergeIntoThread(threadID string, content []byte) bool {
	return s.h.mergeIntoThread(s.agentID, s.agentProvider, threadID, content)
}

func (s *agentOutputSink) PersistNotification(role leapmuxv1.MessageRole, content []byte) error {
	return s.h.persistNotificationThreaded(s.agentID, s.agentProvider, role, content)
}

func (s *agentOutputSink) BroadcastStreamChunk(content []byte) {
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_StreamChunk{
			StreamChunk: &leapmuxv1.AgentStreamChunk{
				MessageId:     s.agentID,
				Delta:         content,
				AgentProvider: s.agentProvider,
			},
		},
	})
}

func (s *agentOutputSink) PersistControlRequest(requestID string, payload []byte) {
	if err := s.h.queries.CreateControlRequest(bgCtx(), db.CreateControlRequestParams{
		AgentID:   s.agentID,
		RequestID: requestID,
		Payload:   payload,
	}); err != nil {
		slog.Error("persist control request", "agent_id", s.agentID, "request_id", requestID, "error", err)
	}
}

func (s *agentOutputSink) DeleteControlRequest(requestID string) {
	_ = s.h.queries.DeleteControlRequest(bgCtx(), db.DeleteControlRequestParams{
		AgentID:   s.agentID,
		RequestID: requestID,
	})
}

func (s *agentOutputSink) BroadcastControlRequest(requestID string, payload []byte) {
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_ControlRequest{
			ControlRequest: &leapmuxv1.AgentControlRequest{
				AgentId:       s.agentID,
				RequestId:     requestID,
				Payload:       payload,
				AgentProvider: s.agentProvider,
			},
		},
	})
}

func (s *agentOutputSink) BroadcastControlCancel(requestID string) {
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{
			ControlCancel: &leapmuxv1.AgentControlCancelRequest{
				AgentId:   s.agentID,
				RequestId: requestID,
			},
		},
	})
}

func (s *agentOutputSink) UpdateSessionID(sessionID string) {
	existingAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for session ID comparison",
			"agent_id", s.agentID, "error", err)
		return
	}

	if existingAgent.AgentSessionID != sessionID {
		if err := s.h.queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{
			AgentSessionID: sessionID,
			ID:             s.agentID,
		}); err != nil {
			slog.Error("failed to store agent session ID",
				"agent_id", s.agentID, "error", err)
			return
		}

		slog.Info("agent session ID updated",
			"agent_id", s.agentID, "session_id", sessionID)
	}
}

// buildStatusChange constructs an AgentStatusChange from the given DB agent
// and overrides.  Fields that are always the same across callers (agentID,
// workerOnline, agentProvider, gitStatus) are filled in
// automatically.
func (s *agentOutputSink) buildStatusChange(
	dbAgent db.Agent,
	status leapmuxv1.AgentStatus,
	sessionID, permissionMode string,
) *leapmuxv1.AgentStatusChange {
	return &leapmuxv1.AgentStatusChange{
		AgentId:                s.agentID,
		Status:                 status,
		AgentSessionId:         sessionID,
		WorkerOnline:           true,
		PermissionMode:         permissionMode,
		Model:                  modelOrDefault(dbAgent.Model, dbAgent.AgentProvider),
		Effort:                 dbAgent.Effort,
		GitStatus:              gitutil.GetGitStatus(dbAgent.WorkingDir),
		AgentProvider:          s.agentProvider,
		CodexCollaborationMode: codexCollaborationModeForProvider(dbAgent.CodexCollaborationMode, dbAgent.AgentProvider),
	}
}

func (s *agentOutputSink) UpdatePermissionMode(mode string) {
	dbAgent, fetchErr := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	oldMode := ""
	if fetchErr == nil {
		oldMode = dbAgent.PermissionMode
	}

	_ = s.h.queries.SetAgentPermissionMode(bgCtx(), db.SetAgentPermissionModeParams{
		PermissionMode: mode,
		ID:             s.agentID,
	})

	// Broadcast statusChange so frontends update their permission mode display.
	if fetchErr == nil {
		sc := s.buildStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, dbAgent.AgentSessionID, mode)
		s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
			AgentId: s.agentID,
			Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
		})
	}

	// Broadcast settings_changed notification for the chat view.
	if oldMode != "" && oldMode != mode {
		s.BroadcastNotification(map[string]interface{}{
			"type": "settings_changed",
			"changes": map[string]interface{}{
				"permissionMode": map[string]string{"old": oldMode, "new": mode},
			},
		})
	}
}

func (s *agentOutputSink) BroadcastStatusActive(sessionID string) {
	existingAgent, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		slog.Error("failed to fetch agent for status broadcast",
			"agent_id", s.agentID, "error", err)
		return
	}

	sc := s.buildStatusChange(existingAgent, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sessionID, existingAgent.PermissionMode)
	s.h.watcher.BroadcastAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
		AgentId: s.agentID,
		Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
	})
}

func (s *agentOutputSink) BroadcastSessionInfo(info map[string]interface{}) {
	s.h.broadcastAgentSessionInfo(s.agentID, info)
}

func (s *agentOutputSink) BroadcastNotification(content map[string]interface{}) {
	s.h.BroadcastNotification(s.agentID, s.agentProvider, content)
}

func (s *agentOutputSink) SoftClearNotifThread() {
	s.h.softClearNotifThread(s.agentID)
}

func (s *agentOutputSink) StorePlanModeToolUse(toolUseID, targetMode string) {
	s.h.planModeToolUse.Store(toolUseID, targetMode)
}

func (s *agentOutputSink) LoadAndDeletePlanModeToolUse(toolUseID string) (string, bool) {
	v, ok := s.h.planModeToolUse.LoadAndDelete(toolUseID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (s *agentOutputSink) UpdatePlan(filePath string, content []byte, compression leapmuxv1.ContentCompression, title string) {
	s.h.updatePlan(s.agentID, filePath, content, compression, title)
}

// --- Internal helpers ---

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

// persistAndBroadcast persists a message and broadcasts it to watchers.
func (h *OutputHandler) persistAndBroadcast(agentID string, agentProvider leapmuxv1.AgentProvider, role leapmuxv1.MessageRole, contentJSON []byte, threadID string) error {
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
		AgentProvider:      agentProvider,
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
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(now),
	})
	return nil
}

// mergeIntoThread appends a child message to an existing thread parent.
func (h *OutputHandler) mergeIntoThread(agentID string, agentProvider leapmuxv1.AgentProvider, threadID string, childJSON []byte) bool {
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
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
		UpdatedAt:          timefmt.Format(now),
	})
	return true
}

// persistNotificationThreaded persists a notification message, appending it
// to the current notification thread if one exists.
func (h *OutputHandler) persistNotificationThreaded(agentID string, agentProvider leapmuxv1.AgentProvider, role leapmuxv1.MessageRole, contentJSON []byte) error {
	mu := h.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := h.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if threadRef.softClear.IsZero() || time.Since(threadRef.softClear) < notifThreadGracePeriod {
			if err := h.appendToNotificationThread(agentID, agentProvider, threadRef, role, contentJSON); err == nil {
				return nil
			}
		}
	}

	return h.createNotificationStandalone(agentID, agentProvider, role, contentJSON)
}

// appendToNotificationThread appends a message to an existing notification thread.
func (h *OutputHandler) appendToNotificationThread(agentID string, agentProvider leapmuxv1.AgentProvider, threadRef *notifThreadRef, role leapmuxv1.MessageRole, contentJSON []byte) error {
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
		AgentProvider:      agentProvider,
		CreatedAt:          timefmt.Format(parentRow.CreatedAt),
		UpdatedAt:          timefmt.Format(now),
	})

	return nil
}

// createNotificationStandalone creates a new standalone notification message.
func (h *OutputHandler) createNotificationStandalone(agentID string, agentProvider leapmuxv1.AgentProvider, role leapmuxv1.MessageRole, contentJSON []byte) error {
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
		AgentProvider:      agentProvider,
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
		AgentProvider:      agentProvider,
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
func (h *OutputHandler) BroadcastNotification(agentID string, agentProvider leapmuxv1.AgentProvider, content map[string]interface{}) {
	contentJSON, _ := json.Marshal(content)
	if err := h.persistNotificationThreaded(agentID, agentProvider, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// updatePlan persists a plan file path, content, and title for an agent.
func (h *OutputHandler) updatePlan(agentID, filePath string, compressed []byte, compression leapmuxv1.ContentCompression, title string) {
	agentRow, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for plan update", "agent_id", agentID, "error", err)
		return
	}

	// Preserve existing plan_title when the new content yields no title.
	if title == "" {
		title = agentRow.PlanTitle
	}

	shouldAutoRename := title != "" &&
		title != agentRow.Title &&
		(agentRow.Title == agentRow.PlanTitle ||
			agentAutoTitlePattern.MatchString(agentRow.Title))

	if shouldAutoRename {
		if err := h.queries.UpdateAgentPlanAndTitle(bgCtx(), db.UpdateAgentPlanAndTitleParams{
			PlanFilePath:           filePath,
			PlanContent:            compressed,
			PlanContentCompression: compression,
			PlanTitle:              title,
			Title:                  title,
			ID:                     agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
		} else {
			h.BroadcastNotification(agentID, agentRow.AgentProvider, map[string]interface{}{
				"type":  "agent_renamed",
				"title": title,
			})
		}
	} else {
		if err := h.queries.UpdateAgentPlan(bgCtx(), db.UpdateAgentPlanParams{
			PlanFilePath:           filePath,
			PlanContent:            compressed,
			PlanContentCompression: compression,
			PlanTitle:              title,
			ID:                     agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan", "agent_id", agentID, "error", err)
		}
	}
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
		Method  string                    `json:"method"`
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

	var codexRateLimitRaw json.RawMessage
	codexRateLimitLastIdx := -1

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
			// context_cleared supersedes any earlier compaction boundaries.
			keepAll = slices.DeleteFunc(keepAll, func(ir indexedRaw) bool {
				var e envelope
				if json.Unmarshal(ir.raw, &e) != nil {
					return false
				}
				return e.Type == "system" && (e.Subtype == "compact_boundary" || e.Subtype == "microcompact_boundary")
			})

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

		case env.Method == "account/rateLimits/updated":
			// Codex native rate limit notification: deduplicate, keep last.
			codexRateLimitRaw = raw
			codexRateLimitLastIdx = i

		case env.Type == "system" && env.Subtype == "status":
			latestStatusRaw = raw
			statusLastIdx = i

		case env.Type == "system" && (env.Subtype == "compact_boundary" || env.Subtype == "microcompact_boundary"):
			// Compaction supersedes any earlier context_cleared.
			if contextClearedLastIdx >= 0 && i > contextClearedLastIdx {
				contextClearedRaw = nil
				contextClearedLastIdx = -1
			}
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

	if codexRateLimitLastIdx >= 0 {
		entries = append(entries, indexedRaw{codexRateLimitLastIdx, codexRateLimitRaw})
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
