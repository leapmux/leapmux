package auth

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// WorkspaceCanRead reports whether userID is permitted to read
// workspaceID. The owner short-circuits true; anyone else needs an
// explicit workspace_access row. Missing workspaces fail closed.
//
// This is the canonical implementation of the "owner OR explicit
// grant" pattern that was previously duplicated across channel_service
// (twice), workspace_service.loadWorkspaceForRead, the worker
// delegation handler, and crdtAuthChecker. Centralising it both
// keeps the policy consistent and surfaces ACL changes in one place.
//
// Errors from store calls propagate (caller decides whether to map
// to internal-error / 5xx); the bool is meaningless when err != nil.
// Workspace-not-found returns (false, nil) so callers don't need to
// pattern-match store.ErrNotFound — read access to a missing
// workspace is "no" without an explanation.
func WorkspaceCanRead(ctx context.Context, st store.Store, workspaceID, userID string) (bool, error) {
	if workspaceID == "" || userID == "" {
		return false, nil
	}
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if ws.OwnerUserID == userID {
		return true, nil
	}
	return st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
}

// WorkspaceCanReadInOrg is WorkspaceCanRead with an additional
// org-membership cross-check: the workspace's org must match
// orgID. Used by the CRDT auth path where a stale subscriber on org
// A must never see a workspace that's been re-homed to org B.
// Empty orgID skips the cross-check (matches the legacy behavior in
// crdtAuthChecker.CanReadWorkspace).
func WorkspaceCanReadInOrg(ctx context.Context, st store.Store, orgID, workspaceID, userID string) (bool, error) {
	if workspaceID == "" || userID == "" {
		return false, nil
	}
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if orgID != "" && ws.OrgID != orgID {
		return false, nil
	}
	if ws.OwnerUserID == userID {
		return true, nil
	}
	return st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
}

// isNotFound is a private alias for store.ErrNotFound matching. The
// auth package can't depend on errors-is conventions of other
// packages, but store.ErrNotFound is the only "expected" error from
// the helpers above so a small wrapper keeps the call sites tidy.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}

// WorkerCanUse reports whether userID is the registrant of workerID
// or has an explicit worker_access_grants row. Returns the worker
// record so callers can apply additional filters (Status check for
// the channel-service path, DeletedAt for the CRDT auth path)
// without a re-fetch.
//
// Result triples:
//   - (worker, true, nil)  — access granted; caller may still reject
//     based on the worker's status/deletion state.
//   - (worker, false, nil) — worker exists but caller has no grant.
//   - (nil,    false, nil) — worker missing or one of workerID/userID
//     was empty.
//   - (nil,    false, err) — store error; treat as deny.
func WorkerCanUse(ctx context.Context, st store.Store, workerID, userID string) (*store.Worker, bool, error) {
	if workerID == "" || userID == "" {
		return nil, false, nil
	}
	w, err := st.Workers().GetByID(ctx, workerID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if w.RegisteredBy == userID {
		return w, true, nil
	}
	ok, err := st.WorkerAccessGrants().HasAccess(ctx, store.HasWorkerAccessParams{
		WorkerID: workerID,
		UserID:   userID,
	})
	if err != nil {
		return nil, false, err
	}
	return w, ok, nil
}
