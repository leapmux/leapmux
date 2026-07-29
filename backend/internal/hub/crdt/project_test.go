package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// seedWorkspaceWithRoot returns a state that has one workspace whose
// root_node_id is wired up correctly. Used as the starting point
// for projection tests so we don't have to set up the workspace
// every time.
func seedWorkspaceWithRoot(workspaceID, rootID string) *leapmuxv1.UserCrdtState {
	state := crdt.NewState("user-1")
	state.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
		WorkspaceId: workspaceID,
		RootNodeId:  rootID,
	}
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: rootID,
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(1, 0, "seed")))
	return state
}

// A tombstoned NODE is only observable to the hub through its tabs: a tab
// anchored to it can no longer render, because its chain no longer reaches a
// live root.
func TestProject_TombstonedNodeSkipped(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
	}, hlcAt(2, 1, "a")))
	placeTab(t, state, "t1", "child", 3)
	require.Len(t, crdt.Project(state).RenderedTabs, 1, "precondition: the tab renders while its tile lives")

	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "child"}, hlcAt(4, 0, "a")))

	proj := crdt.Project(state)
	assert.Empty(t, proj.RenderedTabs, "the tab's tile is gone, so it cannot render")
	assert.Empty(t, proj.OwnedTabs, "and its chain no longer reaches a root, so it is an orphan")
}

func TestProject_OrphansDropped(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	// Add a node whose parent_id points at a non-existent ancestor.
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "orphan",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "orphan",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "ghost"},
	}, hlcAt(2, 1, "a")))
	// Add a tab whose tile_id points at the orphan.
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-orphan",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "orphan"},
	}, hlcAt(3, 0, "a")))

	proj := crdt.Project(state)
	// The orphan tab does not pass projection — its tile resolves to
	// nothing.
	for _, tab := range proj.RenderedTabs {
		if tab.TabID == "t-orphan" {
			t.Errorf("orphan tab should not render: %+v", tab)
		}
	}
	for _, tab := range proj.OwnedTabs {
		if tab.TabID == "t-orphan" {
			t.Errorf("orphan tab should not appear in owned tabs either: %+v", tab)
		}
	}
}

func TestProject_LiveTabReachable_RendersInBothViews(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t1",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(5, 0, "a")))
	proj := crdt.Project(state)
	require.Len(t, proj.OwnedTabs, 1)
	require.Len(t, proj.RenderedTabs, 1)
	assert.Equal(t, "w1", proj.RenderedTabs[0].WorkspaceID)
	assert.Equal(t, "t1", proj.RenderedTabs[0].TabID)
}

// setNode applies one SetNodeRegisterOp. The tree-shaping tests below need
// several registers per node and read better as a list of these than as a wall
// of inline Apply calls. Takes the whole op because the oneof wrapper interface
// is unexported and cannot be named from this package.
func setNode(t *testing.T, state *leapmuxv1.UserCrdtState, phys int64, op *leapmuxv1.SetNodeRegisterOp) {
	t.Helper()
	crdt.Apply(state, stamped(op, hlcAt(phys, 0, "a")))
}

// leaf creates a live LEAF under parent at position pos.
func leaf(t *testing.T, state *leapmuxv1.UserCrdtState, id, parent, pos string, phys int64) {
	t.Helper()
	setNode(t, state, phys, &leapmuxv1.SetNodeRegisterOp{
		NodeId: id, Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	})
	setNode(t, state, phys+1, &leapmuxv1.SetNodeRegisterOp{
		NodeId: id, Field: &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: parent},
	})
	setNode(t, state, phys+2, &leapmuxv1.SetNodeRegisterOp{
		NodeId: id, Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: pos},
	})
}

// splitNode flips an existing node's kind to SPLIT.
func splitNode(t *testing.T, state *leapmuxv1.UserCrdtState, id string, phys int64) {
	t.Helper()
	setNode(t, state, phys, &leapmuxv1.SetNodeRegisterOp{
		NodeId: id, Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	})
}

// renderedTileFor returns the TileID the projection reports for tabID, plus
// whether the tab rendered at all.
func renderedTileFor(proj *crdt.Projection, tabID string) (string, bool) {
	for _, tab := range proj.RenderedTabs {
		if tab.TabID == tabID {
			return tab.TileID, true
		}
	}
	return "", false
}

func ownedTileFor(proj *crdt.Projection, tabID string) (string, bool) {
	for _, tab := range proj.OwnedTabs {
		if tab.TabID == tabID {
			return tab.TileID, true
		}
	}
	return "", false
}

// placeTab anchors a tab to a tile.
func placeTab(t *testing.T, state *leapmuxv1.UserCrdtState, tabID, tileID string, phys int64) {
	t.Helper()
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   tabID,
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID},
	}, hlcAt(phys, 0, "a")))
}

// A SPLIT with one live child renders as that child under the SPLIT's node id
// (see TestProject_SplitWithOneLiveChild_RendersAsTheChild). The child's own
// NodeRecord is untouched, so after a collapse the rendered TREE and the
// child's real id disagree.
//
// RenderedTabs deliberately keeps reporting the RAW tile_id anyway. It is an
// addressable index, not a view: it feeds `workspace_tab_rendered`, which backs
// LocateTab / GetTab / ListTabs, and the CLI derives `--tile-id` from a
// `--tab-id` through exactly that path and then emits SetTabRegister(tile_id)
// with it. `validateTabPlacement` requires a LEAF, so handing back the
// collapsed SPLIT's id would turn `tab open --after-tab <id>` into a rejected
// batch. The hub has no rendered tree of its own for the id to be "wrong"
// against.
//
// The frontend agrees: its projection reports the same raw tile_id, and its
// render tree collapses a single-child SPLIT to the surviving CHILD's id so the
// two halves of that view still name the same tile. An earlier version renamed
// the survivor to its parent, which left the tab attached to no tile at all.
// The shared conformance fixture pins the agreement; do not "fix" either side
// to remap without re-checking the CLI write path.
func TestProject_CollapsedSplit_RenderedTabKeepsTheRawTileID(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	splitNode(t, state, "root1", 2)
	leaf(t, state, "childA", "root1", "N", 10)
	leaf(t, state, "childB", "root1", "V", 20)
	placeTab(t, state, "t1", "childA", 30)

	// Close childB: root1 now has one live child and the tree collapses.
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "childB"}, hlcAt(40, 0, "a")))

	proj := crdt.Project(state)
	tile, ok := renderedTileFor(proj, "t1")
	require.True(t, ok, "the tab is still live on a live leaf, so it still renders")
	assert.Equal(t, "childA", tile,
		"RenderedTabs must stay addressable: childA is the LEAF a write may target, root1 is not")

	owned, ok := ownedTileFor(proj, "t1")
	require.True(t, ok)
	assert.Equal(t, "childA", owned, "OwnedTabs agrees -- both report what the CRDT holds")
}

// The tile RenderedTabs reports must always be a LEAF, because that is the only
// thing `validateTabPlacement` accepts as a tab's tile_id. This is the property
// the CLI's derive-tile-from-tab flow depends on.
func TestProject_RenderedTabTileIsAlwaysALeaf(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	splitNode(t, state, "root1", 2)
	leaf(t, state, "childA", "root1", "N", 10)
	leaf(t, state, "childB", "root1", "V", 20)
	placeTab(t, state, "t1", "childA", 30)
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "childB"}, hlcAt(40, 0, "a")))

	for _, tab := range crdt.Project(state).RenderedTabs {
		node := state.GetNodes()[tab.TileID]
		require.NotNil(t, node, "rendered tile %q must exist", tab.TileID)
		assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, node.GetKind().GetValue(),
			"rendered tile %q must be a leaf, or a tab placed on it would be rejected", tab.TileID)
	}
}

// A GRID never renames its children, so a grid cell keeps its own id.
func TestProject_GridChild_KeepsItsOwnTileID(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	setNode(t, state, 2, &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1", Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_GRID},
	})
	setNode(t, state, 3, &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1", Field: &leapmuxv1.SetNodeRegisterOp_Rows{Rows: 1},
	})
	setNode(t, state, 4, &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1", Field: &leapmuxv1.SetNodeRegisterOp_Cols{Cols: 2},
	})
	leaf(t, state, "cell00", "root1", "0,0", 10)
	leaf(t, state, "cell01", "root1", "0,1", 20)
	placeTab(t, state, "t1", "cell00", 30)

	tile, ok := renderedTileFor(crdt.Project(state), "t1")
	require.True(t, ok)
	assert.Equal(t, "cell00", tile)
}

// Tombstoned tabs leave both views.
func TestProject_TombstonedTabsExcluded(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-doomed",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-doomed",
	}, hlcAt(3, 0, "a")))

	proj := crdt.Project(state)
	assert.Empty(t, proj.OwnedTabs)
	assert.Empty(t, proj.RenderedTabs)
}

// A tab whose tile chain never reaches a registered root is an orphan and
// leaves BOTH views -- unlike a tab on a live-but-non-leaf tile, which stays
// owned and merely stops rendering.
func TestProject_OrphanTabsDroppedFromBothSlices(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-orphan",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost-tile"},
	}, hlcAt(2, 0, "a")))

	proj := crdt.Project(state)
	assert.Empty(t, proj.OwnedTabs)
	assert.Empty(t, proj.RenderedTabs)
}
