package crdt

import (
	"context"
	"math"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ValidationResult is the outcome of validating a single batch. When
// Reason != UNSPECIFIED, the batch is rejected and OffendingOpID
// names the first op that failed (or "" if the failure is
// batch-level).
type ValidationResult struct {
	Reason        leapmuxv1.BatchRejectionReason
	OffendingOpID string
	// Err is set when a permission lookup during auth validation failed
	// transiently (a store error), as opposed to a genuine deny. The caller
	// surfaces it as a retryable error instead of a permanent FORBIDDEN
	// op-rejection, so a brief DB hiccup does not silently drop a user's edit.
	// When Err is non-nil, Reason/OffendingOpID/AffectedEntities are meaningless.
	Err error
	// AffectedEntities maps EntityRef -> (preWorkspaceID,
	// postWorkspaceID). The manager broadcasts per-entity and uses
	// this to compute visibility transitions per subscriber.
	AffectedEntities map[EntityRef]EntityWorkspaceTransition
}

// EntityWorkspaceTransition is "where was this entity, where is it now".
type EntityWorkspaceTransition struct {
	Pre  string
	Post string
}

// ValidateBatch runs the full validation pipeline against working
// copies of the state. The batch's ops MUST already have canonical
// HLCs assigned (so LWW outcomes are well-defined). Returns the
// per-batch result plus the post-batch working state on success.
//
// The pre-batch tombstone check is run against `pre`; the per-op
// checks (parent_immutable, root_immutable, root_node_protected, ...)
// are run against snapshot states tracked along the way.
//
// `ctx` is threaded through to every AuthChecker call so the
// backing implementation (which hits the DB) can observe request
// cancellation. Tests typically pass `context.Background()`.
func ValidateBatch(
	ctx context.Context,
	pre *leapmuxv1.OrgCrdtState,
	batch []*leapmuxv1.OrgOp,
	internal bool,
	principalID string,
	auth AuthChecker,
) (ValidationResult, *leapmuxv1.OrgCrdtState) {
	result := ValidationResult{
		AffectedEntities: map[EntityRef]EntityWorkspaceTransition{},
	}
	// Memoize per-batch auth lookups. Each predicate is backed by a DB
	// fetch (workspaces / workers); a batch with N ops
	// touching the same workspace would otherwise issue N round-trips on
	// the manager goroutine, blocking every other submitter for the org.
	if !internal {
		auth = &memoAuthChecker{inner: auth}
	}

	// 1. Pre-apply tombstone check. Walking once over `pre` is enough
	//    because creation-order ops within the batch can't observe
	//    pre-batch tombstones; the validator's "set on tombstoned
	//    entity" check is against pre-batch state only.
	for _, op := range batch {
		if msg := preApplyTombstoneCheck(pre, op); msg != "" {
			result.Reason = leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TOMBSTONED_TARGET
			result.OffendingOpID = op.GetOpId()
			return result, nil
		}
	}

	// 2. tab_id cross-TabType uniqueness — both inter-batch and intra-batch.
	if reason, opID := validateTabIDCrossTabTypes(pre, batch); reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
		result.Reason = reason
		result.OffendingOpID = opID
		return result, nil
	}

	// 3. parent_id set-once, root_immutable, hub-only-op.
	for _, op := range batch {
		if reason, opID := validateSetOnce(pre, op, internal); reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
			result.Reason = reason
			result.OffendingOpID = opID
			return result, nil
		}
	}

	// 4. Apply working copy. CloneStateForBatch shares record refs with
	// `pre` except for entities the batch actually touches, so the
	// per-batch clone cost scales with |touched|, not |total entities|.
	working := CloneStateForBatch(pre, batch)
	for _, op := range batch {
		Apply(working, op)
	}

	// Pre/post workspace resolution is bounded to the EntityRefs the
	// batch actually touches. The full workspaceForEntities walk used
	// to scan every node, tab, and window in the org twice per batch;
	// for a batch that touches one tile in a large org that's enormous
	// overkill.
	//
	// `resolved` carries one entry per batch op alongside its target
	// ref + pre/post workspace. Subsequent passes (auth, tracking)
	// consume this slice instead of re-computing OpTarget or doing map
	// lookups, so each downstream loop sees ref+workspace data inline.
	preRoots := registeredRoots(pre)
	postRoots := registeredRoots(working)
	resolved := make([]resolvedOp, len(batch))
	preWSCache := map[EntityRef]string{}
	postWSCache := map[EntityRef]string{}
	for i, op := range batch {
		ref := OpTarget(op)
		entry := resolvedOp{op: op, ref: ref}
		if ref.Kind != EntityKindUnknown {
			preW, ok := preWSCache[ref]
			if !ok {
				preW = workspaceForEntity(pre, ref, preRoots)
				preWSCache[ref] = preW
			}
			// Tombstone ops wipe the post-state registers used to resolve the
			// entity's workspace, so workspaceForEntity is destined to return
			// "" — and the tracking loop below pins postW to preW anyway.
			// Skip the redundant post-state walk.
			var postW string
			if IsTombstoneOp(op) {
				postW = ""
			} else if cached, ok := postWSCache[ref]; ok {
				postW = cached
			} else {
				postW = workspaceForEntity(working, ref, postRoots)
				postWSCache[ref] = postW
			}
			entry.preW = preW
			entry.postW = postW
		}
		resolved[i] = entry
	}

	// 5. Tab placement invariant: every live tab's tile_id must
	//    resolve to a raw-live leaf. Pre-existing tabs already passed
	//    this check on the batch that registered them, so we only
	//    re-validate tabs whose chain could plausibly have transitioned
	//    in this batch — see tabPlacementCheck for the restriction logic.
	if opID := tabPlacementCheck(working, batch); opID != "" {
		result.Reason = leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID
		result.OffendingOpID = opID
		return result, nil
	}

	// 6. Value-domain validation per op.
	for _, op := range batch {
		if reason, opID := valueDomainCheck(op); reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
			result.Reason = reason
			result.OffendingOpID = opID
			return result, nil
		}
	}

	// 7. Completeness check on live records in post-batch state.
	if opID := completenessCheck(working, postRoots); opID != "" {
		result.Reason = leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INCOMPLETE_RECORD
		result.OffendingOpID = opID
		return result, nil
	}

	// Build the post-state parent→children adjacency once. Every
	// downstream check that walks node subtrees (rootChecks →
	// subtreeHasOtherLive, floatingMoveCheck, the transitive-subtree
	// loop below) reuses this index instead of rescanning the full
	// `working.Nodes` map, dropping the per-batch cost from O(N · ops)
	// to O(N + subtree-size · ops).
	postChildren := BuildAllChildrenIndex(working)

	// 8. Root protection + uniqueness.
	if reason, opID := rootChecks(working, batch, internal, preRoots, postRoots, postChildren); reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
		result.Reason = reason
		result.OffendingOpID = opID
		return result, nil
	}

	// 9. Floating-window cross-workspace move requires empty subtree.
	if reason, opID := floatingMoveCheck(working, pre, batch, postChildren); reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
		result.Reason = reason
		result.OffendingOpID = opID
		return result, nil
	}

	// 10. Auth check per op (skipped under internal=true).
	if !internal {
		for _, r := range resolved {
			reason, opID, err := authCheck(ctx, r.op, r.preW, r.postW, principalID, pre.GetOrgId(), auth)
			if err != nil {
				result.Err = err
				return result, nil
			}
			if reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
				result.Reason = reason
				result.OffendingOpID = opID
				return result, nil
			}
		}
	}

	// 10b. Worker-reference check. SetTabRegisterOp.worker_id pins a
	// tab to a worker; without this gate, any client (including a
	// trustless CLI) could write an arbitrary worker_id into the
	// CRDT and orphan the tab. Empty worker_id is allowed — that's
	// the "clear" case (file tabs that haven't picked a worker yet,
	// or explicit unassignment).
	if !internal {
		reason, opID, err := validateWorkerRefs(ctx, batch, principalID, pre.GetOrgId(), auth)
		if err != nil {
			result.Err = err
			return result, nil
		}
		if reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
			result.Reason = reason
			result.OffendingOpID = opID
			return result, nil
		}
	}

	// 11. Build the workspaces / affected-entities tracking, plus
	//     the transitive subtree affected by floating-window moves.
	for _, r := range resolved {
		preW := r.preW
		postW := r.postW
		// Tombstone ops wipe the registers used to resolve the entity's
		// post-workspace (parent_id / tile_id / workspace_id), so the
		// `workspaceForEntities` lookup returns "" for the post-state.
		// Without this override the broadcast layer would treat tombstones
		// as a becoming-hidden transition and emit `EntityRemoved` — even
		// to the originating client, whose pending tombstone is then
		// dropped as if redacted out-of-view. Pin the entity to its
		// pre-workspace so subscribers that already saw it receive the
		// raw tombstone op via the visible-to-visible path and apply it
		// through `consumeRemote`.
		if postW == "" && IsTombstoneOp(r.op) {
			postW = preW
		}
		if existing, ok := result.AffectedEntities[r.ref]; ok {
			result.AffectedEntities[r.ref] = EntityWorkspaceTransition{
				Pre:  existing.Pre,
				Post: postW,
			}
		} else {
			result.AffectedEntities[r.ref] = EntityWorkspaceTransition{Pre: preW, Post: postW}
		}

		// Transitive subtree: a SetFloatingWindowRegister(workspace_id=…)
		// that actually moves the window pulls every descendant
		// node and every tab whose tile_id resolves into the
		// window's subtree across the visibility boundary. The
		// validator currently bounds non-root descendants to zero,
		// but tabs and the window's root NodeRecord still need
		// transitive entries.
		body, ok := r.op.GetBody().(*leapmuxv1.OrgOp_SetFloatingWindowRegister)
		if !ok {
			continue
		}
		setOp := body.SetFloatingWindowRegister
		if _, isWS := setOp.GetField().(*leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId); !isWS {
			continue
		}
		if preW == postW {
			continue
		}
		postFW := working.GetFloatingWindows()[setOp.GetWindowId()]
		if postFW == nil {
			continue
		}
		rootID := postFW.GetRootNodeId()
		if rootID == "" {
			continue
		}
		subtree := collectSubtree(postChildren, rootID)
		for nodeID := range subtree {
			rec := working.GetNodes()[nodeID]
			if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
				continue
			}
			nref := EntityRef{Kind: EntityKindNode, NodeID: nodeID}
			if _, exists := result.AffectedEntities[nref]; !exists {
				result.AffectedEntities[nref] = EntityWorkspaceTransition{Pre: preW, Post: postW}
			}
		}
		for _, t := range working.GetTabs() {
			if !HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			if !subtree[t.GetTileId().GetValue()] {
				continue
			}
			tref := EntityRef{Kind: EntityKindTab, TabType: t.GetTabType(), TabID: t.GetTabId()}
			if _, exists := result.AffectedEntities[tref]; !exists {
				result.AffectedEntities[tref] = EntityWorkspaceTransition{Pre: preW, Post: postW}
			}
		}
	}

	return result, working
}

// preApplyTombstoneCheck returns a non-empty string when the op
// targets an already-tombstoned entity in `pre`. Redundant tombstones
// (`Tombstone…` on something already tombstoned) are allowed; only Set
// ops on tombstoned entities are rejected.
func preApplyTombstoneCheck(pre *leapmuxv1.OrgCrdtState, op *leapmuxv1.OrgOp) string {
	switch op.GetBody().(type) {
	case *leapmuxv1.OrgOp_TombstoneNode, *leapmuxv1.OrgOp_TombstoneTab, *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		return ""
	}
	ref := OpTarget(op)
	switch ref.Kind {
	case EntityKindNode:
		if rec, ok := pre.GetNodes()[ref.NodeID]; ok && !HLCIsZero(rec.GetTombstoneAt()) {
			return op.GetOpId()
		}
	case EntityKindTab:
		if rec, ok := pre.GetTabs()[ref.TabID]; ok && !HLCIsZero(rec.GetTombstoneAt()) {
			return op.GetOpId()
		}
	case EntityKindFloatingWindow:
		if rec, ok := pre.GetFloatingWindows()[ref.WindowID]; ok && !HLCIsZero(rec.GetTombstoneAt()) {
			return op.GetOpId()
		}
	}
	return ""
}

// validateSetOnce enforces parent_id set-once, root_node_id
// set-once, and the hub-only gate on SetWorkspaceRootNodeOp.
func validateSetOnce(pre *leapmuxv1.OrgCrdtState, op *leapmuxv1.OrgOp, internal bool) (leapmuxv1.BatchRejectionReason, string) {
	switch body := op.GetBody().(type) {
	case *leapmuxv1.OrgOp_SetNodeRegister:
		setOp := body.SetNodeRegister
		if _, ok := setOp.GetField().(*leapmuxv1.SetNodeRegisterOp_ParentId); ok {
			// parent_id is set-once: any write to a node already present
			// in pre-batch state is a re-parent attempt.
			if _, exists := pre.GetNodes()[setOp.GetNodeId()]; exists {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_PARENT_IMMUTABLE, op.GetOpId()
			}
		}
	case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
		setOp := body.SetFloatingWindowRegister
		if _, ok := setOp.GetField().(*leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId); ok {
			if _, exists := pre.GetFloatingWindows()[setOp.GetWindowId()]; exists {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_IMMUTABLE, op.GetOpId()
			}
		}
	case *leapmuxv1.OrgOp_SetWorkspaceRootNode:
		if !internal {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, op.GetOpId()
		}
		setOp := body.SetWorkspaceRootNode
		if rec, ok := pre.GetWorkspaces()[setOp.GetWorkspaceId()]; ok && rec.GetRootNodeId() != "" {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_IMMUTABLE, op.GetOpId()
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}

// valueDomainCheck rejects NaN/±∞/out-of-range values.
func valueDomainCheck(op *leapmuxv1.OrgOp) (leapmuxv1.BatchRejectionReason, string) {
	switch body := op.GetBody().(type) {
	case *leapmuxv1.OrgOp_SetNodeRegister:
		setOp := body.SetNodeRegister
		switch field := setOp.GetField().(type) {
		case *leapmuxv1.SetNodeRegisterOp_Ratios:
			if !validRatios(field.Ratios.GetValues()) {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetNodeRegisterOp_RowRatios:
			if !validRatios(field.RowRatios.GetValues()) {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetNodeRegisterOp_ColRatios:
			if !validRatios(field.ColRatios.GetValues()) {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetNodeRegisterOp_Rows:
			if field.Rows == 0 || field.Rows > MaxGridDimension {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetNodeRegisterOp_Cols:
			if field.Cols == 0 || field.Cols > MaxGridDimension {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		}
	case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
		setOp := body.SetFloatingWindowRegister
		switch field := setOp.GetField().(type) {
		case *leapmuxv1.SetFloatingWindowRegisterOp_X:
			if !finite(field.X) {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetFloatingWindowRegisterOp_Y:
			if !finite(field.Y) {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetFloatingWindowRegisterOp_Width:
			if !finite(field.Width) || field.Width <= 0 {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetFloatingWindowRegisterOp_Height:
			if !finite(field.Height) || field.Height <= 0 {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		case *leapmuxv1.SetFloatingWindowRegisterOp_Opacity:
			if !finite(field.Opacity) || field.Opacity < 0 || field.Opacity > 1 {
				return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, op.GetOpId()
			}
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

const ratioTolerance = 1e-9

func validRatios(values []float64) bool {
	if len(values) == 0 {
		return true
	}
	sum := 0.0
	for _, v := range values {
		if !finite(v) || v < 0 {
			return false
		}
		sum += v
	}
	return math.Abs(sum-1.0) < ratioTolerance
}

// completenessCheck ensures every live (non-tombstoned) record has
// every required field populated. `roots` is the post-batch registered
// roots map (workspace + floating-window roots), reused from the
// caller's `registeredRoots(state)` precomputation so the parentless-
// node case doesn't re-scan workspaces+floating-windows per node.
func completenessCheck(state *leapmuxv1.OrgCrdtState, roots rootSet) string {
	for id, t := range state.GetTabs() {
		if !HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if t.GetTileId() == nil || t.GetTileId().GetValue() == "" {
			return id
		}
		if t.GetWorkerId() == nil || t.GetWorkerId().GetValue() == "" {
			return id
		}
		if t.GetPosition() == nil {
			return id
		}
	}
	for id, n := range state.GetNodes() {
		if !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		if n.GetKind() == nil {
			return id
		}
		// Non-root nodes must have a parent_id and a position.
		if n.GetParentId() == "" {
			// Allowed only when registered as a workspace or
			// floating-window root.
			if _, isRoot := roots.roots[id]; !isRoot {
				return id
			}
		} else if n.GetPosition() == nil {
			return id
		}
		switch n.GetKind().GetValue() {
		case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
			if n.GetDirection() == nil || n.GetRatios() == nil {
				return id
			}
		case leapmuxv1.NodeKind_NODE_KIND_GRID:
			if n.GetRows() == nil || n.GetCols() == nil {
				return id
			}
			if n.GetRowRatios() == nil || n.GetColRatios() == nil {
				return id
			}
		}
	}
	for id, fw := range state.GetFloatingWindows() {
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if fw.GetWorkspaceId() == nil || fw.GetWorkspaceId().GetValue() == "" {
			return id
		}
		if fw.GetRootNodeId() == "" {
			return id
		}
		if fw.GetX() == nil || fw.GetY() == nil || fw.GetWidth() == nil || fw.GetHeight() == nil || fw.GetOpacity() == nil {
			return id
		}
	}
	return ""
}
