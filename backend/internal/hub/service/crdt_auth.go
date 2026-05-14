package service

import (
	"context"
	"errors"

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

func (a *crdtAuthChecker) CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool {
	if workspaceID == "" {
		return false
	}
	ws, err := a.store.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false
		}
		return false
	}
	if orgID != "" && ws.OrgID != orgID {
		return false
	}
	return ws.OwnerUserID == principalID
}

// CanReadWorkspace defers to auth.WorkspaceCanReadInOrg, the
// canonical "owner OR explicit grant" predicate shared with the
// channel/workspace/worker-delegation read paths. The org cross-
// check guards against a stale subscriber on org A reading a
// workspace that's been re-homed to org B. Errors are mapped to
// "deny" — the CRDT validator path treats a transient DB hiccup
// the same as a missing grant.
func (a *crdtAuthChecker) CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool {
	ok, err := auth.WorkspaceCanReadInOrg(ctx, a.store, orgID, workspaceID, principalID)
	if err != nil {
		return false
	}
	return ok
}

// CanUseWorker gates SetTabRegisterOp.worker_id writes: the
// registrant always has access; everyone else needs an explicit
// worker_access_grants row. Missing/deleted workers fail closed.
// Empty workerID short-circuits true so callers can clear the
// register without an extra round-trip.
func (a *crdtAuthChecker) CanUseWorker(ctx context.Context, _, workerID, principalID string) bool {
	if workerID == "" {
		return true
	}
	w, ok, err := auth.WorkerCanUse(ctx, a.store, workerID, principalID)
	if err != nil || !ok || w == nil {
		return false
	}
	return w.DeletedAt == nil
}
