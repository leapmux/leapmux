package service

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/layout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

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
