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
func seedWorkspaceWithRoot(workspaceID, rootID string) *leapmuxv1.OrgCrdtState {
	state := crdt.NewState("org")
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

func TestProject_TombstonedNodeSkipped(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	// Add a child, then tombstone it. The child should not appear in
	// the projection.
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
	}, hlcAt(2, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "child"}, hlcAt(3, 0, "a")))

	proj := crdt.Project(state)
	ws := proj.Workspaces["w1"]
	require.NotNil(t, ws)
	require.NotNil(t, ws.MainTree)
	// MainTree is the root with no children (the only child is tombstoned).
	assert.Empty(t, ws.MainTree.Children)
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

func TestProject_SplitWithOneLiveChild_RendersAsTheChild(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	// Promote root1 from LEAF to SPLIT, give it one child.
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Direction{Direction: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL},
	}, hlcAt(2, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(3, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
	}, hlcAt(3, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "child",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "N"},
	}, hlcAt(3, 2, "a")))

	proj := crdt.Project(state)
	ws := proj.Workspaces["w1"]
	require.NotNil(t, ws)
	require.NotNil(t, ws.MainTree)
	// A SPLIT with one live child renders as that child — but with
	// the SPLIT's own NodeID preserved.
	assert.Equal(t, "root1", ws.MainTree.NodeID, "node id should be preserved through visual collapse")
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, ws.MainTree.Kind, "single-child split renders as a leaf (the surviving child)")
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
