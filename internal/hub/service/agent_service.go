package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// contextUsageSnapshot holds the latest token usage snapshot for an agent.
// Each assistant message reports the full context usage for that turn, so
// we simply store the most recent values rather than accumulating.
type contextUsageSnapshot struct {
	mu                       sync.Mutex
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ContextWindow            int64     // Set from result message modelUsage; 0 = unknown
	LastBroadcast            time.Time // Last time contextUsage was included in a broadcast
}

// notifThreadRef tracks the current notification thread row for an agent.
// Used by persistNotificationThreaded to append consecutive notifications
// into a single DB row.
type notifThreadRef struct {
	msgID     string
	seq       int64
	softClear time.Time // set by non-notif messages; zero = thread is live
}

// AgentService implements the AgentServiceHandler interface.
type AgentService struct {
	queries         *db.Queries
	workerMgr       *workermgr.Manager
	agentMgr        *agentmgr.Manager
	pending         *workermgr.PendingRequests
	worktreeHelper  *WorktreeHelper
	timeoutCfg      *timeout.Config
	restartPending  sync.Map // agentID -> *RestartOptions: agents waiting for restart
	planModeToolUse sync.Map // tool_use_id (string) -> targetMode (string): pending EnterPlanMode/ExitPlanMode tool uses
	contextUsage    sync.Map // agentID -> *contextUsageSnapshot: latest token usage
	lastNotifThread sync.Map // agentID -> *notifThreadRef: current notification thread
	notifMu         sync.Map // agentID -> *sync.Mutex: serializes notification threading
	lastAgentStatus sync.Map // agentID -> string: last status value ("" = null, non-empty = actual value)
	gitStatus       sync.Map // agentID -> *leapmuxv1.AgentGitStatus: latest git status from worker
	autoContinue    sync.Map // agentID -> *autoContinueState: pending auto-continue on API errors
	planExecPending sync.Map // tool_use_id (string) -> *PlanExecConfig: pending ExitPlanMode plan executions
}

// RestartOptions controls behavior when an agent is restarted via the
// AgentStopped → re-launch cycle (e.g. settings change or /clear).
type RestartOptions struct {
	ClearSession         bool   // when true, clear agent session ID before restart and broadcast context_cleared
	SyntheticUserMessage string // hidden user message to send after restart (empty = none)
	PlanExec             bool   // broadcast plan_execution notification after restart
	PlanFilePath         string // plan file path for plan_execution notification
}

// PlanExecConfig holds pending plan execution state, set after an
// ExitPlanMode approval to intercept the follow-up tool_result.
type PlanExecConfig struct {
	AgentID    string
	TargetMode string
	Done       chan struct{} // closed when tool_result is received; signals timeout goroutine to stop
}

// NewAgentService creates a new AgentService.
func NewAgentService(q *db.Queries, bm *workermgr.Manager, am *agentmgr.Manager, pr *workermgr.PendingRequests, wh *WorktreeHelper, tc *timeout.Config) *AgentService {
	return &AgentService{queries: q, workerMgr: bm, agentMgr: am, pending: pr, worktreeHelper: wh, timeoutCfg: tc}
}

func (s *AgentService) OpenAgent(
	ctx context.Context,
	req *connect.Request[leapmuxv1.OpenAgentRequest],
) (*connect.Response[leapmuxv1.OpenAgentResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace_id is required"))
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	workingDir := validate.SanitizePath(req.Msg.GetWorkingDir(), worker.HomeDir)
	if workingDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("working_dir is required"))
	}

	// Verify the user can see the workspace and it is not archived.
	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := getVisibleNonArchivedWorkspace(ctx, s.queries, user, wsInternal.OrgID, workspaceID); err != nil {
		return nil, err
	}

	// Verify the worker is online and not being deregistered.
	if !s.workerMgr.IsOnline(workerID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}
	if s.workerMgr.IsDeregistering(workerID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is being deregistered"))
	}

	// Create worktree if requested (before DB insert so failures are clean).
	var worktreeID string
	if req.Msg.GetCreateWorktree() {
		var wtErr error
		workingDir, worktreeID, wtErr = s.worktreeHelper.CreateWorktreeIfRequested(
			ctx, workerID, workingDir, true, req.Msg.GetWorktreeBranch(),
		)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("create worktree: %w", wtErr))
		}
	}

	// Auto-detect existing worktree if the working directory matches one.
	if worktreeID == "" {
		worktreeID = s.worktreeHelper.LookupWorktreeForWorkingDir(ctx, workerID, workingDir)
	}

	model := req.Msg.GetModel()
	if model == "" {
		model = DefaultModel
	}

	title := req.Msg.GetTitle()
	if title == "" {
		title = "New Agent"
	}

	effort := req.Msg.GetEffort()
	if effort == "" {
		effort = DefaultEffort
	}
	agentSessionID := req.Msg.GetAgentSessionId()

	agentID := id.Generate()
	if err := s.queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:           agentID,
		WorkspaceID:  workspaceID,
		WorkerID:     workerID,
		WorkingDir:   workingDir,
		Title:        title,
		Model:        model,
		SystemPrompt: req.Msg.GetSystemPrompt(),
		Effort:       effort,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create agent: %w", err))
	}

	// If a session ID is provided, store it in DB so the agent can be resumed.
	if agentSessionID != "" {
		if err := s.queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
			AgentSessionID: agentSessionID,
			ID:             agentID,
		}); err != nil {
			slog.Warn("failed to store agent session ID", "agent_id", agentID, "error", err)
		}
	}

	// Register tab for worktree tracking before SendAndWait so that closing
	// another tab during the potentially slow worker handshake won't
	// mistakenly delete the worktree.
	s.worktreeHelper.RegisterTabForWorktree(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)

	// Tell the worker to start an agent and wait for a response.
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		s.unregisterWorktreeTab(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker disconnected"))
	}

	startupCtx, startupCancel := context.WithTimeout(context.Background(), s.timeoutCfg.AgentStartupTimeout()+s.timeoutCfg.APITimeout())
	defer startupCancel()

	resp, err := s.pending.SendAndWait(startupCtx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_AgentStart{
			AgentStart: &leapmuxv1.AgentStartRequest{
				WorkspaceId:    workspaceID,
				AgentId:        agentID,
				Model:          model,
				WorkingDir:     workingDir,
				SystemPrompt:   req.Msg.GetSystemPrompt(),
				AgentSessionId: agentSessionID,
				PermissionMode: "default", // New agents start with default
				Effort:         effort,
			},
		},
	})
	if err != nil {
		s.unregisterWorktreeTab(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send agent start: %w", err))
	}

	// Check if the worker returned an error (e.g. invalid working directory).
	if errMsg := resp.GetAgentStarted().GetError(); errMsg != "" {
		// Clean up the DB agent and worktree association we just created.
		s.unregisterWorktreeTab(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
		if closeErr := s.queries.CloseAgent(ctx, agentID); closeErr != nil {
			slog.Warn("failed to close agent after start error", "agent_id", agentID, "error", closeErr)
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", errMsg))
	}

	// Update DB with confirmed permission mode from startup handshake.
	if confirmedMode := resp.GetAgentStarted().GetPermissionMode(); confirmedMode != "" {
		if err := s.queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
			PermissionMode: confirmedMode,
			ID:             agentID,
		}); err != nil {
			slog.Warn("failed to set agent permission mode", "agent_id", agentID, "error", err)
		}
	}

	// Persist the worker's home directory.
	if agentHomeDir := validate.SanitizePath(resp.GetAgentStarted().GetHomeDir(), ""); agentHomeDir != "" {
		if err := s.queries.UpdateAgentHomeDir(ctx, db.UpdateAgentHomeDirParams{
			HomeDir: agentHomeDir,
			ID:      agentID,
		}); err != nil {
			slog.Warn("failed to store agent home dir", "agent_id", agentID, "error", err)
		}
	}

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var workerName string
	if worker, err := s.queries.GetWorkerByIDInternal(ctx, agent.WorkerID); err == nil {
		workerName = worker.Name
	}

	return connect.NewResponse(&leapmuxv1.OpenAgentResponse{
		Agent: agentToProto(&agent, workerName),
	}), nil
}

func (s *AgentService) CloseAgent(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CloseAgentRequest],
) (*connect.Response[leapmuxv1.CloseAgentResponse], error) {
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

	// Send stop request to worker.
	conn := s.workerMgr.Get(agent.WorkerID)
	if conn != nil {
		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_AgentStop{
				AgentStop: &leapmuxv1.AgentStopRequest{
					WorkspaceId: agent.WorkspaceID,
					AgentId:     agentID,
				},
			},
		}); err != nil {
			slog.Warn("failed to send agent stop to worker", "agent_id", agentID, "error", err)
		}
	}

	if err := s.queries.CloseAgent(ctx, agentID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Remove the persisted tab so it doesn't reappear on page reload.
	if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
		WorkspaceID: agent.WorkspaceID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       agentID,
	}); err != nil {
		slog.Warn("failed to delete workspace tab", "agent_id", agentID, "workspace_id", agent.WorkspaceID, "error", err)
	}

	// Clear any pending control requests for the closed agent.
	if err := s.queries.DeleteControlRequestsByAgentID(ctx, agentID); err != nil {
		slog.Error("delete control requests on agent close", "agent_id", agentID, "error", err)
	}

	// Clean up in-memory per-agent state.
	s.cleanupAutoContinue(agentID)
	s.lastAgentStatus.Delete(agentID)

	// Notify watchers of the status change.
	sc := AgentStatusChange(&agent, true)
	sc.GitStatus = s.GetGitStatus(agentID)
	s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: sc,
		},
	})

	// Handle worktree cleanup.
	result := s.worktreeHelper.UnregisterTabAndCleanup(ctx, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
	resp := &leapmuxv1.CloseAgentResponse{}
	if result.NeedsConfirmation {
		resp.WorktreeCleanupPending = true
		resp.WorktreePath = result.WorktreePath
		resp.WorktreeId = result.WorktreeID
	}

	return connect.NewResponse(resp), nil
}

func (s *AgentService) ListAgents(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListAgentsRequest],
) (*connect.Response[leapmuxv1.ListAgentsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace_id is required"))
	}

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, workspaceID); err != nil {
		return nil, err
	}

	agents, err := s.queries.ListAgentsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Collect unique worker IDs and batch-fetch worker names.
	workerNameByID := map[string]string{}
	for i := range agents {
		wid := agents[i].WorkerID
		if _, exists := workerNameByID[wid]; !exists {
			workerNameByID[wid] = "" // placeholder
		}
	}
	for wid := range workerNameByID {
		if w, err := s.queries.GetWorkerByIDInternal(ctx, wid); err == nil {
			workerNameByID[wid] = w.Name
		}
	}

	protoAgents := make([]*leapmuxv1.AgentInfo, len(agents))
	for i := range agents {
		protoAgents[i] = agentToProto(&agents[i], workerNameByID[agents[i].WorkerID])
	}

	return connect.NewResponse(&leapmuxv1.ListAgentsResponse{
		Agents: protoAgents,
	}), nil
}

func (s *AgentService) ListAgentMessages(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListAgentMessagesRequest],
) (*connect.Response[leapmuxv1.ListAgentMessagesResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()

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

	afterSeq := req.Msg.GetAfterSeq()
	beforeSeq := req.Msg.GetBeforeSeq()

	// Cannot use both cursors simultaneously.
	if afterSeq > 0 && beforeSeq > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("cannot specify both after_seq and before_seq"))
	}

	// Enforce maximum limit.
	const maxLimit int64 = 50
	limit := int64(req.Msg.GetLimit())
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}

	var msgs []db.Message
	var hasMore bool

	if beforeSeq > 0 {
		// Backward pagination: messages with seq < before_seq, ordered DESC.
		msgs, err = s.queries.ListMessagesByAgentIDReverse(ctx, db.ListMessagesByAgentIDReverseParams{
			AgentID: agentID,
			Seq:     beforeSeq,
			Limit:   limit + 1,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		hasMore = int64(len(msgs)) > limit
		if hasMore {
			msgs = msgs[:limit]
		}
		// Reverse to ascending order.
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	} else if afterSeq > 0 {
		// Forward pagination: messages with seq > after_seq, ordered ASC.
		msgs, err = s.queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
			AgentID: agentID,
			Seq:     afterSeq,
			Limit:   limit + 1,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		hasMore = int64(len(msgs)) > limit
		if hasMore {
			msgs = msgs[:limit]
		}
	} else {
		// Latest N: fetch most recent messages (no cursor).
		msgs, err = s.queries.ListLatestMessagesByAgentID(ctx, db.ListLatestMessagesByAgentIDParams{
			AgentID: agentID,
			Limit:   limit + 1,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		hasMore = int64(len(msgs)) > limit
		if hasMore {
			msgs = msgs[:limit]
		}
		// Reverse to ascending order.
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}

	protoMsgs := make([]*leapmuxv1.AgentChatMessage, len(msgs))
	for i := range msgs {
		protoMsgs[i] = MessageToProto(&msgs[i])
	}

	return connect.NewResponse(&leapmuxv1.ListAgentMessagesResponse{
		Messages: protoMsgs,
		HasMore:  hasMore,
	}), nil
}

func (s *AgentService) RenameAgent(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RenameAgentRequest],
) (*connect.Response[leapmuxv1.RenameAgentResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	agentID := req.Msg.GetAgentId()
	title, err := validate.SanitizeName(req.Msg.GetTitle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid title: %w", err))
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
	if _, err := getVisibleNonArchivedWorkspace(ctx, s.queries, user, wsInternal.OrgID, agent.WorkspaceID); err != nil {
		return nil, err
	}

	if _, err := s.queries.RenameAgent(ctx, db.RenameAgentParams{
		ID:    agentID,
		Title: title,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.RenameAgentResponse{}), nil
}

// getVisibleWorkspace looks up a workspace by ID and org, then verifies the user can see it.
func (s *AgentService) getVisibleWorkspace(ctx context.Context, user *auth.UserInfo, orgID, workspaceID string) (*db.Workspace, error) {
	return getVisibleWorkspace(ctx, s.queries, user, orgID, workspaceID)
}

func agentToProto(a *db.Agent, workerName string) *leapmuxv1.AgentInfo {
	pa := &leapmuxv1.AgentInfo{
		Id:             a.ID,
		WorkspaceId:    a.WorkspaceID,
		WorkerId:       a.WorkerID,
		WorkerName:     workerName,
		WorkingDir:     a.WorkingDir,
		HomeDir:        a.HomeDir,
		Title:          a.Title,
		Model:          a.Model,
		Status:         a.Status,
		CreatedAt:      timefmt.Format(a.CreatedAt),
		AgentSessionId: a.AgentSessionID,
		PermissionMode: a.PermissionMode,
		Effort:         a.Effort,
	}
	if a.ClosedAt.Valid {
		pa.ClosedAt = timefmt.Format(a.ClosedAt.Time)
	}
	return pa
}

// AgentStatusChange creates an AgentStatusChange proto from an agent record.
func AgentStatusChange(a *db.Agent, workerOnline bool) *leapmuxv1.AgentStatusChange {
	return &leapmuxv1.AgentStatusChange{
		AgentId:        a.ID,
		Status:         a.Status,
		AgentSessionId: a.AgentSessionID,
		WorkerOnline:   workerOnline,
		PermissionMode: a.PermissionMode,
		Model:          a.Model,
		Effort:         a.Effort,
	}
}

// GetGitStatus returns the cached git status for an agent, or nil if not available.
func (s *AgentService) GetGitStatus(agentID string) *leapmuxv1.AgentGitStatus {
	if v, ok := s.gitStatus.Load(agentID); ok {
		return v.(*leapmuxv1.AgentGitStatus)
	}
	return nil
}

// StoreGitStatus caches the git status for an agent.
func (s *AgentService) StoreGitStatus(agentID string, status *leapmuxv1.AgentGitStatus) {
	if status != nil {
		s.gitStatus.Store(agentID, status)
	}
}

// unregisterWorktreeTab removes a worktree tab association on agent start failure.
// No-op if worktreeID is empty.
func (s *AgentService) unregisterWorktreeTab(ctx context.Context, worktreeID string, tabType leapmuxv1.TabType, tabID string) {
	if worktreeID == "" {
		return
	}
	if err := s.queries.RemoveWorktreeTab(ctx, db.RemoveWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to unregister worktree tab", "worktree_id", worktreeID, "tab_id", tabID, "error", err)
	}
}

// MessageToProto converts a db.Message to an AgentChatMessage proto.
func MessageToProto(m *db.Message) *leapmuxv1.AgentChatMessage {
	msg := &leapmuxv1.AgentChatMessage{
		Id:                 m.ID,
		Role:               m.Role,
		Content:            m.Content,
		ContentCompression: m.ContentCompression,
		Seq:                m.Seq,
		CreatedAt:          timefmt.Format(m.CreatedAt),
		DeliveryError:      m.DeliveryError,
	}
	if m.UpdatedAt.Valid {
		msg.UpdatedAt = timefmt.Format(m.UpdatedAt.Time)
	}
	return msg
}
