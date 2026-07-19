package auth

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// IsOwner reports whether userID owns ws -- the single owner-only rule every
// workspace access check gates on, written once so the four predicates in this
// file AND the service package's workspace read/write loaders (which live in a
// package that cannot name an unexported helper) cannot drift. An empty userID
// never matches: a workspace's OwnerUserID is always a real user id, so this also
// fail-closes the batch path, where an empty id can appear in the caller's input
// list. A nil ws is likewise a deny, not a panic -- this predicate is advertised
// as the one every caller routes through, so a store path that returns
// (nil, nil) or a batch entry that failed to load must fail closed here rather
// than crash the request goroutine on the OwnerUserID deref. Exported so
// service.loadOwnedWorkspaceOr403 routes through it rather than re-inlining
// ws.OwnerUserID == userID -- which would drop these fail-closes and give a
// future access-rule change a second site to silently miss.
//
// The empty-userID fail-close is still a per-predicate guard here and at ~8 other
// identity-consuming sites across the Hub and Worker; consolidating them behind a
// non-empty UserID value type is tracked in
// https://github.com/leapmux/leapmux/issues/288.
func IsOwner(ws *store.Workspace, userID string) bool {
	return ws != nil && userID != "" && ws.OwnerUserID == userID
}

// WorkspaceCanRead reports whether userID is permitted to access
// workspaceID. Workspace access is owner-only: read and write collapse
// to the same "is the workspace's owner" rule. Missing workspaces fail
// closed.
//
// Errors from store calls propagate (caller decides whether to map
// to internal-error / 5xx); the bool is meaningless when err != nil.
// Workspace-not-found returns (false, nil) so callers don't need to
// pattern-match store.ErrNotFound — read access to a missing
// workspace is "no" without an explanation.
func WorkspaceCanRead(ctx context.Context, st store.Store, workspaceID, userID string) (bool, error) {
	if userID == "" || workspaceID == "" {
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
// workspace exists AND belongs to orgID; a missing or out-of-org workspace is
// (nil, false, nil) -- a genuine deny, not an explanation -- while a
// transient store failure is surfaced as (nil, false, err) so the caller can
// retry rather than permanently deny. Empty orgID/workspaceID fails closed.
func loadWorkspaceInOrg(ctx context.Context, st store.Store, orgID, workspaceID string) (*store.Workspace, bool, error) {
	if orgID == "" {
		return nil, false, nil
	}
	ws, ok, err := loadWorkspace(ctx, st, workspaceID)
	if err != nil || !ok {
		return nil, false, err
	}
	if ws.OrgID != orgID {
		return nil, false, nil
	}
	return ws, true, nil
}

// WorkspaceCanAccessInOrg reports whether userID may read or write
// workspaceID within orgID — access is owner-only, so the read and write
// rules are one predicate. The org cross-check exists for the CRDT auth
// path, where a stale subscriber on org A must never see a workspace
// that's been re-homed to org B. Empty orgID fails closed: CRDT callers
// must always bind authorization to the manager's concrete organization.
//
// The missing/out-of-org deny-vs-transient-retry prologue lives in
// loadWorkspaceInOrg: a missing or out-of-org workspace is a plain deny,
// while a transient store failure is surfaced so the caller can retry
// rather than permanently deny.
func WorkspaceCanAccessInOrg(ctx context.Context, st store.Store, orgID, workspaceID, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	ws, ok, err := loadWorkspaceInOrg(ctx, st, orgID, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return IsOwner(ws, userID), nil
}

// WorkspaceReadableByUsersInOrg batches WorkspaceCanAccessInOrg across many
// users: it loads the workspace once, enforces the org binding, then marks
// the owner. Returns the map of userID -> readable (absent means not
// readable). A missing or out-of-org workspace yields the empty set (deny
// all); a store error is surfaced (the map is nil and meaningless when
// err != nil) so the caller can distinguish "nobody may read" from
// "lookup failed".
//
// It is the batch counterpart of WorkspaceCanAccessInOrg for the CRDT
// subscriber-expansion path, which re-checks the SAME workspace for many
// subscribers at once.
func WorkspaceReadableByUsersInOrg(ctx context.Context, st store.Store, orgID, workspaceID string, userIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	ws, ok, err := loadWorkspaceInOrg(ctx, st, orgID, workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	for _, userID := range userIDs {
		if IsOwner(ws, userID) {
			out[userID] = true
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
// orgID binds the workspace's organization: a workspace whose OrgID != orgID
// is excluded. An EMPTY orgID deliberately SKIPS that binding -- delegation
// callers legitimately pass no org because their pinned workspace may live
// outside the user's home org, and access is still gated by the owner check.
// (This differs from the CRDT-path WorkspaceCanAccessInOrg, which fails
// closed on an empty orgID because that path must always bind a concrete
// org; the contracts differ by caller, not by accident.)
//
// The empty-orgID contract riding on a bare "" sentinel is a latent hazard: a
// future bulk-path caller passing "" would silently skip the org binding. Making
// the fail-closed-vs-skip choice an explicit, greppable policy is tracked in
// https://github.com/leapmux/leapmux/issues/286.
//
// The readable subset is returned in the input order; empty and duplicate
// IDs and workspaces missing from the store are dropped. A store error is
// surfaced (the slice is meaningless when err != nil).
func WorkspacesReadableByUser(ctx context.Context, st store.Store, orgID, userID string, workspaceIDs []string) ([]string, error) {
	if userID == "" || len(workspaceIDs) == 0 {
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
		if !ok || (orgID != "" && ws.OrgID != orgID) {
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
