package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// testBootstrap builds the minimal CRDTBootstrap an op builder needs:
// a state, an origin client id, and an HLC clock seeded from zero.
// Tests don't need a real epoch / max_hlc because every op is built
// in-process and inspected, not submitted.
func testBootstrap(state *leapmuxv1.OrgMaterialized) *CRDTBootstrap {
	const origin = "tile-close-test"
	return &CRDTBootstrap{
		OrgID:        "org-1",
		State:        state,
		Clock:        crdt.NewClock(origin),
		OriginClient: origin,
	}
}

// opCase summarises an OrgOp into a single string ("kind:nodeId" or
// "field:tab-id:value") so tests can assert against an order-independent
// set without unpacking the oneof at every assertion. Mirrors the
// frontend's `opCases` helper in `layout.store.crdt.test.ts`.
func opCase(op *leapmuxv1.OrgOp) string {
	switch v := op.GetBody().(type) {
	case *leapmuxv1.OrgOp_TombstoneNode:
		return "tombstoneNode:" + v.TombstoneNode.GetNodeId()
	case *leapmuxv1.OrgOp_TombstoneTab:
		return "tombstoneTab:" + v.TombstoneTab.GetTabId()
	case *leapmuxv1.OrgOp_SetNodeRegister:
		r := v.SetNodeRegister
		nodeID := r.GetNodeId()
		switch f := r.GetField().(type) {
		case *leapmuxv1.SetNodeRegisterOp_Kind:
			return "setNodeKind:" + nodeID + "=" + f.Kind.String()
		default:
			return "setNodeRegister:" + nodeID
		}
	case *leapmuxv1.OrgOp_SetTabRegister:
		r := v.SetTabRegister
		tabID := r.GetTabId()
		switch f := r.GetField().(type) {
		case *leapmuxv1.SetTabRegisterOp_TileId:
			return "setTabTileId:" + tabID + "=" + f.TileId
		case *leapmuxv1.SetTabRegisterOp_Position:
			return "setTabPosition:" + tabID
		default:
			return "setTabRegister:" + tabID
		}
	}
	return "unknown"
}

func opCases(ops []*leapmuxv1.OrgOp) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, opCase(op))
	}
	return out
}

// TestBuildCloseTileOps_InverseSplit_EmptySibling reproduces the bug
// the user hit: split a tile, then close the new empty leaf. Without
// the inverse-split the SPLIT parent is left with one live child
// (the leaf holding the original tabs), `project.ts:buildTree`
// collapses the SPLIT to render as the parent's id, and the tabs
// orphan because they still reference the (now hidden) sibling's
// tile_id. The fix: detect "parent SPLIT with exactly two live
// children" and emit migrate-tabs + tombstone-sibling +
// SetNodeKind(parent=LEAF) in the same batch.
func TestBuildCloseTileOps_InverseSplit_EmptySibling(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		node("T", "").       // SPLIT parent (workspace root)
		node("childA", "T"). // leaf with tabs
		node("childB", "T"). // empty leaf, being closed
		tab("tab-1", "childA", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "childA", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	// Mark T as a SPLIT (the builder leaves kind unset; for this test
	// it just needs to be non-LEAF and live).
	state.GetNodes()["T"].Kind = &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT}

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "childB")
	cases := opCases(ops)

	// childB is tombstoned (no tabs on it, so no tab tombstones).
	assert.Contains(t, cases, "tombstoneNode:childB", "must tombstone the closing tile")

	// Sibling's tabs migrate to the parent (T), each with a re-stamped
	// position so the LexoRank ordering survives the rewrite.
	assert.Contains(t, cases, "setTabTileId:tab-1=T", "tab-1 must migrate from childA to T")
	assert.Contains(t, cases, "setTabTileId:tab-2=T", "tab-2 must migrate from childA to T")
	assert.Contains(t, cases, "setTabPosition:tab-1", "tab positions must be re-stamped")
	assert.Contains(t, cases, "setTabPosition:tab-2", "tab positions must be re-stamped")

	// Sibling tombstoned; parent flips back to LEAF (NOT tombstoned —
	// `T` is the workspace root and the validator rejects root
	// tombstones).
	assert.Contains(t, cases, "tombstoneNode:childA", "sibling leaf must be tombstoned")
	assert.NotContains(t, cases, "tombstoneNode:T", "parent SPLIT must not be tombstoned")
	assert.Contains(t, cases, "setNodeKind:T=NODE_KIND_LEAF", "parent must flip back to LEAF")
}

// TestBuildCloseTileOps_InverseSplit_TabbedSibling covers the
// opposite case: close the leaf that holds the tabs. The user's
// intent here is "I'm done with this pane and its tabs"; the
// inverse-split still applies, but the sibling that survives is
// empty so nothing migrates.
func TestBuildCloseTileOps_InverseSplit_TabbedSibling(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		node("T", "").
		node("childA", "T").
		node("childB", "T").
		tab("tab-1", "childA", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	state.GetNodes()["T"].Kind = &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT}

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "childA")
	cases := opCases(ops)

	// childA's tabs are tombstoned (user is closing the tile that
	// holds them).
	assert.Contains(t, cases, "tombstoneTab:tab-1", "tab on closing tile must be tombstoned")
	assert.Contains(t, cases, "tombstoneNode:childA")

	// Inverse-split still fires: empty sibling tombstoned, parent
	// flips back to LEAF.
	assert.Contains(t, cases, "tombstoneNode:childB")
	assert.Contains(t, cases, "setNodeKind:T=NODE_KIND_LEAF")
}

// TestBuildCloseTileOps_NoInverseSplit_NonSplitParent guards the
// fall-through path: a tile whose parent is the workspace root (a
// LEAF in this fixture, or any non-SPLIT node) gets the plain
// tombstone treatment without inverse-split rewiring.
func TestBuildCloseTileOps_NoInverseSplit_NonSplitParent(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		node("root", "").
		node("orphanChild", "root").
		tab("tab-1", "orphanChild", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	// Leave root.kind unset (UNSPECIFIED) — the inverse-split must
	// only fire when the parent is explicitly SPLIT.

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "orphanChild")
	cases := opCases(ops)

	assert.Contains(t, cases, "tombstoneTab:tab-1")
	assert.Contains(t, cases, "tombstoneNode:orphanChild")
	// Plain close — no kind flip, no extra tombstones on siblings or
	// the parent.
	for _, c := range cases {
		assert.NotContains(t, c, "setNodeKind", "non-SPLIT parent must not trigger inverse-split")
	}
	require.NotContains(t, cases, "tombstoneNode:root")
}

// TestBuildCloseTileOps_NoInverseSplit_GridSibling guards the
// non-leaf-sibling case: the SPLIT parent has two live children, the
// other one is a GRID with its own leaf cells (each holding tabs).
// Naively tombstoning the GRID sibling here would orphan every cell
// + every tab whose tile_id is one of the cells — the validator
// rejects the batch with BATCH_REJECTION_TAB_PLACEMENT_INVALID.
//
// The right behavior is to skip the inverse-split entirely: just
// tombstone the closing leaf. The projection's single-child SPLIT
// collapse handles the rendering (the surviving GRID renders at the
// SPLIT's position, descendants intact).
//
// Reproduces a user-reported regression: closing an empty leaf next
// to a 2x2 grid populated with tabs failed with the placement-
// invalid error.
func TestBuildCloseTileOps_NoInverseSplit_GridSibling(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		node("T", "").           // SPLIT parent (workspace root)
		gridNode("G", "T", "0"). // GRID sibling
		leafNode("cellA", "G", "0,0").
		leafNode("cellB", "G", "0,1").
		leafNode("emptyLeaf", "T", "1"). // empty leaf, being closed
		tab("tab-1", "cellA", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "cellB", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	state.GetNodes()["T"].Kind = &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT}

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "emptyLeaf")
	cases := opCases(ops)

	// The closing leaf is tombstoned, no other structural changes.
	assert.Contains(t, cases, "tombstoneNode:emptyLeaf")
	// GRID sibling is untouched — tombstoning it would orphan its
	// cells and tabs.
	assert.NotContains(t, cases, "tombstoneNode:G", "GRID sibling must NOT be tombstoned")
	assert.NotContains(t, cases, "tombstoneNode:cellA", "grid cells must NOT be tombstoned")
	assert.NotContains(t, cases, "tombstoneNode:cellB", "grid cells must NOT be tombstoned")
	// No tab migration — the projection's single-child SPLIT collapse
	// handles rendering without rewiring the tabs.
	for _, c := range cases {
		assert.NotContains(t, c, "setTabTileId", "tabs must NOT migrate when sibling is non-leaf")
		assert.NotContains(t, c, "setNodeKind", "parent must NOT flip kind when sibling is non-leaf")
	}
	// Tabs themselves stay alive (the user closed the OTHER tile).
	assert.NotContains(t, cases, "tombstoneTab:tab-1")
	assert.NotContains(t, cases, "tombstoneTab:tab-2")
}

// TestBuildCloseTileOps_NoInverseSplit_SplitSibling is the same
// guarantee for a nested-SPLIT sibling: the sibling is a SPLIT with
// its own leaf children carrying tabs. Same reasoning — tombstoning
// the SPLIT would orphan its leaves and tabs.
func TestBuildCloseTileOps_NoInverseSplit_SplitSibling(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		node("T", "").
		splitNode("S", "T", "0").
		leafNode("leafA", "S", "0").
		leafNode("leafB", "S", "1").
		leafNode("emptyLeaf", "T", "1").
		tab("tab-1", "leafA", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	state.GetNodes()["T"].Kind = &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT}

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "emptyLeaf")
	cases := opCases(ops)

	assert.Contains(t, cases, "tombstoneNode:emptyLeaf")
	assert.NotContains(t, cases, "tombstoneNode:S", "SPLIT sibling must NOT be tombstoned")
	assert.NotContains(t, cases, "tombstoneNode:leafA")
	assert.NotContains(t, cases, "tombstoneNode:leafB")
	for _, c := range cases {
		assert.NotContains(t, c, "setTabTileId")
		assert.NotContains(t, c, "setNodeKind")
	}
}

// TestBuildCloseTileOps_NoInverseSplit_ThreeChildSplit guards the
// "exactly two live children" precondition: a SPLIT with three live
// children that loses one is still a multi-leaf split, so no
// collapse happens.
func TestBuildCloseTileOps_NoInverseSplit_ThreeChildSplit(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		node("T", "").
		node("childA", "T").
		node("childB", "T").
		node("childC", "T").
		tab("tab-1", "childA", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	state.GetNodes()["T"].Kind = &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT}

	bs := testBootstrap(state)
	ops := buildCloseTileOps(bs, "childB")
	cases := opCases(ops)

	assert.Contains(t, cases, "tombstoneNode:childB")
	// childA and childC are unaffected; no kind flip.
	assert.NotContains(t, cases, "tombstoneNode:childA")
	assert.NotContains(t, cases, "tombstoneNode:childC")
	for _, c := range cases {
		assert.NotContains(t, c, "setNodeKind", "3-child SPLIT must not collapse on one tombstone")
	}
}
