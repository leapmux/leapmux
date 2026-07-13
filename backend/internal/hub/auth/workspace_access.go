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
	return LoadedWorkspaceCanRead(ctx, st, ws, userID)
}

// LoadedWorkspaceCanRead applies the canonical owner-or-grant policy to an
// already-loaded workspace. Callers that need the row for existence checks or
// response data can avoid a second primary-key lookup without duplicating the
// authorization rule.
//
// Read access is owner OR explicit workspace_access grant; org membership is
// deliberately NOT required, so a workspace can be shared with a user who is
// not a member of the owning organization (cross-org collaboration).
func LoadedWorkspaceCanRead(ctx context.Context, st store.Store, ws *store.Workspace, userID string) (bool, error) {
	if ws == nil || ws.ID == "" || userID == "" {
		return false, nil
	}
	if ws.OwnerUserID == userID {
		return true, nil
	}
	return st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
		WorkspaceID: ws.ID,
		UserID:      userID,
	})
}

// loadWorkspaceInOrg loads the (non-deleted) workspace and enforces the org
// binding shared by the CRDT read and write auth checks. The bool is true only
// when the workspace exists AND belongs to orgID; a missing or out-of-org
// workspace is (nil, false, nil) -- a genuine deny, not an explanation -- while a
// transient store failure is surfaced as (nil, false, err) so the caller can
// retry rather than permanently deny. Empty orgID/workspaceID fails closed.
//
// Centralizing this deny-vs-retry prologue keeps the missing/out-of-org policy
// (and its security-relevant "transient error is retryable, not a permanent
// FORBIDDEN" contract) from being maintained in two places.
func loadWorkspaceInOrg(ctx context.Context, st store.Store, orgID, workspaceID string) (*store.Workspace, bool, error) {
	if orgID == "" || workspaceID == "" {
		return nil, false, nil
	}
	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if ws.OrgID != orgID {
		return nil, false, nil
	}
	return ws, true, nil
}

// WorkspaceCanReadInOrg is WorkspaceCanRead with an additional org cross-check:
// the workspace's org must match orgID. Used by the CRDT auth path where a stale
// subscriber on org A must never see a workspace that's been re-homed to org B.
// Empty orgID fails closed: CRDT callers must always bind authorization to the
// manager's concrete organization.
//
// It loads the (non-deleted) workspace, enforces the org binding, then defers to
// LoadedWorkspaceCanRead so the "owner OR explicit grant" read rule has a single
// source of truth shared with the RPC read paths, rather than a second copy in
// SQL. This adds a primary-key lookup versus a single combined query, but the
// CRDT read path (subscribe / workspace-create expansion, memoized per batch) is
// not per-op hot, so consistency wins over the saved round-trip.
func WorkspaceCanReadInOrg(ctx context.Context, st store.Store, orgID, workspaceID, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	ws, ok, err := loadWorkspaceInOrg(ctx, st, orgID, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return LoadedWorkspaceCanRead(ctx, st, ws, userID)
}

// WorkspaceCanWriteInOrg reports whether userID may WRITE workspaceID within
// orgID. Write access is owner-only (shared-write is not yet implemented), so
// the rule collapses to "is the workspace's owner". It reuses loadWorkspaceInOrg
// so the missing/out-of-org deny-vs-transient-retry prologue is shared with
// WorkspaceCanReadInOrg rather than re-implemented in the CRDT service layer --
// the same delegation the read path already uses.
func WorkspaceCanWriteInOrg(ctx context.Context, st store.Store, orgID, workspaceID, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	ws, ok, err := loadWorkspaceInOrg(ctx, st, orgID, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return ws.OwnerUserID == userID, nil
}

// WorkspaceReadableByUsersInOrg batches WorkspaceCanReadInOrg across many users:
// it loads the workspace once, enforces the org binding, then resolves owner OR
// explicit grant for every userID with a single grant lookup. Returns the map of
// userID -> readable (absent means not readable). A missing or out-of-org
// workspace yields the empty set (deny all); a store error is surfaced (the map
// is nil and meaningless when err != nil) so the caller can distinguish "nobody
// may read" from "lookup failed".
//
// It is the batch counterpart of WorkspaceCanReadInOrg for the CRDT
// subscriber-expansion path, which re-checks the SAME workspace for many
// subscribers at once. Sharing the "owner OR explicit grant" rule (and the org
// cross-check) keeps it from drifting from the per-user WorkspaceCanReadInOrg.
func WorkspaceReadableByUsersInOrg(ctx context.Context, st store.Store, orgID, workspaceID string, userIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	// Share the missing/out-of-org deny-vs-transient-retry prologue with the CRDT
	// read/write checks: a missing or out-of-org workspace is the empty set (deny
	// all), while a transient store error surfaces so the caller can retry.
	ws, ok, err := loadWorkspaceInOrg(ctx, st, orgID, workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	entries, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	granted := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		granted[entry.UserID] = struct{}{}
	}
	for _, userID := range userIDs {
		if userID == "" || out[userID] {
			continue
		}
		if userID == ws.OwnerUserID {
			out[userID] = true
			continue
		}
		if _, ok := granted[userID]; ok {
			out[userID] = true
		}
	}
	return out, nil
}

// WorkspacesReadableByUser filters workspaceIDs down to those userID may read,
// applying the canonical "owner OR explicit grant" rule for MANY workspaces
// against ONE user in at most two store round-trips: one ListByIDs, then a single
// batched grant lookup for the non-owned remainder (skipped entirely when the
// user owns every requested workspace). It is the many-workspaces/single-user
// counterpart of LoadedWorkspaceCanRead (1x1) and WorkspaceReadableByUsersInOrg
// (1-workspace x N-users); sharing the read policy keeps the three from drifting.
//
// orgID binds the workspace's organization: a workspace whose OrgID != orgID is
// excluded. An EMPTY orgID deliberately SKIPS that binding -- delegation callers
// legitimately pass no org because their pinned workspace may live outside the
// user's home org, and access is still gated by the owner-or-grant check. (This
// differs from the CRDT-path WorkspaceCanReadInOrg, which fails closed on an empty
// orgID because that path must always bind a concrete org; the contracts differ by
// caller, not by accident.)
//
// The readable subset is returned in the input order; empty and duplicate IDs and
// workspaces missing from the store are dropped. A store error is surfaced (the
// slice is meaningless when err != nil).
func WorkspacesReadableByUser(ctx context.Context, st store.Store, orgID, userID string, workspaceIDs []string) ([]string, error) {
	if userID == "" || len(workspaceIDs) == 0 {
		return nil, nil
	}
	// Dedup + drop empties so the bulk lookups stay tight and the access query
	// doesn't ask the store about repeats.
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
	// resolveInOrg returns the loaded workspace for wsID, applying the org
	// binding: with a non-empty orgID a workspace in a different org is skipped,
	// while an empty orgID deliberately skips that binding (the cross-org read
	// contract documented on this function). Single-siting the check keeps that
	// subtle empty-orgID rule from drifting between the two passes below.
	resolveInOrg := func(wsID string) (*store.Workspace, bool) {
		ws, ok := wsByID[wsID]
		if !ok || (orgID != "" && ws.OrgID != orgID) {
			return nil, false
		}
		return ws, true
	}
	// First pass over dedup: collect the non-owned, in-org workspaces that need a
	// grant lookup. Ownership alone settles the rest.
	var needCheck []string
	for _, wsID := range dedup {
		ws, ok := resolveInOrg(wsID)
		if !ok {
			continue
		}
		if ws.OwnerUserID != userID {
			needCheck = append(needCheck, wsID)
		}
	}
	grantedSet := make(map[string]struct{}, len(needCheck))
	if len(needCheck) > 0 {
		granted, err := st.WorkspaceAccess().ListForUserIn(ctx, userID, needCheck)
		if err != nil {
			return nil, err
		}
		for _, id := range granted {
			grantedSet[id] = struct{}{}
		}
	}
	// Second pass over dedup (input order): keep a workspace when the user owns
	// it or holds a grant. Iterating dedup rather than emitting owners-first keeps
	// the readable subset in the documented input order even for a mix of owned
	// and granted IDs.
	out := make([]string, 0, len(dedup))
	for _, wsID := range dedup {
		ws, ok := resolveInOrg(wsID)
		if !ok {
			continue
		}
		if ws.OwnerUserID == userID {
			out = append(out, wsID)
			continue
		}
		if _, ok := grantedSet[wsID]; ok {
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
