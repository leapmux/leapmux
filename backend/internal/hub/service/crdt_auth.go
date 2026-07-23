package service

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// crdtAuthChecker implements crdt.AuthChecker by consulting the
// workspaces table. Workspace access is owner-only, so read and write
// collapse to the same "is owner" predicate (consistent with
// loadOwnedWorkspaceOr403).
type crdtAuthChecker struct {
	store store.Store
}

// NewCRDTAuthChecker returns the AuthChecker backing OrgCRDT.SubmitOps.
func NewCRDTAuthChecker(st store.Store) crdt.AuthChecker {
	return &crdtAuthChecker{store: st}
}

// CanAccessWorkspace defers to auth.WorkspaceCanAccessInOrg, the canonical
// owner-only predicate. It backs both the SubmitOps write gate and the
// subscribe/broadcast read gate -- access is owner-only, so read and write
// are the same check. The org cross-check guards against a stale subscriber
// on org A reading a workspace that's been re-homed to org B. A
// missing/out-of-org workspace stays a permanent FORBIDDEN deny; a transient
// store error is surfaced so the validator retries the whole submit rather
// than dropping the edit as permanently forbidden.
//
// principalID is the bare user id from SubmitOps (not the "user:"+prefixed
// presence client id). Mint fails closed when the wire id is empty.
func (a *crdtAuthChecker) CanAccessWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	uid, ok := userid.New(principalID)
	if !ok {
		return false, nil
	}
	// An empty orgID denies, exactly as the predicate's old empty-orgID prologue
	// did -- but stated here, because this path must bind a concrete org and
	// auth.BoundOrg is the type that enforces it.
	bound, ok := auth.NewBoundOrg(orgID)
	if !ok {
		return false, nil
	}
	return auth.WorkspaceCanAccessInOrg(ctx, a.store, bound, workspaceID, uid)
}

// CanAccessWorkspaceForUsers is the batch form of CanAccessWorkspace (the
// optional crdt.workspaceReaderBatch capability): it resolves access for many
// users against one workspace in a single load, deferring to the same
// auth.WorkspaceReadableByUsersInOrg rule that backs CanAccessWorkspace.
// Unlike the per-op CanAccessWorkspace, a store error is PROPAGATED (not
// folded to "deny"): the caller (workspace-create subscriber expansion) must
// retry on a transient lookup failure rather than silently drop the new
// workspace's seed broadcast.
//
// That propagation is conditional on the store being REACHED. A blank org id,
// or a subscriber list whose principals are all blank, is answered here without
// a store call, so those return a clean empty set even while the DB is down.
// Both are permanent input problems -- deny is the right answer and a retry
// would not change it -- but the distinction matters when reading this contract.
func (a *crdtAuthChecker) CanAccessWorkspaceForUsers(ctx context.Context, orgID, workspaceID string, userIDs []string) (map[string]bool, error) {
	// An empty orgID denies every subscriber -- the empty map, not nil, so the
	// caller reads it as "nobody may read" rather than a lookup failure. Same
	// answer the predicate's old unbound-binding arm gave.
	bound, ok := auth.NewBoundOrg(orgID)
	if !ok {
		return map[string]bool{}, nil
	}
	uids := make([]userid.UserID, 0, len(userIDs))
	for _, id := range userIDs {
		uid, ok := userid.New(id)
		if !ok {
			continue
		}
		uids = append(uids, uid)
	}
	return auth.WorkspaceReadableByUsersInOrg(ctx, a.store, bound, workspaceID, uids)
}

// CanUseWorker gates SetTabRegisterOp.worker_id writes: only the
// user the worker is registered to may reference it, and only while the
// worker is ACTIVE -- the same bar service.WorkerReachAuthorizer holds
// for opening a channel. Deregistering is the operator's
// containment action against a compromised worker, and SubmitOps is a
// delegation-allowed procedure: without the status check, a bearer that
// could no longer open a channel to a deregistering minter could still
// bind a tab to it (see the target-IS-minter note on
// auth.ResolveDelegationWorkerScope, which relies on the target bar).
// Missing, deleted, and non-ACTIVE workers all fail closed. Empty
// workerID short-circuits true so callers can clear the register
// without an extra round-trip.
func (a *crdtAuthChecker) CanUseWorker(ctx context.Context, _, workerID, principalID string) (bool, error) {
	if workerID == "" {
		return true, nil
	}
	uid, ok := userid.New(principalID)
	if !ok {
		return false, nil
	}
	w, ok, err := auth.WorkerCanUse(ctx, a.store, workerID, uid)
	if err != nil {
		// Transient lookup failure -- surface it so the validator retries rather
		// than rejecting the worker ref as permanently invalid.
		return false, err
	}
	if !ok || w == nil {
		return false, nil
	}
	return auth.WorkerUsableNow(w), nil
}
