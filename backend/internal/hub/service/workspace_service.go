package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/protobuf/encoding/protojson"
)

// layoutJSON is the structure persisted in the workspace_layouts table.
type layoutJSON struct {
	Layout          json.RawMessage   `json:"layout"`
	FloatingWindows []json.RawMessage `json:"floating_windows,omitempty"`
}

// serializeLayoutJSON marshals a layout node and floating windows into a layoutJSON struct.
func serializeLayoutJSON(marshaler protojson.MarshalOptions, layout *leapmuxv1.LayoutNode, floatingWindows []*leapmuxv1.FloatingWindow) (layoutJSON, error) {
	var stored layoutJSON
	if layout != nil {
		layoutBytes, err := marshaler.Marshal(layout)
		if err != nil {
			return stored, connect.NewError(connect.CodeInternal, fmt.Errorf("serialize layout: %w", err))
		}
		stored.Layout = layoutBytes
	}
	for _, fw := range floatingWindows {
		fwBytes, err := marshaler.Marshal(fw)
		if err != nil {
			return stored, connect.NewError(connect.CodeInternal, fmt.Errorf("serialize floating window: %w", err))
		}
		stored.FloatingWindows = append(stored.FloatingWindows, fwBytes)
	}
	return stored, nil
}

// WorkspaceService implements the WorkspaceServiceHandler interface.
type WorkspaceService struct {
	store    store.Store
	soloMode bool
}

// NewWorkspaceService creates a new WorkspaceService.
func NewWorkspaceService(st store.Store, soloMode bool) *WorkspaceService {
	return &WorkspaceService{store: st, soloMode: soloMode}
}

// workspaceToProto converts a hub DB workspace row to the proto Workspace message.
func workspaceToProto(w *store.Workspace) *leapmuxv1.Workspace {
	return &leapmuxv1.Workspace{
		Id:        w.ID,
		CreatedBy: w.OwnerUserID,
		Title:     w.Title,
		CreatedAt: w.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// loadWorkspaceForRead applies the workspace read policy:
// owners may read, explicitly shared members may read, everyone else is denied.
func (s *WorkspaceService) loadWorkspaceForRead(ctx context.Context, st store.Store, workspaceID, userID string) (*store.Workspace, error) {
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID == userID {
		return ws, nil
	}

	hasAccess, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
		WorkspaceID: ws.ID,
		UserID:      userID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !hasAccess {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no access to workspace"))
	}
	return ws, nil
}

// loadWorkspaceForOwnerWrite applies the workspace write policy:
// only owners may mutate workspace state; shared access is read-only.
func (s *WorkspaceService) loadWorkspaceForOwnerWrite(ctx context.Context, st store.Store, workspaceID, userID string) (*store.Workspace, error) {
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only workspace owner can modify workspace state"))
	}
	return ws, nil
}

func normalizeWorkspaceShareUserIDs(userIDs []string, ownerUserID string) []string {
	seen := make(map[string]struct{}, len(userIDs))
	normalized := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" || userID == ownerUserID {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		normalized = append(normalized, userID)
	}
	return normalized
}

func (s *WorkspaceService) validateWorkspaceShareUsers(ctx context.Context, st store.Store, userIDs []string) error {
	for _, userID := range userIDs {
		if _, err := st.Users().GetByID(ctx, userID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user %s not found", userID))
			}
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	return nil
}

func buildWorkspaceTabParams(workspaceID string, tabs []*leapmuxv1.WorkspaceTab) []store.UpsertWorkspaceTabParams {
	params := make([]store.UpsertWorkspaceTabParams, len(tabs))
	for i, tab := range tabs {
		params[i] = store.UpsertWorkspaceTabParams{
			WorkspaceID: workspaceID,
			WorkerID:    tab.GetWorkerId(),
			TabType:     tab.GetTabType(),
			TabID:       tab.GetTabId(),
			Position:    tab.GetPosition(),
			TileID:      tab.GetTileId(),
		}
	}
	return params
}

func (s *WorkspaceService) saveWorkspaceLayoutEntry(
	ctx context.Context,
	st store.Store,
	workspaceID string,
	layout *leapmuxv1.LayoutNode,
	floatingWindows []*leapmuxv1.FloatingWindow,
	tabs []*leapmuxv1.WorkspaceTab,
	marshaler protojson.MarshalOptions,
) error {
	stored, err := serializeLayoutJSON(marshaler, layout, floatingWindows)
	if err != nil {
		return err
	}

	layoutJSONBytes, err := json.Marshal(stored)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("serialize layout JSON: %w", err))
	}

	if err := st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
		WorkspaceID: workspaceID,
		LayoutJSON:  string(layoutJSONBytes),
	}); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("save layout: %w", err))
	}

	if err := st.WorkspaceTabs().DeleteByWorkspace(ctx, workspaceID); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace tabs: %w", err))
	}

	if err := st.WorkspaceTabs().BulkUpsert(ctx, buildWorkspaceTabParams(workspaceID, tabs)); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("save tabs: %w", err))
	}

	return nil
}

func (s *WorkspaceService) CreateWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CreateWorkspaceRequest],
) (*connect.Response[leapmuxv1.CreateWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	title, err := validate.SanitizeName(req.Msg.GetTitle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
	}

	wsID := id.Generate()
	if err := s.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          wsID,
		OrgID:       req.Msg.GetOrgId(),
		OwnerUserID: user.ID,
		Title:       title,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.CreateWorkspaceResponse{
		WorkspaceId: wsID,
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

	workspaces, err := s.store.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
		UserID: user.ID,
		OrgID:  req.Msg.GetOrgId(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspaces: %w", err))
	}

	pbWorkspaces := make([]*leapmuxv1.Workspace, len(workspaces))
	for i := range workspaces {
		pbWorkspaces[i] = workspaceToProto(&workspaces[i])
	}

	return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{
		Workspaces: pbWorkspaces,
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

	ws, err := s.loadWorkspaceForRead(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID)
	if err != nil {
		return nil, err
	}

	// Verify workspace belongs to the requested org.
	if reqOrgID := req.Msg.GetOrgId(); reqOrgID != "" && ws.OrgID != reqOrgID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
	}

	return connect.NewResponse(&leapmuxv1.GetWorkspaceResponse{
		Workspace: workspaceToProto(ws),
	}), nil
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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
	}

	rows, err := s.store.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
		Title:       title,
		ID:          req.Msg.GetWorkspaceId(),
		OwnerUserID: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("rename workspace: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not owner"))
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

	var workerIDs []string
	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID); err != nil {
			return err
		}

		var err error
		workerIDs, err = tx.WorkspaceTabs().ListDistinctWorkersByWorkspace(ctx, req.Msg.GetWorkspaceId())
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace workers: %w", err))
		}

		rows, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          req.Msg.GetWorkspaceId(),
			OwnerUserID: user.ID,
		})
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace: %w", err))
		}
		if rows == 0 {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not owner"))
		}

		if err := tx.WorkspaceTabs().DeleteByWorkspace(ctx, req.Msg.GetWorkspaceId()); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace tabs: %w", err))
		}
		if err := tx.WorkspaceLayouts().Delete(ctx, req.Msg.GetWorkspaceId()); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace layout: %w", err))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.DeleteWorkspaceResponse{
		WorkerIds: workerIDs,
	}), nil
}

func (s *WorkspaceService) UpdateWorkspaceSharing(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateWorkspaceSharingRequest],
) (*connect.Response[leapmuxv1.UpdateWorkspaceSharingResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace sharing is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	ws, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID)
	if err != nil {
		return nil, err
	}

	switch req.Msg.GetShareMode() {
	case leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
			if _, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID); err != nil {
				return err
			}
			if err := tx.WorkspaceAccess().Clear(ctx, req.Msg.GetWorkspaceId()); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
			}
			return nil
		})

	case leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		normalizedUserIDs := normalizeWorkspaceShareUserIDs(req.Msg.GetUserIds(), ws.OwnerUserID)
		if err := s.validateWorkspaceShareUsers(ctx, s.store, normalizedUserIDs); err != nil {
			return nil, err
		}

		err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
			if _, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID); err != nil {
				return err
			}
			if err := tx.WorkspaceAccess().Clear(ctx, req.Msg.GetWorkspaceId()); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
			}
			grantParams := make([]store.GrantWorkspaceAccessParams, len(normalizedUserIDs))
			for i, uid := range normalizedUserIDs {
				grantParams[i] = store.GrantWorkspaceAccessParams{
					WorkspaceID: req.Msg.GetWorkspaceId(),
					UserID:      uid,
				}
			}
			if err := tx.WorkspaceAccess().BulkGrant(ctx, grantParams); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("grant workspace access: %w", err))
			}
			return nil
		})

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share mode"))
	}
	if err != nil {
		return nil, err
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

	if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}

	accessEntries, err := s.store.WorkspaceAccess().ListByWorkspaceID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace shares: %w", err))
	}

	shareMode := leapmuxv1.ShareMode_SHARE_MODE_PRIVATE
	var members []*leapmuxv1.WorkspaceShareMember
	if len(accessEntries) > 0 {
		shareMode = leapmuxv1.ShareMode_SHARE_MODE_MEMBERS
		members = make([]*leapmuxv1.WorkspaceShareMember, len(accessEntries))
		for i, entry := range accessEntries {
			members[i] = &leapmuxv1.WorkspaceShareMember{
				UserId: entry.UserID,
			}
		}
	}

	return connect.NewResponse(&leapmuxv1.ListWorkspaceSharesResponse{
		ShareMode: shareMode,
		Members:   members,
	}), nil
}

func (s *WorkspaceService) AddTab(
	ctx context.Context,
	req *connect.Request[leapmuxv1.AddTabRequest],
) (*connect.Response[leapmuxv1.AddTabResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}

	tab := req.Msg.GetTab()
	if tab == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tab is required"))
	}

	if err := s.store.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		WorkerID:    tab.GetWorkerId(),
		TabType:     tab.GetTabType(),
		TabID:       tab.GetTabId(),
		Position:    tab.GetPosition(),
		TileID:      tab.GetTileId(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("add tab: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.AddTabResponse{}), nil
}

func (s *WorkspaceService) RemoveTab(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RemoveTabRequest],
) (*connect.Response[leapmuxv1.RemoveTabResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}

	if err := s.store.WorkspaceTabs().Delete(ctx, store.DeleteWorkspaceTabParams{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		TabType:     req.Msg.GetTabType(),
		TabID:       req.Msg.GetTabId(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove tab: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.RemoveTabResponse{}), nil
}

// ListTabs returns tabs across one or more workspaces in a single call.
//
// If the request's WorkspaceIds is empty, it returns tabs for every workspace
// in the org the caller can read. Otherwise the given IDs are resolved
// individually and silently dropped when they don't exist, belong to a
// different org, or aren't accessible to the caller, so stale client state
// never surfaces as a 404 or PermissionDenied.
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

	var workspaceIDs []string
	if len(requested) == 0 {
		workspaces, err := s.store.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list accessible workspaces: %w", err))
		}
		workspaceIDs = make([]string, len(workspaces))
		for i := range workspaces {
			workspaceIDs[i] = workspaces[i].ID
		}
	} else {
		workspaceIDs = make([]string, 0, len(requested))
		seen := make(map[string]struct{}, len(requested))
		for _, wsID := range requested {
			if wsID == "" {
				continue
			}
			if _, dup := seen[wsID]; dup {
				continue
			}
			seen[wsID] = struct{}{}

			ws, err := s.loadWorkspaceForRead(ctx, s.store, wsID, user.ID)
			if err != nil {
				code := connect.CodeOf(err)
				if code == connect.CodeNotFound || code == connect.CodePermissionDenied {
					continue
				}
				return nil, err
			}
			if orgID != "" && ws.OrgID != orgID {
				continue
			}
			workspaceIDs = append(workspaceIDs, ws.ID)
		}
	}

	// One ListByWorkspace per ID. The frontend batcher already collapses N
	// client→hub RPCs into one call, so the remaining cost here is N local
	// DB reads on the hub store. A `WHERE workspace_id IN (...)` query
	// would need sqlc changes across sqlite/postgres/mysql backends +
	// interface churn for a sub-millisecond saving per workspace; not
	// worth the reach today.
	var pbTabs []*leapmuxv1.WorkspaceTab
	for _, wsID := range workspaceIDs {
		tabs, err := s.store.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list tabs for %s: %w", wsID, err))
		}
		for _, t := range tabs {
			pbTabs = append(pbTabs, &leapmuxv1.WorkspaceTab{
				TabType:     t.TabType,
				TabId:       t.TabID,
				Position:    t.Position,
				TileId:      t.TileID,
				WorkerId:    t.WorkerID,
				WorkspaceId: wsID,
			})
		}
	}

	return connect.NewResponse(&leapmuxv1.ListTabsResponse{
		Tabs: pbTabs,
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
	if _, err := s.loadWorkspaceForRead(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}

	layout, err := s.store.WorkspaceLayouts().Get(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewResponse(&leapmuxv1.GetLayoutResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get layout: %w", err))
	}

	resp := &leapmuxv1.GetLayoutResponse{}

	var stored layoutJSON
	if err := json.Unmarshal([]byte(layout.LayoutJSON), &stored); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse layout: %w", err))
	}

	if len(stored.Layout) > 0 {
		resp.Layout = &leapmuxv1.LayoutNode{}
		if err := protojson.Unmarshal(stored.Layout, resp.Layout); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse layout node: %w", err))
		}
	}

	for _, fwJSON := range stored.FloatingWindows {
		fw := &leapmuxv1.FloatingWindow{}
		if err := protojson.Unmarshal(fwJSON, fw); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse floating window: %w", err))
		}
		resp.FloatingWindows = append(resp.FloatingWindows, fw)
	}

	return connect.NewResponse(resp), nil
}

func (s *WorkspaceService) SaveLayout(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SaveLayoutRequest],
) (*connect.Response[leapmuxv1.SaveLayoutResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}

	marshaler := protojson.MarshalOptions{EmitUnpopulated: false}

	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID); err != nil {
			return err
		}
		return s.saveWorkspaceLayoutEntry(
			ctx,
			tx,
			req.Msg.GetWorkspaceId(),
			req.Msg.GetLayout(),
			req.Msg.GetFloatingWindows(),
			req.Msg.GetTabs(),
			marshaler,
		)
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.SaveLayoutResponse{}), nil
}

func (s *WorkspaceService) SaveMultiLayout(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SaveMultiLayoutRequest],
) (*connect.Response[leapmuxv1.SaveMultiLayoutResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	entries := req.Msg.GetEntries()
	if len(entries) == 0 {
		return connect.NewResponse(&leapmuxv1.SaveMultiLayoutResponse{}), nil
	}

	// Verify ownership for all workspaces before entering the transaction.
	for _, entry := range entries {
		wsID := entry.GetWorkspaceId()
		if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, wsID, user.ID); err != nil {
			switch connect.CodeOf(err) {
			case connect.CodeNotFound:
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %s not found", wsID))
			case connect.CodePermissionDenied:
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("only workspace owner can save layout for workspace %s", wsID))
			default:
				return nil, err
			}
		}
	}

	marshaler := protojson.MarshalOptions{EmitUnpopulated: false}

	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		for _, entry := range entries {
			wsID := entry.GetWorkspaceId()

			if err := s.saveWorkspaceLayoutEntry(
				ctx,
				tx,
				wsID,
				entry.GetLayout(),
				entry.GetFloatingWindows(),
				entry.GetTabs(),
				marshaler,
			); err != nil {
				if connect.CodeOf(err) == connect.CodeInternal {
					return connect.NewError(connect.CodeInternal, fmt.Errorf("save layout for %s: %w", wsID, err))
				}
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.SaveMultiLayoutResponse{}), nil
}
