package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/lexorank"
	"github.com/leapmux/leapmux/internal/hub/terminalmgr"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// WorkspaceService implements the WorkspaceServiceHandler interface.
type WorkspaceService struct {
	queries        *db.Queries
	workerMgr      *workermgr.Manager
	agentMgr       *agentmgr.Manager
	termMgr        *terminalmgr.Manager
	termSvc        *TerminalService
	agentSvc       *AgentService
	pending        *workermgr.PendingRequests
	worktreeHelper *WorktreeHelper
}

// NewWorkspaceService creates a new WorkspaceService.
func NewWorkspaceService(q *db.Queries, bm *workermgr.Manager, am *agentmgr.Manager, tm *terminalmgr.Manager, pr *workermgr.PendingRequests, wh *WorktreeHelper) *WorkspaceService {
	return &WorkspaceService{queries: q, workerMgr: bm, agentMgr: am, termMgr: tm, pending: pr, worktreeHelper: wh}
}

// SetTerminalService sets the terminal service for closing terminals on tile removal.
func (s *WorkspaceService) SetTerminalService(svc *TerminalService) {
	s.termSvc = svc
}

// SetAgentService sets the agent service for accessing git status in broadcasts.
func (s *WorkspaceService) SetAgentService(svc *AgentService) {
	s.agentSvc = svc
}

func (s *WorkspaceService) CreateWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CreateWorkspaceRequest],
) (*connect.Response[leapmuxv1.CreateWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// Verify the worker exists and the user can see it.
	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	_, err = s.queries.GetOwnedWorker(ctx, db.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify the worker is online and not being deregistered.
	if !s.workerMgr.IsOnline(worker.ID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}
	if s.workerMgr.IsDeregistering(worker.ID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is being deregistered"))
	}

	workingDir := req.Msg.GetWorkingDir()
	if workingDir == "" {
		workingDir = "."
	}

	// Create worktree if requested (before workspace DB insert so failures are clean).
	var worktreeID string
	if req.Msg.GetCreateWorktree() {
		var wtErr error
		workingDir, worktreeID, wtErr = s.worktreeHelper.CreateWorktreeIfRequested(
			ctx, worker.ID, workingDir, true, req.Msg.GetWorktreeBranch(),
		)
		if wtErr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("create worktree: %w", wtErr))
		}
	}

	workspaceID := id.Generate()
	var title string
	if req.Msg.GetTitle() == "" {
		title = "New Workspace"
	} else {
		var err error
		if title, err = validate.SanitizeName(req.Msg.GetTitle()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid workspace title: %w", err))
		}
	}

	if err := s.queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		ID:        workspaceID,
		OrgID:     orgID,
		CreatedBy: user.ID,
		Title:     title,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
	}

	ws, err := s.queries.GetWorkspaceByID(ctx, db.GetWorkspaceByIDParams{
		ID:    workspaceID,
		OrgID: orgID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Auto-create an initial agent so the workspace starts with one tab.
	// Use SendAndWait to ensure the agent is fully started (startup
	// handshake complete) before returning to the frontend.
	conn := s.workerMgr.Get(worker.ID)
	var agentID string
	if conn != nil {
		agentID = id.Generate()
		if err := s.queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:          agentID,
			WorkspaceID: workspaceID,
			WorkerID:    worker.ID,
			WorkingDir:  workingDir,
			Title:       "Agent 1",
			Model:       DefaultModel,
			Effort:      DefaultEffort,
		}); err != nil {
			slog.Error("failed to create initial agent", "workspace_id", workspaceID, "error", err)
			agentID = ""
		} else {
			// Register worktree association before SendAndWait for consistency.
			s.worktreeHelper.RegisterTabForWorktree(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)

			resp, sendErr := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_AgentStart{
					AgentStart: &leapmuxv1.AgentStartRequest{
						WorkspaceId:    workspaceID,
						AgentId:        agentID,
						Model:          DefaultModel,
						Effort:         DefaultEffort,
						WorkingDir:     workingDir,
						PermissionMode: "default",
					},
				},
			})
			if sendErr != nil {
				slog.Error("failed to start initial agent", "workspace_id", workspaceID, "error", sendErr)
			} else if errMsg := resp.GetAgentStarted().GetError(); errMsg != "" {
				slog.Error("initial agent start failed", "workspace_id", workspaceID, "error", errMsg)
				// Clean up the orphaned DB agent and worktree association.
				s.unregisterWorktreeTab(ctx, worktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
				if closeErr := s.queries.CloseAgent(ctx, agentID); closeErr != nil {
					slog.Warn("failed to close orphaned initial agent", "agent_id", agentID, "error", closeErr)
				}
				agentID = ""
			} else {
				if confirmedMode := resp.GetAgentStarted().GetPermissionMode(); confirmedMode != "" {
					if err := s.queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
						PermissionMode: confirmedMode,
						ID:             agentID,
					}); err != nil {
						slog.Warn("failed to set initial agent permission mode", "agent_id", agentID, "error", err)
					}
				}
				if homeDir := resp.GetAgentStarted().GetHomeDir(); homeDir != "" {
					if err := s.queries.UpdateAgentHomeDir(ctx, db.UpdateAgentHomeDirParams{
						HomeDir: homeDir,
						ID:      agentID,
					}); err != nil {
						slog.Warn("failed to store initial agent home dir", "agent_id", agentID, "error", err)
					}
				}
			}
		}
	}

	// Create initial tab entry for the agent.
	if agentID != "" {
		if err := s.queries.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
			WorkspaceID: workspaceID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       agentID,
			Position:    lexorank.First(),
			TileID:      "",
		}); err != nil {
			slog.Warn("failed to create initial workspace tab", "workspace_id", workspaceID, "agent_id", agentID, "error", err)
		}
	}

	// Auto-assign workspace to user's "In progress" section.
	sections, err := s.queries.ListWorkspaceSectionsByUserID(ctx, user.ID)
	if err == nil {
		for _, sec := range sections {
			if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
				if err := s.queries.SetWorkspaceSectionItem(ctx, db.SetWorkspaceSectionItemParams{
					UserID:      user.ID,
					WorkspaceID: workspaceID,
					SectionID:   sec.ID,
					Position:    lexorank.First(),
				}); err != nil {
					slog.Warn("failed to assign workspace to section", "workspace_id", workspaceID, "section_id", sec.ID, "error", err)
				}
				break
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.CreateWorkspaceResponse{
		Workspace: workspaceToProto(&ws),
	}), nil
}

func (s *WorkspaceService) ListWorkspaces(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkspacesRequest],
) (*connect.Response[leapmuxv1.ListWorkspacesResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	limit := int64(50)
	var offset int64
	if req.Msg.GetPage() != nil {
		if req.Msg.GetPage().GetLimit() > 0 {
			limit = int64(req.Msg.GetPage().GetLimit())
		}
		if req.Msg.GetPage().GetCursor() != "" {
			_, _ = fmt.Sscanf(req.Msg.GetPage().GetCursor(), "%d", &offset)
		}
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	workspaces, err := s.queries.ListVisibleWorkspaces(ctx, db.ListVisibleWorkspacesParams{
		UserID: user.ID,
		OrgID:  orgID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoWorkspaces := make([]*leapmuxv1.Workspace, len(workspaces))
	for i := range workspaces {
		protoWorkspaces[i] = workspaceToProto(&workspaces[i])
	}

	var page *leapmuxv1.PageResponse
	if int64(len(workspaces)) >= limit {
		page = &leapmuxv1.PageResponse{
			NextCursor: fmt.Sprintf("%d", offset+limit),
			HasMore:    true,
		}
	}

	return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{
		Workspaces: protoWorkspaces,
		Page:       page,
	}), nil
}

func (s *WorkspaceService) GetWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkspaceRequest],
) (*connect.Response[leapmuxv1.GetWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	ws, err := s.getVisibleWorkspace(ctx, user, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.GetWorkspaceResponse{
		Workspace: workspaceToProto(ws),
	}), nil
}

func (s *WorkspaceService) UpdateWorkspaceSharing(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateWorkspaceSharingRequest],
) (*connect.Response[leapmuxv1.UpdateWorkspaceSharingResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()
	shareMode := req.Msg.GetShareMode()

	if shareMode != leapmuxv1.ShareMode_SHARE_MODE_PRIVATE && shareMode != leapmuxv1.ShareMode_SHARE_MODE_ORG && shareMode != leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share_mode: %s", shareMode))
	}

	// Only the workspace creator can change sharing.
	result, err := s.queries.UpdateWorkspaceShareMode(ctx, db.UpdateWorkspaceShareModeParams{
		ShareMode: shareMode,
		ID:        workspaceID,
		CreatedBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace not found or not the owner"))
	}

	// Clear existing shares and set new ones for 'members' mode.
	if rows > 0 {
		if err := s.queries.ClearWorkspaceShares(ctx, workspaceID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		if shareMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
			for _, userID := range req.Msg.GetUserIds() {
				if err := s.queries.CreateWorkspaceShare(ctx, db.CreateWorkspaceShareParams{
					WorkspaceID: workspaceID,
					UserID:      userID,
				}); err != nil {
					return nil, connect.NewError(connect.CodeInternal, err)
				}
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.UpdateWorkspaceSharingResponse{}), nil
}

func (s *WorkspaceService) ListWorkspaceShares(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkspaceSharesRequest],
) (*connect.Response[leapmuxv1.ListWorkspaceSharesResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	ws, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, workspaceID)
	if err != nil {
		return nil, err
	}

	var members []*leapmuxv1.WorkspaceShareMember
	if ws.ShareMode == leapmuxv1.ShareMode_SHARE_MODE_MEMBERS {
		shares, err := s.queries.ListWorkspaceSharesByWorkspaceID(ctx, workspaceID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		members = make([]*leapmuxv1.WorkspaceShareMember, len(shares))
		for i, sh := range shares {
			members[i] = &leapmuxv1.WorkspaceShareMember{
				UserId:      sh.UserID,
				Username:    sh.Username,
				DisplayName: sh.DisplayName,
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.ListWorkspaceSharesResponse{
		ShareMode: ws.ShareMode,
		Members:   members,
	}), nil
}

// getVisibleWorkspace looks up a workspace by ID and org, then verifies the user can see it.
func (s *WorkspaceService) getVisibleWorkspace(ctx context.Context, user *auth.UserInfo, orgID, workspaceID string) (*db.Workspace, error) {
	return getVisibleWorkspace(ctx, s.queries, user, orgID, workspaceID)
}

// unregisterWorktreeTab removes a worktree tab association on agent start failure.
// No-op if worktreeID is empty.
func (s *WorkspaceService) unregisterWorktreeTab(ctx context.Context, worktreeID string, tabType leapmuxv1.TabType, tabID string) {
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

func (s *WorkspaceService) RenameWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RenameWorkspaceRequest],
) (*connect.Response[leapmuxv1.RenameWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	title, err := validate.SanitizeName(req.Msg.GetTitle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid workspace title: %w", err))
	}

	result, err := s.queries.RenameWorkspace(ctx, db.RenameWorkspaceParams{
		Title:     title,
		ID:        req.Msg.GetWorkspaceId(),
		CreatedBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not the owner"))
	}

	return connect.NewResponse(&leapmuxv1.RenameWorkspaceResponse{}), nil
}

func (s *WorkspaceService) DeleteWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteWorkspaceRequest],
) (*connect.Response[leapmuxv1.DeleteWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()

	// Look up the workspace.
	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	ws, err := s.getVisibleWorkspace(ctx, user, wsInternal.OrgID, workspaceID)
	if err != nil {
		return nil, err
	}

	if ws.CreatedBy != user.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not the workspace owner"))
	}

	// Close all active agents, sending stop to each agent's worker.
	agents, err := s.queries.ListAgentsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		slog.Error("failed to list agents", "workspace_id", workspaceID, "error", err)
	} else {
		for i := range agents {
			if agents[i].Status != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				continue
			}
			conn := s.workerMgr.Get(agents[i].WorkerID)
			if conn != nil {
				if sendErr := conn.Send(&leapmuxv1.ConnectResponse{
					Payload: &leapmuxv1.ConnectResponse_AgentStop{
						AgentStop: &leapmuxv1.AgentStopRequest{
							WorkspaceId: workspaceID,
							AgentId:     agents[i].ID,
						},
					},
				}); sendErr != nil {
					slog.Warn("failed to send agent stop", "agent_id", agents[i].ID, "error", sendErr)
				}
			}
		}
	}

	if err := s.queries.CloseActiveAgentsByWorkspace(ctx, workspaceID); err != nil {
		slog.Error("failed to close agents for workspace", "workspace_id", workspaceID, "error", err)
	}

	// Soft delete the workspace.
	result, err := s.queries.SoftDeleteWorkspace(ctx, db.SoftDeleteWorkspaceParams{
		ID:        workspaceID,
		CreatedBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not the owner"))
	}

	return connect.NewResponse(&leapmuxv1.DeleteWorkspaceResponse{}), nil
}

func (s *WorkspaceService) ListTabs(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListTabsRequest],
) (*connect.Response[leapmuxv1.ListTabsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()

	// Verify the user can see this workspace.
	_, err = s.getVisibleWorkspace(ctx, user, req.Msg.GetOrgId(), workspaceID)
	if err != nil {
		return nil, err
	}

	tabs, err := s.queries.ListWorkspaceTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoTabs := make([]*leapmuxv1.WorkspaceTab, len(tabs))
	for i, tab := range tabs {
		protoTabs[i] = &leapmuxv1.WorkspaceTab{
			TabType:  tab.TabType,
			TabId:    tab.TabID,
			Position: tab.Position,
			TileId:   tab.TileID,
		}
	}

	return connect.NewResponse(&leapmuxv1.ListTabsResponse{
		Tabs: protoTabs,
	}), nil
}

func (s *WorkspaceService) GetLayout(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetLayoutRequest],
) (*connect.Response[leapmuxv1.GetLayoutResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()
	_, err = s.getVisibleWorkspace(ctx, user, req.Msg.GetOrgId(), workspaceID)
	if err != nil {
		return nil, err
	}

	// Load layout from DB.
	var layoutNode leapmuxv1.LayoutNode
	dbLayout, err := s.queries.GetWorkspaceLayout(ctx, workspaceID)
	if err == sql.ErrNoRows {
		// No layout saved yet — return a default single-leaf layout.
		tileID := id.Generate()
		layoutNode = leapmuxv1.LayoutNode{
			Node: &leapmuxv1.LayoutNode_Leaf{
				Leaf: &leapmuxv1.LayoutLeaf{Id: tileID},
			},
		}
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get layout: %w", err))
	} else {
		if err := protojson.Unmarshal([]byte(dbLayout.LayoutJson), &layoutNode); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal layout: %w", err))
		}
	}

	// Load per-tile active tabs.
	dbActiveTabs, err := s.queries.ListTileActiveTabs(ctx, workspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list tile active tabs: %w", err))
	}

	activeTabs := make([]*leapmuxv1.TileActiveTab, len(dbActiveTabs))
	for i, at := range dbActiveTabs {
		activeTabs[i] = &leapmuxv1.TileActiveTab{
			TileId:  at.TileID,
			TabType: at.TabType,
			TabId:   at.TabID,
		}
	}

	return connect.NewResponse(&leapmuxv1.GetLayoutResponse{
		Layout:     &layoutNode,
		ActiveTabs: activeTabs,
	}), nil
}

func workspaceToProto(w *db.Workspace) *leapmuxv1.Workspace {
	return &leapmuxv1.Workspace{
		Id:        w.ID,
		OrgId:     w.OrgID,
		CreatedBy: w.CreatedBy,
		Title:     w.Title,
		CreatedAt: timefmt.Format(w.CreatedAt),
		ShareMode: w.ShareMode,
	}
}
