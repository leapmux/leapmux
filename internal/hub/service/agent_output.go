package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// notifThreadGracePeriod is how long a soft-cleared notification thread
// remains eligible for merging. Rapid-fire notifications (e.g. plan→default
// then default→bypassPermissions) interleaved with non-notification messages
// can still be consolidated within this window.
const notifThreadGracePeriod = time.Second

// notifMutex returns a per-agent mutex that serializes notification threading
// operations, preventing races between concurrent persistNotificationThreaded
// and softClearNotifThread calls.
func (s *AgentService) notifMutex(agentID string) *sync.Mutex {
	v, _ := s.notifMu.LoadOrStore(agentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// softClearNotifThread marks the current notification thread as soft-cleared.
// A subsequent notification arriving within notifThreadGracePeriod can still
// merge into the thread.
func (s *AgentService) softClearNotifThread(agentID string) {
	mu := s.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()
	if ref, ok := s.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if threadRef.softClear.IsZero() {
			threadRef.softClear = time.Now()
		}
	}
}

// HandleAgentOutput processes agent output from a worker and routes it to
// watchers. It also persists complete messages. Called by the worker connector
// service when it receives agent output.
func (s *AgentService) HandleAgentOutput(ctx context.Context, output *leapmuxv1.AgentOutput) {
	agentID := output.GetAgentId()
	content := output.GetContent()

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

	switch envelope.Type {
	case "assistant", "system", "user", "result":
		// For "system" messages, extract session_id from the init message
		// and persist it. Claude Code emits this as its first output after
		// receiving the first stdin message.
		if envelope.Type == "system" {
			var initMsg struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(content, &initMsg); err == nil && initMsg.SessionID != "" {
				// Fetch the current agent to compare session IDs and reuse for broadcast.
				existingAgent, fetchErr := s.queries.GetAgentByID(ctx, agentID)
				if fetchErr != nil {
					slog.Error("failed to fetch agent for session ID comparison",
						"agent_id", agentID, "error", fetchErr)
				} else if existingAgent.AgentSessionID != initMsg.SessionID {
					// Session ID is new or changed -- persist and broadcast.
					oldSessionID := existingAgent.AgentSessionID
					if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
						AgentSessionID: initMsg.SessionID,
						ID:             agentID,
					}); err != nil {
						slog.Error("failed to store agent session ID from init message",
							"agent_id", agentID, "error", err)
					} else if oldSessionID == "" {
						slog.Info("extracted agent session ID from init message",
							"agent_id", agentID, "session_id", initMsg.SessionID)
					} else {
						slog.Info("agent session ID changed",
							"agent_id", agentID,
							"old_session_id", oldSessionID,
							"new_session_id", initMsg.SessionID)
					}
					// Broadcast session ID to frontend so it knows the agent is resumable.
					sc := AgentStatusChange(&existingAgent, true)
					sc.Status = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
					sc.AgentSessionId = initMsg.SessionID
					sc.GitStatus = s.GetGitStatus(agentID)
					s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
						AgentId: agentID,
						Event: &leapmuxv1.AgentEvent_StatusChange{
							StatusChange: sc,
						},
					})
				}
			}
		}

		// Thread notification-eligible system messages (status, compact_boundary,
		// microcompact_boundary) into the current notification thread.
		if envelope.Type == "system" && isNotificationThreadable(content, role) {
			// Skip null status when there is no meaningful transition to communicate.
			// This covers (a) the very first status if it is null (renders invisible
			// on the frontend anyway) and (b) redundant null→null repeats.
			if statusVal, ok := extractStatusValue(content); ok {
				prev, hasPrev := s.lastAgentStatus.Swap(agentID, statusVal)
				if statusVal == "" && (!hasPrev || prev.(string) == "") {
					return
				}
			}

			if err := s.persistNotificationThreaded(ctx, agentID, role, content); err != nil {
				slog.Error("persist notification-threaded system message", "agent_id", agentID, "error", err)
			}
			return
		}

		// Non-notification messages soft-clear the notification thread so that
		// rapid-fire notifications (e.g. plan→default→bypass within milliseconds)
		// can still merge within the grace period.
		s.softClearNotifThread(agentID)

		// Extract agent context metadata (total_cost_usd, token usage)
		// from assistant and result messages. These fields are optional and may be
		// absent -- only broadcast when at least one is present.
		if envelope.Type == "assistant" || envelope.Type == "result" {
			var infoFields struct {
				CostUSD *float64 `json:"total_cost_usd"`
			}
			if err := json.Unmarshal(content, &infoFields); err == nil {
				info := map[string]interface{}{}
				if infoFields.CostUSD != nil {
					info["total_cost_usd"] = *infoFields.CostUSD
				}

				// Snapshot token usage from assistant messages and extract
				// contextWindow from result messages. Each assistant message
				// reports the full context usage for that turn, so we store
				// the latest values rather than accumulating.
				snapshot := s.getOrCreateUsageSnapshot(agentID)

				if envelope.Type == "assistant" {
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

				if envelope.Type == "result" {
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

				// Broadcast the latest usage snapshot only when there is
				// actual non-zero token data to report.
				// Debounce: include contextUsage at most once every 10s,
				// unless this is a result message (turn end) which always
				// reports the final usage immediately.
				snapshot.mu.Lock()
				hasUsage := snapshot.InputTokens > 0 || snapshot.OutputTokens > 0 ||
					snapshot.CacheCreationInputTokens > 0 || snapshot.CacheReadInputTokens > 0
				if hasUsage {
					now := time.Now()
					shouldBroadcastUsage := envelope.Type == "result" ||
						now.Sub(snapshot.LastBroadcast) >= 10*time.Second
					if shouldBroadcastUsage {
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
					s.broadcastAgentSessionInfo(agentID, info)
				}
			}
		}

		// Extract thread_id — try each extractor until one succeeds.
		// Each looks at different JSON paths, so at most one will match.
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

		// Child message with a matching parent: merge into the parent's row
		// instead of creating a new row. This keeps thread messages in a
		// single DB row and bumps the parent's seq so reconnection via
		// afterSeq replays the updated thread.
		if threadID != "" && (role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER || role == leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM) {
			if s.mergeIntoThread(ctx, agentID, threadID, content) {
				if role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
					// Detect plan mode changes from tool_result.
					s.detectPlanModeFromToolResult(ctx, agentID, content)
				}
				return
			}
			// Parent not found or merge failed — fall through to standalone insert.
		}

		// Standalone message or no matching parent — wrap content and insert.
		if err := s.persistAndBroadcast(ctx, agentID, role, content, threadID); err != nil {
			slog.Error("persist agent message", "agent_id", agentID, "error", err)
			return
		}

		// Detect plan mode changes from tool_use / tool_result messages.
		switch envelope.Type {
		case "assistant":
			s.trackPlanModeToolUse(content)
			s.trackPlanFilePath(ctx, agentID, content)
			// Schedule auto-continue on transient API errors, or reset
			// the backoff when the agent produces a normal response.
			if isSyntheticAPIError(content) {
				s.scheduleAutoContinue(agentID)
			} else {
				s.resetAutoContinue(agentID)
			}
		case "user":
			s.detectPlanModeFromToolResult(ctx, agentID, content)
		}

	case "control_request":
		// Parse request_id from control request JSON.
		var cr struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(content, &cr); err != nil {
			slog.Warn("invalid control_request JSON", "agent_id", agentID, "error", err)
			return
		}
		if err := s.queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
			AgentID:   agentID,
			RequestID: cr.RequestID,
			Payload:   content,
		}); err != nil {
			slog.Error("persist control request", "agent_id", agentID, "request_id", cr.RequestID, "error", err)
		}
		s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_ControlRequest{
				ControlRequest: &leapmuxv1.AgentControlRequest{
					AgentId:   agentID,
					RequestId: cr.RequestID,
					Payload:   content,
				},
			},
		})

	case "control_cancel_request":
		// Parse request_id from cancel request JSON.
		var cc struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(content, &cc); err != nil {
			slog.Warn("invalid control_cancel_request JSON", "agent_id", agentID, "error", err)
			return
		}
		if err := s.queries.DeleteControlRequest(ctx, db.DeleteControlRequestParams{
			AgentID:   agentID,
			RequestID: cc.RequestID,
		}); err != nil {
			slog.Error("delete control request on cancel", "agent_id", agentID, "request_id", cc.RequestID, "error", err)
		}
		s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_ControlCancel{
				ControlCancel: &leapmuxv1.AgentControlCancelRequest{
					AgentId:   agentID,
					RequestId: cc.RequestID,
				},
			},
		})

	case "control_response":
		// Handle control_response from Claude Code (e.g. ack for set_permission_mode).
		// Update DB with confirmed permission mode and broadcast to watchers.
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
				s.setAgentPermissionMode(ctx, agentID, cr.Response.Response.Mode)
			}
		}

	case "rate_limit_event":
		var rle struct {
			RateLimitInfo json.RawMessage `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(content, &rle); err != nil || len(rle.RateLimitInfo) == 0 {
			break
		}

		// Extract rateLimitType to use as key; default to "unknown".
		var rlType struct {
			RateLimitType string `json:"rateLimitType"`
		}
		_ = json.Unmarshal(rle.RateLimitInfo, &rlType)
		if rlType.RateLimitType == "" {
			rlType.RateLimitType = "unknown"
		}

		// Broadcast ephemeral agent_session_info so the frontend
		// popover store gets updated in real time.
		rateLimits := map[string]json.RawMessage{
			rlType.RateLimitType: rle.RateLimitInfo,
		}
		s.broadcastAgentSessionInfo(agentID, map[string]interface{}{
			"rateLimits": rateLimits,
		})

		// Persist as a LEAPMUX notification for the chat bubble.
		notifContent, _ := json.Marshal(map[string]interface{}{
			"type":            "rate_limit",
			"rate_limit_info": rle.RateLimitInfo,
		})
		if err := s.persistNotificationThreaded(ctx, agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, notifContent); err != nil {
			slog.Error("persist rate_limit notification", "agent_id", agentID, "error", err)
		}

	default:
		// Streaming chunk or other event -- forward to watchers without persisting.
		s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
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

// persistAndBroadcast persists a message and broadcasts it to watchers.
// It retries with an updated sequence number if the insert fails due to a
// concurrent seq collision (UNIQUE constraint on agent_id, seq).
func (s *AgentService) persistAndBroadcast(ctx context.Context, agentID string, role leapmuxv1.MessageRole, contentJSON []byte, threadID string) error {
	msgID := id.Generate()
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           threadID,
		CreatedAt:          now,
	})
	if err != nil {
		return fmt.Errorf("persist message: %w", err)
	}

	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})
	return nil
}

// broadcastNotification persists and broadcasts a MESSAGE_ROLE_LEAPMUX message
// for platform notifications (settings changes, interrupts). Consecutive
// notifications are threaded into a single DB row.
func (s *AgentService) broadcastNotification(ctx context.Context, agentID string, content map[string]interface{}) {
	contentJSON, _ := json.Marshal(content)
	if err := s.persistNotificationThreaded(ctx, agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, contentJSON); err != nil {
		slog.Warn("failed to persist notification", "agent_id", agentID, "error", err)
	}
}

// persistNotificationThreaded persists a notification message, appending it to
// the current notification thread if one exists. If no thread exists or the
// merge fails, creates a new standalone message. The thread is consolidated
// after each append to keep it bounded.
//
// A per-agent mutex serializes calls so that concurrent notifications (e.g.
// from the gRPC handler and the worker stream) don't race on the thread ref.
func (s *AgentService) persistNotificationThreaded(ctx context.Context, agentID string, role leapmuxv1.MessageRole, contentJSON []byte) error {
	mu := s.notifMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	// Check if there's an existing notification thread to append to.
	// A soft-cleared thread is still eligible within the grace period so that
	// rapid-fire notifications interleaved with non-notification messages
	// (e.g. plan→default, assistant msg, default→bypass) get consolidated.
	if ref, ok := s.lastNotifThread.Load(agentID); ok {
		threadRef := ref.(*notifThreadRef)
		if threadRef.softClear.IsZero() || time.Since(threadRef.softClear) < notifThreadGracePeriod {
			if err := s.appendToNotificationThread(ctx, agentID, threadRef, role, contentJSON); err == nil {
				return nil
			}
			// Merge failed — fall through to standalone insert.
			slog.Debug("notification thread merge failed, creating standalone", "agent_id", agentID)
		}
	}

	// Create a new standalone notification message.
	return s.createNotificationStandalone(ctx, agentID, role, contentJSON)
}

// appendToNotificationThread appends a message to an existing notification
// thread row, consolidates, and broadcasts the updated thread.
func (s *AgentService) appendToNotificationThread(ctx context.Context, agentID string, threadRef *notifThreadRef, role leapmuxv1.MessageRole, contentJSON []byte) error {
	parentRow, err := s.queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{
		ID:      threadRef.msgID,
		AgentID: agentID,
	})
	if err != nil {
		return fmt.Errorf("fetch notification thread parent: %w", err)
	}

	parentData, err := msgcodec.Decompress(parentRow.Content, parentRow.ContentCompression)
	if err != nil {
		return fmt.Errorf("decompress notification thread parent: %w", err)
	}

	wrapper, err := unwrapContent(parentData)
	if err != nil {
		return fmt.Errorf("parse notification thread parent: %w", err)
	}

	// Append the new message and consolidate.
	wrapper.OldSeqs = append(wrapper.OldSeqs, parentRow.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}
	wrapper.Messages = append(wrapper.Messages, contentJSON)
	wrapper.Messages = consolidateNotificationThread(wrapper.Messages)

	merged, _ := json.Marshal(wrapper)

	now := time.Now()
	mergedCompressed, mergedCompType := msgcodec.Compress(merged)
	newSeq, err := s.queries.UpdateMessageThread(ctx, db.UpdateMessageThreadParams{
		Content:            mergedCompressed,
		ContentCompression: mergedCompType,
		UpdatedAt:          sql.NullTime{Time: now, Valid: true},
		ID:                 parentRow.ID,
		AgentID:            agentID,
	})
	if err != nil {
		return fmt.Errorf("update notification thread: %w", err)
	}

	// Update the in-memory reference.
	threadRef.seq = newSeq
	s.lastNotifThread.Store(agentID, threadRef)

	// Broadcast the updated thread. Use the role of the parent row
	// (the first message in the thread determines the role).
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
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

// createNotificationStandalone creates a new standalone notification message
// and records it as the current notification thread for the agent.
func (s *AgentService) createNotificationStandalone(ctx context.Context, agentID string, role leapmuxv1.MessageRole, contentJSON []byte) error {
	msgID := id.Generate()
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	now := time.Now()

	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		return fmt.Errorf("persist notification: %w", err)
	}

	// Record as current notification thread.
	s.lastNotifThread.Store(agentID, &notifThreadRef{
		msgID: msgID,
		seq:   seq,
	})

	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               role,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})
	return nil
}

// getOrCreateUsageSnapshot returns the token usage snapshot for the given agent,
// creating one if it doesn't exist.
func (s *AgentService) getOrCreateUsageSnapshot(agentID string) *contextUsageSnapshot {
	if v, ok := s.contextUsage.Load(agentID); ok {
		return v.(*contextUsageSnapshot)
	}
	snap := &contextUsageSnapshot{}
	actual, _ := s.contextUsage.LoadOrStore(agentID, snap)
	return actual.(*contextUsageSnapshot)
}

// resetUsageSnapshot resets the token usage snapshot for the given agent
// (e.g. after /clear).
func (s *AgentService) resetUsageSnapshot(agentID string) {
	s.contextUsage.Delete(agentID)
}

// broadcastAgentSessionInfo broadcasts agent session metadata (gitBranch, cwd,
// version, total_cost_usd, rateLimits) to watchers as a LEAPMUX message WITHOUT
// persisting it. The frontend stores this info in per-agent localStorage.
func (s *AgentService) broadcastAgentSessionInfo(agentID string, info map[string]interface{}) {
	content := map[string]interface{}{
		"type": "agent_session_info",
		"info": info,
	}
	contentJSON, _ := json.Marshal(content)
	s.broadcastEphemeral(agentID, contentJSON)
}

// broadcastEphemeral broadcasts a LEAPMUX-role message to watchers without
// persisting it to the database. Uses seq=-1 as a sentinel to indicate the
// message is ephemeral and should not be stored in chat history.
func (s *AgentService) broadcastEphemeral(agentID string, contentJSON []byte) {
	compressed, compressionType := msgcodec.Compress(contentJSON)
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 id.Generate(),
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                -1,
	})
}
