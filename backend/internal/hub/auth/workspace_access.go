package auth

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// IsOwner reports whether userID owns ws -- the single owner-only rule every
// workspace access check gates on, written once so the four predicates in this
// file AND the service package's workspace read/write loaders (which live in a
// package that cannot name an unexported helper) cannot drift. Comparison goes
// through userid.UserID.Matches, which fails closed when either side is empty
// -- a workspace's OwnerUserID is always a real user id, and a zero UserID
// never matches. A nil ws is likewise a deny, not a panic -- this predicate is
// advertised as the one every caller routes through, so a store path that
// returns (nil, nil) or a batch entry that failed to load must fail closed
// here rather than crash the request goroutine on the OwnerUserID deref.
// Exported so service.loadOwnedWorkspaceOr403 routes through it rather than
// re-inlining ws.OwnerUserID == userID -- which would drop these fail-closes
// and give a future access-rule change a second site to silently miss.
func IsOwner(ws *store.Workspace, userID userid.UserID) bool {
	return ws != nil && userID.Matches(ws.OwnerUserID)
}

// WorkspaceCanRead reports whether userID is permitted to access
// workspaceID. Workspace access is owner-only: read and write collapse
// to the same "is the workspace's owner" rule. Missing workspaces fail
// closed.
//
// binding names the organization policy, and every caller states it: the two
// org-agnostic callers pass AnyOrg() rather than expressing "no org rule" by
// the ABSENCE of a parameter. That absence was the surviving half of the
// problem OrgBinding was introduced to fix -- the "" sentinel is gone, but a
// caller could still skip the org check by picking this function over
// WorkspaceCanAccessInOrg, invisibly to the `rg AnyOrg` audit both this
// package's and workspace_tabs.go's docs advertise as complete. Now it is.
//
// Errors from store calls propagate (caller decides whether to map
// to internal-error / 5xx); the bool is meaningless when err != nil.
// Workspace-not-found returns (false, nil) so callers don't need to
// pattern-match store.ErrNotFound — read access to a missing
// workspace is "no" without an explanation.
func WorkspaceCanRead(ctx context.Context, st store.Store, binding OrgBinding, workspaceID string, userID userid.UserID) (bool, error) {
	if userID.IsZero() || workspaceID == "" {
		return false, nil
	}
	ws, ok, err := loadWorkspaceBound(ctx, st, binding, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return IsOwner(ws, userID), nil
}

// loadWorkspace loads a workspace by id, mapping the not-found-vs-fault
// distinction every workspace access check in this file needs: a missing
// workspace (or an empty id) is a plain deny -- (nil, false, nil) -- while a
// transient store failure is surfaced as (nil, false, err) so the caller can
// retry rather than permanently deny. It applies no access or org policy; the
// caller layers that on the returned record.
func loadWorkspace(ctx context.Context, st store.Store, workspaceID string) (*store.Workspace, bool, error) {
	if workspaceID == "" {
		return nil, false, nil
	}
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return ws, true, nil
}

// loadWorkspaceInOrg loads the (non-deleted) workspace and enforces the org
// binding shared by the CRDT auth checks. The bool is true only when the
// workspace exists AND belongs to bound's org; a missing or out-of-org
// workspace is (nil, false, nil) -- a genuine deny, not an explanation --
// while a transient store failure is surfaced as (nil, false, err) so the
// caller can retry rather than permanently deny.
//
// It takes BoundOrg, not OrgBinding: this path must always bind a concrete
// org, and requiring the narrower type makes AnyOrg() a compile error rather
// than a silent deny-everything.
func loadWorkspaceInOrg(ctx context.Context, st store.Store, bound BoundOrg, workspaceID string) (*store.Workspace, bool, error) {
	return loadWorkspaceBound(ctx, st, bound.Binding(), workspaceID)
}

// loadWorkspaceBound is the shared body: load the workspace, then apply the org
// binding. It takes the general OrgBinding so WorkspaceCanRead (whose callers
// legitimately pass AnyOrg) and the CRDT path can share one implementation --
// loadWorkspaceInOrg is the BoundOrg-typed door onto it, which is what keeps
// AnyOrg a compile error for CRDT callers rather than a silent total deny.
func loadWorkspaceBound(ctx context.Context, st store.Store, binding OrgBinding, workspaceID string) (*store.Workspace, bool, error) {
	// A deny-all binding (the zero BoundOrg, from a caller that ignored
	// NewBoundOrg's ok) admits nothing, so deny BEFORE the store read rather
	// than after: the read can only be wasted work, and its transient failure
	// would turn a permanent, error-free deny into a retryable error the
	// caller cannot distinguish from a real lookup problem.
	if binding.DeniesAll() {
		return nil, false, nil
	}
	ws, ok, err := loadWorkspace(ctx, st, workspaceID)
	if err != nil || !ok {
		return nil, false, err
	}
	if !binding.permits(ws) {
		return nil, false, nil
	}
	return ws, true, nil
}

// WorkspaceCanAccessInOrg reports whether userID may read or write
// workspaceID within bound's organization — access is owner-only, so the read
// and write rules are one predicate. The org cross-check exists for the CRDT
// auth path, where a stale subscriber on org A must never see a workspace
// that's been re-homed to org B. Taking BoundOrg is what enforces "CRDT
// callers must always bind a concrete organization": AnyOrg cannot be
// converted to one, so the mistake does not compile.
//
// The body is WorkspaceCanRead's: this predicate differs from it ONLY in
// requiring the narrower binding type. Delegating rather than repeating the
// three steps keeps exactly one implementation of the owner-only read rule, so
// a future change to it cannot land in one predicate and miss the other -- while
// the BoundOrg parameter still makes AnyOrg() a compile error here.
func WorkspaceCanAccessInOrg(ctx context.Context, st store.Store, bound BoundOrg, workspaceID string, userID userid.UserID) (bool, error) {
	return WorkspaceCanRead(ctx, st, bound.Binding(), workspaceID, userID)
}

// WorkspaceReadableByUsersInOrg batches WorkspaceCanAccessInOrg across many
// users: it loads the workspace once, enforces the org binding, then marks
// the owner. Returns the map of userID.String() -> readable (absent means not
// readable). A missing or out-of-org workspace yields the empty set (deny
// all); a store error is surfaced (the map is nil and meaningless when
// err != nil) so the caller can distinguish "nobody may read" from
// "lookup failed".
//
// It is the batch counterpart of WorkspaceCanAccessInOrg for the CRDT
// subscriber-expansion path, which re-checks the SAME workspace for many
// subscribers at once. Map keys stay strings so they match the CRDT actor
// wire format; mint each principal with userid.New at the call site.
func WorkspaceReadableByUsersInOrg(ctx context.Context, st store.Store, bound BoundOrg, workspaceID string, userIDs []userid.UserID) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	ws, ok, err := loadWorkspaceInOrg(ctx, st, bound, workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	for _, userID := range userIDs {
		if IsOwner(ws, userID) {
			out[userID.String()] = true
		}
	}
	return out, nil
}

// WorkspacesReadableByUser filters workspaceIDs down to those userID may
// read — i.e. owns — for MANY workspaces against ONE user in a single
// ListByIDs round-trip. It is the many-workspaces/single-user counterpart
// of WorkspaceCanRead (1x1) and WorkspaceReadableByUsersInOrg
// (1-workspace x N-users).
//
// binding names the organization policy: BindOrg excludes workspaces whose
// OrgID differs; AnyOrg deliberately skips that binding -- delegation
// callers legitimately unbound because their pinned workspace may live
// outside the user's home org, and access is still gated by the owner check.
// (This differs from the CRDT-path WorkspaceCanAccessInOrg, which requires
// BindOrg because that path must always bind a concrete org; the contracts
// differ by caller, expressed as an explicit OrgBinding rather than a ""
// sentinel.)
//
// The readable subset is returned in the input order; empty and duplicate
// IDs and workspaces missing from the store are dropped. A store error is
// surfaced (the slice is meaningless when err != nil).
func WorkspacesReadableByUser(ctx context.Context, st store.Store, binding OrgBinding, userID userid.UserID, workspaceIDs []string) ([]string, error) {
	// A deny-all binding permits nothing, so the bulk ListByIDs below would be
	// pure waste -- every row it returned would be rejected by permits. Short-
	// circuiting also matches loadWorkspaceInOrg, which never reaches the store
	// on a binding that cannot admit anything.
	if userID.IsZero() || len(workspaceIDs) == 0 || binding.DeniesAll() {
		return nil, nil
	}
	// Dedup + drop empties so the bulk lookup stays tight.
	dedup := make([]string, 0, len(workspaceIDs))
	seen := make(map[string]struct{}, len(workspaceIDs))
	for _, wsID := range workspaceIDs {
		if wsID == "" {
			continue
		}
		if _, dup := seen[wsID]; dup {
			continue
		}
		seen[wsID] = struct{}{}
		dedup = append(dedup, wsID)
	}
	if len(dedup) == 0 {
		return nil, nil
	}
	rows, err := st.Workspaces().ListByIDs(ctx, dedup)
	if err != nil {
		return nil, err
	}
	wsByID := make(map[string]*store.Workspace, len(rows))
	for i := range rows {
		wsByID[rows[i].ID] = &rows[i]
	}
	// Keep a workspace when the user owns it (respecting the org binding
	// above). Iterating dedup keeps the readable subset in input order.
	out := make([]string, 0, len(dedup))
	for _, wsID := range dedup {
		ws, ok := wsByID[wsID]
		if !ok || !binding.permits(ws) {
			continue
		}
		if IsOwner(ws, userID) {
			out = append(out, wsID)
		}
	}
	return out, nil
}

// isNotFound is a private alias for store.ErrNotFound matching. The
// auth package can't depend on errors-is conventions of other
// packages, but store.ErrNotFound is the only "expected" error from
// the helpers above so a small wrapper keeps the call sites tidy.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
