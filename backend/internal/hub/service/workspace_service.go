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
	store         store.Store
	registry      *crdt.Registry
	channelCloser WorkspaceChannelCloser
}

// WorkspaceChannelCloser removes channels whose worker-side workspace
// snapshot became stale after the workspace was deleted.
type WorkspaceChannelCloser interface {
	CloseChannelsByUsersForWorkspace(workspaceID string, userIDs []string) int
}

// NewWorkspaceService creates a new WorkspaceService. registry is optional;
// when set, workspace lifecycle drives the CRDT outbox. channelCloser is
// required because workspace deletion must invalidate worker-side snapshots.
func NewWorkspaceService(
	st store.Store,
	registry *crdt.Registry,
	channelCloser WorkspaceChannelCloser,
) *WorkspaceService {
	if isNilDependency(channelCloser) {
		panic("workspace service requires a workspace channel closer")
	}
	return &WorkspaceService{
		store:         st,
		registry:      registry,
		channelCloser: channelCloser,
	}
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

// workspacesToProto maps a store workspace slice to its proto slice.
func workspacesToProto(workspaces []store.Workspace) []*leapmuxv1.Workspace {
	pb := make([]*leapmuxv1.Workspace, len(workspaces))
	for i := range workspaces {
		pb[i] = workspaceToProto(&workspaces[i])
	}
	return pb
}

// loadWorkspaceOr404 fetches a workspace, mapping a missing or soft-deleted row
// to NotFound and any other store error to Internal. Callers apply their own
// authorization gate (read ACL or owner check) on the returned row, so the
// not-found-vs-internal mapping has a single source of truth here.
func loadWorkspaceOr404(ctx context.Context, st store.Store, workspaceID string) (*store.Workspace, error) {
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return ws, nil
}

// loadOwnedWorkspaceOr403 loads a workspace and enforces the owner-only access
// rule: a missing or soft-deleted row is NotFound (loadWorkspaceOr404), a
// non-owner is PermissionDenied carrying denyMsg. Access is owner-only, so a
// read handler and a write handler run the SAME check -- the only thing that
// differs is the denial message, which the caller supplies. Routing both
// through one core keeps "read access == write access" structural so the two
// cannot drift on what a non-owner sees. It does NOT check the delegation
// workspace scope -- callers apply that guard first, with the error code their
// operation requires (loadWorkspaceForRead uses NotFound so a scoped bearer
// cannot probe existence; PrepareWorkspaceAccess uses PermissionDenied for an
// explicit prepare-access request).
func loadOwnedWorkspaceOr403(ctx context.Context, st store.Store, workspaceID, userID, denyMsg string) (*store.Workspace, error) {
	ws, err := loadWorkspaceOr404(ctx, st, workspaceID)
	if err != nil {
		return nil, err
	}
	if !auth.IsOwner(ws, userID) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(denyMsg))
	}
	return ws, nil
}

// loadWorkspaceForRead is the single loader every workspace-read handler goes
// through, so it centrally enforces the delegation workspace scope: a scoped
// bearer may only reach its own delegated workspace, and a mismatch fails closed
// with NotFound (not PermissionDenied) so it cannot probe which other workspaces
// exist. Enforcing the scope here -- rather than as a per-handler guard -- means
// a new read handler cannot forget the check and leak cross-scope access.
func loadWorkspaceForRead(ctx context.Context, st store.Store, workspaceID string, user *auth.UserInfo) (*store.Workspace, error) {
	if err := requireDelegationWorkspaceOrNotFound(user, workspaceID, "workspace not found"); err != nil {
		return nil, err
	}
	return loadOwnedWorkspaceOr403(ctx, st, workspaceID, user.ID, "no access to workspace")
}

func (s *WorkspaceService) CreateWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CreateWorkspaceRequest],
) (*connect.Response[leapmuxv1.CreateWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := rejectDelegationBearer(user, "workspace lifecycle mutation"); err != nil {
		return nil, err
	}

	// Home the workspace only in the caller's own (personal) org. Without this
	// the caller-supplied org_id would let a user create and own a workspace
	// in an arbitrary org's namespace (polluting its CRDT log / lifecycle
	// outbox). ResolveOrgID fails closed with NotFound for any foreign org and
	// falls back to the user's personal org when org_id is empty.
	orgID, err := auth.ResolveOrgID(user, req.Msg.GetOrgId())
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
				OrgID:       orgID,
				OwnerUserID: user.ID,
				Title:       title,
			}); err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
			}
			return orgID, crdt.LifecyclePayload{
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
	// time (`auth.UserInfo.Credential.WorkspaceScopeID()`). Mirror
	// ChannelService.OpenChannel's "narrow accessible-workspace
	// reasoning to this single id" rule on the read side so a leaked
	// delegation token cannot enumerate the user's full grant set.
	// loadWorkspaceForRead returns NotFound for soft-deleted rows and
	// PermissionDenied for revoked access — both collapse to an
	// empty list here.
	if user.Credential.IsDelegation() {
		ws, err := loadWorkspaceForRead(ctx, s.store, user.Credential.WorkspaceScopeID(), user)
		if err != nil {
			code := connect.CodeOf(err)
			if code == connect.CodeNotFound || code == connect.CodePermissionDenied {
				return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{}), nil
			}
			return nil, err
		}
		if reqOrgID := req.Msg.GetOrgId(); reqOrgID != "" && ws.OrgID != reqOrgID {
			return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{}), nil
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
	return connect.NewResponse(&leapmuxv1.ListWorkspacesResponse{
		Workspaces: workspacesToProto(workspaces),
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
	ws, err := loadWorkspaceForRead(ctx, s.store, req.Msg.GetWorkspaceId(), user)
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
	if err := rejectDelegationBearer(user, "workspace lifecycle mutation"); err != nil {
		return nil, err
	}
	title, err := validate.SanitizeName(req.Msg.GetTitle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
	}

	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpRename,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.OrgOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, req.Msg.GetWorkspaceId(), user.ID, "only workspace owner can modify workspace state")
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
	if err := rejectDelegationBearer(user, "workspace lifecycle mutation"); err != nil {
		return nil, err
	}
	workspaceID := req.Msg.GetWorkspaceId()

	var workerIDs []string
	var affectedUserIDs []string
	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpDelete,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.OrgOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, workspaceID, user.ID, "only workspace owner can modify workspace state")
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			workerIDs, err = tx.WorkspaceTabIndex().ListDistinctWorkersByWorkspace(ctx, workspaceID)
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace workers: %w", err))
			}
			// The owner is the only user whose open channels announced this
			// workspace; their worker-side snapshots must be invalidated.
			affectedUserIDs = []string{ws.OwnerUserID}
			rows, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
				ID:          workspaceID,
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
				WorkspaceID: workspaceID,
				WorkerIDs:   workerIDs,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
	}
	s.channelCloser.CloseChannelsByUsersForWorkspace(workspaceID, affectedUserIDs)

	return connect.NewResponse(&leapmuxv1.DeleteWorkspaceResponse{
		WorkerIds: workerIDs,
	}), nil
}
