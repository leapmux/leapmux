package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// ListTabs reads the materialized rendered-tab view, filtered to the
// requested workspaces. Mutations now flow through UserCRDT.SubmitOps;
// this RPC is read-only.
func (s *WorkspaceService) ListTabs(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListTabsRequest],
) (*connect.Response[leapmuxv1.ListTabsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceIDs, err := resolveAllowedWorkspacesForUser(
		ctx, s.store, req.Msg.GetWorkspaceIds(), user)
	if err != nil {
		// Only a delegation-scope PermissionDenied is a genuine authorization
		// failure; an uncoded transient store failure must surface as a retryable
		// Internal, not a permanent PermissionDenied the frontend stops retrying.
		// Keying on the specific authz code (not "any coded error") keeps this
		// robust if the resolver's error coding changes. Mirrors ws_userevents.
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var pbTabs []*leapmuxv1.WorkspaceTab
	if len(workspaceIDs) > 0 {
		// user.ID is the row owner, not merely the caller: every workspace
		// resolveAllowedWorkspacesForUser returned passed an owner-only access
		// check against this same id, so the tabs the caller may see are the
		// ones stamped with it. Without the predicate the (user_id, tab_id) key
		// lets another tenant's row for one of these workspaces come back too.
		rows, err := s.store.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, store.ListRenderedTabsByWorkspaceIDsParams{
			UserID:       user.ID,
			WorkspaceIDs: workspaceIDs,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list rendered tabs: %w", err))
		}
		pbTabs = make([]*leapmuxv1.WorkspaceTab, 0, len(rows))
		for _, t := range rows {
			pbTabs = append(pbTabs, workspaceTabToProto(&t))
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
	if _, err := loadWorkspaceForRead(ctx, s.store, req.Msg.GetWorkspaceId(), user); err != nil {
		return nil, err
	}
	// loadWorkspaceForRead proved the caller owns the workspace; user.ID is
	// therefore the owner whose rendered tabs live in it. Binding it is what
	// stops a foreign row -- workspace_tab_rendered's workspace_id is a plain
	// FK, so any tenant may name any workspace -- from answering this lookup.
	row, err := s.store.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
		UserID:      user.ID,
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
		Tab: workspaceTabToProto(row),
	}), nil
}

// LocateTab finds a tab by (tab_type, tab_id) without a workspace
// filter. The store layer scopes the search to workspaces the user
// owns, so the lookup is safe without leaking other users' tabs. Used
// by the `leapmux control` CLI to derive a spawning tab's full context
// (workspace / tile / worker) from just the env-injected tab id.
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
	// One lookup for every credential kind. A delegation bearer used to take a
	// separate arm that pinned the search to its mint-scope workspace; with the
	// workspace axis gone it is scoped exactly like a session -- to the tabs its
	// user owns -- so the search is the same search.
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
		Tab: workspaceTabToProto(row),
	}), nil
}

// LocateTile resolves a tile_id to its workspace_id by walking the
// in-memory CRDT state of the caller's user tenancy. After the walk
// identifies the owning workspace, we re-verify it via loadWorkspaceForRead so a
// caller cannot resolve a tile in a workspace it does not own. Returns NotFound
// when the tile isn't visible to the caller.
//
// There is no delegation-scope arm any more: a delegation bearer authenticates AS
// its owner and carries no workspace bound, so it reaches every workspace that
// owner has -- see the note on LocateTab, and `docs/operating/security.md` for
// what that bound now is. The CRDT registry is keyed by user id, so this always
// resolves against exactly one UserCRDT manager: the authenticated user's own.
//
// The CLI's universal resolver uses this when a script only knows a tile id (e.g.,
// from an event stream's `layout_changed` notice) and needs the workspace context
// for follow-up CRDT mutations.
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
	userID := user.ID.String()
	notFound := connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
	mgr, err := s.registry.Get(ctx, userID)
	if err != nil {
		// A transient failure bootstrapping the manager must NOT collapse to
		// NotFound: NotFound is a permanent answer -- the CLI tile resolver stops
		// looking -- so a store blip during bootstrap would report a live tile as
		// gone. Surface a retryable Internal instead, and log the ids the wrapped
		// error doesn't carry.
		slog.Warn("locate tile: get crdt manager failed", "user_id", userID, "tile_id", tileID, "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get crdt manager: %w", err))
	}
	workspaceID := mgr.LocateTileWorkspace(tileID)
	if workspaceID == "" {
		return nil, notFound
	}
	// Found the owning workspace. Verify the caller can read it; collapse
	// Denied / NotFound to NotFound so we don't leak existence to a caller
	// whose ownership has since changed.
	if _, err := readWorkspaceOrNotFound(ctx, s.store, workspaceID, user, notFound); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.LocateTileResponse{
		WorkspaceId: workspaceID,
	}), nil
}

func workspaceTabToProto(row *store.WorkspaceTabRow) *leapmuxv1.WorkspaceTab {
	if row == nil {
		return nil
	}
	return &leapmuxv1.WorkspaceTab{
		TabType:     row.TabType,
		TabId:       row.TabID,
		Position:    row.Position,
		TileId:      row.TileID,
		WorkerId:    row.WorkerID,
		WorkspaceId: row.WorkspaceID,
	}
}
