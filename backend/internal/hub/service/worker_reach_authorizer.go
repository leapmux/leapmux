package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// WorkerReachAuthorizer answers "may this principal reach this worker" and is
// the value workermgr.Manager is constructed with, so the registry cannot exist
// without its gate.
//
// It lives here rather than in workermgr because both halves of the rule need
// the store, which that package must not depend on. It depends on nothing but
// the store itself, which is why it can be built before the registry -- the
// reason the gate is a constructor argument rather than something wired into a
// half-built Manager afterwards.
type WorkerReachAuthorizer struct {
	store store.Store
}

// NewWorkerReachAuthorizer returns the gate for a hub backed by st.
func NewWorkerReachAuthorizer(st store.Store) *WorkerReachAuthorizer {
	return &WorkerReachAuthorizer{store: st}
}

// AuthorizeWorkerReach satisfies workermgr.ReachAuthorizer: it checks that the
// user owns the worker (a worker serves only its registrant -- see
// auth.WorkerCanUse) AND -- for a delegation bearer -- that this worker is one
// the token's minter is entitled to reach (see verifyDelegationWorkerScope).
//
// WorkerCanUse alone is not enough: it answers "may this USER use this worker",
// and a delegation token can carry a user the minting worker does not own. Both
// checks live in this one method so no caller can take one without the other --
// and because workermgr.ConnForUser invokes it before touching the registry
// map, no entrypoint can read the registry with a user-supplied worker id
// without both having run.
func (a *WorkerReachAuthorizer) AuthorizeWorkerReach(ctx context.Context, user *auth.UserInfo, workerID string) error {
	worker, ok, err := auth.WorkerCanUse(ctx, a.store, workerID, user.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if worker == nil || !ok || !auth.WorkerUsableNow(worker) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}
	return a.verifyDelegationWorkerScope(ctx, user, workerID)
}

// verifyDelegationWorkerScope bounds WHICH workers a delegation bearer may reach.
// The rule itself -- and the reasoning behind it -- lives in
// auth.DelegationWorkerScope, because the CRDT validator needs the identical bound
// on the worker ids a SetTabRegisterOp names and cannot reach into this package for
// it. See that type's doc for why the bound exists.
//
// It is called from AuthorizeWorkerReach rather than from each entrypoint: that is
// the one place that answers "may this principal reach this worker", so folding the
// bound into it scopes every worker-directed call -- OpenChannel,
// GetWorkerHandshakeParams, and PrepareWorkspaceAccess -- and any future one, which
// now inherits it by construction rather than by remembering to call it. Bolted onto
// OpenChannel alone it left GetWorkerHandshakeParams -- which asks the identical
// question one line apart -- still willing to hand a cross-tenant bearer the victim
// worker's key bundle, live encryption mode, and online status.
func (a *WorkerReachAuthorizer) verifyDelegationWorkerScope(ctx context.Context, user *auth.UserInfo, targetWorkerID string) error {
	err := auth.CheckDelegationWorkerScope(ctx, a.store, user, targetWorkerID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrDelegationMinterUnknown),
		errors.Is(err, auth.ErrDelegationWorkerOutOfScope):
		// Permanent: the token either cannot be scoped or is out of scope. Both are
		// definitive answers, not faults -- a retryable code would invite a client to
		// hammer a decision that will never change.
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		// A store fault must not become a permanent deny-or-allow: surface it as
		// retryable, matching the ownership arm above.
		return connect.NewError(connect.CodeInternal, err)
	}
}
