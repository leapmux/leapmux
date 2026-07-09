package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// validateTabIDCrossTabTypes ensures every op targeting a given
// tab_id agrees on tab_type, both inside the batch and against any
// pre-existing TabRecord.
func validateTabIDCrossTabTypes(pre *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp) (leapmuxv1.BatchRejectionReason, string) {
	seen := map[string]leapmuxv1.TabType{}
	for _, op := range batch {
		var tabID string
		var tabType leapmuxv1.TabType
		switch body := op.GetBody().(type) {
		case *leapmuxv1.OrgOp_SetTabRegister:
			tabID = body.SetTabRegister.GetTabId()
			tabType = body.SetTabRegister.GetTabType()
		case *leapmuxv1.OrgOp_TombstoneTab:
			tabID = body.TombstoneTab.GetTabId()
			tabType = body.TombstoneTab.GetTabType()
		default:
			continue
		}
		if existing, ok := seen[tabID]; ok && existing != tabType {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_ID_COLLISION_ACROSS_TYPES, op.GetOpId()
		}
		seen[tabID] = tabType
		if rec, ok := pre.GetTabs()[tabID]; ok && rec.GetTabType() != tabType && HLCIsZero(rec.GetTombstoneAt()) {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_ID_COLLISION_ACROSS_TYPES, op.GetOpId()
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}

// tabPlacementCheck returns the offending tab id (or "" on success).
// We don't actually have op-level granularity here, so we return the
// tab id as a best-effort identifier; callers map this back to the op
// via the projection.
//
// Scoping: pre-existing tabs already passed this check on the batch
// that registered them. A pre-existing tab's chain can only transition
// in this batch via a "structural" op — one that tombstones a node,
// flips a node's Kind, registers a workspace/floating-window root, or
// tombstones a floating window. (parent_id is set-once, so SetNode
// Register(parent_id) cannot re-route an existing chain.) When no
// structural op is present we restrict the walk to tabs whose
// SetTabRegister(tile_id) op landed in this batch (which also covers
// newly-created tabs, since their registration op names them).
//
// When a structural op IS present we fall back to the full walk: the
// affected subtree can span any number of pre-existing tabs and a
// precise scoping would re-create most of the per-batch cost we're
// trying to avoid.
func tabPlacementCheck(working *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp) string {
	roots := registeredRoots(working)
	if batchHasStructuralOp(batch) {
		for _, t := range working.GetTabs() {
			if id := checkTabPlacement(working, t, roots); id != "" {
				return id
			}
		}
		return ""
	}
	// Restricted walk: only inspect tabs touched by this batch.
	// Pre-existing tabs not in this set kept their pre-state placement,
	// which is valid by induction.
	for _, op := range batch {
		ref := OpTarget(op)
		if ref.Kind != EntityKindTab {
			continue
		}
		t := working.GetTabs()[ref.TabID]
		if t == nil {
			continue
		}
		if id := checkTabPlacement(working, t, roots); id != "" {
			return id
		}
	}
	return ""
}

// checkTabPlacement runs the placement invariant against a single
// tab. Returns the tab id on failure, "" on success.
func checkTabPlacement(state *leapmuxv1.OrgCrdtState, t *leapmuxv1.TabRecord, roots rootSet) string {
	if !HLCIsZero(t.GetTombstoneAt()) {
		return ""
	}
	tile := t.GetTileId().GetValue()
	if tile == "" {
		return t.GetTabId()
	}
	wsID, leafLive := resolveTileWorkspace(state, tile, roots)
	if wsID == "" || !leafLive || !tileIsLeaf(state, tile) {
		return t.GetTabId()
	}
	return ""
}

// batchHasStructuralOp reports whether `batch` contains an op that
// could rewire which root a pre-existing tab's tile chain terminates
// at, or could change whether the tile leaf is still a leaf. See
// tabPlacementCheck for the rationale.
func batchHasStructuralOp(batch []*leapmuxv1.OrgOp) bool {
	for _, op := range batch {
		switch body := op.GetBody().(type) {
		case *leapmuxv1.OrgOp_TombstoneNode,
			*leapmuxv1.OrgOp_TombstoneFloatingWindow,
			*leapmuxv1.OrgOp_SetWorkspaceRootNode:
			return true
		case *leapmuxv1.OrgOp_SetNodeRegister:
			// Only Kind flips can transition a leaf into a non-leaf.
			// parent_id is set-once so it can't re-route an existing
			// chain. position/direction/ratios/etc. don't affect
			// reachability.
			if _, isKind := body.SetNodeRegister.GetField().(*leapmuxv1.SetNodeRegisterOp_Kind); isKind {
				return true
			}
		case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
			// workspace_id move + root_node_id assignment both affect
			// which tiles end up reachable.
			switch body.SetFloatingWindowRegister.GetField().(type) {
			case *leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId,
				*leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId:
				return true
			}
		}
	}
	return false
}

// validateWorkerRefs walks the batch and rejects any
// SetTabRegisterOp.worker_id write naming a worker the principal
// can't use. Empty worker_id is allowed (the "clear" case).
//
// This is the authoritative gate for CRDT writes that pin tabs to
// workers: without it, a client could submit `worker_id: "f"` and
// produce a tab that points at a non-existent worker. The CLI
// preflight catches this earlier with a friendlier message; this
// is the defense-in-depth catch for trustless / future writers.
func validateWorkerRefs(ctx context.Context, batch []*leapmuxv1.OrgOp, principalID, orgID string, auth AuthChecker) (leapmuxv1.BatchRejectionReason, string, error) {
	for _, op := range batch {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_SetTabRegister)
		if !ok {
			continue
		}
		field, ok := body.SetTabRegister.GetField().(*leapmuxv1.SetTabRegisterOp_WorkerId)
		if !ok {
			continue
		}
		workerID := field.WorkerId
		if workerID == "" {
			continue
		}
		allowed, err := auth.CanUseWorker(ctx, orgID, workerID, principalID)
		if err != nil {
			// Transient worker-lookup failure: surface it as retryable rather than
			// rejecting the op as an invalid worker ref.
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", err
		}
		if !allowed {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INVALID_WORKER_REF, op.GetOpId(), nil
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, "", nil
}
