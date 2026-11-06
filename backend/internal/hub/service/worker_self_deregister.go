package service

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// DeregisterSelf removes exactly the worker its token identifies. The
// empty request is the whole design: the Authorization bearer is the
// only identity, so a caller cannot choose another worker to remove.
//
// Rules the handler keeps:
//
//   - A bundled (auto-registered) worker is refused, online or offline.
//     Those rows belong to the solo launcher's lifecycle, and removing
//     one out from under it is the accident both owner surfaces guard
//     against.
//   - Deletion completes BEFORE the reply, so the CLI removes its local
//     credentials only once the Hub no longer knows the worker.
//   - The deregistration effects run after the commit, exactly as every
//     other removal surface runs them.
//   - A repeated call for the same retained deleted row succeeds: the
//     first call's reply may have been lost, and the end state -- the
//     row deleted -- is what the caller needs to see.
//
// The lookup is deliberately NOT auth.AuthenticateWorkerBearer: that
// shared helper refuses a deleted row on every ordinary credential
// path, and this handler must admit one for its repeat-accept rule. The
// narrower lookup here cannot widen anything else.
func (s *WorkerConnectorService) DeregisterSelf(
	ctx context.Context,
	_ *leapmuxv1.DeregisterSelfRequest,
) (*leapmuxv1.DeregisterSelfResponse, error) {
	info := serverCallInfo(ctx)
	token, ok := auth.BearerToken(info.RequestHeader().Get("Authorization"))
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, auth.ErrMissingBearer.Error())
	}
	worker, err := s.store.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated,
				"the worker token is unknown; deregister this installation only with its own credentials")
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	if worker.AutoRegistered {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"the bundled local worker cannot deregister itself; use its own launcher")
	}

	// The retained deleted row: a repeat of a call whose reply was lost.
	// The deletion is already complete, so the caller may proceed to its
	// local cleanup.
	if worker.DeletedAt != nil {
		return &leapmuxv1.DeregisterSelfResponse{}, nil
	}

	// Complete deletion inside the user's transaction, fencing the same
	// admission races every other removal fences.
	owner, ownerOK := s.ownerOfWorker(worker)
	if !ownerOK {
		return nil, wrappedConnectError(connect.CodeInternal,
			errors.New("the worker row names an unusable owner"))
	}
	if err := s.store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		// Re-read under the owner's lock: a concurrent deregistration or
		// deletion must not be double-counted or outrun.
		fresh, freshErr := tx.Workers().GetByAuthTokenIncludeDeleted(ctx, token)
		if freshErr != nil {
			return freshErr
		}
		if fresh.DeletedAt != nil {
			return nil
		}
		if fresh.AutoRegistered {
			return connect.Errorf(connect.CodeFailedPrecondition,
				"the bundled local worker cannot deregister itself; use its own launcher")
		}
		return tx.Workers().MarkDeleted(ctx, fresh.ID)
	}); err != nil {
		// A transaction callback may run more than once; publish only the
		// final verdict.
		return nil, err
	}

	// After the commit, never inside the transaction -- the same rule
	// every other removal surface follows (see WorkerDeregisterEffects).
	if err := s.deregisterEffects.Apply(ctx, worker.ID, worker.RegisteredBy); err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return &leapmuxv1.DeregisterSelfResponse{}, nil
}

// ownerOfWorker mints the worker row's registrant as the transaction
// owner the self-deletion runs under.
func (s *WorkerConnectorService) ownerOfWorker(worker *store.Worker) (userid.UserID, bool) {
	return userid.New(worker.RegisteredBy)
}
