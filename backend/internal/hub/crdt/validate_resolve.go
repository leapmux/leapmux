package crdt

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// resolvedOp carries an op alongside its target ref and the workspace
// it occupies pre- and post-batch. ValidateBatch builds one slice of
// these immediately after the per-op working-copy apply, so the
// downstream auth and tracking passes can iterate it without
// re-running OpTarget / workspaceForEntity per op.
type resolvedOp struct {
	op    *leapmuxv1.OrgOp
	ref   EntityRef
	preW  string
	postW string
}

// workspaceForEntity resolves the owning workspace for a single entity
// in `state`. For tabs it walks tile_id to a root; for nodes it walks
// parent_id; for floating windows it reads the explicit register; for
// workspace-root refs it's the workspace_id itself. Returns "" when
// the entity doesn't exist, is tombstoned, or its parent chain doesn't
// terminate at a registered root.
//
// `roots` MUST come from `registeredRoots(state)` against the same
// state. The validator caches a pre/post pair per batch and reuses it
// across the per-op auth loop and the per-op affected-entities loop.
func workspaceForEntity(state *leapmuxv1.OrgCrdtState, ref EntityRef, roots rootSet) string {
	switch ref.Kind {
	case EntityKindNode:
		return nodeWorkspace(state, state.GetNodes()[ref.NodeID], roots)
	case EntityKindTab:
		t, ok := state.GetTabs()[ref.TabID]
		if !ok {
			return ""
		}
		ws, _ := resolveTileWorkspace(state, t.GetTileId().GetValue(), roots)
		return ws
	case EntityKindFloatingWindow:
		fw, ok := state.GetFloatingWindows()[ref.WindowID]
		if !ok {
			return ""
		}
		return fw.GetWorkspaceId().GetValue()
	case EntityKindWorkspaceRoot:
		if _, ok := state.GetWorkspaces()[ref.WorkspaceID]; !ok {
			return ""
		}
		return ref.WorkspaceID
	}
	return ""
}

func nodeWorkspace(state *leapmuxv1.OrgCrdtState, node *leapmuxv1.NodeRecord, roots rootSet) string {
	if node == nil || !HLCIsZero(node.GetTombstoneAt()) {
		return ""
	}
	return resolveParentChain(roots.roots, node.GetNodeId(), func(id string) (string, bool) {
		next, ok := state.GetNodes()[id]
		if !ok {
			return "", false
		}
		return next.GetParentId(), true
	})
}
