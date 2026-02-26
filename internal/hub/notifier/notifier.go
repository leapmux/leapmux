package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// Notifier manages sending notifications to workers with persistent
// queue fallback for reliable delivery.
type Notifier struct {
	queries    *db.Queries
	workerMgr  *workermgr.Manager
	pending    *workermgr.PendingRequests
	agentMgr   *agentmgr.Manager
	timeoutCfg *timeout.Config
}

// New creates a new Notifier.
func New(q *db.Queries, wMgr *workermgr.Manager, pr *workermgr.PendingRequests, am *agentmgr.Manager, tc *timeout.Config) *Notifier {
	return &Notifier{
		queries:    q,
		workerMgr:  wMgr,
		pending:    pr,
		agentMgr:   am,
		timeoutCfg: tc,
	}
}

// SendOrQueue attempts to deliver a notification to a worker immediately.
// If the worker is offline or delivery fails, the notification is persisted
// to the worker_notifications queue for later delivery.
func (n *Notifier) SendOrQueue(ctx context.Context, workerID string, notificationType leapmuxv1.NotificationType, payload string, msg *leapmuxv1.ConnectResponse) error {
	conn := n.workerMgr.Get(workerID)
	if conn != nil {
		sendCtx, cancel := context.WithTimeout(ctx, n.timeoutCfg.APITimeout())
		defer cancel()

		_, err := n.pending.SendAndWait(sendCtx, conn, msg)
		if err == nil {
			return nil // Delivered and acked.
		}
		slog.Warn("failed to deliver notification, queueing", "worker_id", workerID, "type", notificationType, "error", err)
	}

	// Queue for later delivery.
	return n.queries.CreateWorkerNotification(ctx, db.CreateWorkerNotificationParams{
		ID:       id.Generate(),
		WorkerID: workerID,
		Type:     notificationType,
		Payload:  payload,
	})
}

// ProcessPendingNotifications delivers any queued notifications to a connected worker.
// Called when a worker connects or reconnects.
func (n *Notifier) ProcessPendingNotifications(ctx context.Context, workerID string) error {
	notifications, err := n.queries.ListPendingNotificationsByWorker(ctx, workerID)
	if err != nil {
		return fmt.Errorf("list pending notifications: %w", err)
	}

	conn := n.workerMgr.Get(workerID)
	if conn == nil {
		return fmt.Errorf("worker not connected")
	}

	for _, notif := range notifications {
		_ = n.queries.IncrementNotificationAttempts(ctx, notif.ID)

		msg, err := n.buildNotificationMessage(notif)
		if err != nil {
			slog.Error("failed to build notification message", "notification_id", notif.ID, "error", err)
			if notif.Attempts+1 >= notif.MaxAttempts {
				_ = n.queries.MarkNotificationFailed(ctx, notif.ID)
			}
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, n.timeoutCfg.APITimeout())
		_, sendErr := n.pending.SendAndWait(sendCtx, conn, msg)
		cancel()

		if sendErr != nil {
			slog.Warn("failed to deliver queued notification", "notification_id", notif.ID, "error", sendErr)
			if notif.Attempts+1 >= notif.MaxAttempts {
				_ = n.queries.MarkNotificationFailed(ctx, notif.ID)
			}
			continue
		}

		_ = n.queries.MarkNotificationDelivered(ctx, notif.ID)

		// For deregister notifications, mark worker as fully deleted after ack.
		if notif.Type == leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER {
			_ = n.queries.MarkWorkerDeleted(ctx, workerID)
			n.workerMgr.ClearDeregistering(workerID)
			slog.Info("worker deregistration complete", "worker_id", workerID)
		}
	}

	return nil
}

// SendDeregister terminates all active workspaces on a worker and sends
// a deregistration notification.
func (n *Notifier) SendDeregister(ctx context.Context, workerID string) error {
	n.workerMgr.MarkDeregistering(workerID)

	// Terminate all active workspaces (agents + terminals) on this worker.
	n.terminateWorkspacesOnWorker(ctx, workerID)

	msg := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Deregister{
			Deregister: &leapmuxv1.DeregisterNotification{
				Reason: "worker deregistered by owner",
			},
		},
	}

	return n.SendOrQueue(ctx, workerID, leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER, "{}", msg)
}

// EnforceOrgMemberRemoval deregisters all workers owned by the removed user
// in the org and terminates their workspaces on other users' workers.
func (n *Notifier) EnforceOrgMemberRemoval(ctx context.Context, orgID, removedUserID string) error {
	// 1. Deregister all workers the removed user owns in this org.
	workerIDs, err := n.queries.ListWorkersByOrgAndRegisteredBy(ctx, db.ListWorkersByOrgAndRegisteredByParams{
		OrgID:        orgID,
		RegisteredBy: removedUserID,
	})
	if err != nil {
		return fmt.Errorf("list workers by org and user: %w", err)
	}

	for _, workerID := range workerIDs {
		result, err := n.queries.ForceDeregisterWorker(ctx, workerID)
		if err != nil {
			slog.Error("failed to force deregister worker", "worker_id", workerID, "error", err)
			continue
		}
		rows, _ := result.RowsAffected()
		if rows > 0 {
			if err := n.SendDeregister(ctx, workerID); err != nil {
				slog.Error("failed to send deregister notification", "worker_id", workerID, "error", err)
			}
		}
	}

	// 2. Terminate workspaces the removed user has on OTHER users' workers in this org.
	wsIDs, err := n.queries.ListWorkspaceIDsByOrgAndCreator(ctx, db.ListWorkspaceIDsByOrgAndCreatorParams{
		OrgID:     orgID,
		CreatedBy: removedUserID,
	})
	if err != nil {
		return fmt.Errorf("list workspaces by org and creator: %w", err)
	}

	// Group workspace IDs by worker (determined through their agents).
	byWorker := make(map[string][]string)
	for _, wsID := range wsIDs {
		n.closeWorkspace(ctx, wsID)

		// Find unique workers for agents in this workspace.
		agents, err := n.queries.ListAgentsByWorkspaceID(ctx, wsID)
		if err != nil {
			continue
		}
		seen := make(map[string]bool)
		for _, a := range agents {
			if !seen[a.WorkerID] {
				seen[a.WorkerID] = true
				byWorker[a.WorkerID] = append(byWorker[a.WorkerID], wsID)
			}
		}
	}

	for wid, wWsIDs := range byWorker {
		payload, _ := json.Marshal(map[string]any{"workspace_ids": wWsIDs})
		msg := &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_TerminateWorkspaces{
				TerminateWorkspaces: &leapmuxv1.TerminateWorkspacesNotification{
					WorkspaceIds: wWsIDs,
					Reason:       "org member removed",
				},
			},
		}
		if err := n.SendOrQueue(ctx, wid, leapmuxv1.NotificationType_NOTIFICATION_TYPE_TERMINATE_WORKSPACES, string(payload), msg); err != nil {
			slog.Error("failed to send terminate workspaces notification", "worker_id", wid, "error", err)
		}
	}

	return nil
}

// terminateWorkspacesOnWorker closes all agents on a worker and broadcasts
// closure events. Workspaces are no longer closed since they can span multiple
// workers.
func (n *Notifier) terminateWorkspacesOnWorker(ctx context.Context, workerID string) {
	agentIDs, err := n.queries.ListActiveAgentIDsByWorker(ctx, workerID)
	if err != nil {
		slog.Error("failed to list agents for worker", "worker_id", workerID, "error", err)
		return
	}

	if err := n.queries.CloseActiveAgentsByWorker(ctx, workerID); err != nil {
		slog.Error("failed to close agents for worker", "worker_id", workerID, "error", err)
	}

	// Broadcast agent closures to frontend watchers.
	if n.agentMgr != nil {
		for _, agentID := range agentIDs {
			var sessionID string
			var workspaceID string
			if a, err := n.queries.GetAgentByID(ctx, agentID); err == nil {
				sessionID = a.AgentSessionID
				workspaceID = a.WorkspaceID
			}
			n.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: &leapmuxv1.AgentStatusChange{
						AgentId:        agentID,
						Status:         leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE,
						AgentSessionId: sessionID,
						WorkerOnline:   false,
					},
				},
			})
			if sessionID == "" && workspaceID != "" {
				_ = n.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
					WorkspaceID: workspaceID,
					TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabID:       agentID,
				})
			}
		}
	}
}

// closeWorkspace closes a single workspace's agents and broadcasts closure events.
func (n *Notifier) closeWorkspace(ctx context.Context, workspaceID string) {
	agentIDs, err := n.queries.ListActiveAgentIDsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		slog.Error("failed to list agents for workspace", "workspace_id", workspaceID, "error", err)
		return
	}

	if err := n.queries.CloseActiveAgentsByWorkspace(ctx, workspaceID); err != nil {
		slog.Error("failed to close agents for workspace", "workspace_id", workspaceID, "error", err)
	}

	// Broadcast agent closures to frontend watchers.
	// Include agent_session_id so the frontend knows the agent is resumable.
	if n.agentMgr != nil {
		for _, agentID := range agentIDs {
			var sessionID string
			if a, err := n.queries.GetAgentByID(ctx, agentID); err == nil {
				sessionID = a.AgentSessionID
			}
			n.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
				AgentId: agentID,
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: &leapmuxv1.AgentStatusChange{
						AgentId:        agentID,
						Status:         leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE,
						AgentSessionId: sessionID,
						WorkerOnline:   false, // Workspace closed due to worker going away
					},
				},
			})
			// Remove persisted tab for agents without a session ID — they
			// can never be resumed and would appear permanently disconnected.
			if sessionID == "" {
				_ = n.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
					WorkspaceID: workspaceID,
					TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabID:       agentID,
				})
			}
		}
	}
}

// buildNotificationMessage converts a persisted notification into a ConnectResponse.
func (n *Notifier) buildNotificationMessage(notif db.WorkerNotification) (*leapmuxv1.ConnectResponse, error) {
	switch notif.Type {
	case leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER:
		return &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Deregister{
				Deregister: &leapmuxv1.DeregisterNotification{
					Reason: "worker deregistered by owner",
				},
			},
		}, nil

	case leapmuxv1.NotificationType_NOTIFICATION_TYPE_TERMINATE_WORKSPACES:
		var payload struct {
			WorkspaceIDs []string `json:"workspace_ids"`
		}
		if err := json.Unmarshal([]byte(notif.Payload), &payload); err != nil {
			return nil, fmt.Errorf("unmarshal terminate_workspaces payload: %w", err)
		}
		return &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_TerminateWorkspaces{
				TerminateWorkspaces: &leapmuxv1.TerminateWorkspacesNotification{
					WorkspaceIds: payload.WorkspaceIDs,
					Reason:       "queued notification",
				},
			},
		}, nil

	default:
		return nil, fmt.Errorf("unknown notification type: %s", notif.Type)
	}
}
