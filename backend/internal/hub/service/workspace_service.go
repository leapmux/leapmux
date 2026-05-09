package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/validate"
)

// WorkspaceService implements the WorkspaceServiceHandler interface.
// Layout / tab mutations now live on OrgCRDT (see crdt_service.go).
// This service owns the workspace metadata table plus read-only tab
// views fed by the CRDT manager's workspace_tab_rendered index.
type WorkspaceService struct {
	store    store.Store
	soloMode bool
	registry *crdt.Registry
}

// NewWorkspaceService creates a new WorkspaceService. registry is
// optional; when set, workspace lifecycle (create/rename/delete) drives
// the CRDT outbox.
func NewWorkspaceService(st store.Store, soloMode bool, registry *crdt.Registry) *WorkspaceService {
	return &WorkspaceService{store: st, soloMode: soloMode, registry: registry}
}

// workspaceToProto converts a hub DB workspace row to the proto Workspace message.
func workspaceToProto(w *store.Workspace) *leapmuxv1.Workspace {
	return &leapmuxv1.Workspace{
		Id:        w.ID,
		OrgId:     w.OrgID,
		CreatedBy: w.OwnerUserID,
		Title:     w.Title,
		CreatedAt: w.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

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
	// Non-owner: defer to the canonical "owner OR explicit grant" check
	// in the auth package. GetByID was already done above, so we know
	// the row exists; auth.WorkspaceCanRead will re-fetch it but the
	// cost is one extra query on the cold path. The benefit is a single
	// source of truth for workspace-read policy across services.
	hasAccess, err := auth.WorkspaceCanRead(ctx, st, ws.ID, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !hasAccess {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no access to workspace"))
	}
	return ws, nil
}

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

func (s *WorkspaceService) validateWorkspaceShareUsers(ctx context.Context, st store.Store, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	rows, err := st.Users().ListByIDs(ctx, userIDs)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	found := make(map[string]struct{}, len(rows))
	for _, u := range rows {
		found[u.ID] = struct{}{}
	}
	for _, userID := range userIDs {
		if _, ok := found[userID]; !ok {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user %s not found", userID))
		}
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
	rootID := id.Generate()

	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpCreate,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.OrgOp, error) {
			if err := tx.Workspaces().Create(ctx, store.CreateWorkspaceParams{
				ID:          wsID,
				OrgID:       req.Msg.GetOrgId(),
				OwnerUserID: user.ID,
				Title:       title,
			}); err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
			}
			return req.Msg.GetOrgId(), crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpCreate,
				WorkspaceID: wsID,
				Title:       title,
				RootNodeID:  rootID,
			}, buildSeedRootOps(wsID, rootID, user.ID), nil
		},
	}); err != nil {
		return nil, err
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
	// Delegation bearers are pinned to a single workspace at mint
	// time (`auth.UserInfo.DelegationWorkspaceID`). Mirror
	// ChannelService.OpenChannel's "narrow accessible-workspace
	// reasoning to this single id" rule on the read side so a leaked
	// delegation token cannot enumerate the user's full grant set.
	// loadWorkspaceForRead returns NotFound for soft-deleted rows and
	// PermissionDenied for revoked access — both collapse to an
	// empty list here.
	if user.DelegationWorkspaceID != "" {
		ws, err := s.loadWorkspaceForRead(ctx, s.store, user.DelegationWorkspaceID, user.ID)
		if err != nil {
			code := connect.CodeOf(err)
			if code == connect.CodeNotFound || code == connect.CodePermissionDenied {
				return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{}), nil
			}
			return nil, err
		}
		return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{
			Workspaces: []*leapmuxv1.Workspace{workspaceToProto(ws)},
		}), nil
	}
	// The underlying SQL filter matches `w.org_id = sqlc.arg(org_id)`
	// literally, so an empty arg never hits a row. Fall back to the
	// authenticated user's home org when the caller doesn't specify
	// one — this is what `leapmux remote workspace list` (no
	// --org-id) wants, and what the web frontend already passes
	// explicitly.
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		orgID = user.OrgID
	}
	workspaces, err := s.store.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
		UserID: user.ID,
		OrgID:  orgID,
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

	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpRename,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.OrgOp, error) {
			ws, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID)
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			rows, err := tx.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
				Title:       title,
				ID:          req.Msg.GetWorkspaceId(),
				OwnerUserID: user.ID,
			})
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("rename workspace: %w", err))
			}
			if rows == 0 {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not owner"))
			}
			return ws.OrgID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpRename,
				WorkspaceID: req.Msg.GetWorkspaceId(),
				NewTitle:    title,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
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
	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpDelete,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.OrgOp, error) {
			ws, err := s.loadWorkspaceForOwnerWrite(ctx, tx, req.Msg.GetWorkspaceId(), user.ID)
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			workerIDs, err = tx.WorkspaceTabIndex().ListDistinctWorkersByWorkspace(ctx, req.Msg.GetWorkspaceId())
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace workers: %w", err))
			}
			rows, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
				ID:          req.Msg.GetWorkspaceId(),
				OwnerUserID: user.ID,
			})
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete workspace: %w", err))
			}
			if rows == 0 {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found or not owner"))
			}
			return ws.OrgID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpDelete,
				WorkspaceID: req.Msg.GetWorkspaceId(),
				WorkerIDs:   workerIDs,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.DeleteWorkspaceResponse{
		WorkerIds: workerIDs,
	}), nil
}
