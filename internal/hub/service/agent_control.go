package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
)

func (s *AgentService) SendControlResponse(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SendControlResponseRequest],
) (*connect.Response[leapmuxv1.SendControlResponseResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	content := req.Msg.GetContent()
	if len(content) == 0 {
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

	// Send the raw control_response bytes to the worker, retrying once if the agent process is gone.
	agentNotFound, err := s.deliverRawInputToWorker(ctx, agent.WorkerID, agent.WorkspaceID, agentID, content)
	if agentNotFound {
		slog.Info("agent process not found, restarting before retry", "agent_id", agentID)
		if restartErr := s.ensureAgentActive(ctx, &agent, ws); restartErr == nil {
			_, err = s.deliverRawInputToWorker(ctx, agent.WorkerID, agent.WorkspaceID, agentID, content)
		}
	}
	if err != nil {
		return nil, err
	}

	// Persist a display message for the control response so it appears in chat history.
	var crPayload struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &crPayload); err == nil {
		// Fetch the control request record to get tool_use_id for threading
		// and tool_name for plan mode detection.
		var toolUseID string
		if reqID := crPayload.Response.RequestID; reqID != "" {
			cr, crErr := s.queries.GetControlRequest(ctx, db.GetControlRequestParams{
				AgentID:   agentID,
				RequestID: reqID,
			})
			if crErr == nil {
				var crBody struct {
					Request struct {
						ToolName  string `json:"tool_name"`
						ToolUseID string `json:"tool_use_id"`
					} `json:"request"`
				}
				if json.Unmarshal(cr.Payload, &crBody) == nil {
					toolUseID = crBody.Request.ToolUseID

					// Detect plan mode changes from control responses (agent-initiated).
					if crPayload.Response.Response.Behavior == "allow" {
						switch crBody.Request.ToolName {
						case "EnterPlanMode":
							s.setAgentPermissionMode(ctx, agentID, "plan")
						case "ExitPlanMode":
							s.setAgentPermissionMode(ctx, agentID, "default")
						}
					}
				}
			}

			if err := s.queries.DeleteControlRequest(ctx, db.DeleteControlRequestParams{
				AgentID:   agentID,
				RequestID: reqID,
			}); err != nil {
				slog.Error("delete control request on response", "agent_id", agentID, "request_id", reqID, "error", err)
			}
		}

		action := "approved"
		if crPayload.Response.Response.Behavior == "deny" {
			action = "rejected"
		}

		displayContent := map[string]interface{}{
			"isSynthetic": true,
			"controlResponse": map[string]string{
				"action":  action,
				"comment": crPayload.Response.Response.Message,
			},
		}
		displayJSON, _ := json.Marshal(displayContent)

		// Thread the control response into the parent tool_use message so it
		// appears alongside the tool rather than as a separate message that
		// can get mis-ordered when the tool_result merge bumps the parent's seq.
		merged := false
		if toolUseID != "" {
			merged = s.mergeIntoThread(ctx, agentID, toolUseID, displayJSON)
		}
		if !merged {
			if err := s.persistAndBroadcast(ctx, agentID, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, displayJSON); err != nil {
				slog.Warn("failed to persist control response notification", "agent_id", agentID, "error", err)
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.SendControlResponseResponse{}), nil
}

// setAgentPermissionMode updates the permission mode in the DB and broadcasts
// a status change event so frontends receive the update via WatchAgent.
func (s *AgentService) setAgentPermissionMode(ctx context.Context, agentID string, mode string) {
	// Read old mode before the DB update for notification.
	oldAgent, oldErr := s.queries.GetAgentByID(ctx, agentID)
	oldMode := ""
	if oldErr == nil {
		oldMode = oldAgent.PermissionMode
	}

	if err := s.queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
		PermissionMode: mode,
		ID:             agentID,
	}); err != nil {
		slog.Error("set agent permission mode", "agent_id", agentID, "error", err)
		return
	}

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		slog.Error("get agent for permission mode broadcast", "agent_id", agentID, "error", err)
		return
	}

	_, wsErr := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	workerOnline := wsErr == nil && s.workerMgr.IsOnline(agent.WorkerID)

	permSc := AgentStatusChange(&agent, workerOnline)
	permSc.GitStatus = s.GetGitStatus(agentID)
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: permSc,
		},
	})

	// Broadcast permission mode change notification.
	if oldMode != "" && oldMode != mode {
		s.broadcastNotification(ctx, agentID, map[string]interface{}{
			"type": "settings_changed",
			"changes": map[string]interface{}{
				"permissionMode": map[string]string{"old": oldMode, "new": mode},
			},
		})
	}
}

// ensureAgentActive resumes an inactive agent that has a session ID. If the
// agent has no session ID or the worker is unreachable, it returns an error.
// On success the agent's status is set to ACTIVE both in-memory and in DB.
//
// When resume fails (e.g. the Claude Code session is no longer available),
// it falls back to starting a fresh agent without a session ID. The chat
// history in the Hub DB is preserved regardless.
func (s *AgentService) ensureAgentActive(ctx context.Context, agent *db.Agent, ws *db.Workspace) error {
	if !s.workerMgr.IsOnline(agent.WorkerID) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}
	if s.workerMgr.IsDeregistering(agent.WorkerID) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is being deregistered"))
	}

	conn := s.workerMgr.Get(agent.WorkerID)
	if conn == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker disconnected"))
	}

	sessionID := agent.AgentSessionID

	resumeResp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentStart{
			AgentStart: &leapmuxv1.AgentStartRequest{
				WorkspaceId:    agent.WorkspaceID,
				AgentId:        agent.ID,
				Model:          agent.Model,
				WorkingDir:     agent.WorkingDir,
				SystemPrompt:   agent.SystemPrompt,
				AgentSessionId: sessionID,
				PermissionMode: agent.PermissionMode,
				Effort:         agent.Effort,
			},
		},
	})

	// If resume failed and we had a session ID, fall back to a fresh start.
	resumeFailed := err != nil || resumeResp.GetAgentStarted().GetError() != ""
	if resumeFailed && sessionID != "" {
		if err != nil {
			slog.Warn("session resume failed, retrying without session ID",
				"agent_id", agent.ID, "session_id", sessionID, "error", err)
		} else {
			slog.Warn("session resume failed, retrying without session ID",
				"agent_id", agent.ID, "session_id", sessionID,
				"error", resumeResp.GetAgentStarted().GetError())
		}

		// Re-check worker connectivity; the first attempt may have taken a while.
		conn = s.workerMgr.Get(agent.WorkerID)
		if conn == nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker disconnected"))
		}

		resumeResp, err = s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_AgentStart{
				AgentStart: &leapmuxv1.AgentStartRequest{
					WorkspaceId:    agent.WorkspaceID,
					AgentId:        agent.ID,
					Model:          agent.Model,
					WorkingDir:     agent.WorkingDir,
					SystemPrompt:   agent.SystemPrompt,
					AgentSessionId: "", // Fresh start — no session ID
					PermissionMode: agent.PermissionMode,
					Effort:         agent.Effort,
				},
			},
		})
	}

	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("start agent: %w", err))
	}

	if errMsg := resumeResp.GetAgentStarted().GetError(); errMsg != "" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("start failed: %s", errMsg))
	}

	if err := s.queries.ReopenAgent(ctx, agent.ID); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("reopen agent: %w", err))
	}

	newSessionID := resumeResp.GetAgentStarted().GetAgentSessionId()
	if newSessionID != "" {
		_ = s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
			AgentSessionID: newSessionID,
			ID:             agent.ID,
		})
	}

	// Update DB with confirmed permission mode from startup handshake.
	confirmedMode := resumeResp.GetAgentStarted().GetPermissionMode()
	if confirmedMode != "" {
		_ = s.queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
			PermissionMode: confirmedMode,
			ID:             agent.ID,
		})
	}

	agent.Status = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE

	// Re-read agent to get updated fields after DB writes.
	updatedAgent, err := s.queries.GetAgentByID(ctx, agent.ID)
	if err == nil {
		resumeSc := AgentStatusChange(&updatedAgent, true)
		resumeSc.GitStatus = s.GetGitStatus(agent.ID)
		s.agentMgr.Broadcast(agent.ID, &leapmuxv1.AgentEvent{
			AgentId: agent.ID,
			Event: &leapmuxv1.AgentEvent_StatusChange{
				StatusChange: resumeSc,
			},
		})
	}

	slog.Info("agent resumed successfully", "agent_id", agent.ID, "session_id", newSessionID)
	return nil
}

func (s *AgentService) UpdateAgentSettings(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateAgentSettingsRequest],
) (*connect.Response[leapmuxv1.UpdateAgentSettingsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	if agentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent_id is required"))
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

	// Apply non-empty fields.
	newModel := agent.Model
	newEffort := agent.Effort
	if req.Msg.GetModel() != "" {
		newModel = req.Msg.GetModel()
	}
	if req.Msg.GetEffort() != "" {
		newEffort = req.Msg.GetEffort()
	}

	// Update DB.
	if err := s.queries.UpdateAgentModelAndEffort(ctx, db.UpdateAgentModelAndEffortParams{
		Model:  newModel,
		Effort: newEffort,
		ID:     agentID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update settings: %w", err))
	}

	// Re-read agent for broadcast.
	updatedAgent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// If the agent is currently active on a worker, restart it to apply new settings.
	if agent.Status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		conn := s.workerMgr.Get(agent.WorkerID)
		if conn != nil {
			// Mark as pending restart so AgentStopped handler re-launches.
			s.restartPending.Store(agentID, &RestartOptions{})
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStop{
					AgentStop: &leapmuxv1.AgentStopRequest{
						WorkspaceId: agent.WorkspaceID,
						AgentId:     agentID,
					},
				},
			})
		}
	}

	// Broadcast updated settings to watchers.
	workerOnline := s.workerMgr.IsOnline(agent.WorkerID)
	settingsSc := AgentStatusChange(&updatedAgent, workerOnline)
	settingsSc.GitStatus = s.GetGitStatus(agentID)
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: settingsSc,
		},
	})

	// Broadcast settings change notification.
	changes := map[string]interface{}{}
	if agent.Model != newModel {
		changes["model"] = map[string]string{"old": agent.Model, "new": newModel}
	}
	if agent.Effort != newEffort {
		changes["effort"] = map[string]string{"old": agent.Effort, "new": newEffort}
	}
	if len(changes) > 0 {
		s.broadcastNotification(ctx, agentID, map[string]interface{}{
			"type":    "settings_changed",
			"changes": changes,
		})
	}

	return connect.NewResponse(&leapmuxv1.UpdateAgentSettingsResponse{}), nil
}

// ConsumeRestartPending atomically checks and removes an agent from
// the restart-pending map. Returns the options if it was pending, nil otherwise.
func (s *AgentService) ConsumeRestartPending(agentID string) *RestartOptions {
	v, loaded := s.restartPending.LoadAndDelete(agentID)
	if !loaded {
		return nil
	}
	return v.(*RestartOptions)
}
