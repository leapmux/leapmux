package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// ProjectOwnership is the commit-path fast variant of Project: it
// computes OwnedTabs + RenderedTabs (everything DiffProjection reads)
// without walking the render trees. These tests pin its contract
// against the full Project so we can't silently drift.

func TestProjectOwnership_ReturnsSameTabsAsFullProject(t *testing.T) {
	// Build a workspace with a SPLIT root, two LEAF children, and an
	// AGENT tab on one of the leaves.
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Direction{Direction: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL},
	}, hlcAt(2, 1, "a")))
	for _, child := range []string{"leaf-A", "leaf-B"} {
		crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: child,
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}, hlcAt(3, 0, child)))
		crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: child,
			Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
		}, hlcAt(3, 1, child)))
		crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: child,
			Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: child},
		}, hlcAt(3, 2, child)))
	}
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "tab-X",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "leaf-A"},
	}, hlcAt(4, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "tab-X",
		Field:   &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w-1"},
	}, hlcAt(4, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "tab-X",
		Field:   &leapmuxv1.SetTabRegisterOp_Position{Position: "p0"},
	}, hlcAt(4, 2, "a")))

	full := crdt.Project(state)
	fast := crdt.ProjectOwnership(state)

	// Full and fast must agree on OwnedTabs and RenderedTabs.
	assert.Equal(t, full.OwnedTabs, fast.OwnedTabs, "OwnedTabs must match")
	assert.Equal(t, full.RenderedTabs, fast.RenderedTabs, "RenderedTabs must match")
	assert.Equal(t, full.OrgID, fast.OrgID)

	// Fast must NOT populate per-workspace MainTree / FloatingWindows
	// — that's the whole point of the split.
	assert.Empty(t, fast.Workspaces, "ProjectOwnership must not build render trees")
}

func TestProjectOwnership_DiffProjectionMatchesFullProjectDiff(t *testing.T) {
	// The diff fed to journal commit must be identical whether we use
	// Project or ProjectOwnership upstream — DiffProjection only reads
	// OwnedTabs + RenderedTabs.
	prev := seedWorkspaceWithRoot("w1", "root1")
	next := seedWorkspaceWithRoot("w1", "root1")
	// Same state in prev/next initially, then add a tab to next.
	crdt.Apply(next, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-fresh",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(5, 0, "a")))
	crdt.Apply(next, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-fresh",
		Field:   &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w-1"},
	}, hlcAt(5, 1, "a")))
	crdt.Apply(next, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-fresh",
		Field:   &leapmuxv1.SetTabRegisterOp_Position{Position: "p0"},
	}, hlcAt(5, 2, "a")))

	diffFull := crdt.DiffProjection(crdt.Project(prev), crdt.Project(next))
	diffFast := crdt.DiffProjection(crdt.ProjectOwnership(prev), crdt.ProjectOwnership(next))

	assert.Equal(t, diffFull.OwnedUpserts, diffFast.OwnedUpserts)
	assert.Equal(t, diffFull.OwnedDeletes, diffFast.OwnedDeletes)
	assert.Equal(t, diffFull.RenderedUpserts, diffFast.RenderedUpserts)
	assert.Equal(t, diffFull.RenderedDeletes, diffFast.RenderedDeletes)
}

func TestProjectOwnership_TombstonedTabsExcluded(t *testing.T) {
	// Edge case: tombstoned tabs must not appear in Owned or Rendered.
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
	fast := crdt.ProjectOwnership(state)
	assert.Empty(t, fast.OwnedTabs)
	assert.Empty(t, fast.RenderedTabs)
}

func TestProjectOwnership_OrphanTabsDroppedFromBothSlices(t *testing.T) {
	// A tab whose tile doesn't reach a registered root is dropped from
	// BOTH owned and rendered — same as full Project.
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t-orphan",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost-tile"},
	}, hlcAt(2, 0, "a")))
	full := crdt.Project(state)
	fast := crdt.ProjectOwnership(state)
	assert.Empty(t, fast.OwnedTabs)
	assert.Empty(t, fast.RenderedTabs)
	assert.Equal(t, full.OwnedTabs, fast.OwnedTabs)
}
