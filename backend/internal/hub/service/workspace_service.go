package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// WorkspaceService implements the WorkspaceServiceHandler interface.
// Layout / tab mutations now live on UserCRDT (see crdt_service.go).
// This service owns the workspace metadata table plus read-only tab
// views fed by the rendered rows of the CRDT manager's workspace_tab_owned
// index.
type WorkspaceService struct {
	store           store.Store
	registry        *crdt.Registry
	reconcileNudger workspaceReconcileNudger
}

type workspaceReconcileNudger interface {
	NudgeReconcile(workerID string)
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
	reconcileNudger workspaceReconcileNudger,
) *WorkspaceService {
	return &WorkspaceService{
		store:           st,
		registry:        registry,
		reconcileNudger: reconcileNudger,
	}
}

// workspaceToProto converts a hub DB workspace row to the proto Workspace message.
func workspaceToProto(w *store.Workspace) *leapmuxv1.Workspace {
	return &leapmuxv1.Workspace{
		Id:        w.ID,
		CreatedBy: w.OwnerUserID,
		Title:     w.Title,
		CreatedAt: timestamppb.New(w.CreatedAt),
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
			return nil, connect.Errorf(connect.CodeNotFound, "workspace not found")
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
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
		return nil, connect.NewError(connect.CodePermissionDenied, denyMsg)
	}
	return ws, nil
}

// refuseArchivedWorkspace refuses a mutation of a workspace that sits in the
// caller's Archived section.
//
// Hiding the menu item is not enforcement. RenameWorkspace is reachable from
// the CLI (`leapmux workspace rename`) and from any client, and an archived
// workspace is read-only everywhere else in the app: the tab bar's `+`
// disappears, the branch menu is hidden, and `isWorkspaceMutatable` states the
// rule outright.
//
// FailedPrecondition, not NotFound: the workspace is well-formed and the caller
// owns it, and only its STATE forbids the write. Folding the rule into the
// store as an anti-join -- the way the section queries carry
// `section_type = custom` -- would collapse the outcome into the existing
// `rows == 0` -> NotFound, which already means "not found or not owner".
//
// `verb` gives the message the refused operation, so this guard serves the next
// archived-workspace rule without reporting a rename that never ran. The
// sibling `requireCustomSection` takes the same parameter for the same reason.
//
// It FAILS OPEN for an unminted caller: the adapters answer `(false, nil)` when
// `userid.OwnerFilter` refuses, because binding "" would match every
// blank-owner row rather than none. That is safe only because `MustGetUser` and
// `loadOwnedWorkspaceOr403` already ran and rejected such a caller.
func refuseArchivedWorkspace(ctx context.Context, tx store.Store, workspaceID string, userID userid.UserID, verb string) error {
	archived, err := tx.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("check archived section: %w", err))
	}
	if archived {
		return connect.Errorf(connect.CodeFailedPrecondition, "cannot %s an archived workspace", verb)
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
	req *leapmuxv1.CreateWorkspaceRequest,
) (*leapmuxv1.CreateWorkspaceResponse, error) {
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

	title, err := validate.SanitizeName(req.GetTitle())
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
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
				return "", crdt.LifecyclePayload{}, nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("create workspace: %w", err))
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

	return &leapmuxv1.CreateWorkspaceResponse{
		WorkspaceId: wsID,
	}, nil
}

func (s *WorkspaceService) ListWorkspaces(
	ctx context.Context,
	req *leapmuxv1.ListWorkspacesRequest,
) (*leapmuxv1.ListWorkspacesResponse, error) {
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
		return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("list workspaces: %w", err))
	}
	return &leapmuxv1.ListWorkspacesResponse{
		Workspaces: workspacesToProto(workspaces),
	}, nil
}

func (s *WorkspaceService) GetWorkspace(
	ctx context.Context,
	req *leapmuxv1.GetWorkspaceRequest,
) (*leapmuxv1.GetWorkspaceResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := loadWorkspaceForRead(ctx, s.store, req.GetWorkspaceId(), user)
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.GetWorkspaceResponse{
		Workspace: workspaceToProto(ws),
	}, nil
}

func (s *WorkspaceService) RenameWorkspace(
	ctx context.Context,
	req *leapmuxv1.RenameWorkspaceRequest,
) (*leapmuxv1.RenameWorkspaceResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := rejectDelegationBearer(user, "workspace lifecycle mutation"); err != nil {
		return nil, err
	}
	title, err := validate.SanitizeName(req.GetTitle())
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf("title: %w", err))
	}

	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpRename,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.CrdtOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, req.GetWorkspaceId(), user.ID, "only workspace owner can modify workspace state")
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			// Inside the same transaction as the write, so the guard and the
			// rename commit or abort together -- an archive landing between a
			// check outside and the update would otherwise slip through. It is
			// a read, so it is safe under storetest.NewDoubleRunStore.
			if err := refuseArchivedWorkspace(ctx, tx, req.GetWorkspaceId(), user.ID, "rename"); err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			rows, err := tx.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
				Title:       title,
				ID:          req.GetWorkspaceId(),
				OwnerUserID: user.ID,
			})
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("rename workspace: %w", err))
			}
			if rows == 0 {
				return "", crdt.LifecyclePayload{}, nil, connect.Errorf(connect.CodeNotFound, "workspace not found or not owner")
			}
			return ws.OwnerUserID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpRename,
				WorkspaceID: req.GetWorkspaceId(),
				NewTitle:    title,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
	}

	return &leapmuxv1.RenameWorkspaceResponse{}, nil
}

// MoveWorkspace puts a workspace in a section at a position and nudges every
// Worker that hosts one of its tabs.
//
// The destination section decides the archive state: the Archived section
// archives the workspace, any other workspace section makes it active. One RPC
// for both, so "Move to <custom section>" on an archived workspace is ONE
// transaction -- the workspace never rests in In progress because a second
// call failed, and it lands at the position the user dropped it at.
//
// The nudge IS the delivery. The Worker applies the Hub's authoritative state
// through the same code its reconcile pass uses, so a client-side fan-out over
// the tab list would be a second path to the same effect -- one that exists
// only while a browser session is open, and that a move from any other client
// would skip.
//
// Every move nudges, including one that stays on the same side of the archive
// boundary. That is the recovery path: when a Worker misses its nudge -- a
// refused control frame, a reconnect -- the caller's remedy is to ask again,
// and a repeat that nudged nobody would do nothing, leaving those processes
// running until the Worker's hourly tick. Every step is idempotent: the
// Worker's UPDATE changes a row only when the flag differs, and the nudge is
// best-effort by contract.
func (s *WorkspaceService) MoveWorkspace(
	ctx context.Context,
	req *leapmuxv1.MoveWorkspaceRequest,
) (*leapmuxv1.MoveWorkspaceResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workspaceID := req.GetWorkspaceId()
	var workerIDs []string
	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := loadOwnedWorkspaceOr403(ctx, tx, workspaceID, user.ID, "only workspace owner can move workspace"); err != nil {
			return err
		}
		if req.GetSectionId() == "" {
			return connect.Errorf(connect.CodeInvalidArgument, "section_id is required")
		}
		destination, err := requireOwnedSection(ctx, tx, user.ID, req.GetSectionId())
		if err != nil {
			return err
		}
		if err := requireWorkspaceSection(destination); err != nil {
			return err
		}
		currentlyArchived, err := tx.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
			UserID:      user.ID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return fmt.Errorf("read workspace archive state: %w", err)
		}
		destinationArchived := destination.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
		// Crossing the archive boundary stops or resumes every process of the
		// workspace, which is a lifecycle change a delegation bearer may not
		// make. A move that stays on one side only reorders the sidebar.
		if currentlyArchived != destinationArchived {
			if err := rejectDelegationBearer(user, "workspace archive state change"); err != nil {
				return err
			}
		}
		position := req.GetPosition()
		if position == "" {
			items, err := tx.WorkspaceSectionItems().ListByUser(ctx, user.ID)
			if err != nil {
				return fmt.Errorf("list workspace section items: %w", err)
			}
			position = defaultMovePosition(items, workspaceID, destination.ID)
		}
		if err := tx.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: workspaceID,
			SectionID:   destination.ID,
			Position:    position,
		}); err != nil {
			return fmt.Errorf("move workspace: %w", err)
		}

		tabs, err := tx.WorkspaceTabIndex().ListOwnedTabsByWorkspace(ctx, store.ListOwnedTabsByWorkspaceParams{
			UserID:      user.ID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return fmt.Errorf("list workspace tabs: %w", err)
		}
		workerIDs = distinctWorkerIDs(tabs)
		return nil
	})
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}

	if s.reconcileNudger != nil {
		for _, workerID := range workerIDs {
			s.reconcileNudger.NudgeReconcile(workerID)
		}
	}
	return &leapmuxv1.MoveWorkspaceResponse{}, nil
}

// defaultMovePosition answers where a move with no position puts the workspace.
// A workspace that already sits in the destination keeps its rank, so a repeat
// -- the recovery path for a lost nudge -- does not reorder the sidebar on
// every retry. Any other workspace is appended after the destination's tail.
func defaultMovePosition(items []store.WorkspaceSectionItem, workspaceID, destinationID string) string {
	for _, item := range items {
		if item.WorkspaceID == workspaceID && item.SectionID == destinationID {
			return item.Position
		}
	}
	return appendPositionAfter(lastPositionInSection(items, destinationID), workspaceID)
}

// distinctWorkerIDs reduces a workspace's owned tabs to the workers that host
// them, in first-seen order so the nudges go out deterministically.
func distinctWorkerIDs(tabs []store.OwnedTabRef) []string {
	seen := make(map[string]struct{}, len(tabs))
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		if tab.WorkerID == "" {
			continue
		}
		if _, dup := seen[tab.WorkerID]; dup {
			continue
		}
		seen[tab.WorkerID] = struct{}{}
		out = append(out, tab.WorkerID)
	}
	return out
}

func (s *WorkspaceService) DeleteWorkspace(
	ctx context.Context,
	req *leapmuxv1.DeleteWorkspaceRequest,
) (*leapmuxv1.DeleteWorkspaceResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := rejectDelegationBearer(user, "workspace lifecycle mutation"); err != nil {
		return nil, err
	}
	workspaceID := req.GetWorkspaceId()

	var workerTabs []*leapmuxv1.WorkerTabs
	if err := s.runLifecycleMutation(ctx, lifecycleMutation{
		OpType: crdt.LifecycleOpDelete,
		Fn: func(tx store.Store) (string, crdt.LifecyclePayload, []*leapmuxv1.CrdtOp, error) {
			ws, err := loadOwnedWorkspaceOr403(ctx, tx, workspaceID, user.ID, "only workspace owner can modify workspace state")
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, err
			}
			// The fan-out is scoped to the deleting owner. The composite
			// workspace key already makes every row of this workspace the
			// owner's, and binding the owner keeps the read owner-scoped by
			// its own WHERE clause.
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
				return "", crdt.LifecyclePayload{}, nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("list workspace tabs: %w", err))
			}
			workerTabs = groupTabsByWorker(tabs)
			rows, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
				ID:          workspaceID,
				OwnerUserID: user.ID,
			})
			if err != nil {
				return "", crdt.LifecyclePayload{}, nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("delete workspace: %w", err))
			}
			if rows == 0 {
				return "", crdt.LifecyclePayload{}, nil, connect.Errorf(connect.CodeNotFound, "workspace not found or not owner")
			}
			// Drop the sidebar placement in the SAME transaction. A section item
			// states where a workspace sits, so one for a workspace that no
			// longer exists states nothing, and this is what the ON DELETE
			// CASCADE on workspace_id would do if the delete were a hard one.
			//
			// ListWorkspaceSectionItemsByUser already excludes a soft-deleted
			// workspace, so a row left here changes nothing a reader sees. It
			// still must go: it is dead data that no path ever collects, and a
			// second source of truth for which section holds a workspace.
			if err := tx.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
				UserID:      user.ID,
				WorkspaceID: workspaceID,
			}); err != nil {
				return "", crdt.LifecyclePayload{}, nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("delete workspace section item: %w", err))
			}
			return ws.OwnerUserID, crdt.LifecyclePayload{
				OpType:      crdt.LifecycleOpDelete,
				WorkspaceID: workspaceID,
			}, nil, nil
		},
	}); err != nil {
		return nil, err
	}
	return &leapmuxv1.DeleteWorkspaceResponse{
		WorkerTabs: workerTabs,
	}, nil
}

// appendPositionAfter returns a rank that sorts after last and that no OTHER
// workspace can mint, by suffixing a deterministic tie-break derived from
// workspaceID.
//
// lexorank.After alone is not enough here. It answers last+"n" for everybody,
// and this transaction reads `last` and writes the new rank at READ COMMITTED
// -- so two unarchives that overlap both read the same tail and both store the
// identical rank. Two items in a tie come back in planner-defined order, which
// the user sees as the sidebar reshuffling the whole set on every refresh. The
// suffix makes the collision impossible instead of relying on the caller to
// serialize, which only the one client loop does today and which a second
// browser or a second device defeats.
//
// The tie-break appends to the rank rather than replacing it, so ordering
// against every existing item is unchanged; it decides only the order BETWEEN
// two workspaces that raced into the same slot, and it decides it the same way
// on every read.
func appendPositionAfter(last, workspaceID string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	// FNV-1a over the workspace id, rendered in the rank alphabet. A hash
	// rather than the raw id, because a rank may hold only [a-z].
	var h uint32 = 2166136261
	for i := 0; i < len(workspaceID); i++ {
		h ^= uint32(workspaceID[i])
		h *= 16777619
	}
	var suffix [6]byte
	for i := range suffix {
		suffix[i] = alphabet[h%uint32(len(alphabet))]
		h /= uint32(len(alphabet))
	}
	// The LAST character skips 'a'. lexorank pads a short rank with 'a' before
	// it compares (see padRight), so a rank ending in 'a' ties with the same
	// rank without it -- which is the collision this function exists to remove.
	suffix[len(suffix)-1] = alphabet[1+uint32(suffix[len(suffix)-1]-'a')%uint32(len(alphabet)-1)]
	return lexorank.After(last) + string(suffix[:])
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
		wt.Tabs = append(wt.Tabs, &leapmuxv1.TabIdent{TabType: t.TabType, TabId: t.TabID})
	}
	return out
}
