package auth

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// IsOwner reports whether userID owns ws -- the single owner-only rule every
// workspace access check gates on, written once so the predicates in this
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

// WorkspaceCanAccess reports whether userID is permitted to access
// workspaceID. Workspace access is owner-only: read and write collapse
// to the same "is the workspace's owner" rule. Missing workspaces fail
// closed.
//
// Errors from store calls propagate (caller decides whether to map
// to internal-error / 5xx); the bool is meaningless when err != nil.
// Workspace-not-found returns (false, nil) so callers don't need to
// pattern-match store.ErrNotFound — read access to a missing
// workspace is "no" without an explanation.
func WorkspaceCanAccess(ctx context.Context, st store.Store, workspaceID string, userID userid.UserID) (bool, error) {
	if userID.IsZero() || workspaceID == "" {
		return false, nil
	}
	ws, ok, err := loadWorkspace(ctx, st, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return IsOwner(ws, userID), nil
}

// loadWorkspace loads a workspace by id, mapping the not-found-vs-fault
// distinction every workspace access check in this file needs: a missing
// workspace (or an empty id) is a plain deny -- (nil, false, nil) -- while a
// transient store failure is surfaced as (nil, false, err) so the caller can
// retry rather than permanently deny. It applies no access policy; the
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

// WorkspaceReadableByUsers batches WorkspaceCanAccess across many users: it
// loads the workspace once, then marks the owner. Returns the map of
// userID.String() -> readable (absent means not readable). A missing
// workspace yields the empty set (deny all); a store error is surfaced (the
// map is nil and meaningless when err != nil) so the caller can distinguish
// "nobody may read" from "lookup failed".
//
// It is the batch counterpart of WorkspaceCanAccess for the CRDT
// subscriber-expansion path, which re-checks the SAME workspace for many
// subscribers at once. Map keys stay strings so they match the CRDT actor
// wire format; mint each principal with userid.New at the call site.
func WorkspaceReadableByUsers(ctx context.Context, st store.Store, workspaceID string, userIDs []userid.UserID) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	ws, ok, err := loadWorkspace(ctx, st, workspaceID)
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
// of WorkspaceCanAccess (1x1) and WorkspaceReadableByUsers
// (1-workspace x N-users).
//
// The readable subset is returned in the input order; empty and duplicate
// IDs and workspaces missing from the store are dropped. A store error is
// surfaced (the slice is meaningless when err != nil).
func WorkspacesReadableByUser(ctx context.Context, st store.Store, userID userid.UserID, workspaceIDs []string) ([]string, error) {
	if userID.IsZero() || len(workspaceIDs) == 0 {
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
	// Keep a workspace when the user owns it. Iterating dedup keeps the
	// readable subset in input order.
	out := make([]string, 0, len(dedup))
	for _, wsID := range dedup {
		ws, ok := wsByID[wsID]
		if !ok {
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
