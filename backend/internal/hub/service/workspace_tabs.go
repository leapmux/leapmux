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
	// Empty `requested` means "every workspace I can read." Default
	// the listOrgID to the authenticated user's home org only for
	// unrestricted callers. Delegation callers are already pinned to a
	// concrete workspace, which may live outside the user's home org.
	listOrgID := orgID
	if listOrgID == "" && !user.Credential.IsDelegation() {
		listOrgID = user.OrgID
	}
	workspaceIDs, err := resolveAllowedWorkspacesForUser(ctx, s.store, listOrgID, req.Msg.GetWorkspaceIds(), user)
	if err != nil {
		// Only a delegation-scope PermissionDenied is a genuine authorization
		// failure; an uncoded transient store failure must surface as a retryable
		// Internal, not a permanent PermissionDenied the frontend stops retrying.
		// Keying on the specific authz code (not "any coded error") keeps this
		// robust if the resolver's error coding changes. Mirrors ws_orgevents.
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, err
		}
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
		Tab: workspaceTabToProto(row),
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
	var row *store.WorkspaceTabRow
	if user.Credential.IsDelegation() {
		if _, err := loadWorkspaceForRead(ctx, s.store, user.Credential.WorkspaceScopeID(), user); err != nil {
			if code := connect.CodeOf(err); code == connect.CodePermissionDenied || code == connect.CodeNotFound {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tab not found in any accessible workspace"))
			}
			return nil, err
		}
		row, err = s.store.WorkspaceTabIndex().GetRendered(ctx, store.GetRenderedTabParams{
			WorkspaceID: user.Credential.WorkspaceScopeID(),
			TabType:     req.Msg.GetTabType(),
			TabID:       req.Msg.GetTabId(),
		})
	} else {
		row, err = s.store.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			UserID:  user.ID,
			TabType: req.Msg.GetTabType(),
			TabID:   req.Msg.GetTabId(),
		})
	}
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

// LocateTile resolves a tile_id to its (workspace_id, org_id) by walking the
// in-memory CRDT state of the orgs the caller can read a workspace in. After the
// walk identifies the owning workspace, we re-verify access via loadWorkspaceForRead
// so a delegated bearer can't discover siblings outside its delegation scope and a
// non-owner can't resolve a tile in a workspace whose grant was revoked. Returns
// NotFound when the tile isn't visible to the caller.
//
// A regular caller's tile may live in a workspace shared from ANOTHER org
// (cross-org collaboration), so every accessible org is searched -- home org first
// -- matching the sibling LocateTab, which already resolves cross-org through the
// store's access-joined query. A delegation bearer is pinned to a single workspace,
// so only that workspace's org is searched.
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
	orgIDs, err := s.locateTileOrgCandidates(ctx, user)
	if err != nil {
		return nil, err
	}
	notFound := connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
	// A transient failure bootstrapping ONE org's manager must not abort the
	// whole resolve: the tile (globally-unique id) may live in a later healthy
	// org. Record the failure and keep scanning; only if no org resolves the tile
	// AND at least one Get failed do we surface a retryable Internal (rather than
	// a false NotFound that tells the client to stop looking).
	var getErr error
	for _, orgID := range orgIDs {
		mgr, err := s.registry.Get(ctx, orgID)
		if err != nil {
			getErr = err
			slog.Warn("locate tile: get crdt manager failed", "org_id", orgID, "tile_id", tileID, "error", err)
			continue
		}
		workspaceID := mgr.LocateTileWorkspace(tileID)
		if workspaceID == "" {
			continue // tile not in this org's state; try the next accessible org
		}
		// Found the owning workspace. Verify the caller can read it
		// (loadWorkspaceForRead also enforces the delegation scope); collapse
		// Denied / NotFound to NotFound so we don't leak existence to a non-owner
		// whose grant was revoked or a delegation bearer probing outside its scope.
		// A tile id is globally unique, so a found-but-unreadable match is
		// authoritative -- there is no readable copy in another org to keep looking for.
		if _, err := loadWorkspaceForRead(ctx, s.store, workspaceID, user); err != nil {
			if code := connect.CodeOf(err); code == connect.CodePermissionDenied || code == connect.CodeNotFound {
				return nil, notFound
			}
			return nil, err
		}
		return connect.NewResponse(&leapmuxv1.LocateTileResponse{
			WorkspaceId: workspaceID,
			OrgId:       orgID,
		}), nil
	}
	if getErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get crdt manager: %w", getErr))
	}
	return nil, notFound
}

// locateTileOrgCandidates returns the orgs LocateTile should search, most-likely
// first. A delegation bearer is pinned to a single workspace, so only that
// workspace's org is searched (its existence is verified up front, collapsing a
// denied or missing scope to NotFound). A regular caller's tile may live in a
// workspace shared from another org, so its home org is searched first (the common
// case), then every other org it can read a workspace in, deduped.
func (s *WorkspaceService) locateTileOrgCandidates(ctx context.Context, user *auth.UserInfo) ([]string, error) {
	if user.Credential.IsDelegation() {
		ws, err := loadWorkspaceForRead(ctx, s.store, user.Credential.WorkspaceScopeID(), user)
		if err != nil {
			if code := connect.CodeOf(err); code == connect.CodePermissionDenied || code == connect.CodeNotFound {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tile not found in any accessible workspace"))
			}
			return nil, err
		}
		return []string{ws.OrgID}, nil
	}
	workspaces, err := s.store.Workspaces().ListAllAccessible(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list accessible workspaces: %w", err))
	}
	orgIDs := make([]string, 0, len(workspaces)+1)
	seen := make(map[string]struct{}, len(workspaces)+1)
	add := func(orgID string) {
		if orgID == "" {
			return
		}
		if _, dup := seen[orgID]; dup {
			return
		}
		seen[orgID] = struct{}{}
		orgIDs = append(orgIDs, orgID)
	}
	add(user.OrgID) // home org first: the common case
	for i := range workspaces {
		add(workspaces[i].OrgID)
	}
	return orgIDs, nil
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
