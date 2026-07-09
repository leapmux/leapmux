package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// AuthChecker is the per-workspace permission predicate the validator
// consults for each op. Returns true if `principalID` may write to
// `workspaceID` in `orgID`. An empty workspaceID is DENIED by every
// implementation (the crdt checker's loadWorkspaceInOrg fails closed and the
// workspace-scoped checker rejects a non-matching id); it is never a bypass.
// Internal batches skip authorization by not running the auth pass at all
// (validate.go gates it behind `if !internal`), not by passing an empty
// workspaceID.
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
//
// Each predicate returns (allowed, error): a nil error with allowed=false is a
// genuine DENY, while a non-nil error is a LOOKUP FAILURE (a transient store
// error) that the validator surfaces as a retryable error instead of a permanent
// FORBIDDEN op-rejection -- so a brief DB hiccup does not silently drop a user's
// edit. Implementations must map a legitimately-missing workspace/worker to
// (false, nil) and reserve the error for transient failures.
type AuthChecker interface {
	CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error)
	CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error)
	CanUseWorker(ctx context.Context, orgID, workerID, principalID string) (bool, error)
}

// workspaceReaderBatch is an OPTIONAL AuthChecker capability: resolve read access
// for many users against ONE workspace in a single pass (one workspace load + one
// grant lookup) instead of a CanReadWorkspace round-trip per user. The production
// checker implements it; ExpandSubscribersForWorkspace uses it when present and
// falls back to per-user CanReadWorkspace otherwise, so test fakes need not
// implement it. The returned map holds userID -> readable (absent means denied).
//
// Unlike the per-op CanReadWorkspace (which folds a store error into "deny"), the
// batch form surfaces the error: its sole caller, workspace-create subscriber
// expansion, must distinguish "denied" from "lookup failed" so a transient DB
// blip retries the create instead of silently dropping the new workspace's seed.
type workspaceReaderBatch interface {
	CanReadWorkspaceForUsers(ctx context.Context, orgID, workspaceID string, userIDs []string) (map[string]bool, error)
}

type workspaceScopedAuthChecker struct {
	inner       AuthChecker
	workspaceID string
}

func scopedAuthChecker(inner AuthChecker, workspaceID string) AuthChecker {
	if workspaceID == "" {
		return inner
	}
	return workspaceScopedAuthChecker{inner: inner, workspaceID: workspaceID}
}

func (a workspaceScopedAuthChecker) CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	if workspaceID != a.workspaceID {
		return false, nil
	}
	return a.inner.CanWriteWorkspace(ctx, orgID, workspaceID, principalID)
}

func (a workspaceScopedAuthChecker) CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	if workspaceID != a.workspaceID {
		return false, nil
	}
	return a.inner.CanReadWorkspace(ctx, orgID, workspaceID, principalID)
}

func (a workspaceScopedAuthChecker) CanUseWorker(ctx context.Context, orgID, workerID, principalID string) (bool, error) {
	return a.inner.CanUseWorker(ctx, orgID, workerID, principalID)
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

// memoize returns the cached result for key, or runs fetch and caches it. A
// lookup that ERRORS is never cached (the error is transient, so a later op in
// the same batch should retry the lookup rather than inherit the failure) and is
// propagated to the caller.
func memoize(cache *map[[3]string]bool, key [3]string, fetch func() (bool, error)) (bool, error) {
	if v, ok := (*cache)[key]; ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		return false, err
	}
	if *cache == nil {
		*cache = map[[3]string]bool{}
	}
	(*cache)[key] = v
	return v, nil
}

func (m *memoAuthChecker) CanWriteWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	return memoize(&m.writeWS, [3]string{orgID, workspaceID, principalID}, func() (bool, error) {
		return m.inner.CanWriteWorkspace(ctx, orgID, workspaceID, principalID)
	})
}

func (m *memoAuthChecker) CanReadWorkspace(ctx context.Context, orgID, workspaceID, principalID string) (bool, error) {
	return memoize(&m.readWS, [3]string{orgID, workspaceID, principalID}, func() (bool, error) {
		return m.inner.CanReadWorkspace(ctx, orgID, workspaceID, principalID)
	})
}

func (m *memoAuthChecker) CanUseWorker(ctx context.Context, orgID, workerID, principalID string) (bool, error) {
	return memoize(&m.useW, [3]string{orgID, workerID, principalID}, func() (bool, error) {
		return m.inner.CanUseWorker(ctx, orgID, workerID, principalID)
	})
}

// authCheck applies the per-op auth rule with create/delete/move exceptions.
// Returns (reason, offendingOpID, error): a non-nil error is a transient
// permission-lookup failure the validator surfaces as retryable rather than a
// permanent FORBIDDEN rejection.
func authCheck(ctx context.Context, op *leapmuxv1.OrgOp, preWS, postWS, principalID, orgID string, auth AuthChecker) (leapmuxv1.BatchRejectionReason, string, error) {
	unknown := func() (leapmuxv1.BatchRejectionReason, string, error) {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNKNOWN_WORKSPACE, op.GetOpId(), nil
	}
	ok := func() (leapmuxv1.BatchRejectionReason, string, error) {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", nil
	}
	// requireWrite gates on write access to ws, distinguishing a genuine deny
	// (FORBIDDEN) from a transient lookup failure (propagated error). granted
	// reports whether ws was writable so the move case can require both sides.
	requireWrite := func(ws string) (granted bool, reason leapmuxv1.BatchRejectionReason, opID string, err error) {
		allowed, lookupErr := auth.CanWriteWorkspace(ctx, orgID, ws, principalID)
		if lookupErr != nil {
			return false, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", lookupErr
		}
		if !allowed {
			return false, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId(), nil
		}
		return true, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", nil
	}

	switch op.GetBody().(type) {
	case *leapmuxv1.OrgOp_TombstoneNode, *leapmuxv1.OrgOp_TombstoneTab, *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		// Pure delete: require write access to pre-workspace only.
		if preWS == "" {
			return unknown()
		}
		if granted, reason, opID, err := requireWrite(preWS); !granted {
			return reason, opID, err
		}
		return ok()
	}
	if preWS == "" && postWS == "" {
		return unknown()
	}
	if preWS == "" {
		// Pure create.
		if granted, reason, opID, err := requireWrite(postWS); !granted {
			return reason, opID, err
		}
		return ok()
	}
	if postWS == "" {
		// Effectively pure delete (entity disappeared from
		// projections); fall back to pre-workspace permission.
		if granted, reason, opID, err := requireWrite(preWS); !granted {
			return reason, opID, err
		}
		return ok()
	}
	// Move OR in-place edit. Require write to both.
	if granted, reason, opID, err := requireWrite(preWS); !granted {
		return reason, opID, err
	}
	if granted, reason, opID, err := requireWrite(postWS); !granted {
		return reason, opID, err
	}
	return ok()
}
