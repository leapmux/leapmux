package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"

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
	// Wake any long-polling layout readers even if the prompt drain below fails
	// so they observe the pending outbox row without waiting for a periodic timer.
	s.registry.SignalLayoutEvents(userID)
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
		return connect.Errorf(connect.CodeInternal, "lifecycle outbox: blank user id")
	}
	encoded, err := crdt.EncodeLifecyclePayload(payload, seedOps)
	if err != nil {
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("encode lifecycle payload: %w", err))
	}
	if err := tx.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
		UserID:  owner,
		OpType:  leapmuxv1.WorkspaceLifecycleOp(opType),
		Payload: encoded,
	}); err != nil {
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("insert lifecycle outbox: %w", err))
	}
	return nil
}

// drainLifecycleOutbox asks the per-user manager to apply committed lifecycle
// rows promptly. A failed drain leaves rows pending for a later lifecycle
// mutation or a tab publication with a missing workspace dependency to retry.
// An inactive user's projection can
// lag until another operation requests that drain.
// Call this after the transaction commits so the manager observes its new row.
func (s *WorkspaceService) drainLifecycleOutbox(ctx context.Context, userID string) {
	if s.registry == nil || userID == "" {
		return
	}
	_ = s.registry.DrainLifecycle(ctx, userID)
}

// buildSeedRootOps returns the two ops a freshly-created workspace
// needs: a SetNodeRegister(root_id, kind=LEAF) plus a
// CreateWorkspaceOp creating the workspace record with that root. The
// SetNodeRegister comes first: validateRootAssignment needs the root node to
// exist in the post-batch state when the create is checked.
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
			Body: &leapmuxv1.CrdtOp_CreateWorkspace{
				CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{
					WorkspaceId: workspaceID,
					RootNodeId:  rootID,
				},
			},
		},
	}
}

// NewLifecycleOutboxReader returns a crdt.LifecycleOutboxReader backed by st.
func NewLifecycleOutboxReader(st store.Store) crdt.LifecycleOutboxReader {
	return lifecycleOutboxAdapter{store: st}
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
			ID:                r.ID,
			UserID:            r.UserID,
			OpType:            crdt.LifecycleOpType(r.OpType),
			Payload:           r.Payload,
			LayoutFactWritten: r.LayoutFactWritten,
		}
	}
	return out, nil
}

func (a lifecycleOutboxAdapter) MarkLifecycleOutboxConsumed(ctx context.Context, rowID int64, consumedAt time.Time) error {
	return a.store.LifecycleOutbox().MarkConsumed(ctx, store.MarkLifecycleOutboxConsumedParams{
		ID:         rowID,
		ConsumedAt: consumedAt,
	})
}

func (a lifecycleOutboxAdapter) ConsumeLifecycleOutboxWithLayoutFact(ctx context.Context, row crdt.LifecycleOutboxRow, consumedAt time.Time, fact *leapmuxv1.LayoutEventFact) error {
	if fact == nil {
		return errors.New("lifecycle outbox: layout event fact is required")
	}
	owner, err := mintTenant(row.UserID)
	if err != nil {
		return err
	}
	payloads, err := marshalLayoutFacts([]*leapmuxv1.LayoutEventFact{fact})
	if err != nil {
		return err
	}
	mark := store.MarkLifecycleOutboxConsumedParams{
		ID:         row.ID,
		ConsumedAt: consumedAt,
	}
	return a.store.RunInTransaction(ctx, func(tx store.Store) error {
		events := tx.LayoutEvents()
		claimed, err := events.ClaimLifecycle(ctx, owner, row.ID)
		if err != nil {
			return err
		}
		if claimed {
			if err := appendLayoutEvents(ctx, events, owner, consumedAt.UnixMilli(), payloads, ""); err != nil {
				return err
			}
		}
		return tx.LifecycleOutbox().MarkConsumed(ctx, mark)
	})
}
