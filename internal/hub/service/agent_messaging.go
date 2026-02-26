package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
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
		ref.(*notifThreadRef).softClear = time.Now()
	}
}

func (s *AgentService) SendAgentMessage(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SendAgentMessageRequest],
) (*connect.Response[leapmuxv1.SendAgentMessageResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	content := strings.TrimSpace(req.Msg.GetContent())
	if content == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content is required"))
	}

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	ws, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}

	// Any control_request (set_permission_mode, interrupt, etc.) is sent as raw
	// input to Claude Code's stdin — not persisted as a chat message.
	if isControlRequest(content) {
		// set_permission_mode and interrupt on a non-running agent can be handled
		// entirely in the Hub. The agent is considered non-running if its DB
		// status is not ACTIVE *or* if the worker is offline (the agent process
		// can't be alive without a worker, even if the DB status is stale).
		agentNotRunning := agent.Status != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE ||
			!s.workerMgr.IsOnline(agent.WorkerID)
		if agentNotRunning {
			if mode, ok := parseSetPermissionMode(content); ok {
				s.setAgentPermissionMode(ctx, agentID, mode)
				return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
			}
			if isInterruptRequest(content) {
				// Agent is already gone — nothing to interrupt.
				return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
			}
		}

		deliverCtx, deliverCancel := context.WithTimeout(context.Background(), s.timeoutCfg.AgentStartupTimeout()+s.timeoutCfg.APITimeout())
		defer deliverCancel()

		agentNotFound, err := s.deliverRawInputToWorker(deliverCtx, agent.WorkerID, agent.WorkspaceID, agentID, []byte(content))
		if agentNotFound {
			if isInterruptRequest(content) {
				// Agent is already gone — nothing to interrupt.
				return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
			}
			slog.Info("agent process not found, restarting before retry", "agent_id", agentID)
			if restartErr := s.ensureAgentActive(deliverCtx, &agent, ws); restartErr == nil {
				_, err = s.deliverRawInputToWorker(deliverCtx, agent.WorkerID, agent.WorkspaceID, agentID, []byte(content))
			}
		}
		if err != nil {
			return nil, err
		}
		// Broadcast interrupt notification after successful delivery.
		if isInterruptRequest(content) {
			s.cancelAutoContinue(agentID)
			s.broadcastNotification(ctx, agentID, map[string]interface{}{
				"type": "interrupted",
			})
		}
		return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
	}

	// Hub-intercepted /clear: restart the agent without a session ID to
	// clear context, then broadcast a notification.
	if content == "/clear" {
		s.clearAgentContext(ctx, &agent, ws)
		return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
	}

	// Cancel any pending auto-continue — the user is sending their own message.
	s.cancelAutoContinue(agentID)

	// Persist the user message BEFORE attempting resume/delivery so the
	// message appears in chat even if delivery fails.
	contentJSON, _ := json.Marshal(map[string]string{"content": content})
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	msgID := id.Generate()
	now := time.Now()
	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist message: %w", err))
	}

	// Broadcast the user message to watchers so the frontend sees it in real-time.
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})

	// Create a fresh context for agent operations. If the agent needs to be
	// started/resumed, use a longer timeout that includes the startup time.
	agentActive := agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
	var msgTimeout time.Duration
	if agentActive {
		msgTimeout = s.timeoutCfg.APITimeout()
	} else {
		msgTimeout = s.timeoutCfg.AgentStartupTimeout() + s.timeoutCfg.APITimeout()
	}
	msgCtx, msgCancel := context.WithTimeout(context.Background(), msgTimeout)
	defer msgCancel()

	// Resume the agent if needed.
	if !agentActive {
		if err := s.ensureAgentActive(msgCtx, &agent, ws); err != nil {
			errMsg := fmt.Sprintf("resume failed: %v", err)
			s.setDeliveryError(context.Background(), agentID, msgID, errMsg)
			return nil, err
		}
	}

	// Deliver the message to the worker, retrying once if the agent process is gone.
	agentNotFound, deliveryErr := s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
	if agentNotFound {
		slog.Info("agent process not found, restarting before retry", "agent_id", agentID)
		// Extend timeout for the restart + redeliver cycle.
		msgCancel()
		msgCtx, msgCancel = context.WithTimeout(context.Background(), s.timeoutCfg.AgentStartupTimeout()+s.timeoutCfg.APITimeout())
		defer msgCancel()
		if restartErr := s.ensureAgentActive(msgCtx, &agent, ws); restartErr == nil {
			_, deliveryErr = s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
		}
	}
	if deliveryErr != nil {
		s.setDeliveryError(context.Background(), agentID, msgID, deliveryErr.Error())
		return nil, deliveryErr
	}

	return connect.NewResponse(&leapmuxv1.SendAgentMessageResponse{}), nil
}

func (s *AgentService) RetryAgentMessage(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RetryAgentMessageRequest],
) (*connect.Response[leapmuxv1.RetryAgentMessageResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	messageID := req.Msg.GetMessageId()
	if agentID == "" || messageID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent_id and message_id are required"))
	}

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	ws, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}

	// Load the message and verify it's a failed user message.
	msg, err := s.queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{
		ID:      messageID,
		AgentID: agentID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only user messages can be retried"))
	}
	if msg.DeliveryError == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("message has no delivery error"))
	}

	// Decompress and extract the content string from the stored JSON.
	decompressed, err := msgcodec.Decompress(msg.Content, msg.ContentCompression)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decompress message content: %w", err))
	}
	var parsed struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(decompressed, &parsed); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse message content: %w", err))
	}

	// Create a fresh context for agent operations.
	retryActive := agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
	var retryTimeout time.Duration
	if retryActive {
		retryTimeout = s.timeoutCfg.APITimeout()
	} else {
		retryTimeout = s.timeoutCfg.AgentStartupTimeout() + s.timeoutCfg.APITimeout()
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), retryTimeout)
	defer retryCancel()

	// Resume agent if needed.
	if !retryActive {
		if err := s.ensureAgentActive(retryCtx, &agent, ws); err != nil {
			return nil, err
		}
	}

	// Deliver to worker, retrying once if the agent process is gone.
	agentNotFound, deliveryErr := s.deliverMessageToWorker(retryCtx, agent.WorkerID, agent.WorkspaceID, agentID, parsed.Content)
	if agentNotFound {
		slog.Info("agent process not found, restarting before retry", "agent_id", agentID)
		// Extend timeout for the restart + redeliver cycle.
		retryCancel()
		retryCtx, retryCancel = context.WithTimeout(context.Background(), s.timeoutCfg.AgentStartupTimeout()+s.timeoutCfg.APITimeout())
		defer retryCancel()
		if restartErr := s.ensureAgentActive(retryCtx, &agent, ws); restartErr == nil {
			_, deliveryErr = s.deliverMessageToWorker(retryCtx, agent.WorkerID, agent.WorkspaceID, agentID, parsed.Content)
		}
	}
	if deliveryErr != nil {
		s.setDeliveryError(context.Background(), agentID, messageID, deliveryErr.Error())
		return nil, deliveryErr
	}

	// Clear the delivery error on success.
	s.clearDeliveryError(ctx, agentID, messageID)

	return connect.NewResponse(&leapmuxv1.RetryAgentMessageResponse{}), nil
}

func (s *AgentService) DeleteAgentMessage(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteAgentMessageRequest],
) (*connect.Response[leapmuxv1.DeleteAgentMessageResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	messageID := req.Msg.GetMessageId()
	if agentID == "" || messageID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent_id and message_id are required"))
	}

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, agent.WorkspaceID); err != nil {
		return nil, err
	}

	// Load the message and verify it's a failed user message.
	msg, err := s.queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{
		ID:      messageID,
		AgentID: agentID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only user messages can be deleted"))
	}
	if msg.DeliveryError == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("message has no delivery error"))
	}

	// Delete from DB.
	if err := s.queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{
		ID:      messageID,
		AgentID: agentID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete message: %w", err))
	}

	// Broadcast deletion to watchers.
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_MessageDeleted{
			MessageDeleted: &leapmuxv1.AgentMessageDeleted{
				AgentId:   agentID,
				MessageId: messageID,
			},
		},
	})

	return connect.NewResponse(&leapmuxv1.DeleteAgentMessageResponse{}), nil
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

// deliverRawInputToWorker sends raw NDJSON bytes directly to the worker agent's stdin,
// bypassing the UserInputMessage wrapper. Used for synthetic messages like plan mode toggles.
// Returns agentNotFound=true when the worker reports that the agent process does not exist.
func (s *AgentService) deliverRawInputToWorker(ctx context.Context, workerID, workspaceID, agentID string, content []byte) (agentNotFound bool, err error) {
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("worker is offline"))
	}

	ackResp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentRawInput{
			AgentRawInput: &leapmuxv1.AgentRawInput{
				WorkspaceId: workspaceID,
				AgentId:     agentID,
				Content:     content,
			},
		},
	})
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("send raw input: %w", err))
	}

	ack := ackResp.GetAgentInputAck()
	if ack.GetError() == leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND {
		return true, connect.NewError(connect.CodeInternal, fmt.Errorf("raw input failed: %s", ack.GetErrorReason()))
	}
	if ack.GetError() != leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_UNSPECIFIED {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("raw input failed: %s", ack.GetErrorReason()))
	}

	return false, nil
}

// deliverMessageToWorker sends a user message to the worker agent.
// Returns agentNotFound=true when the worker reports that the agent process does not exist.
// The caller is responsible for persisting delivery errors.
func (s *AgentService) deliverMessageToWorker(ctx context.Context, workerID, workspaceID, agentID, content string) (agentNotFound bool, err error) {
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("worker is offline"))
	}

	ackResp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentInput{
			AgentInput: &leapmuxv1.AgentInput{
				WorkspaceId: workspaceID,
				AgentId:     agentID,
				Content:     []byte(content),
			},
		},
	})
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("send agent input: %w", err))
	}

	ack := ackResp.GetAgentInputAck()
	if ack.GetError() == leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_AGENT_NOT_FOUND {
		return true, connect.NewError(connect.CodeInternal, fmt.Errorf("agent input failed: %s", ack.GetErrorReason()))
	}
	if ack.GetError() != leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_UNSPECIFIED {
		return false, connect.NewError(connect.CodeInternal, fmt.Errorf("agent input failed: %s", ack.GetErrorReason()))
	}

	return false, nil
}

// setDeliveryError persists a delivery error in DB and broadcasts it to watchers.
// Uses context.Background() for the DB write because the RPC context may have expired.
func (s *AgentService) setDeliveryError(ctx context.Context, agentID, msgID, errMsg string) {
	if dbErr := s.queries.SetMessageDeliveryError(ctx, db.SetMessageDeliveryErrorParams{
		DeliveryError: errMsg,
		ID:            msgID,
		AgentID:       agentID,
	}); dbErr != nil {
		slog.Error("persist delivery error", "agent_id", agentID, "msg_id", msgID, "error", dbErr)
	}
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_MessageError{
			MessageError: &leapmuxv1.AgentMessageError{
				AgentId:   agentID,
				MessageId: msgID,
				Error:     errMsg,
			},
		},
	})
}

// clearDeliveryError clears a delivery error in DB and broadcasts the cleared state.
func (s *AgentService) clearDeliveryError(ctx context.Context, agentID, msgID string) {
	s.setDeliveryError(ctx, agentID, msgID, "")
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
				threadRef.softClear = time.Time{} // revive
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

// clearAgentContext clears the agent's context. If the agent is active and
// its worker is connected, it is stopped and marked for a fresh restart
// (without a session ID); the context_cleared notification is broadcast
// after the restart completes. Otherwise (agent not active or worker
// disconnected), the session ID is cleared immediately and the notification
// is broadcast right away.
func (s *AgentService) clearAgentContext(ctx context.Context, agent *db.Agent, ws *db.Workspace) {
	s.resetUsageSnapshot(agent.ID)
	s.lastAgentStatus.Delete(agent.ID)

	// If the agent is active and the worker is connected, stop it and
	// let the restart cycle handle the rest.
	if agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		if conn := s.workerMgr.Get(agent.WorkerID); conn != nil {
			s.restartPending.Store(agent.ID, &RestartOptions{
				ClearSession: true,
			})
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStop{
					AgentStop: &leapmuxv1.AgentStopRequest{
						WorkspaceId: agent.WorkspaceID,
						AgentId:     agent.ID,
					},
				},
			})
			return
		}
	}

	// Agent is not running or worker is disconnected — clear session ID
	// directly and notify.
	if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "",
		ID:             agent.ID,
	}); err != nil {
		slog.Warn("failed to clear agent session ID", "agent_id", agent.ID, "error", err)
	}
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type": "context_cleared",
	})
}

// clearAgentContextForPlanExecution clears the agent's context and queues a
// synthetic user message containing the plan content to be sent after restart.
func (s *AgentService) clearAgentContextForPlanExecution(ctx context.Context, agent *db.Agent, ws *db.Workspace, planContent string) {
	s.resetUsageSnapshot(agent.ID)
	s.lastAgentStatus.Delete(agent.ID)

	opts := &RestartOptions{
		ClearSession:         true,
		SyntheticUserMessage: "Execute the following plan:\n\n---\n\n" + planContent,
		PlanExec:             true,
		PlanFilePath:         agent.PlanFilePath,
	}

	// If the agent is active and the worker is connected, stop it and
	// let the restart cycle handle the rest.
	if agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		if conn := s.workerMgr.Get(agent.WorkerID); conn != nil {
			s.restartPending.Store(agent.ID, opts)
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStop{
					AgentStop: &leapmuxv1.AgentStopRequest{
						WorkspaceId: agent.WorkspaceID,
						AgentId:     agent.ID,
					},
				},
			})
			return
		}
	}

	// Agent is not running or worker is disconnected — clear session ID
	// directly and notify.
	if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "",
		ID:             agent.ID,
	}); err != nil {
		slog.Warn("failed to clear agent session ID", "agent_id", agent.ID, "error", err)
	}
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type": "context_cleared",
	})
	s.broadcastNotification(ctx, agent.ID, map[string]interface{}{
		"type":            "plan_execution",
		"context_cleared": true,
		"plan_file_path":  opts.PlanFilePath,
	})
}

// sendSyntheticUserMessage persists a hidden user message (not displayed in
// chat) and delivers it to the worker. Used to inject plan content after a
// context-clearing restart.
func (s *AgentService) sendSyntheticUserMessage(ctx context.Context, agentID string, content string) error {
	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Build hidden message content.
	contentJSON, _ := json.Marshal(map[string]interface{}{
		"content": content,
		"hidden":  true,
	})
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	msgID := id.Generate()
	now := time.Now()

	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		return fmt.Errorf("persist synthetic message: %w", err)
	}

	// Broadcast to watchers (frontend checks hidden flag and skips rendering).
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})

	// Deliver to worker.
	msgCtx, msgCancel := context.WithTimeout(ctx, s.timeoutCfg.APITimeout())
	defer msgCancel()

	agentNotFound, deliveryErr := s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
	if agentNotFound {
		slog.Warn("synthetic message: agent not found on worker", "agent_id", agentID)
		return fmt.Errorf("agent not found on worker")
	}
	if deliveryErr != nil {
		s.setDeliveryError(ctx, agentID, msgID, deliveryErr.Error())
		return deliveryErr
	}

	return nil
}

// trackPlanModeToolUse inspects an assistant message for EnterPlanMode or
// ExitPlanMode tool_use blocks and records the tool_use_id for later matching
// against the tool_result confirmation.
func (s *AgentService) trackPlanModeToolUse(content []byte) {
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
			s.planModeToolUse.Store(block.ID, "plan")
		case "ExitPlanMode":
			s.planModeToolUse.Store(block.ID, "default")
		}
	}
}

// trackPlanFilePath inspects an assistant message for Write or Edit tool_use
// blocks whose file_path targets the agent's ~/.claude/plans/ directory,
// and persists the plan file path and compressed plan content to the DB.
func (s *AgentService) trackPlanFilePath(ctx context.Context, agentID string, content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					FilePath string `json:"file_path"`
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
		if filePath == "" {
			continue
		}

		agent, err := s.queries.GetAgentByID(ctx, agentID)
		if err != nil || agent.HomeDir == "" {
			continue
		}

		planDir := agent.HomeDir + "/.claude/plans/"
		if !strings.HasPrefix(filePath, planDir) {
			continue
		}

		// Persist plan file path.
		if err := s.queries.UpdateAgentPlanFilePath(ctx, db.UpdateAgentPlanFilePathParams{
			PlanFilePath: filePath,
			ID:           agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan file path", "agent_id", agentID, "error", err)
			continue
		}

		// Read and persist compressed plan content.
		data, err := os.ReadFile(filePath)
		if err != nil || len(data) == 0 {
			continue
		}
		compressed, compression := msgcodec.Compress(data)
		if err := s.queries.UpdateAgentPlanContent(ctx, db.UpdateAgentPlanContentParams{
			PlanContent:            compressed,
			PlanContentCompression: compression,
			ID:                     agentID,
		}); err != nil {
			slog.Warn("failed to update agent plan content", "agent_id", agentID, "error", err)
		}

		// Only track the first matching plan file per message.
		return
	}
}

// detectPlanModeFromToolResult inspects a user message (tool_result) for
// confirmation of a previously tracked EnterPlanMode or ExitPlanMode tool_use.
// When a match is found, it calls setAgentPermissionMode to update the DB and
// broadcast a notification. It also intercepts ExitPlanMode tool_results that
// have a pending plan execution (set by SendControlResponse on approval).
func (s *AgentService) detectPlanModeFromToolResult(ctx context.Context, agentID string, content []byte) {
	var msg struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
		ToolUseResult *struct {
			Message  string `json:"message"`
			Plan     string `json:"plan"`
			FilePath string `json:"filePath"`
		} `json:"tool_use_result"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}

	for _, block := range msg.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}

		// Check for plan execution pending (takes priority over planModeToolUse).
		if val, ok := s.planExecPending.LoadAndDelete(block.ToolUseID); ok {
			config := val.(*PlanExecConfig)
			close(config.Done) // cancel the timeout goroutine

			// Parse plan content from tool_use_result.
			planContent, filePath := extractPlanFromToolUseResult(msg.ToolUseResult)

			// Fallback 1: DB-persisted compressed plan content.
			if planContent == "" {
				if agent, err := s.queries.GetAgentByID(ctx, agentID); err == nil {
					if len(agent.PlanContent) > 0 {
						if decompressed, err := msgcodec.Decompress(agent.PlanContent, agent.PlanContentCompression); err == nil {
							planContent = string(decompressed)
						}
					}
					if planContent == "" && filePath == "" {
						filePath = agent.PlanFilePath
					}
				}
			}

			// Fallback 2: Read plan file from disk.
			if planContent == "" && filePath != "" {
				if data, err := os.ReadFile(filePath); err == nil && len(data) > 0 {
					planContent = string(data)
				}
			}

			if planContent != "" {
				s.initiatePlanExecRestart(ctx, agentID, planContent)
			} else {
				slog.Warn("plan execution: no plan content found, continuing with retained context",
					"agent_id", agentID, "tool_use_id", block.ToolUseID)
				planFilePath := filePath
				if planFilePath == "" {
					if a, err := s.queries.GetAgentByID(ctx, agentID); err == nil {
						planFilePath = a.PlanFilePath
					}
				}
				s.broadcastNotification(ctx, agentID, map[string]interface{}{
					"type":            "plan_execution",
					"context_cleared": false,
					"plan_file_path":  planFilePath,
				})
			}
			continue
		}

		targetModeVal, ok := s.planModeToolUse.LoadAndDelete(block.ToolUseID)
		if !ok {
			continue
		}
		targetMode := targetModeVal.(string)

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
				"agent_id", agentID,
				"tool_use_id", block.ToolUseID,
				"mode", targetMode)
			s.setAgentPermissionMode(ctx, agentID, targetMode)
		} else {
			truncated := resultText
			if len(truncated) > 64 {
				truncated = truncated[:64]
			}
			slog.Debug("plan mode tool_result did not contain expected confirmation",
				"agent_id", agentID,
				"tool_use_id", block.ToolUseID,
				"expected_mode", targetMode,
				"result_text", truncated)
		}
	}
}

// extractPlanFromToolUseResult extracts plan content and file path from the
// ExitPlanMode tool_use_result.
func extractPlanFromToolUseResult(result *struct {
	Message  string `json:"message"`
	Plan     string `json:"plan"`
	FilePath string `json:"filePath"`
}) (plan, filePath string) {
	if result == nil {
		return "", ""
	}
	return result.Plan, result.FilePath
}

// initiatePlanExecRestart looks up the agent and workspace, then initiates a
// context-clearing restart with the plan content as a synthetic user message.
func (s *AgentService) initiatePlanExecRestart(ctx context.Context, agentID string, planContent string) {
	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		slog.Error("plan exec restart: get agent", "agent_id", agentID, "error", err)
		return
	}
	ws, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		slog.Error("plan exec restart: get workspace", "agent_id", agentID, "error", err)
		return
	}
	s.clearAgentContextForPlanExecution(ctx, &agent, &ws, planContent)
}

// isControlRequest checks if the content is a control_request JSON message
// (e.g. set_permission_mode, interrupt). These are sent as raw input to
// Claude Code's stdin and not persisted as chat messages.
func isControlRequest(content string) bool {
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Type == "control_request"
}

// parseSetPermissionMode checks if a control_request is a set_permission_mode
// request and returns the requested mode. Returns ("", false) if not a match.
func parseSetPermissionMode(content string) (string, bool) {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return "", false
	}
	if msg.Request.Subtype != "set_permission_mode" || msg.Request.Mode == "" {
		return "", false
	}
	return msg.Request.Mode, true
}

// isInterruptRequest checks if a control_request has subtype "interrupt".
func isInterruptRequest(content string) bool {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Request.Subtype == "interrupt"
}
