package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// ListTabs reads the materialized rendered-tab view, filtered to the
// requested workspaces. Mutations now flow through OrgCRDT.SubmitOps;
// this RPC is read-only.
func (s *WorkspaceService) ListTabs(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListTabsRequest],
) (*connect.Response[leapmuxv1.ListTabsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID := req.Msg.GetOrgId()
	requested := req.Msg.GetWorkspaceIds()

	// Empty `requested` means "every workspace I can read." Default
	// the listOrgID to the authenticated user's home org so the CLI's
	// `tab list` with no flags returns something useful (mirrors
	// ListWorkspaces above). The non-empty branch delegates to the
	// shared resolver so the dedup / ListByIDs / owner-or-ACL filter
	// stays in one place.
	listOrgID := orgID
	if listOrgID == "" {
		listOrgID = user.OrgID
	}
	workspaceIDs, err := resolveAllowedWorkspaces(ctx, s.store, listOrgID, requested, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var pbTabs []*leapmuxv1.WorkspaceTab
	if len(workspaceIDs) > 0 {
		rows, err := s.store.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, workspaceIDs)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list rendered tabs: %w", err))
		}
		pbTabs = make([]*leapmuxv1.WorkspaceTab, 0, len(rows))
		for _, t := range rows {
			pbTabs = append(pbTabs, &leapmuxv1.WorkspaceTab{
				TabType:     t.TabType,
				TabId:       t.TabID,
				Position:    t.Position,
				TileId:      t.TileID,
				WorkerId:    t.WorkerID,
				WorkspaceId: t.WorkspaceID,
			})
		}
	}

	return connect.NewResponse(&leapmuxv1.ListTabsResponse{
		Tabs: pbTabs,
	}), nil
}

// GetTab resolves a single workspace tab from the materialized
// rendered-tab view.
func (s *WorkspaceService) GetTab(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetTabRequest],
) (*connect.Response[leapmuxv1.GetTabResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadWorkspaceForRead(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}
	row, err := s.store.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		TabType:     req.Msg.GetTabType(),
		TabID:       req.Msg.GetTabId(),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tab not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get tab: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.GetTabResponse{
		Tab: &leapmuxv1.WorkspaceTab{
			TabType:     row.TabType,
			TabId:       row.TabID,
			Position:    row.Position,
			TileId:      row.TileID,
			WorkerId:    row.WorkerID,
			WorkspaceId: row.WorkspaceID,
		},
	}), nil
}

// LocateTab finds a tab by (tab_type, tab_id) without a workspace
// filter. The store layer scopes the search to workspaces the user
// owns or has a share grant for, so the lookup is safe across orgs
// without leaking other users' tabs. Used by the `leapmux remote` CLI
// to derive a spawning tab's full context (org / workspace / tile /
// worker) from just the env-injected tab id.
func (s *WorkspaceService) LocateTab(
	ctx context.Context,
	req *connect.Request[leapmuxv1.LocateTabRequest],
) (*connect.Response[leapmuxv1.LocateTabResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetTabId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tab_id is required"))
	}
	row, err := s.store.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
		UserID:  user.ID,
		TabType: req.Msg.GetTabType(),
		TabID:   req.Msg.GetTabId(),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tab not found in any accessible workspace"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("locate tab: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.LocateTabResponse{
		Tab: &leapmuxv1.WorkspaceTab{
			TabType:     row.TabType,
			TabId:       row.TabID,
			Position:    row.Position,
			TileId:      row.TileID,
			WorkerId:    row.WorkerID,
			WorkspaceId: row.WorkspaceID,
		},
	}), nil
}

// LocateTile resolves a tile_id to its (workspace_id, org_id) by
// walking the in-memory CRDT state of the user's org. After the
// walk identifies the owning workspace, we re-verify access via
// loadWorkspaceForRead so a delegated bearer can't discover
// siblings outside its delegation scope. Returns NotFound when the
// tile isn't visible to the caller.
//
// The CLI's universal resolver uses this when a script only knows
// a tile id (e.g., from an event stream's `layout_changed` notice)
// and needs the workspace context for follow-up CRDT mutations.
func (s *WorkspaceService) LocateTile(
	ctx context.Context,
	req *connect.Request[leapmuxv1.LocateTileRequest],
) (*connect.Response[leapmuxv1.LocateTileResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	tileID := req.Msg.GetTileId()
	if tileID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tile_id is required"))
	}
	if s.registry == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("crdt registry not configured"))
	}
	mgr, err := s.registry.Get(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get crdt manager: %w", err))
	}
	workspaceID := mgr.LocateTileWorkspace(tileID)
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
	}
	// Delegated bearers (auth.UserInfo.DelegationWorkspaceID set at
	// mint time) are pinned to a single workspace. A tile that lives
	// in any other workspace — even one the underlying user owns —
	// must collapse to NotFound so a leaked delegation token can't
	// enumerate sibling tiles outside its scope.
	if user.DelegationWorkspaceID != "" && workspaceID != user.DelegationWorkspaceID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
	}
	// Verify the caller can read this workspace; collapse Denied /
	// NotFound to NotFound so we don't leak existence to a non-owner
	// whose share grant was revoked.
	if _, err := s.loadWorkspaceForRead(ctx, s.store, workspaceID, user.ID); err != nil {
		if code := connect.CodeOf(err); code == connect.CodePermissionDenied || code == connect.CodeNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
		}
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.LocateTileResponse{
		WorkspaceId: workspaceID,
		OrgId:       user.OrgID,
	}), nil
}
