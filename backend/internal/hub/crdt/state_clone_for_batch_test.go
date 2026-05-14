package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// CloneStateForBatch's contract: top-level state + maps are fresh, but
// record values are shared with `pre` for entities not touched by the
// batch. Touched records are deep-cloned so Apply can mutate the
// working copy without writing through to `pre`.

func hlc(p, l int64, c string) *leapmuxv1.HLC {
	return &leapmuxv1.HLC{Physical: p, Logical: l, ClientId: c}
}

func nodeRec(id string) *leapmuxv1.NodeRecord {
	return &leapmuxv1.NodeRecord{
		NodeId: id,
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: hlc(1, 0, "seed")},
	}
}

func tabRec(id string) *leapmuxv1.TabRecord {
	return &leapmuxv1.TabRecord{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   id,
		TileId:  &leapmuxv1.LWWString{Value: "tile-A", Hlc: hlc(1, 0, "seed")},
	}
}

func setNodeKindOp(id string, kind leapmuxv1.NodeKind, h *leapmuxv1.HLC) *leapmuxv1.OrgOp {
	return &leapmuxv1.OrgOp{
		OpId:         "op-" + id,
		CanonicalHlc: h,
		Body: &leapmuxv1.OrgOp_SetNodeRegister{
			SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: id,
				Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: kind},
			},
		},
	}
}

func tombstoneNodeOp(id string, h *leapmuxv1.HLC) *leapmuxv1.OrgOp {
	return &leapmuxv1.OrgOp{
		OpId:         "op-tomb-" + id,
		CanonicalHlc: h,
		Body: &leapmuxv1.OrgOp_TombstoneNode{
			TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: id},
		},
	}
}

func setTabTileOp(id, tile string, h *leapmuxv1.HLC) *leapmuxv1.OrgOp {
	return &leapmuxv1.OrgOp{
		OpId:         "op-tile-" + id,
		CanonicalHlc: h,
		Body: &leapmuxv1.OrgOp_SetTabRegister{
			SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:   id,
				Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: tile},
			},
		},
	}
}

func TestCloneStateForBatch_UntouchedRecordsShareReferenceWithPre(t *testing.T) {
	// Setup: 3 nodes, batch only touches node "A". Records for "B" and
	// "C" must be the SAME pointer in pre.Nodes and working.Nodes.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"A": nodeRec("A"),
			"B": nodeRec("B"),
			"C": nodeRec("C"),
		},
		MaxHlc: hlc(10, 0, "seed"),
	}
	batch := []*leapmuxv1.OrgOp{setNodeKindOp("A", leapmuxv1.NodeKind_NODE_KIND_SPLIT, hlc(11, 0, "client"))}

	working := crdt.CloneStateForBatch(pre, batch)

	// Untouched: ref equality across pre and working.
	assert.Same(t, pre.Nodes["B"], working.Nodes["B"], "untouched record B must share ref with pre")
	assert.Same(t, pre.Nodes["C"], working.Nodes["C"], "untouched record C must share ref with pre")
	// Touched: distinct refs.
	assert.NotSame(t, pre.Nodes["A"], working.Nodes["A"], "touched record A must be a deep clone")
	// And the maps themselves are distinct (so map writes in working
	// don't leak to pre).
	assert.NotSame(t, &pre.Nodes, &working.Nodes, "working.Nodes must be a fresh map")
}

func TestCloneStateForBatch_ApplyOnWorkingDoesNotMutatePre(t *testing.T) {
	// The safety invariant: Apply on the working copy must NOT mutate
	// pre, even though Apply mutates touched records in place.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"A": nodeRec("A"),
		},
		MaxHlc: hlc(10, 0, "seed"),
	}
	originalAKind := pre.Nodes["A"].GetKind().GetValue()

	batch := []*leapmuxv1.OrgOp{setNodeKindOp("A", leapmuxv1.NodeKind_NODE_KIND_SPLIT, hlc(20, 0, "client"))}
	working := crdt.CloneStateForBatch(pre, batch)
	for _, op := range batch {
		crdt.Apply(working, op)
	}

	// pre.Nodes["A"].Kind unchanged.
	assert.Equal(t, originalAKind, pre.Nodes["A"].GetKind().GetValue(),
		"Apply on working must not write through to pre via a shared record")
	// working sees the LWW write.
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_SPLIT, working.Nodes["A"].GetKind().GetValue())
}

func TestCloneStateForBatch_TombstoneOpDoesNotMutateUntouchedRecords(t *testing.T) {
	// A tombstone op replaces the slot via state.Nodes[id] = new
	// record. Since the working map is fresh, this shouldn't affect
	// pre's map either way — but the shared "B" record must stay
	// untouched.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"A": nodeRec("A"),
			"B": nodeRec("B"),
		},
		MaxHlc: hlc(10, 0, "seed"),
	}
	batch := []*leapmuxv1.OrgOp{tombstoneNodeOp("A", hlc(20, 0, "client"))}
	working := crdt.CloneStateForBatch(pre, batch)
	for _, op := range batch {
		crdt.Apply(working, op)
	}
	// pre's "A" still has its original Kind (not tombstoned).
	assert.True(t, crdt.HLCIsZero(pre.Nodes["A"].GetTombstoneAt()),
		"pre.Nodes[A] must not be tombstoned")
	// working's "A" IS tombstoned (the slot was reassigned).
	assert.False(t, crdt.HLCIsZero(working.Nodes["A"].GetTombstoneAt()))
	// "B" is still the same pointer in both.
	assert.Same(t, pre.Nodes["B"], working.Nodes["B"])
}

func TestCloneStateForBatch_TabBatchSharesUntouchedNodeAndWindowRecords(t *testing.T) {
	pre := &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"n1": nodeRec("n1"),
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			"t1": tabRec("t1"),
			"t2": tabRec("t2"),
		},
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
			"w1": {WindowId: "w1"},
		},
		MaxHlc: hlc(10, 0, "seed"),
	}
	batch := []*leapmuxv1.OrgOp{setTabTileOp("t1", "tile-B", hlc(20, 0, "c"))}
	working := crdt.CloneStateForBatch(pre, batch)

	assert.NotSame(t, pre.Tabs["t1"], working.Tabs["t1"], "touched tab t1 must be cloned")
	assert.Same(t, pre.Tabs["t2"], working.Tabs["t2"], "untouched tab t2 must share ref")
	// Nodes / windows untouched by the batch: shared refs.
	assert.Same(t, pre.Nodes["n1"], working.Nodes["n1"])
	assert.Same(t, pre.FloatingWindows["w1"], working.FloatingWindows["w1"])
}

func TestCloneStateForBatch_EmptyBatchSharesEveryRecordRef(t *testing.T) {
	// Edge case: an empty batch produces a working state that shares
	// every record ref with pre. The validator never invokes Apply on
	// an empty batch but the function should still behave sensibly.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId: "org-1",
		Nodes: map[string]*leapmuxv1.NodeRecord{
			"A": nodeRec("A"),
		},
		MaxHlc: hlc(10, 0, "seed"),
	}
	working := crdt.CloneStateForBatch(pre, nil)
	require.NotNil(t, working)
	assert.Same(t, pre.Nodes["A"], working.Nodes["A"])
}

func TestCloneStateForBatch_NilPreReturnsNil(t *testing.T) {
	assert.Nil(t, crdt.CloneStateForBatch(nil, nil))
	assert.Nil(t, crdt.CloneStateForBatch(nil, []*leapmuxv1.OrgOp{
		setNodeKindOp("x", leapmuxv1.NodeKind_NODE_KIND_LEAF, hlc(1, 0, "c")),
	}))
}

func TestCloneStateForBatch_PreservesMaxHLCViaDeepClone(t *testing.T) {
	// MaxHlc bump on the working copy must not write through to pre.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId:  "org-1",
		MaxHlc: hlc(5, 0, "seed"),
	}
	working := crdt.CloneStateForBatch(pre, nil)
	working.MaxHlc.Physical = 999
	assert.Equal(t, int64(5), pre.GetMaxHlc().GetPhysical(),
		"working.MaxHlc mutation must not affect pre.MaxHlc")
}

func TestCloneStateForBatch_TouchedNodeNotInPreIsHarmless(t *testing.T) {
	// A SetNodeRegister op for a brand-new node id (not yet in pre) is
	// the common create-node case. CloneStateForBatch should produce a
	// working state where the new id is absent from working.Nodes —
	// Apply's ensureNode will then create it inside the working map
	// without touching pre.
	pre := &leapmuxv1.OrgCrdtState{
		OrgId:  "org-1",
		Nodes:  map[string]*leapmuxv1.NodeRecord{},
		MaxHlc: hlc(0, 0, "seed"),
	}
	batch := []*leapmuxv1.OrgOp{setNodeKindOp("fresh", leapmuxv1.NodeKind_NODE_KIND_LEAF, hlc(1, 0, "c"))}
	working := crdt.CloneStateForBatch(pre, batch)
	_, hasFresh := working.Nodes["fresh"]
	assert.False(t, hasFresh, "fresh node id must not appear until Apply creates it")
	for _, op := range batch {
		crdt.Apply(working, op)
	}
	assert.NotNil(t, working.Nodes["fresh"], "Apply must create the fresh node in working")
	_, leakedToPre := pre.Nodes["fresh"]
	assert.False(t, leakedToPre, "creating fresh node in working must not leak to pre")
}
