package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// extractToolUseID parses an assistant message's JSON content and returns the
// first tool_use block's ID, or "" if none is found.
// Expected structure: {"message":{"content":[{"type":"tool_use","id":"toolu_..."}]}}
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

// extractParentToolUseID extracts the top-level "parent_tool_use_id" field
// from a user control message, or "" if not present.
// Expected structure: {"type":"user","parent_tool_use_id":"toolu_...","message":{...}}
func extractParentToolUseID(content []byte) string {
	var msg struct {
		ParentToolUseID string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	return msg.ParentToolUseID
}

// extractSystemToolUseID extracts the top-level tool_use_id from a system
// message, or "" if not present.
// Expected structure: {"type":"system","subtype":"task_started","tool_use_id":"toolu_..."}
func extractSystemToolUseID(content []byte) string {
	var msg struct {
		ToolUseID string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return ""
	}
	return msg.ToolUseID
}

// extractToolResultID parses a user message's JSON content and returns the
// first tool_result block's tool_use_id, or "" if none is found.
// Expected structure: {"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_..."}]}}
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

// threadWrapper is the content envelope stored in the DB for every message.
// It tracks the original seq values of the message before thread merges
// (so reconnection via afterSeq still works) and holds the raw Claude Code
// JSON for each message in the thread.
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

// unwrapContent parses a threadWrapper from compressed content.
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

// notificationThreadable subtypes for system messages that participate in
// notification threading. Unrecognized subtypes remain standalone.
var notificationThreadableSubtypes = map[string]bool{
	"status":                true,
	"compact_boundary":      true,
	"microcompact_boundary": true,
}

// isNotificationThreadable returns true if a message should participate in
// notification threading. This includes LEAPMUX notifications (settings_changed,
// context_cleared, interrupted) and recognized system subtypes (status,
// compact_boundary, microcompact_boundary). System init messages and
// unrecognized subtypes are excluded.
func isNotificationThreadable(content []byte, role leapmuxv1.MessageRole) bool {
	switch role {
	case leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX:
		var msg struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(content, &msg) != nil {
			return false
		}
		return msg.Type == "settings_changed" || msg.Type == "context_cleared" || msg.Type == "interrupted"

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

// extractStatusValue extracts the status value from a system status message.
// Returns (status, true) for status messages, where status is "" for JSON null
// and the actual string for non-null values. Returns ("", false) for non-status messages.
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

// consolidateNotificationThread consolidates a notification thread's messages,
// keeping the thread bounded:
//   - settings_changed: merge into one, keeping original old and latest new;
//     remove if all changes are no-ops (old == new).
//   - context_cleared: keep only one.
//   - interrupted: keep only one.
//   - status: keep only the latest.
//   - compact_boundary / microcompact_boundary: keep all (each is a distinct event).
func consolidateNotificationThread(messages []json.RawMessage) []json.RawMessage {
	type envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}

	// Accumulate settings changes across all settings_changed messages.
	type settingsChange struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	mergedChanges := map[string]settingsChange{}
	hasContextCleared := false
	contextClearedRaw := json.RawMessage(nil)
	hasInterrupted := false
	interruptedRaw := json.RawMessage(nil)
	var latestStatusRaw json.RawMessage
	var compactionBoundaries []json.RawMessage

	for _, raw := range messages {
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			// Unrecognized — keep as-is (compaction boundary).
			compactionBoundaries = append(compactionBoundaries, raw)
			continue
		}

		switch {
		case env.Type == "settings_changed":
			var sc struct {
				Changes        map[string]settingsChange `json:"changes"`
				ContextCleared bool                      `json:"contextCleared"`
			}
			if json.Unmarshal(raw, &sc) == nil {
				for key, val := range sc.Changes {
					if existing, ok := mergedChanges[key]; ok {
						// Keep original old, use latest new.
						mergedChanges[key] = settingsChange{Old: existing.Old, New: val.New}
					} else {
						mergedChanges[key] = val
					}
				}
				if sc.ContextCleared {
					hasContextCleared = true
				}
			}

		case env.Type == "context_cleared":
			hasContextCleared = true
			contextClearedRaw = raw

		case env.Type == "interrupted":
			hasInterrupted = true
			interruptedRaw = raw

		case env.Type == "system" && env.Subtype == "status":
			latestStatusRaw = raw

		case env.Type == "system" && (env.Subtype == "compact_boundary" || env.Subtype == "microcompact_boundary"):
			compactionBoundaries = append(compactionBoundaries, raw)

		default:
			// Unknown — keep as-is.
			compactionBoundaries = append(compactionBoundaries, raw)
		}
	}

	var result []json.RawMessage

	// Emit consolidated settings_changed (if any effective changes remain).
	if len(mergedChanges) > 0 {
		// Filter out no-op changes.
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
			if hasContextCleared {
				entry["contextCleared"] = true
			}
			if data, err := json.Marshal(entry); err == nil {
				result = append(result, data)
			}
		} else if hasContextCleared {
			// Settings cancelled out, but context was cleared.
			entry := map[string]interface{}{
				"type": "context_cleared",
			}
			if data, err := json.Marshal(entry); err == nil {
				result = append(result, data)
			}
			hasContextCleared = false // Already emitted.
		}
	}

	// Emit context_cleared if not already part of settings_changed.
	if hasContextCleared && contextClearedRaw != nil {
		result = append(result, contextClearedRaw)
	}

	// Emit interrupted.
	if hasInterrupted && interruptedRaw != nil {
		result = append(result, interruptedRaw)
	}

	// Emit latest status.
	if latestStatusRaw != nil {
		result = append(result, latestStatusRaw)
	}

	// Emit all compaction boundaries.
	result = append(result, compactionBoundaries...)

	if len(result) == 0 {
		// Everything consolidated away (e.g. settings toggled back and forth).
		// Return a no-op settings_changed that renders as invisible on the frontend.
		data, _ := json.Marshal(map[string]interface{}{
			"type":    "settings_changed",
			"changes": map[string]interface{}{},
		})
		return []json.RawMessage{data}
	}

	return result
}

// mergeIntoThread appends a child message JSON to an existing thread parent
// identified by threadID. It bumps the parent's seq so reconnection via
// afterSeq replays the updated thread. Returns true if the merge succeeded.
func (s *AgentService) mergeIntoThread(ctx context.Context, agentID, threadID string, childJSON []byte) bool {
	parentRow, err := s.queries.GetMessageByAgentAndThreadID(ctx, db.GetMessageByAgentAndThreadIDParams{
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

	maxSeq, err := s.queries.GetMaxSeqByAgentID(ctx, agentID)
	if err != nil {
		slog.Error("get max seq for thread merge", "agent_id", agentID, "error", err)
		return false
	}
	newSeq := maxSeq + 1

	now := time.Now()
	mergedCompressed, mergedCompType := msgcodec.Compress(merged)
	if err := s.queries.UpdateMessageThread(ctx, db.UpdateMessageThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		Seq:                newSeq,
		UpdatedAt:          sql.NullTime{Time: now, Valid: true},
		ID:                 parentRow.ID,
		AgentID:            agentID,
	}); err != nil {
		slog.Error("update parent thread", "agent_id", agentID, "thread_id", threadID, "error", err)
		return false
	}

	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
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

// broadcastMessage broadcasts a single agent message event to all watchers.
func (s *AgentService) broadcastMessage(agentID string, msg *leapmuxv1.AgentChatMessage) {
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_AgentMessage{
			AgentMessage: msg,
		},
	})
}
