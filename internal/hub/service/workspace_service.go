package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/layout"
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

	_, err = s.queries.GetVisibleWorker(ctx, db.GetVisibleWorkerParams{
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

func (s *WorkspaceService) SaveLayout(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SaveLayoutRequest],
) (*connect.Response[leapmuxv1.SaveLayoutResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.Msg.GetWorkspaceId()
	ws, err := s.getVisibleWorkspace(ctx, user, req.Msg.GetOrgId(), workspaceID)
	if err != nil {
		return nil, err
	}

	layoutNode := req.Msg.GetLayout()
	if layoutNode == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("layout is required"))
	}

	// Validate the layout structure.
	if err := layout.Validate(layoutNode); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid layout: %w", err))
	}

	// Reject unoptimized layouts — the frontend must send canonical form.
	if !layout.IsOptimized(layoutNode) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("layout is not in canonical (optimized) form"))
	}

	// Serialize to JSON.
	jsonBytes, err := protojson.Marshal(layoutNode)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal layout: %w", err))
	}

	if err := s.queries.UpsertWorkspaceLayout(ctx, db.UpsertWorkspaceLayoutParams{
		WorkspaceID: workspaceID,
		LayoutJson:  string(jsonBytes),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save layout: %w", err))
	}

	// Save per-tile active tabs.
	if err := s.queries.DeleteTileActiveTabs(ctx, workspaceID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("clear tile active tabs: %w", err))
	}

	for _, at := range req.Msg.GetActiveTabs() {
		if err := s.queries.UpsertTileActiveTab(ctx, db.UpsertTileActiveTabParams{
			WorkspaceID: workspaceID,
			TileID:      at.TileId,
			TabType:     at.TabType,
			TabID:       at.TabId,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save tile active tab: %w", err))
		}
	}

	// Reconcile workspace tabs if the request includes them.
	if tabs := req.Msg.GetTabs(); len(tabs) > 0 {
		if err := s.queries.DeleteWorkspaceTabsByWorkspace(ctx, workspaceID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace tabs: %w", err))
		}
		for _, tab := range tabs {
			if err := s.queries.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
				WorkspaceID: workspaceID,
				TabType:     tab.GetTabType(),
				TabID:       tab.GetTabId(),
				Position:    tab.GetPosition(),
				TileID:      tab.GetTileId(),
			}); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save workspace tab: %w", err))
			}
		}
	}

	// Clean up agents and terminals whose tiles no longer exist in the layout.
	s.cleanupRemovedTiles(ctx, ws, layoutNode)

	return connect.NewResponse(&leapmuxv1.SaveLayoutResponse{}), nil
}

// cleanupRemovedTiles finds workspace_tabs whose tile_id is not in the new
// layout and closes the associated agents/terminals.
func (s *WorkspaceService) cleanupRemovedTiles(ctx context.Context, ws *db.Workspace, layoutNode *leapmuxv1.LayoutNode) {
	tileIDs := layout.CollectLeafIDs(layoutNode)

	tabs, err := s.queries.ListWorkspaceTabsByWorkspace(ctx, ws.ID)
	if err != nil {
		slog.Error("failed to list workspace tabs for tile cleanup", "workspace_id", ws.ID, "error", err)
		return
	}

	for _, tab := range tabs {
		if tab.TileID == "" {
			continue // Tab not assigned to a tile; skip.
		}
		if _, ok := tileIDs[tab.TileID]; ok {
			continue // Tile still exists in the layout.
		}

		// This tab's tile has been removed — clean up.
		switch tab.TabType {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			agent, agentErr := s.queries.GetAgentByID(ctx, tab.TabID)
			var conn *workermgr.Conn
			if agentErr == nil {
				conn = s.workerMgr.Get(agent.WorkerID)
			} else {
				slog.Warn("failed to get agent for tile cleanup", "agent_id", tab.TabID, "error", agentErr)
			}
			s.closeAgentForTileCleanup(ctx, ws, conn, tab.TabID)
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			var conn *workermgr.Conn
			if s.termSvc != nil {
				if workerID := s.termSvc.GetTerminalWorkerID(tab.TabID); workerID != "" {
					conn = s.workerMgr.Get(workerID)
				}
			}
			s.closeTerminalForTileCleanup(ctx, ws, conn, tab.TabID)
		}

		// Remove the workspace_tab entry.
		if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
			WorkspaceID: ws.ID,
			TabType:     tab.TabType,
			TabID:       tab.TabID,
		}); err != nil {
			slog.Warn("failed to delete workspace tab during tile cleanup",
				"workspace_id", ws.ID, "tab_type", tab.TabType, "tab_id", tab.TabID, "error", err)
		}
	}
}

// closeAgentForTileCleanup stops an agent on the worker, marks it closed in
// the DB, and broadcasts a status change to watchers.
func (s *WorkspaceService) closeAgentForTileCleanup(ctx context.Context, ws *db.Workspace, conn *workermgr.Conn, agentID string) {
	if conn != nil {
		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_AgentStop{
				AgentStop: &leapmuxv1.AgentStopRequest{
					WorkspaceId: ws.ID,
					AgentId:     agentID,
				},
			},
		}); err != nil {
			slog.Warn("failed to send agent stop during tile cleanup", "agent_id", agentID, "error", err)
		}
	}

	if err := s.queries.CloseAgent(ctx, agentID); err != nil {
		slog.Warn("failed to close agent during tile cleanup", "agent_id", agentID, "error", err)
	}

	if err := s.queries.DeleteControlRequestsByAgentID(ctx, agentID); err != nil {
		slog.Warn("failed to delete control requests during tile cleanup", "agent_id", agentID, "error", err)
	}

	// Read agent for status broadcast (best-effort).
	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err == nil {
		tileSc := AgentStatusChange(&agent, conn != nil)
		if s.agentSvc != nil {
			tileSc.GitStatus = s.agentSvc.GetGitStatus(agentID)
		}
		s.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event: &leapmuxv1.AgentEvent_StatusChange{
				StatusChange: tileSc,
			},
		})
	}

	// Best-effort worktree cleanup (no user confirmation during tile cleanup).
	s.worktreeHelper.UnregisterTabBestEffort(ctx, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
}

// closeTerminalForTileCleanup stops a terminal on the worker and broadcasts
// a closed event to watchers.
func (s *WorkspaceService) closeTerminalForTileCleanup(ctx context.Context, _ *db.Workspace, conn *workermgr.Conn, terminalID string) {
	if s.termSvc != nil {
		s.termSvc.CloseTerminalInternal(ctx, conn, terminalID)
	}
}

// watchEventsMerged is the internal type sent through the merged channel
// by per-watcher goroutines in WatchEvents.
type watchEventsMerged struct {
	agentEvent    *leapmuxv1.AgentEvent
	terminalEvent *leapmuxv1.TerminalEvent
}

// watchEventsCore contains the core WatchEvents logic decoupled from transport.
// send is called for each WatchEventsResponse; return an error to stop the stream.
func (s *WorkspaceService) watchEventsCore(
	ctx context.Context,
	user *auth.UserInfo,
	req *leapmuxv1.WatchEventsRequest,
	send func(*leapmuxv1.WatchEventsResponse) error,
) error {
	ws, err := s.getVisibleWorkspace(ctx, user, req.GetOrgId(), req.GetWorkspaceId())
	if err != nil {
		return err
	}

	// Collect entries, limiting total to 32.
	const maxEntries = 32
	agentEntries := req.GetAgents()
	terminalEntries := req.GetTerminals()
	total := len(agentEntries) + len(terminalEntries)
	if total > maxEntries {
		// Truncate terminal entries first, then agent entries.
		if len(terminalEntries) > maxEntries-len(agentEntries) {
			if len(agentEntries) >= maxEntries {
				agentEntries = agentEntries[:maxEntries]
				terminalEntries = nil
			} else {
				terminalEntries = terminalEntries[:maxEntries-len(agentEntries)]
			}
		}
	}

	// Deduplicate agent IDs.
	seenAgents := make(map[string]struct{}, len(agentEntries))
	var dedupAgents []*leapmuxv1.WatchAgentEntry
	for _, e := range agentEntries {
		aid := e.GetAgentId()
		if _, ok := seenAgents[aid]; ok {
			continue
		}
		seenAgents[aid] = struct{}{}
		dedupAgents = append(dedupAgents, e)
	}
	agentEntries = dedupAgents

	// Deduplicate terminal IDs.
	seenTerminals := make(map[string]struct{}, len(terminalEntries))
	var dedupTerminals []*leapmuxv1.WatchTerminalEntry
	for _, e := range terminalEntries {
		tid := e.GetTerminalId()
		if _, ok := seenTerminals[tid]; ok {
			continue
		}
		seenTerminals[tid] = struct{}{}
		dedupTerminals = append(dedupTerminals, e)
	}
	terminalEntries = dedupTerminals

	// --- Register watchers before replay to capture live events ---
	type agentWatcher struct {
		agentID string
		watcher *agentmgr.Watcher
	}
	type termWatcher struct {
		terminalID string
		watcher    *terminalmgr.Watcher
	}

	var agentWatchers []agentWatcher
	for _, entry := range agentEntries {
		agentID := entry.GetAgentId()
		w := s.agentMgr.Watch(agentID)
		agentWatchers = append(agentWatchers, agentWatcher{agentID: agentID, watcher: w})
	}
	defer func() {
		for _, aw := range agentWatchers {
			s.agentMgr.Unwatch(aw.agentID, aw.watcher)
		}
	}()

	var termWatchers []termWatcher
	for _, entry := range terminalEntries {
		terminalID := entry.GetTerminalId()
		w := s.termMgr.Watch(terminalID)
		termWatchers = append(termWatchers, termWatcher{terminalID: terminalID, watcher: w})
	}
	defer func() {
		for _, tw := range termWatchers {
			s.termMgr.Unwatch(tw.terminalID, tw.watcher)
		}
	}()

	// Track replayed control request IDs per agent so we can deduplicate
	// against live events that arrived in the watcher channel during replay.
	replayedCRs := make(map[string]map[string]struct{}) // agentID -> set of requestIDs

	// --- Replay historical data and send snapshots for agents ---
	for _, entry := range agentEntries {
		agentID := entry.GetAgentId()
		agent, err := s.queries.GetAgentByID(ctx, agentID)
		if err != nil {
			if err == sql.ErrNoRows {
				slog.Debug("watchEventsCore: agent not found, skipping", "agent_id", agentID)
				continue
			}
			return fmt.Errorf("get agent %s: %w", agentID, err)
		}
		if agent.WorkspaceID != ws.ID {
			slog.Debug("watchEventsCore: agent not in workspace, skipping", "agent_id", agentID, "workspace_id", ws.ID)
			continue
		}

		// Send historical messages if requested (afterSeq >= 0).
		afterSeq := entry.GetAfterSeq()
		if afterSeq >= 0 {
			msgs, err := s.queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
				AgentID: agentID,
				Seq:     afterSeq,
				Limit:   50,
			})
			if err != nil {
				return fmt.Errorf("list messages for agent %s: %w", agentID, err)
			}
			for i := range msgs {
				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_AgentMessage{
								AgentMessage: MessageToProto(&msgs[i]),
							},
						},
					},
				}); err != nil {
					return err
				}
			}
		}

		// Send current status snapshot (check worker online per-agent).
		workerOnline := s.workerMgr.IsOnline(agent.WorkerID)
		catchupSc := AgentStatusChange(&agent, workerOnline)
		if s.agentSvc != nil {
			catchupSc.GitStatus = s.agentSvc.GetGitStatus(agentID)
		}
		if err := send(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
				AgentEvent: &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event: &leapmuxv1.AgentEvent_StatusChange{
						StatusChange: catchupSc,
					},
				},
			},
		}); err != nil {
			return err
		}

		// Replay pending control requests and track their IDs.
		pendingCRs, err := s.queries.ListControlRequestsByAgentID(ctx, agentID)
		if err != nil {
			slog.Warn("watchEventsCore: list pending control requests", "agent_id", agentID, "error", err)
		} else {
			for _, cr := range pendingCRs {
				if replayedCRs[agentID] == nil {
					replayedCRs[agentID] = make(map[string]struct{})
				}
				replayedCRs[agentID][cr.RequestID] = struct{}{}

				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_ControlRequest{
								ControlRequest: &leapmuxv1.AgentControlRequest{
									AgentId:   cr.AgentID,
									RequestId: cr.RequestID,
									Payload:   cr.Payload,
								},
							},
						},
					},
				}); err != nil {
					return err
				}
			}
		}
	}

	// --- Drain buffered watcher events that arrived during replay ---
	// Any controlRequest events whose requestId was already replayed are
	// duplicates and must be skipped; all other events are forwarded.
	for _, aw := range agentWatchers {
		crSet := replayedCRs[aw.agentID]
	drain:
		for {
			select {
			case ev := <-aw.watcher.C():
				if cr := ev.GetControlRequest(); cr != nil && crSet != nil {
					if _, dup := crSet[cr.GetRequestId()]; dup {
						continue // skip duplicate
					}
				}
				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: ev,
					},
				}); err != nil {
					return err
				}
			default:
				break drain
			}
		}
	}

	// Merged channel collects events from all watchers.
	merged := make(chan watchEventsMerged, 64)

	var wg sync.WaitGroup

	// Spawn goroutines that forward agent watcher events into merged.
	for _, aw := range agentWatchers {
		wg.Add(1)
		go func(aw agentWatcher) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-aw.watcher.C():
					if !ok {
						return
					}
					select {
					case merged <- watchEventsMerged{agentEvent: ev}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(aw)
	}

	// Spawn goroutines that forward terminal watcher events into merged.
	for _, tw := range termWatchers {
		wg.Add(1)
		go func(tw termWatcher) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-tw.watcher.C():
					if !ok {
						return
					}
					select {
					case merged <- watchEventsMerged{terminalEvent: ev}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(tw)
	}

	// Close merged channel once all forwarders exit.
	go func() {
		wg.Wait()
		close(merged)
	}()

	// Stream loop: read from merged, wrap and send.
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-merged:
			if !ok {
				return nil
			}
			var resp leapmuxv1.WatchEventsResponse
			if m.agentEvent != nil {
				resp.Event = &leapmuxv1.WatchEventsResponse_AgentEvent{
					AgentEvent: m.agentEvent,
				}
			} else if m.terminalEvent != nil {
				resp.Event = &leapmuxv1.WatchEventsResponse_TerminalEvent{
					TerminalEvent: m.terminalEvent,
				}
			}
			if err := send(&resp); err != nil {
				return err
			}
		}
	}
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
