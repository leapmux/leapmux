package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

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
