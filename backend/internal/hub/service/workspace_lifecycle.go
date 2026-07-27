package service

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// lifecycleMutation describes one transactional lifecycle change.
// `fn` runs inside the workspace_service's transaction and is
// responsible for the workspace-table mutation (create / rename /
// soft-delete) plus assembling the lifecycle payload. The returned
// `userID` drives the post-commit outbox drain.
type lifecycleMutation struct {
	OpType crdt.LifecycleOpType
	Fn     func(tx store.Store) (userID string, payload crdt.LifecyclePayload, seedOps []*leapmuxv1.CrdtOp, err error)
}

// runLifecycleMutation collapses the "run-in-tx → emit outbox row →
// drain on commit" scaffolding that every workspace lifecycle handler
// (CreateWorkspace, RenameWorkspace, DeleteWorkspace) repeats. The
// caller supplies the per-handler mutation; this helper owns the tx,
// the outbox insert, and the post-commit manager drain.
func (s *WorkspaceService) runLifecycleMutation(ctx context.Context, m lifecycleMutation) error {
	var userID string
	err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		gotUserID, payload, seedOps, err := m.Fn(tx)
		if err != nil {
			return err
		}
		userID = gotUserID
		return emitLifecycleOutbox(ctx, tx, gotUserID, m.OpType, payload, seedOps)
	})
	if err != nil {
		return err
	}
	s.drainLifecycleOutbox(ctx, userID)
	return nil
}

// emitLifecycleOutbox encodes `payload` (with optional seed ops) and
// inserts the outbox row inside `tx`. Caller is responsible for
// running this inside the same transaction that wrote the workspace
// mutation so the outbox + mutation commit atomically. The returned
// connect.Error carries CodeInternal — propagate to the caller.
func emitLifecycleOutbox(
	ctx context.Context,
	tx store.Store,
	userID string,
	opType crdt.LifecycleOpType,
	payload crdt.LifecyclePayload,
	seedOps []*leapmuxv1.CrdtOp,
) error {
	// The outbox row's owner is minted here, at the store boundary: a blank one
	// would land a row that ListPending (which refuses to bind a blank owner)
	// can never return, so the lifecycle event would be lost rather than
	// delayed. Every caller reaches this with an authenticated user's id.
	owner, ok := userid.New(userID)
	if !ok {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("lifecycle outbox: blank user id"))
	}
	encoded, err := crdt.EncodeLifecyclePayload(payload, seedOps)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("encode lifecycle payload: %w", err))
	}
	if err := tx.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
		UserID:  owner,
		OpType:  string(opType),
		Payload: encoded,
	}); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("insert lifecycle outbox: %w", err))
	}
	return nil
}

// drainLifecycleOutbox kicks the per-user CRDT manager so the freshly
// inserted outbox row turns into a `WorkspaceCreated` / `Renamed` /
// `Deleted` broadcast quickly. Failure is non-fatal: the row stays pending
// (ListPending returns it again), so the NEXT lifecycle mutation for this user
// re-drains it -- note nothing periodic re-drains it, so on a low-traffic user
// a transiently-failed create's live projection can lag until the next such
// mutation (see the deep-review steering note on adding a periodic drain).
// Pre-commit callers MUST run this AFTER the transaction commits so the
// manager observes the new row.
func (s *WorkspaceService) drainLifecycleOutbox(ctx context.Context, userID string) {
	if s.registry == nil || userID == "" {
		return
	}
	mgr, err := s.registry.Get(ctx, userID)
	if err != nil {
		return
	}
	_ = mgr.SubmitLifecycle(ctx, lifecycleOutboxAdapter{store: s.store})
}

// buildSeedRootOps returns the two ops a freshly-created workspace
// needs: a SetNodeRegister(root_id, kind=LEAF) plus a
// SetWorkspaceRootNodeOp registering the root.
func buildSeedRootOps(workspaceID, rootID string) []*leapmuxv1.CrdtOp {
	return []*leapmuxv1.CrdtOp{
		{
			OpId: id.Generate(),
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{
				SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
					NodeId: rootID,
					Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
				},
			},
		},
		{
			OpId: id.Generate(),
			Body: &leapmuxv1.CrdtOp_SetWorkspaceRootNode{
				SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
					WorkspaceId: workspaceID,
					RootNodeId:  rootID,
				},
			},
		},
	}
}

// lifecycleOutboxAdapter bridges store.LifecycleOutboxStore to the
// crdt.LifecycleOutboxReader interface the manager expects.
type lifecycleOutboxAdapter struct {
	store store.Store
}

func (a lifecycleOutboxAdapter) ListPendingLifecycleOutbox(ctx context.Context, userID string) ([]crdt.LifecycleOutboxRow, error) {
	// One page only: MarkConsumed hasn't run during the assembly pass
	// (the manager consumes each row in its goroutine after the slice
	// returns), so re-listing would yield the same rows. The CRDT
	// pager limit caps the assembled batch — a backlog larger than
	// the page returns the first page now and resumes on the manager's
	// next SubmitLifecycle tick.
	owner, ok := userid.New(userID)
	if !ok {
		// An unminted caller owns nothing; the store would refuse the bind for
		// the same reason (userid.OwnerFilter), so this returns the identical
		// empty page one call earlier.
		return nil, nil
	}
	rows, err := a.store.LifecycleOutbox().ListPending(ctx, store.ListPendingLifecycleOutboxParams{
		UserID: owner,
		Limit:  store.CRDTBatchPageLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]crdt.LifecycleOutboxRow, len(rows))
	for i, r := range rows {
		out[i] = crdt.LifecycleOutboxRow{
			ID:      r.ID,
			UserID:  r.UserID,
			OpType:  crdt.LifecycleOpType(r.OpType),
			Payload: r.Payload,
		}
	}
	return out, nil
}

func (a lifecycleOutboxAdapter) MarkLifecycleOutboxConsumed(ctx context.Context, id int64, consumedAt time.Time) error {
	return a.store.LifecycleOutbox().MarkConsumed(ctx, store.MarkLifecycleOutboxConsumedParams{
		ID:         id,
		ConsumedAt: consumedAt,
	})
}
