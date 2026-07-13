package service

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// crdtAuthChecker implements crdt.AuthChecker by consulting the
// workspaces / workspace_access tables. Owner can always write;
// shared members are read-only (consistent with
// loadWorkspaceForOwnerWrite). The shared-write extension lives
// behind a separate ACL check that's not yet implemented; for now
// we collapse "write access" to "is owner".
type crdtAuthChecker struct {
	store store.Store
}

// NewCRDTAuthChecker returns the AuthChecker backing OrgCRDT.SubmitOps.
func NewCRDTAuthChecker(st store.Store) crdt.AuthChecker {
	return &crdtAuthChecker{store: st}
}

// CanWriteWorkspace defers to auth.WorkspaceCanWriteInOrg, the canonical
// owner-only write predicate that shares the missing/out-of-org deny-vs-retry
// prologue with the read path (CanReadWorkspace / WorkspaceCanReadInOrg). Owner
// can always write; shared members are read-only (the shared-write ACL is not
// yet implemented). A missing/out-of-org workspace stays a permanent FORBIDDEN
// deny; a transient store error is surfaced so the validator retries the whole
// submit rather than dropping the edit as permanently forbidden.
func (a *crdtAuthChecker) CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	return auth.WorkspaceCanWriteInOrg(ctx, a.store, orgID, workspaceID, principalID)
}

// CanReadWorkspace defers to auth.WorkspaceCanReadInOrg, the canonical "owner OR
// explicit grant" predicate shared with the channel/workspace/worker-delegation
// read paths. The org cross-check guards against a stale subscriber on org A
// reading a workspace that's been re-homed to org B. WorkspaceCanReadInOrg
// already maps a missing/out-of-org workspace to (false, nil) and reserves a
// non-nil error for a transient store failure, which the validator surfaces as
// retryable rather than a permanent deny.
func (a *crdtAuthChecker) CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	return auth.WorkspaceCanReadInOrg(ctx, a.store, orgID, workspaceID, principalID)
}

// CanReadWorkspaceForUsers is the batch form of CanReadWorkspace (the optional
// crdt.workspaceReaderBatch capability): it resolves read access for many users
// against one workspace in a single load + grant lookup, deferring to the same
// auth.WorkspaceReadableByUsersInOrg rule that backs CanReadWorkspace. Unlike the
// per-op CanReadWorkspace, a store error is PROPAGATED (not folded to "deny"): the
// caller (workspace-create subscriber expansion) must retry on a transient lookup
// failure rather than silently drop the new workspace's seed broadcast.
func (a *crdtAuthChecker) CanReadWorkspaceForUsers(ctx context.Context, orgID, workspaceID string, userIDs []string) (map[string]bool, error) {
	return auth.WorkspaceReadableByUsersInOrg(ctx, a.store, orgID, workspaceID, userIDs)
}

// CanUseWorker gates SetTabRegisterOp.worker_id writes: the
// registrant always has access; everyone else needs an explicit
// worker_access_grants row. Missing/deleted workers fail closed.
// Empty workerID short-circuits true so callers can clear the
// register without an extra round-trip.
func (a *crdtAuthChecker) CanUseWorker(ctx context.Context, _, workerID, principalID string) (bool, error) {
	if workerID == "" {
		return true, nil
	}
	w, ok, err := auth.WorkerCanUse(ctx, a.store, workerID, principalID)
	if err != nil {
		// Transient lookup failure -- surface it so the validator retries rather
		// than rejecting the worker ref as permanently invalid.
		return false, err
	}
	if !ok || w == nil {
		return false, nil
	}
	return w.DeletedAt == nil, nil
}
