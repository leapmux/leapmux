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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// WorkspaceService implements the WorkspaceServiceHandler interface.
// Layout / tab mutations now live on UserCRDT (see crdt_service.go).
// This service owns the workspace metadata table plus read-only tab
// views fed by the CRDT manager's workspace_tab_rendered index.
type WorkspaceService struct {
	store    store.Store
	registry *crdt.Registry
}

// NewWorkspaceService creates a new WorkspaceService. registry is optional;
// when set, workspace lifecycle drives the CRDT outbox.
//
// There is no channel-closer dependency any more. Deleting a workspace used to
// have to tear down every E2EE channel that carried the user's
// accessible-workspace snapshot, because the snapshot was seeded at open time
// and could not shrink. A channel now carries no workspace set at all, so a
// deleted workspace leaves nothing stale on it -- the worker's own tabs are
// reaped by CleanupWorkspace and, as a backstop, the orphan reconciler.
func NewWorkspaceService(
	st store.Store,
	registry *crdt.Registry,
) *WorkspaceService {
	return &WorkspaceService{
		store:    st,
		registry: registry,
	}
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
// cannot drift on what a non-owner sees. There is no second, delegation-scope
// guard in front of it any more: a delegation bearer authenticates as its owner
// and carries that owner's reach across every workspace they own, so ownership
// IS the whole check.
func loadOwnedWorkspaceOr403(ctx context.Context, st store.Store, workspaceID string, userID userid.UserID, denyMsg string) (*store.Workspace, error) {
	ws, err := loadWorkspaceOr404(ctx, st, workspaceID)
	if err != nil {
		return nil, err
	}
	if !auth.IsOwner(ws, userID) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(denyMsg))
	}
	return ws, nil
}

// refuseArchivedWorkspace refuses a mutation of a workspace that sits in the
// caller's Archived section.
//
// Hiding the menu item is not enforcement. RenameWorkspace is reachable from
// the CLI (`leapmux workspace rename`) and from any client, and an archived
// workspace is read-only everywhere else in the app: the tab bar's `+`
// disappears, the branch menu is hidden, and `isWorkspaceMutatable` names the
// rule outright.
//
// FailedPrecondition, not NotFound: the workspace is well-formed and the caller
// owns it, and only its STATE forbids the write. Folding the rule into the
// store as an anti-join -- the way the section queries carry
// `section_type = custom` -- would collapse the outcome into the existing
// `rows == 0` -> NotFound, which already means "not found or not owner".
//
// It FAILS OPEN for an unminted caller: the adapters answer `(false, nil)` when
// `userid.OwnerFilter` refuses, because binding "" would match every
// blank-owner row rather than none. That is safe only because `MustGetUser` and
// `loadOwnedWorkspaceOr403` already ran and rejected such a caller.
func refuseArchivedWorkspace(ctx context.Context, tx store.Store, workspaceID string, userID userid.UserID) error {
	archived, err := tx.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("check archived section: %w", err))
	}
	if archived {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot rename an archived workspace"))
	}
	return nil
}

// readWorkspaceOrNotFound loads a workspace for reading and collapses the two
// existence-revealing codes into notFound.
//
// PermissionDenied and NotFound both become notFound so a non-owner cannot tell "exists but not
// yours" from "does not exist". Anything else passes through unchanged: a
// transient Internal must stay retryable rather than becoming the permanent
// answer a NotFound is.
//
// One function rather than the three hand-copied ladders this replaced, so the
// tab and tile lookups cannot drift into leaking existence through a code one
// of them forgot to fold.
func readWorkspaceOrNotFound(ctx context.Context, st store.Store, workspaceID string, user *auth.UserInfo, notFound error) (*store.Workspace, error) {
	ws, err := loadWorkspaceForRead(ctx, st, workspaceID, user)
	if err != nil {
		if code := connect.CodeOf(err); code == connect.CodePermissionDenied || code == connect.CodeNotFound {
			return nil, notFound
		}
		return nil, err
	}
	return ws, nil
}

// loadWorkspaceForRead is the single loader every workspace-read handler goes
// through, so ownership is enforced in one place rather than as a per-handler
// guard a new read handler could forget.
//
// It no longer enforces a delegation WORKSPACE scope, because there is no such
// scope any more: a delegation bearer authenticates as its user and reaches
// exactly what that user owns. What still bounds a bearer is which MACHINE it
// may reach.
func loadWorkspaceForRead(ctx context.Context, st store.Store, workspaceID string, user *auth.UserInfo) (*store.Workspace, error) {
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

	// Home the workspace under the caller's own user tenancy. The CRDT
	// manager / lifecycle outbox are keyed by user.ID, so every create
	// lands in the caller's UserCRDT namespace.
	userID := user.ID.String()

	title, err := validate.SanitizeName(req.Msg.GetTitle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
	}

	wsID := id.Generate()
	rootID := id.Generate()

	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpCreate,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.CrdtOp, error) {
			if err := tx.Workspaces().Create(ctx, store.CreateWorkspaceParams{
				ID:          wsID,
				OwnerUserID: user.ID,
				Title:       title,
			}); err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
			}
			return userID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpCreate,
				WorkspaceID: wsID,
				Title:       title,
				RootNodeID:  rootID,
			}, buildSeedRootOps(wsID, rootID), nil
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
	// Owner-only listing: every workspace the authenticated user owns.
	// A delegation bearer sees the same list -- it authenticates AS the owner,
	// and the axis that still bounds it is which WORKER it may reach, not which
	// workspace it may read.
	workspaces, err := s.store.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
		UserID: user.ID,
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
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.CrdtOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, req.Msg.GetWorkspaceId(), user.ID, "only workspace owner can modify workspace state")
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			// Inside the same transaction as the write, so the guard and the
			// rename commit or abort together -- an archive landing between a
			// check outside and the update would otherwise slip through. It is
			// a read, so it is safe under storetest.NewDoubleRunStore.
			if err := refuseArchivedWorkspace(ctx, tx, req.Msg.GetWorkspaceId(), user.ID); err != nil {
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
			return ws.OwnerUserID, crdt.LifecyclePayload{
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

	var (
		workerIDs  []string
		workerTabs []*leapmuxv1.WorkerTabs
	)
	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpDelete,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.CrdtOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, workspaceID, user.ID, "only workspace owner can modify workspace state")
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			// The fan-out is scoped to the deleting owner. workspace_tab_owned
			// is keyed by (user_id, tab_id), and workspace_id is a plain FK, so
			// a row another user wrote against this workspace_id would
			// otherwise contribute its worker here.
			//
			// Read INSIDE this transaction, and read as ROWS rather than as a
			// DISTINCT worker projection: the caller has to name the tabs it
			// wants torn down (the Worker stores no workspace id), and taking
			// both facts from one atomic read of the authoritative table is what
			// stops a tab opened mid-delete from being missed.
			//
			// user.ID rather than ws.OwnerUserID: loadOwnedWorkspaceOr403 above
			// already refused a non-owner (auth.IsOwner compares exactly these
			// two), so they hold the same id and user.ID is the typed one --
			// the same value SoftDelete binds a few lines below.
			tabs, err := tx.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
				UserID:      user.ID,
				WorkspaceID: workspaceID,
			})
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace tabs: %w", err))
			}
			workerTabs = groupTabsByWorker(tabs)
			workerIDs = make([]string, 0, len(workerTabs))
			for _, wt := range workerTabs {
				workerIDs = append(workerIDs, wt.GetWorkerId())
			}
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
			return ws.OwnerUserID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpDelete,
				WorkspaceID: workspaceID,
				WorkerIDs:   workerIDs,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.DeleteWorkspaceResponse{
		WorkerTabs: workerTabs,
	}), nil
}

// groupTabsByWorker turns the flat (worker_id, tab_type, tab_id) read into the
// per-worker fan-out shape the response carries. Rows arrive ordered by
// worker_id, so grouping is a single pass and the output order is stable --
// which keeps the response, and the tests asserting on it, deterministic.
//
// Rows with an empty worker_id are dropped: a tab no machine hosts has nothing
// to tear down, and binding it to a blank worker would send one caller's
// cleanup to nobody.
func groupTabsByWorker(tabs []store.OwnedTabRef) []*leapmuxv1.WorkerTabs {
	var out []*leapmuxv1.WorkerTabs
	byWorker := make(map[string]*leapmuxv1.WorkerTabs, len(tabs))
	for _, t := range tabs {
		if t.WorkerID == "" {
			continue
		}
		wt, ok := byWorker[t.WorkerID]
		if !ok {
			wt = &leapmuxv1.WorkerTabs{WorkerId: t.WorkerID}
			byWorker[t.WorkerID] = wt
			out = append(out, wt)
		}
		wt.Tabs = append(wt.Tabs, &leapmuxv1.TabRef{TabType: t.TabType, TabId: t.TabID})
	}
	return out
}
