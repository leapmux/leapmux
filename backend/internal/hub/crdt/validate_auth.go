package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// AuthChecker is the per-workspace permission predicate the validator
// consults for each op. Returns true if `principalID` may access
// `workspaceID`. An empty workspaceID is DENIED by every
// implementation (the production checker's workspace load fails closed and the
// workspace-scoped checker rejects a non-matching id); it is never a bypass.
// Internal batches skip authorization by not running the auth pass at all
// (validate.go gates it behind `if !internal`), not by passing an empty
// workspaceID.
//
// The predicates do not take a userID: tenancy is resolved per call from the
// stored entity against `principalID`. The production checker loads the
// workspace row and answers `IsOwner(ws, principalID)`, so the result is a pure
// function of (workspaceID, principalID) and the row -- it does not depend on
// which manager asked. (The old `userID` parameter was threaded through every
// call site as the manager's own id, so it could only ever equal `m.owner.String()` --
// a tautology that hid mismatches instead of catching them.)
//
// Note the checker is NOT bound to a tenant at construction: hub/server.go
// builds ONE crdtAuthChecker and shares it across every per-user Manager. An
// implementation must therefore not cache a decision under a construction-time
// tenant, or short-circuit on one -- there isn't one, and the shared instance
// would make the mistake global rather than per-user.
//
// `CanAccessWorkspace` backs both the op-write gate (authCheck's
// requireWrite) and the broadcast-expansion read gate
// (ExpandSubscribersForWorkspace) -- workspace access is owner-only, so read
// and write are the same predicate. For the broadcast path, when a new
// workspace appears (CreateWorkspace lifecycle), the manager has to decide
// which of the already-subscribed users should learn about it; returning
// true expands that subscriber's filter to include the workspace.
//
// `CanUseWorker` gates SetTabRegisterOp.worker_id writes: the
// referenced worker must be registered to the principal. Empty
// workerID returns true so callers can clear the field without a
// permission check.
//
// Each predicate returns (allowed, error): a nil error with allowed=false is a
// genuine DENY, while a non-nil error is a LOOKUP FAILURE (a transient store
// error) that the validator surfaces as a retryable error instead of a permanent
// FORBIDDEN op-rejection -- so a brief DB hiccup does not silently drop a user's
// edit. Implementations must map a legitimately-missing workspace/worker to
// (false, nil) and reserve the error for transient failures.
type AuthChecker interface {
	CanAccessWorkspace(ctx context.Context, workspaceID, principalID string) (bool, error)
	CanUseWorker(ctx context.Context, workerID, principalID string) (bool, error)
}

// workspaceReaderBatch is an OPTIONAL AuthChecker capability: resolve access
// for many users against ONE workspace in a single pass (one workspace load)
// instead of a CanAccessWorkspace round-trip per user. The production
// checker implements it; ExpandSubscribersForWorkspace uses it when present and
// falls back to per-user CanAccessWorkspace otherwise, so test fakes need not
// implement it. The returned map holds userID -> accessible (absent means denied).
//
// Unlike the per-op CanAccessWorkspace (which folds a store error into "deny"), the
// batch form surfaces the error: its sole caller, workspace-create subscriber
// expansion, must distinguish "denied" from "lookup failed" so a transient DB
// blip retries the create instead of silently dropping the new workspace's seed.
type workspaceReaderBatch interface {
	CanAccessWorkspaceForUsers(ctx context.Context, workspaceID string, userIDs []string) (map[string]bool, error)
}

// workerScopedAuthChecker narrows an AuthChecker to the one scope a delegation
// bearer's credential still carries: the set of workers its minting worker is
// entitled to reach. It is an upper bound applied BEFORE the inner checker, so
// it can only ever subtract access, never add it.
//
// The workspace bound that used to sit beside it is gone. A Worker serves one
// user and stores no workspace id, so pinning a bearer to one workspace
// narrowed nothing an agent could not already reach through its own tab; what
// it must not reach is another MACHINE of the same user, which is what this
// answers.
type workerScopedAuthChecker struct {
	inner AuthChecker
	// workerScope bounds which worker ids the ops may reference. Never nil --
	// scopedAuthChecker returns inner untouched when there is nothing to bound.
	// See SubmitInput.WorkerScope for why this is a predicate and not an id.
	workerScope func(workerID string) bool
}

// scopedAuthChecker wraps inner with the worker bound when the credential
// carries one, returning inner untouched when it does not -- the session /
// API-credential case.
func scopedAuthChecker(inner AuthChecker, workerScope func(string) bool) AuthChecker {
	if workerScope == nil {
		return inner
	}
	return workerScopedAuthChecker{inner: inner, workerScope: workerScope}
}

func (a workerScopedAuthChecker) CanAccessWorkspace(ctx context.Context, workspaceID, principalID string) (bool, error) {
	return a.inner.CanAccessWorkspace(ctx, workspaceID, principalID)
}

// CanUseWorker applies the credential's worker bound before the inner "may this
// USER use this worker" check. Both must pass: the inner one answers whether the
// principal has any claim to the worker at all, and the scope answers whether THIS
// bearer -- which may be carrying an identity its minting worker was merely lent --
// is entitled to reach it.
func (a workerScopedAuthChecker) CanUseWorker(ctx context.Context, workerID, principalID string) (bool, error) {
	if !a.workerScope(workerID) {
		return false, nil
	}
	return a.inner.CanUseWorker(ctx, workerID, principalID)
}

// CanAccessWorkspaceForUsers forwards the OPTIONAL workspaceReaderBatch
// capability, so wrapping a batch-capable checker can never silently drop a
// caller off the batched fast path onto N per-user loads. The worker bound
// says nothing about workspace readability, so this is a pure forward.
func (a workerScopedAuthChecker) CanAccessWorkspaceForUsers(ctx context.Context, workspaceID string, userIDs []string) (map[string]bool, error) {
	return accessWorkspaceForUsers(ctx, a.inner, workspaceID, userIDs)
}

// accessWorkspaceForUsers resolves the batch capability against inner: the
// genuinely batched load when inner provides it, else one CanAccessWorkspace
// per user -- the same fallback ExpandSubscribersForWorkspace runs for a
// checker without the capability. A per-user lookup error is PROPAGATED (not
// folded to deny), matching the batch contract: the caller must distinguish
// "denied" from "lookup failed".
func accessWorkspaceForUsers(ctx context.Context, inner AuthChecker, workspaceID string, userIDs []string) (map[string]bool, error) {
	if batch, ok := inner.(workspaceReaderBatch); ok {
		return batch.CanAccessWorkspaceForUsers(ctx, workspaceID, userIDs)
	}
	result := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		allowed, err := inner.CanAccessWorkspace(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if allowed {
			result[id] = true
		}
	}
	return result, nil
}

// memoAuthChecker caches the (targetID, principalID) → bool
// lookups for the lifetime of one ValidateBatch call. Backing
// implementations hit the workspace / worker tables; a batch that
// touches the same workspace or worker N times then collapses to a
// single fetch.
type memoAuthChecker struct {
	inner    AuthChecker
	accessWS map[[2]string]bool
	useW     map[[2]string]bool
}

// memoize returns the cached result for key, or runs fetch and caches it. A
// lookup that ERRORS is never cached (the error is transient, so a later op in
// the same batch should retry the lookup rather than inherit the failure) and is
// propagated to the caller.
func memoize(cache *map[[2]string]bool, key [2]string, fetch func() (bool, error)) (bool, error) {
	if v, ok := (*cache)[key]; ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		return false, err
	}
	if *cache == nil {
		*cache = map[[2]string]bool{}
	}
	(*cache)[key] = v
	return v, nil
}

func (m *memoAuthChecker) CanAccessWorkspace(ctx context.Context, workspaceID, principalID string) (bool, error) {
	return memoize(&m.accessWS, [2]string{workspaceID, principalID}, func() (bool, error) {
		return m.inner.CanAccessWorkspace(ctx, workspaceID, principalID)
	})
}

func (m *memoAuthChecker) CanUseWorker(ctx context.Context, workerID, principalID string) (bool, error) {
	return memoize(&m.useW, [2]string{workerID, principalID}, func() (bool, error) {
		return m.inner.CanUseWorker(ctx, workerID, principalID)
	})
}

// CanAccessWorkspaceForUsers forwards the OPTIONAL workspaceReaderBatch
// capability so memo-wrapping a batch-capable checker can never silently drop
// a caller off the batched fast path. Batch results are deliberately not
// folded into the per-op memo: the batch form's error contract differs
// (propagate, never fold to deny), and its caller runs outside the
// single-ValidateBatch lifetime this memo exists for.
func (m *memoAuthChecker) CanAccessWorkspaceForUsers(ctx context.Context, workspaceID string, userIDs []string) (map[string]bool, error) {
	return accessWorkspaceForUsers(ctx, m.inner, workspaceID, userIDs)
}

// authCheck applies the per-op auth rule with create/delete/move exceptions.
// Returns (reason, offendingOpID, error): a non-nil error is a transient
// permission-lookup failure the validator surfaces as retryable rather than a
// permanent FORBIDDEN rejection.
func authCheck(ctx context.Context, op *leapmuxv1.CrdtOp, preWS, postWS, principalID string, auth AuthChecker) (leapmuxv1.BatchRejectionReason, string, error) {
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
		allowed, lookupErr := auth.CanAccessWorkspace(ctx, ws, principalID)
		if lookupErr != nil {
			return false, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", lookupErr
		}
		if !allowed {
			return false, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FORBIDDEN_WORKSPACE, op.GetOpId(), nil
		}
		return true, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", nil
	}

	switch op.GetBody().(type) {
	case *leapmuxv1.CrdtOp_TombstoneNode, *leapmuxv1.CrdtOp_TombstoneTab, *leapmuxv1.CrdtOp_TombstoneFloatingWindow:
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
