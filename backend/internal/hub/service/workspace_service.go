package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/validate"
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

	ws, err := s.store.Workspaces().GetByID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify workspace belongs to the requested org.
	if reqOrgID := req.Msg.GetOrgId(); reqOrgID != "" && ws.OrgID != reqOrgID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
	}

	// Check access: must be owner or have explicit access.
	if ws.OwnerUserID != user.ID {
		hasAccess, err := s.store.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: ws.ID,
			UserID:      user.ID,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !hasAccess {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no access to workspace"))
		}
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

	// Get distinct worker_ids from tabs before deleting.
	workerIDs, err := s.store.WorkspaceTabs().ListDistinctWorkersByWorkspace(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace workers: %w", err))
	}

	rows, err := s.store.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
		ID:          req.Msg.GetWorkspaceId(),
		OwnerUserID: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not owner"))
	}

	// Clean up tabs and layout for the deleted workspace.
	if err := s.store.WorkspaceTabs().DeleteByWorkspace(ctx, req.Msg.GetWorkspaceId()); err != nil {
		slog.Error("failed to delete workspace tabs", "workspace_id", req.Msg.GetWorkspaceId(), "error", err)
	}
	if err := s.store.WorkspaceLayouts().Delete(ctx, req.Msg.GetWorkspaceId()); err != nil {
		slog.Error("failed to delete workspace layout", "workspace_id", req.Msg.GetWorkspaceId(), "error", err)
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

	// Verify ownership.
	ws, err := s.store.Workspaces().GetByID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != user.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only workspace owner can update sharing"))
	}

	switch req.Msg.GetShareMode() {
	case leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		if err := s.store.WorkspaceAccess().Clear(ctx, req.Msg.GetWorkspaceId()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
		}

	case leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		if err := s.store.WorkspaceAccess().Clear(ctx, req.Msg.GetWorkspaceId()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
		}
		grantParams := make([]store.GrantWorkspaceAccessParams, len(req.Msg.GetUserIds()))
		for i, uid := range req.Msg.GetUserIds() {
			grantParams[i] = store.GrantWorkspaceAccessParams{
				WorkspaceID: req.Msg.GetWorkspaceId(),
				UserID:      uid,
			}
		}
		if err := s.store.WorkspaceAccess().BulkGrant(ctx, grantParams); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("grant workspace access: %w", err))
		}

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share mode"))
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

	// Verify ownership.
	ws, err := s.store.Workspaces().GetByID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != user.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only workspace owner can list shares"))
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
	if _, err := auth.MustGetUser(ctx); err != nil {
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
	if _, err := auth.MustGetUser(ctx); err != nil {
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

func (s *WorkspaceService) ListTabs(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListTabsRequest],
) (*connect.Response[leapmuxv1.ListTabsResponse], error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	tabs, err := s.store.WorkspaceTabs().ListByWorkspace(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list tabs: %w", err))
	}

	pbTabs := make([]*leapmuxv1.WorkspaceTab, len(tabs))
	for i, t := range tabs {
		pbTabs[i] = &leapmuxv1.WorkspaceTab{
			TabType:  t.TabType,
			TabId:    t.TabID,
			Position: t.Position,
			TileId:   t.TileID,
			WorkerId: t.WorkerID,
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
	if _, err := auth.MustGetUser(ctx); err != nil {
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

	// Verify ownership for write operation.
	ws, err := s.store.Workspaces().GetByID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != user.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only workspace owner can save layout"))
	}

	marshaler := protojson.MarshalOptions{EmitUnpopulated: false}

	stored, err := serializeLayoutJSON(marshaler, req.Msg.GetLayout(), req.Msg.GetFloatingWindows())
	if err != nil {
		return nil, err
	}

	layoutJSONBytes, err := json.Marshal(stored)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("serialize layout JSON: %w", err))
	}

	if err := s.store.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		LayoutJSON:  string(layoutJSONBytes),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save layout: %w", err))
	}

	// Delete existing tabs and re-insert the ones from the request.
	if err := s.store.WorkspaceTabs().DeleteByWorkspace(ctx, req.Msg.GetWorkspaceId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace tabs: %w", err))
	}
	tabParams := make([]store.UpsertWorkspaceTabParams, len(req.Msg.GetTabs()))
	for i, tab := range req.Msg.GetTabs() {
		tabParams[i] = store.UpsertWorkspaceTabParams{
			WorkspaceID: req.Msg.GetWorkspaceId(),
			WorkerID:    tab.GetWorkerId(),
			TabType:     tab.GetTabType(),
			TabID:       tab.GetTabId(),
			Position:    tab.GetPosition(),
			TileID:      tab.GetTileId(),
		}
	}
	if err := s.store.WorkspaceTabs().BulkUpsert(ctx, tabParams); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save tabs: %w", err))
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
		ws, err := s.store.Workspaces().GetByID(ctx, wsID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %s not found", wsID))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if ws.OwnerUserID != user.ID {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("only workspace owner can save layout for workspace %s", wsID))
		}
	}

	marshaler := protojson.MarshalOptions{EmitUnpopulated: false}

	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		for _, entry := range entries {
			wsID := entry.GetWorkspaceId()

			// Serialize layout.
			stored, err := serializeLayoutJSON(marshaler, entry.GetLayout(), entry.GetFloatingWindows())
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("serialize layout for %s: %w", wsID, err))
			}

			layoutJSONBytes, err := json.Marshal(stored)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("serialize layout JSON for %s: %w", wsID, err))
			}

			if err := tx.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
				WorkspaceID: wsID,
				LayoutJSON:  string(layoutJSONBytes),
			}); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("save layout for %s: %w", wsID, err))
			}

			// Delete existing tabs and re-insert.
			if err := tx.WorkspaceTabs().DeleteByWorkspace(ctx, wsID); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("delete tabs for %s: %w", wsID, err))
			}
			tabParams := make([]store.UpsertWorkspaceTabParams, len(entry.GetTabs()))
			for i, tab := range entry.GetTabs() {
				tabParams[i] = store.UpsertWorkspaceTabParams{
					WorkspaceID: wsID,
					WorkerID:    tab.GetWorkerId(),
					TabType:     tab.GetTabType(),
					TabID:       tab.GetTabId(),
					Position:    tab.GetPosition(),
					TileID:      tab.GetTileId(),
				}
			}
			if err := tx.WorkspaceTabs().BulkUpsert(ctx, tabParams); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("save tabs for %s: %w", wsID, err))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.SaveMultiLayoutResponse{}), nil
}
