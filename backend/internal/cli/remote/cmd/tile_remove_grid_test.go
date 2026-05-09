package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// gridNode is a stateBuilder helper specific to remove-grid tests:
// registers a live GRID node with an explicit position so heir-finding
// and root detection behave deterministically.
func (b *stateBuilder) gridNode(nodeID, parentID, position string) *stateBuilder {
	b.st.Nodes[nodeID] = &leapmuxv1.NodeRecord{
		NodeId:   nodeID,
		ParentId: parentID,
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		Position: &leapmuxv1.LWWString{Value: position},
	}
	return b
}

// TestBuildCloseSubtreeOps_NonRootGridReplacement walks the
// non-root replace-with-leaf path the way RunTileRemoveGrid does:
// emit subtree close ops with migrateTo = newLeafID, then append
// the new-leaf creation ops. The result must:
//
//   - Tombstone every cell and the grid itself.
//   - Migrate every tab in the subtree onto newLeafID.
//   - Create newLeafID under the grid's parent at the grid's position.
//
// This pins the contract that the CLI's `tile remove-grid --with-tabs=move`
// matches the frontend's emitReplaceGridWithLeaf for non-root grids.
func TestRemoveGrid_NonRoot_MoveMigratesTabsToFreshLeaf(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		splitNode("R", "", "").
		leafNode("sibling", "R", "a").
		gridNode("G", "R", "b").
		leafNode("c00", "G", "0,0").
		leafNode("c01", "G", "0,1").
		tab("tab-1", "c00", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "c01", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st

	bs := testBootstrap(state)
	// Simulate the move path: buildCloseSubtreeOps with migrateTo
	// (= the not-yet-created new leaf id), then append the new-leaf
	// creation. We can pick any id for the new leaf in tests.
	newLeaf := "new-leaf"
	ops := buildCloseSubtreeOps(bs, "G", newLeaf, false)
	cases := opCases(ops)

	// Both tabs migrate to the new leaf.
	assert.Contains(t, cases, "setTabTileId:tab-1="+newLeaf)
	assert.Contains(t, cases, "setTabTileId:tab-2="+newLeaf)
	// Tabs get fresh positions so LexoRank survives the rewrite.
	assert.Contains(t, cases, "setTabPosition:tab-1")
	assert.Contains(t, cases, "setTabPosition:tab-2")
	// Every node in the subtree is tombstoned including the grid.
	assert.Contains(t, cases, "tombstoneNode:c00")
	assert.Contains(t, cases, "tombstoneNode:c01")
	assert.Contains(t, cases, "tombstoneNode:G")
	// No tab tombstones — the move path preserves tabs.
	for _, c := range cases {
		assert.NotContains(t, c, "tombstoneTab", "move path must not tombstone tabs")
	}
}

// TestRemoveGrid_RootMoveKeepsRoot pins the root-grid move path:
// passing keepRoot=true causes buildCloseSubtreeOps to skip the
// tombstoneNode for the root, leaving its NodeRecord alive so the
// caller can flip the kind to LEAF without colliding with the
// `root_node_protected` validator rule.
func TestRemoveGrid_RootMoveKeepsRoot(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "G").
		gridNode("G", "", "").
		leafNode("c00", "G", "0,0").
		leafNode("c01", "G", "0,1").
		tab("tab-1", "c00", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	bs := testBootstrap(state)
	// Root move: migrateTo = root itself, keepRoot = true; tabs move
	// onto the root, which the caller then flips to LEAF.
	ops := buildCloseSubtreeOps(bs, "G", "G", true)
	require.NotEmpty(t, ops)
	cases := opCases(ops)
	assert.NotContains(t, cases, "tombstoneNode:G", "keepRoot=true must skip root tombstone")
	// Descendant tombstones still fire.
	assert.Contains(t, cases, "tombstoneNode:c00")
	assert.Contains(t, cases, "tombstoneNode:c01")
	// Tab migrates onto root.
	assert.Contains(t, cases, "setTabTileId:tab-1=G")
}

// TestRemoveGrid_RootClosePolicyKeepsRoot covers the symmetric close
// (no-tabs / tombstone-tabs) path for a root grid. keepRoot=true
// must skip the root tombstone here too — otherwise a single user
// attempt to clean up a root grid leaves the workspace permanently
// stuck on a `root_node_protected` rejection.
func TestRemoveGrid_RootClosePolicyKeepsRoot(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "G").
		gridNode("G", "", "").
		leafNode("c00", "G", "0,0").
		st

	bs := testBootstrap(state)
	ops := buildCloseSubtreeOps(bs, "G", "", true)
	cases := opCases(ops)
	assert.NotContains(t, cases, "tombstoneNode:G")
	assert.Contains(t, cases, "tombstoneNode:c00")
}

// TestBuildCloseSubtreeOps_KeepRootFalseTombstonesRoot pins the
// default (non-root) shape: keepRoot=false tombstones the targeted
// node, matching the cascade-close path used by `tile close`.
func TestBuildCloseSubtreeOps_KeepRootFalseTombstonesRoot(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		splitNode("R", "", "").
		leafNode("sibling", "R", "a").
		gridNode("G", "R", "b").
		leafNode("c00", "G", "0,0").
		st
	bs := testBootstrap(state)

	ops := buildCloseSubtreeOps(bs, "G", "", false)
	cases := opCases(ops)
	assert.Equal(t, "tombstoneNode:G", cases[len(cases)-1], "keepRoot=false must tombstone the target")
	assert.Contains(t, cases, "tombstoneNode:c00")
}
