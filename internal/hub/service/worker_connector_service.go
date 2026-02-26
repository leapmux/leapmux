package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// WorkerConnectorService implements the Hub-side service called by Worker
// instances for registration and bidirectional streaming.
type WorkerConnectorService struct {
	queries     *db.Queries
	workerMgr   *workermgr.Manager
	agentSvc    *AgentService
	terminalSvc *TerminalService
	agentMgr    *agentmgr.Manager
	pending     *workermgr.PendingRequests
	notifier    *notifier.Notifier
	shutdownCh  <-chan struct{}
}

// NewWorkerConnectorService creates a new WorkerConnectorService.
func NewWorkerConnectorService(q *db.Queries, mgr *workermgr.Manager) *WorkerConnectorService {
	return &WorkerConnectorService{queries: q, workerMgr: mgr}
}

// SetAgentService sets the agent service for routing agent output.
func (s *WorkerConnectorService) SetAgentService(svc *AgentService) {
	s.agentSvc = svc
}

// SetTerminalService sets the terminal service for routing terminal output.
func (s *WorkerConnectorService) SetTerminalService(svc *TerminalService) {
	s.terminalSvc = svc
}

// SetAgentMgr sets the agent watcher manager for broadcasting status changes.
func (s *WorkerConnectorService) SetAgentMgr(mgr *agentmgr.Manager) {
	s.agentMgr = mgr
}

// SetPendingRequests sets the pending requests tracker for file operations.
func (s *WorkerConnectorService) SetPendingRequests(pr *workermgr.PendingRequests) {
	s.pending = pr
}

// SetNotifier sets the notifier for processing pending notifications on connect.
func (s *WorkerConnectorService) SetNotifier(n *notifier.Notifier) {
	s.notifier = n
}

// SetShutdownCh sets the channel used to signal hub shutdown.
// When closed, cleanupWorker skips DB operations and broadcasts.
func (s *WorkerConnectorService) SetShutdownCh(ch <-chan struct{}) {
	s.shutdownCh = ch
}

func (s *WorkerConnectorService) RequestRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RequestRegistrationRequest],
) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
	// Expire old pending registrations first.
	if err := s.queries.ExpireRegistrations(ctx); err != nil {
		slog.Debug("failed to expire registrations", "error", err)
	}

	regID := id.Generate()
	expiresAt := time.Now().Add(10 * time.Minute).UTC()

	// Sanitize worker properties: strip invalid characters, reject if empty.
	hostname, err := validate.ValidateProperty("hostname", req.Msg.GetHostname())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	osName, err := validate.ValidateProperty("os", req.Msg.GetOs())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	arch, err := validate.ValidateProperty("arch", req.Msg.GetArch())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	version, err := validate.ValidateProperty("version", req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.queries.CreateRegistration(ctx, db.CreateRegistrationParams{
		ID:        regID,
		Hostname:  hostname,
		Os:        osName,
		Arch:      arch,
		Version:   version,
		ExpiresAt: expiresAt,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create registration: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
		RegistrationToken: regID,
		RegistrationUrl:   "/register/" + regID,
	}), nil
}

func (s *WorkerConnectorService) PollRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.PollRegistrationRequest],
) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Long-poll: if still pending, wait for notification or timeout.
	if reg.Status == leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING {
		_ = s.workerMgr.WaitForRegistrationChange(ctx, regID, 30*time.Second)

		// Re-query after waking up.
		reg, err = s.queries.GetRegistrationByID(ctx, regID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	resp := &leapmuxv1.PollRegistrationResponse{}

	switch reg.Status {
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED
		if reg.WorkerID.Valid {
			resp.WorkerId = reg.WorkerID.String
			worker, err := s.queries.GetWorkerByIDInternal(ctx, reg.WorkerID.String)
			if err == nil {
				resp.AuthToken = worker.AuthToken
				resp.OrgId = worker.OrgID
			}
		}
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED
	default:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_UNSPECIFIED
	}

	return connect.NewResponse(resp), nil
}

func (s *WorkerConnectorService) Connect(
	ctx context.Context,
	stream *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
) error {
	// The worker must authenticate via auth_token in the first message or
	// via metadata. For now, extract from the request header.
	authToken := stream.RequestHeader().Get("Authorization")
	if authToken == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth_token required"))
	}

	token := ""
	const prefix = "Bearer "
	if len(authToken) > len(prefix) {
		token = authToken[len(prefix):]
	}

	worker, err := s.queries.GetWorkerByAuthToken(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid auth token"))
		}
		return connect.NewError(connect.CodeInternal, err)
	}

	// Register the connection.
	conn := &workermgr.Conn{
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
		Stream:   stream,
	}
	s.workerMgr.Register(conn)
	defer func() {
		// Only run cleanup if this connection is still the registered one.
		// A newer worker process may have already replaced it, in which
		// case we must not unregister the replacement or close its agents.
		if s.workerMgr.Unregister(worker.ID, conn) {
			s.cleanupWorker(worker.ID)
		}
	}()

	// Notify frontends that the worker is back online so agent tabs
	// with session IDs become writable again (resumable).
	if worker.Status != leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING {
		s.broadcastWorkerOnline(worker.ID)
	}

	// Update last seen.
	if err := s.queries.UpdateWorkerLastSeen(ctx, worker.ID); err != nil {
		slog.Warn("failed to update worker last seen", "worker_id", worker.ID, "error", err)
	}

	slog.Info("worker connected", "worker_id", worker.ID, "name", worker.Name, "status", worker.Status)
	defer slog.Info("worker disconnected", "worker_id", worker.ID)

	// Process pending notifications.
	if s.notifier != nil {
		if worker.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING {
			// Worker is being deregistered — process notifications inline, then close.
			if err := s.notifier.ProcessPendingNotifications(ctx, worker.ID); err != nil {
				slog.Error("failed to process pending notifications (deregistering)", "worker_id", worker.ID, "error", err)
			}
			return nil
		}
		// Normal worker: process pending notifications in background.
		go func() {
			if err := s.notifier.ProcessPendingNotifications(ctx, worker.ID); err != nil {
				slog.Error("failed to process pending notifications", "worker_id", worker.ID, "error", err)
			}
		}()
	}

	// Main message loop: read messages from worker and process them.
	// Run stream.Receive() in a goroutine so we can also detect idle
	// timeouts (dead workers that didn't close the TCP connection cleanly).
	type receiveResult struct {
		msg *leapmuxv1.ConnectRequest
		err error
	}
	msgCh := make(chan receiveResult, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case msgCh <- receiveResult{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	const workerIdleTimeout = 10 * time.Second
	idleTimer := time.NewTimer(workerIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case result := <-msgCh:
			if result.err != nil {
				return nil // Connection closed.
			}

			// Reset idle timer on every received message.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(workerIdleTimeout)

			msg := result.msg
			s.processWorkerMessage(ctx, stream, worker.ID, msg)

		case <-idleTimer.C:
			slog.Warn("worker idle timeout, assuming disconnected", "worker_id", worker.ID)
			return nil

		case <-ctx.Done():
			return nil
		}
	}
}

// processWorkerMessage handles a single message from the worker stream.
func (s *WorkerConnectorService) processWorkerMessage(
	ctx context.Context,
	stream *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
	workerID string,
	msg *leapmuxv1.ConnectRequest,
) {
	// Update last seen periodically on heartbeats.
	if msg.GetHeartbeat() != nil {
		if err := s.queries.UpdateWorkerLastSeen(ctx, workerID); err != nil {
			slog.Warn("failed to update worker last seen on heartbeat", "worker_id", workerID, "error", err)
		}
		// Send heartbeat response.
		if err := stream.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Heartbeat{
				Heartbeat: &leapmuxv1.Heartbeat{},
			},
		}); err != nil {
			slog.Debug("failed to send heartbeat response", "worker_id", workerID, "error", err)
		}
		return
	}

	// Try to complete pending request-response pairs (file operations).
	if s.pending != nil && msg.GetRequestId() != "" {
		if s.pending.Complete(msg.GetRequestId(), msg) {
			return
		}
	}

	// Route messages to appropriate services.
	switch payload := msg.GetPayload().(type) {
	case *leapmuxv1.ConnectRequest_AgentOutput:
		if s.agentSvc != nil {
			s.agentSvc.HandleAgentOutput(ctx, payload.AgentOutput)
		}
	case *leapmuxv1.ConnectRequest_AgentStarted:
		agentID := payload.AgentStarted.GetAgentId()
		sessionID := payload.AgentStarted.GetAgentSessionId()
		slog.Info("agent started on worker",
			"worker_id", workerID,
			"workspace_id", payload.AgentStarted.GetWorkspaceId(),
			"agent_id", agentID,
			"agent_session_id", sessionID,
		)
		// Persist session ID if present.
		if sessionID != "" && agentID != "" {
			if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
				AgentSessionID: sessionID,
				ID:             agentID,
			}); err != nil {
				slog.Error("failed to store agent session ID",
					"agent_id", agentID, "error", err)
			}
		}
		// Persist confirmed permission mode from startup handshake.
		if confirmedMode := payload.AgentStarted.GetPermissionMode(); confirmedMode != "" && agentID != "" {
			if err := s.queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
				PermissionMode: confirmedMode,
				ID:             agentID,
			}); err != nil {
				slog.Warn("failed to set agent permission mode", "agent_id", agentID, "error", err)
			}
		}
		// Store git status from agent startup (sanitized).
		if gs := sanitizeGitStatus(payload.AgentStarted.GetGitStatus()); gs != nil {
			s.agentSvc.StoreGitStatus(agentID, gs)
		}
		// Persist worker home directory for tilde path display.
		if homeDir := payload.AgentStarted.GetHomeDir(); homeDir != "" && agentID != "" {
			if err := s.queries.UpdateAgentHomeDir(ctx, db.UpdateAgentHomeDirParams{
				HomeDir: homeDir,
				ID:      agentID,
			}); err != nil {
				slog.Warn("failed to store agent home dir", "agent_id", agentID, "error", err)
			}
		}
	case *leapmuxv1.ConnectRequest_AgentStopped:
		agentID := payload.AgentStopped.GetAgentId()
		slog.Info("agent stopped on worker",
			"worker_id", workerID,
			"workspace_id", payload.AgentStopped.GetWorkspaceId(),
			"agent_id", agentID,
		)
		// Check if this stop was part of a restart cycle (settings change or /clear).
		if s.agentSvc != nil {
			if opts := s.agentSvc.ConsumeRestartPending(agentID); opts != nil {
				go func() {
					bgCtx := context.Background()
					agent, aErr := s.queries.GetAgentByID(bgCtx, agentID)
					if aErr != nil {
						slog.Error("restart pending: get agent", "agent_id", agentID, "error", aErr)
						return
					}

					// Clear session ID if requested (for /clear).
					if opts.ClearSession {
						if err := s.queries.UpdateAgentSessionID(bgCtx, db.UpdateAgentSessionIDParams{
							AgentSessionID: "",
							ID:             agentID,
						}); err != nil {
							slog.Warn("failed to clear agent session ID on restart", "agent_id", agentID, "error", err)
						}
						agent.AgentSessionID = ""
					}

					ws, wErr := s.queries.GetWorkspaceByIDInternal(bgCtx, agent.WorkspaceID)
					if wErr != nil {
						slog.Error("restart pending: get workspace", "agent_id", agentID, "error", wErr)
						return
					}
					if err := s.agentSvc.ensureAgentActive(bgCtx, &agent, &ws); err != nil {
						slog.Error("restart pending: ensureAgentActive", "agent_id", agentID, "error", err)
						return
					}

					// Broadcast context_cleared notification after successful restart.
					if opts.ClearSession {
						s.agentSvc.broadcastNotification(bgCtx, agentID, map[string]interface{}{
							"type": "context_cleared",
						})
					}

					// Broadcast plan execution notification.
					if opts.PlanExec {
						s.agentSvc.broadcastNotification(bgCtx, agentID, map[string]interface{}{
							"type":            "plan_execution",
							"context_cleared": opts.ClearSession,
							"plan_file_path":  opts.PlanFilePath,
						})
					}

					// Send synthetic user message after plan execution restart.
					if opts.SyntheticUserMessage != "" {
						if err := s.agentSvc.sendSyntheticUserMessage(bgCtx, agentID, opts.SyntheticUserMessage); err != nil {
							slog.Error("send synthetic user message after plan restart",
								"agent_id", agentID, "error", err)
						}
					}
				}()
			}
		}
	case *leapmuxv1.ConnectRequest_AgentGitInfo:
		info := payload.AgentGitInfo
		agentID := info.GetAgentId()
		sanitized := sanitizeGitStatus(info.GetGitStatus())
		if sanitized != nil && s.agentSvc != nil {
			s.agentSvc.StoreGitStatus(agentID, sanitized)
			// Broadcast status change so frontend gets the git status update.
			if agent, aErr := s.queries.GetAgentByID(ctx, agentID); aErr == nil {
				workerOnline := s.workerMgr.IsOnline(agent.WorkerID)
				sc := AgentStatusChange(&agent, workerOnline)
				sc.GitStatus = sanitized
				s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: sc},
				})
			}
		}
	case *leapmuxv1.ConnectRequest_TerminalOutput:
		if s.terminalSvc != nil {
			s.terminalSvc.HandleTerminalOutput(payload.TerminalOutput)
		}
	case *leapmuxv1.ConnectRequest_TerminalStarted:
		slog.Info("terminal started on worker",
			"worker_id", workerID,
			"terminal_id", payload.TerminalStarted.GetTerminalId(),
		)
	case *leapmuxv1.ConnectRequest_TerminalExited:
		if s.terminalSvc != nil {
			s.terminalSvc.HandleTerminalExited(payload.TerminalExited)
		}
	default:
		slog.Debug("unhandled worker message",
			"worker_id", workerID,
			"request_id", msg.GetRequestId(),
		)
	}
}

// cleanupWorker closes active agents for a disconnected worker and
// notifies watchers so frontends see the status change.
// Workspaces are NOT closed — their availability is determined by the
// worker's online status (workerMgr), not a DB column.
func (s *WorkerConnectorService) cleanupWorker(workerID string) {
	// During hub shutdown, skip all DB operations and broadcasts.
	// The DB is about to be closed and all workers are disconnecting.
	if s.shutdownCh != nil {
		select {
		case <-s.shutdownCh:
			slog.Info("skipping worker cleanup during hub shutdown", "worker_id", workerID)
			return
		default:
		}
	}

	ctx := context.Background()

	// Query agents directly by worker.
	allAgentIDs, err := s.queries.ListActiveAgentIDsByWorker(ctx, workerID)
	if err != nil {
		slog.Error("failed to list agents for worker", "worker_id", workerID, "error", err)
	}

	// Close agents by worker.
	if err := s.queries.CloseActiveAgentsByWorker(ctx, workerID); err != nil {
		slog.Error("failed to close agents for worker", "worker_id", workerID, "error", err)
	}

	// Broadcast agent closure to watchers so frontends see the status change.
	// Include agent_session_id so the frontend knows the agent is resumable.
	events := make([]agentmgr.AgentBroadcast, 0, len(allAgentIDs))
	for _, agentID := range allAgentIDs {
		a, aErr := s.queries.GetAgentByID(ctx, agentID)
		if err := s.queries.DeleteControlRequestsByAgentID(ctx, agentID); err != nil {
			slog.Warn("failed to delete control requests on worker cleanup", "agent_id", agentID, "error", err)
		}
		if aErr == nil {
			sc := AgentStatusChange(&a, false)
			sc.Status = leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE
			sc.GitStatus = s.agentSvc.GetGitStatus(agentID)
			events = append(events, agentmgr.AgentBroadcast{
				AgentID: agentID,
				Event: &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event: &leapmuxv1.AgentEvent_StatusChange{
						StatusChange: sc,
					},
				},
			})
			// Remove persisted tab for agents without a session ID — they
			// can never be resumed and would appear permanently disconnected.
			if a.AgentSessionID == "" && a.WorkspaceID != "" {
				if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
					WorkspaceID: a.WorkspaceID,
					TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabID:       agentID,
				}); err != nil {
					slog.Warn("failed to delete workspace tab on worker cleanup", "agent_id", agentID, "workspace_id", a.WorkspaceID, "error", err)
				}
			}
		}
	}
	if s.agentMgr != nil && len(events) > 0 {
		s.agentMgr.BroadcastMany(events)
	}

	// Broadcast terminal closure to watchers so frontends see the disconnection.
	if s.terminalSvc != nil {
		s.terminalSvc.CleanupTerminalsByWorker(workerID)
	}

	slog.Info("cleaned up worker resources", "worker_id", workerID, "agents_closed", len(allAgentIDs))
}

// broadcastWorkerOnline notifies all agent watchers that the worker is
// back online, so agent tabs with session IDs become writable (resumable).
func (s *WorkerConnectorService) broadcastWorkerOnline(workerID string) {
	if s.agentMgr == nil {
		return
	}

	ctx := context.Background()
	agents, err := s.queries.ListAgentsByWorker(ctx, workerID)
	if err != nil {
		slog.Error("failed to list agents for worker online broadcast", "worker_id", workerID, "error", err)
		return
	}

	events := make([]agentmgr.AgentBroadcast, len(agents))
	for i := range agents {
		sc := AgentStatusChange(&agents[i], true)
		sc.GitStatus = s.agentSvc.GetGitStatus(agents[i].ID)
		events[i] = agentmgr.AgentBroadcast{
			AgentID: agents[i].ID,
			Event: &leapmuxv1.AgentEvent{
				AgentId: agents[i].ID,
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: sc,
				},
			},
		}
	}
	s.agentMgr.BroadcastMany(events)
}
