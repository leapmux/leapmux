package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// AuthChecker is the per-workspace permission predicate the validator
// consults for each op. Returns true if `principalID` may write to
// `workspaceID` in `orgID`. workspaceID == "" means "any" (the
// auth-bypass code path used by SubmitInternal).
//
// `CanReadWorkspace` mirrors `CanWriteWorkspace` for the broadcast
// path — when a new workspace appears (CreateWorkspace lifecycle), the
// manager has to decide which of the already-subscribed users should
// learn about it. Returning true expands that subscriber's filter to
// include the workspace.
//
// `CanUseWorker` gates SetTabRegisterOp.worker_id writes: the
// principal must have access to the referenced worker (either it's
// their own, or they hold a worker_access_grant). Empty workerID
// returns true so callers can clear the field without a permission
// check.
type AuthChecker interface {
	CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool
	CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool
	CanUseWorker(ctx context.Context, orgID, workerID, principalID string) bool
}

// memoAuthChecker caches the (orgID, targetID, principalID) → bool
// lookups for the lifetime of one ValidateBatch call. Backing
// implementations hit the workspace / worker_access_grant tables; a
// batch that touches the same workspace or worker N times then collapses
// to a single fetch.
type memoAuthChecker struct {
	inner   AuthChecker
	writeWS map[[3]string]bool
	readWS  map[[3]string]bool
	useW    map[[3]string]bool
}

func memoize(cache *map[[3]string]bool, key [3]string, fetch func() bool) bool {
	if v, ok := (*cache)[key]; ok {
		return v
	}
	if *cache == nil {
		*cache = map[[3]string]bool{}
	}
	v := fetch()
	(*cache)[key] = v
	return v
}

func (m *memoAuthChecker) CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool {
	return memoize(&m.writeWS, [3]string{orgID, workspaceID, principalID}, func() bool {
		return m.inner.CanWriteWorkspace(ctx, orgID, workspaceID, principalID)
	})
}

func (m *memoAuthChecker) CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) bool {
	return memoize(&m.readWS, [3]string{orgID, workspaceID, principalID}, func() bool {
		return m.inner.CanReadWorkspace(ctx, orgID, workspaceID, principalID)
	})
}

func (m *memoAuthChecker) CanUseWorker(ctx context.Context, orgID, workerID, principalID string) bool {
	return memoize(&m.useW, [3]string{orgID, workerID, principalID}, func() bool {
		return m.inner.CanUseWorker(ctx, orgID, workerID, principalID)
	})
}

// authCheck applies the per-op auth rule with create/delete/move
// exceptions.
func authCheck(ctx context.Context, op *leapmuxv1.OrgOp, preWS, postWS, principalID, orgID string, auth AuthChecker) (leapmuxv1.BatchRejectionReason, string) {
	switch op.GetBody().(type) {
	case *leapmuxv1.OrgOp_TombstoneNode, *leapmuxv1.OrgOp_TombstoneTab, *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		// Pure delete: require write access to pre-workspace only.
		if preWS == "" {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNKNOWN_WORKSPACE, op.GetOpId()
		}
		if !auth.CanWriteWorkspace(ctx, orgID, preWS, principalID) {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId()
		}
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
	}
	if preWS == "" && postWS == "" {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNKNOWN_WORKSPACE, op.GetOpId()
	}
	if preWS == "" {
		// Pure create.
		if !auth.CanWriteWorkspace(ctx, orgID, postWS, principalID) {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId()
		}
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
	}
	if postWS == "" {
		// Effectively pure delete (entity disappeared from
		// projections); fall back to pre-workspace permission.
		if !auth.CanWriteWorkspace(ctx, orgID, preWS, principalID) {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId()
		}
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
	}
	// Move OR in-place edit. Require write to both.
	if !auth.CanWriteWorkspace(ctx, orgID, preWS, principalID) || !auth.CanWriteWorkspace(ctx, orgID, postWS, principalID) {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId()
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}
