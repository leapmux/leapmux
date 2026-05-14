package crdt

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// rootChecks enforces:
//   - workspace roots can't be tombstoned by client batches.
//   - floating-window roots can be tombstoned, but only when the same
//     batch tombstones the owning window AND post-batch has no other
//     live entities in the subtree.
//   - newly-assigned root_node_ids must point at unregistered, live,
//     parentless nodes (no aliasing).
//
// `internal` toggles the lifecycle-driven bypass: hub-internal batches
// (workspace delete, worker reconciliation tombstones) are allowed to
// tombstone any registered root because they own the full enumeration
// — the workspace_id map entry is removed immediately after. Root
// uniqueness checks still apply: even an internal batch can't alias
// two roots to the same node.
func rootChecks(post *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp, internal bool, preRoots, postRoots rootSet, postChildren map[string][]string) (leapmuxv1.BatchRejectionReason, string) {
	// preRoots carries the pre-batch workspace/window root maps already.
	preWorkspaceRoots := preRoots.workspaceRoots
	preWindowRoots := preRoots.windowRoots

	tombstonedWindows := map[string]bool{}
	for _, op := range batch {
		if t, ok := op.GetBody().(*leapmuxv1.OrgOp_TombstoneFloatingWindow); ok {
			tombstonedWindows[t.TombstoneFloatingWindow.GetWindowId()] = true
		}
	}

	for _, op := range batch {
		switch body := op.GetBody().(type) {
		case *leapmuxv1.OrgOp_TombstoneNode:
			id := body.TombstoneNode.GetNodeId()
			if _, ok := preWorkspaceRoots[id]; ok {
				if !internal {
					return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_PROTECTED, op.GetOpId()
				}
				// Internal lifecycle path is enumerating the full
				// workspace; bypass workspace-root protection.
			}
			if winID, ok := preWindowRoots[id]; ok {
				if internal {
					// Internal cleanup path doesn't require the same-
					// batch window tombstone; the manager enumerates
					// the workspace, including its floating windows.
					continue
				}
				if !tombstonedWindows[winID] {
					return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_PROTECTED, op.GetOpId()
				}
				// The same batch is tombstoning the window. Require
				// the post-batch state to have no other live nodes/
				// tabs in the window's subtree.
				if subtreeHasOtherLive(post, postChildren, id) {
					return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_PROTECTED, op.GetOpId()
				}
			}
		case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
			setOp := body.SetFloatingWindowRegister
			if r, ok := setOp.GetField().(*leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId); ok {
				if reason, ok := validateRootAssignment(post, postRoots, r.RootNodeId); !ok {
					return reason, op.GetOpId()
				}
			}
		case *leapmuxv1.OrgOp_SetWorkspaceRootNode:
			if reason, ok := validateRootAssignment(post, postRoots, body.SetWorkspaceRootNode.GetRootNodeId()); !ok {
				return reason, op.GetOpId()
			}
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}

// validateRootAssignment checks (a) the candidate node exists, (b) is
// not tombstoned, (c) has no non-empty parent_id, (d) is registered as
// exactly one root in the post-batch state. The post-state occurrence
// count comes from the precomputed rootSet (registeredRoots) so this
// check is O(1) per call.
func validateRootAssignment(post *leapmuxv1.OrgCrdtState, postRoots rootSet, candidateID string) (leapmuxv1.BatchRejectionReason, bool) {
	rec := post.GetNodes()[candidateID]
	if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, false
	}
	if rec.GetParentId() != "" {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, false
	}
	if postRoots.counts[candidateID] != 1 {
		return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE, false
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, true
}

// subtreeHasOtherLive returns true if the subtree rooted at rootID
// contains any non-tombstoned node (other than rootID itself) or any
// non-tombstoned tab. `children` is the precomputed parent→children
// adjacency for `state`; it must be built against the same state the
// node records read against (the caller passes postChildren built from
// the same post-batch working copy).
func subtreeHasOtherLive(state *leapmuxv1.OrgCrdtState, children map[string][]string, rootID string) bool {
	subtree := collectSubtree(children, rootID)
	for id := range subtree {
		if id == rootID {
			continue
		}
		rec := state.GetNodes()[id]
		if rec != nil && HLCIsZero(rec.GetTombstoneAt()) {
			return true
		}
	}
	for _, t := range state.GetTabs() {
		if !HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if subtree[t.GetTileId().GetValue()] {
			return true
		}
	}
	return false
}

// collectSubtree returns the closure of nodes reachable from rootID
// via the parent→children adjacency. The adjacency itself is built
// once per ValidateBatch (see buildChildrenIndex) and threaded through
// every call, so each invocation costs O(subtree size) instead of the
// earlier O(N) full-state rescan.
func collectSubtree(children map[string][]string, rootID string) map[string]bool {
	subtree := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, child := range children[next] {
			if subtree[child] {
				continue
			}
			subtree[child] = true
			queue = append(queue, child)
		}
	}
	return subtree
}

// floatingMoveCheck rejects SetFloatingWindowRegister(workspace_id=…)
// that moves a pre-existing window across workspaces while its inner
// subtree has live non-root descendants or tabs. The check is keyed
// on whether `workspace_id` actually changes between pre and post
// state — register writes that are no-ops, or that touch fresh
// windows being created in this batch, fall through.
func floatingMoveCheck(post, pre *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp, postChildren map[string][]string) (leapmuxv1.BatchRejectionReason, string) {
	for _, op := range batch {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_SetFloatingWindowRegister)
		if !ok {
			continue
		}
		setOp := body.SetFloatingWindowRegister
		if _, isWS := setOp.GetField().(*leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId); !isWS {
			continue
		}
		preFW, hadPre := pre.GetFloatingWindows()[setOp.GetWindowId()]
		if !hadPre || !HLCIsZero(preFW.GetTombstoneAt()) {
			// Fresh window in this batch (or one being un-tombstoned,
			// which the remove-wins rule actually drops). Not a move.
			continue
		}
		postFW := post.GetFloatingWindows()[setOp.GetWindowId()]
		if postFW == nil {
			continue
		}
		preWS := preFW.GetWorkspaceId().GetValue()
		postWS := postFW.GetWorkspaceId().GetValue()
		if preWS == postWS {
			continue
		}
		rootID := postFW.GetRootNodeId()
		if rootID == "" {
			continue
		}
		if subtreeHasOtherLive(post, postChildren, rootID) {
			return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_FLOATING_MOVE_WITH_DESCENDANTS, op.GetOpId()
		}
	}
	return leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, ""
}
