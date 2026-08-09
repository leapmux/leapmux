package crdt_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// hlcAt builds a canonical HLC for use in tests; client_id matters
// for tie-break behavior so we let callers pass it explicitly.
func hlcAt(physical, logical int64, clientID string) *leapmuxv1.HLC {
	return &leapmuxv1.HLC{Physical: physical, Logical: logical, ClientId: clientID}
}

// stamped wraps a body in a fully-stamped CrdtOp.
func stamped(body any, hlc *leapmuxv1.HLC) *leapmuxv1.CrdtOp {
	op := &leapmuxv1.CrdtOp{OpId: "op-" + hlc.GetClientId(), CanonicalHlc: hlc}
	switch b := body.(type) {
	case *leapmuxv1.SetNodeRegisterOp:
		op.Body = &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: b}
	case *leapmuxv1.TombstoneNodeOp:
		op.Body = &leapmuxv1.CrdtOp_TombstoneNode{TombstoneNode: b}
	case *leapmuxv1.SetTabRegisterOp:
		op.Body = &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: b}
	case *leapmuxv1.TombstoneTabOp:
		op.Body = &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: b}
	case *leapmuxv1.ReviveTabOp:
		op.Body = &leapmuxv1.CrdtOp_ReviveTab{ReviveTab: b}
	case *leapmuxv1.SetFloatingWindowRegisterOp:
		op.Body = &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: b}
	case *leapmuxv1.TombstoneFloatingWindowOp:
		op.Body = &leapmuxv1.CrdtOp_TombstoneFloatingWindow{TombstoneFloatingWindow: b}
	}
	return op
}

func TestApply_SetNodeKind_FreshAndIdempotent(t *testing.T) {
	state := crdt.NewState("user-1")
	op := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(10, 0, "a"))
	crdt.Apply(state, op)
	require.NotNil(t, state.Nodes["n1"])
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, state.Nodes["n1"].GetKind().GetValue())

	// Re-applying the same op is a no-op (HLC equal, not greater).
	crdt.Apply(state, op)
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, state.Nodes["n1"].GetKind().GetValue())
}

func TestApply_LWWHigherHLCWins(t *testing.T) {
	state := crdt.NewState("user-1")
	first := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "A"},
	}, hlcAt(10, 0, "a"))
	second := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "B"},
	}, hlcAt(20, 0, "b"))
	crdt.Apply(state, first)
	crdt.Apply(state, second)
	assert.Equal(t, "B", state.Nodes["n1"].GetPosition().GetValue())
}

func TestApply_LWWLowerHLCDrops(t *testing.T) {
	state := crdt.NewState("user-1")
	high := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "B"},
	}, hlcAt(20, 0, "b"))
	low := stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "A"},
	}, hlcAt(10, 0, "a"))
	crdt.Apply(state, high)
	crdt.Apply(state, low)
	assert.Equal(t, "B", state.Nodes["n1"].GetPosition().GetValue())
}

func TestApply_TombstoneClearsRegistersAndDropsLaterOps(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "A"},
	}, hlcAt(10, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "n1"}, hlcAt(20, 0, "a")))
	rec := state.Nodes["n1"]
	require.NotNil(t, rec)
	assert.False(t, crdt.HLCIsZero(rec.GetTombstoneAt()))
	// Position register should be cleared post-tombstone.
	assert.Nil(t, rec.GetPosition())

	// A later Set drops.
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "C"},
	}, hlcAt(30, 0, "a")))
	assert.Nil(t, state.Nodes["n1"].GetPosition())
}

func TestApply_TombstoneEarlierThanCurrentSet_DropsTheSet(t *testing.T) {
	state := crdt.NewState("user-1")
	// Tombstone first at HLC 30.
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "n1"}, hlcAt(30, 0, "a")))
	// A Set at HLC 20 lands afterwards (out-of-order delivery).
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "X"},
	}, hlcAt(20, 0, "a")))
	rec := state.Nodes["n1"]
	require.NotNil(t, rec)
	assert.Nil(t, rec.GetPosition(), "set after tombstone (any HLC) must drop")
	assert.False(t, crdt.HLCIsZero(rec.GetTombstoneAt()))
}

func TestApply_ReviveTabClearsTombstone(t *testing.T) {
	state := crdt.NewState("user-1")
	// Place a tab then tombstone it.
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabId:   "tab-1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-1"},
	}, hlcAt(10, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(20, 0, "a")))
	require.False(t, crdt.HLCIsZero(state.Tabs["tab-1"].GetTombstoneAt()))

	// A revive newer than the tombstone clears it. The record stays (a same-
	// batch Set repopulates the registers).
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(30, 0, "a")))
	rec := state.Tabs["tab-1"]
	require.NotNil(t, rec)
	assert.True(t, crdt.HLCIsZero(rec.GetTombstoneAt()), "revive newer than tombstone clears it")
	// tab_type is preserved.
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, rec.GetTabType())

	// A later Set now lands (the tab is live again).
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabId:   "tab-1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-2"},
	}, hlcAt(40, 0, "a")))
	assert.Equal(t, "tile-2", state.Tabs["tab-1"].GetTileId().GetValue())
}

func TestApply_ReviveTabOlderThanTombstoneIsNoOp(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(50, 0, "a")))
	// Revive at an older HLC: remove-wins for concurrent, older-revive no-op.
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(40, 0, "a")))
	assert.False(t, crdt.HLCIsZero(state.Tabs["tab-1"].GetTombstoneAt()), "older revive does not clear tombstone")
}

// TestApply_TombstoneOlderThanReviveIsNoOp verifies the LWW-on-revive rule: a
// tombstone whose HLC is older than a successful revive cannot re-tombstone the
// tab. Without recording the revive's HLC (revived_at), the tombstone register
// had no "last write" to compare against once cleared, so any tombstone -- even
// one older than the revive -- would re-close a revived tab.
func TestApply_TombstoneOlderThanReviveIsNoOp(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(10, 0, "a")))
	// Revive at HLC 30 clears the tombstone and records revived_at = 30.
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(30, 0, "a")))
	assert.True(t, crdt.HLCIsZero(state.Tabs["tab-1"].GetTombstoneAt()), "revive cleared the tombstone")

	// A tombstone at HLC 20 (older than the revive at 30, newer than the
	// original tombstone at 10) must NOT re-tombstone -- the revive won.
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(20, 0, "a")))
	rec := state.Tabs["tab-1"]
	require.NotNil(t, rec)
	assert.True(t, crdt.HLCIsZero(rec.GetTombstoneAt()), "tombstone older than the revive does not re-close a revived tab")

	// A tombstone strictly newer than the revive still wins (genuine close).
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(40, 0, "a")))
	assert.False(t, crdt.HLCIsZero(state.Tabs["tab-1"].GetTombstoneAt()), "tombstone newer than the revive re-closes")
}

// TestApply_RedeliveredOlderReviveDoesNotRegressRevivedAt verifies the
// revived_at register is a monotone max: a redelivered or out-of-order revive
// older than the last successful revive cannot regress revived_at. Without the
// max, a regressed revived_at would let a later tombstone (newer than the
// stale revive, older than the real one) pass both LWW gates in
// applyTombstoneTab and re-close a tab the user reopened.
func TestApply_RedeliveredOlderReviveDoesNotRegressRevivedAt(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(10, 0, "a")))
	// Successful revive at HLC 30 clears the tombstone and sets revived_at = 30.
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(30, 0, "a")))
	wantRevived := hlcAt(30, 0, "a")
	assert.Equal(t, wantRevived, state.Tabs["tab-1"].GetRevivedAt(), "revived_at recorded as the winning revive HLC")

	// A redelivered revive at HLC 25 (older than 30, newer than the cleared
	// tombstone) must NOT regress revived_at to 25.
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(25, 0, "a")))
	assert.Equal(t, wantRevived, state.Tabs["tab-1"].GetRevivedAt(), "older redelivered revive does not regress revived_at")

	// A tombstone at HLC 27 (newer than the stale revive at 25, older than the
	// real revive at 30) must NOT re-close the tab. If revived_at had regressed
	// to 25, this tombstone would wrongly pass the revived_at gate.
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabId: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}, hlcAt(27, 0, "a")))
	assert.True(t, crdt.HLCIsZero(state.Tabs["tab-1"].GetTombstoneAt()), "tombstone older than the real revive does not re-close")
}

func TestApply_ReviveTabAbsentMaterializesLive(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.ReviveTabOp{
		Tab: &leapmuxv1.TabRef{TabId: "tab-new", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	}, hlcAt(10, 0, "a")))
	rec := state.Tabs["tab-new"]
	require.NotNil(t, rec)
	assert.True(t, crdt.HLCIsZero(rec.GetTombstoneAt()), "revive of unseen tab is live")
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, rec.GetTabType())
}

func TestApply_ParentIdSetOnce(t *testing.T) {

	state := crdt.NewState("user-1")
	// First parent_id write lands.
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "P1"},
	}, hlcAt(10, 0, "a")))
	assert.Equal(t, "P1", state.Nodes["n1"].GetParentId())

	// Second parent_id write at higher HLC is silently ignored
	// (set-once at the Apply layer; the validator rejects earlier).
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "n1",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "P2"},
	}, hlcAt(20, 0, "b")))
	assert.Equal(t, "P1", state.Nodes["n1"].GetParentId())
}

func TestApply_SetWorkspaceRegister_SeedsEmptyRecord(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, &leapmuxv1.CrdtOp{
		OpId: "seed-w1", CanonicalHlc: hlcAt(1, 0, "hub"),
		Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{
			SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"},
		},
	})
	rec, ok := state.Workspaces["w1"]
	require.True(t, ok, "workspace record should be seeded")
	assert.Equal(t, "w1", rec.GetWorkspaceId())
	assert.Empty(t, rec.GetRootNodeId(), "root_node_id should start empty")
}

func TestApply_SetWorkspaceRegister_IdempotentDoesNotClobberRootedRecord(t *testing.T) {
	// A rooted workspace (root_node_id already set by SetWorkspaceRootNodeOp
	// or seeded directly) must NOT be clobbered by a re-drained create seed
	// whose SetWorkspaceRegisterOp re-applies after a transient consume fault.
	state := crdt.NewState("user-1")
	state.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{
		WorkspaceId: "w1", RootNodeId: "root1",
	}
	crdt.Apply(state, &leapmuxv1.CrdtOp{
		OpId: "reseed-w1", CanonicalHlc: hlcAt(2, 0, "hub"),
		Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{
			SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{WorkspaceId: "w1"},
		},
	})
	assert.Equal(t, "root1", state.Workspaces["w1"].GetRootNodeId(),
		"a re-applied SetWorkspaceRegisterOp must not clear an existing root_node_id")
}

func TestApply_TombstoneWorkspace_RemovesRecord(t *testing.T) {
	state := crdt.NewState("user-1")
	state.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w1", RootNodeId: "root1"}
	crdt.Apply(state, &leapmuxv1.CrdtOp{
		OpId: "del-w1", CanonicalHlc: hlcAt(3, 0, "hub"),
		Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
		},
	})
	_, ok := state.Workspaces["w1"]
	assert.False(t, ok, "workspace record should be removed")
}

func TestApply_TombstoneWorkspace_IdempotentOnAbsent(t *testing.T) {
	// Deleting a workspace whose record is already gone (re-drain after a
	// consume fault) must be a no-op, not a panic.
	state := crdt.NewState("user-1")
	crdt.Apply(state, &leapmuxv1.CrdtOp{
		OpId: "del-w1", CanonicalHlc: hlcAt(3, 0, "hub"),
		Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{
			TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: "w1"},
		},
	})
	assert.Empty(t, state.Workspaces)
}

func TestApply_NegativeZeroNormalization(t *testing.T) {
	state := crdt.NewState("user-1")
	// math.Copysign(0, -1) is the only portable way to construct
	// -0.0 in Go: the literal `-0.0` is equal to `+0.0` per the
	// IEEE-754 comparison rule and staticcheck flags it.
	negZero := math.Copysign(0, -1)
	crdt.Apply(state, stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
		WindowId: "w1",
		Field:    &leapmuxv1.SetFloatingWindowRegisterOp_X{X: negZero},
	}, hlcAt(10, 0, "a")))
	rec := state.FloatingWindows["w1"]
	require.NotNil(t, rec)
	// `-0.0` and `+0.0` compare equal under `==`; we want the bit-pattern
	// to be `+0.0` so subsequent serialization is byte-equal across
	// permutations.
	assert.False(t, signBit(rec.GetX().GetValue()), "X should be +0.0, not -0.0")
}

// signBit reports whether v's IEEE-754 sign bit is set.
func signBit(v float64) bool {
	if v != 0 {
		return v < 0
	}
	// For zero values, only -0.0 has the sign bit set.
	return 1/v < 0
}

func TestApply_TabRegisterTileIDLWW(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t1",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-A"},
	}, hlcAt(10, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t1",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-B"},
	}, hlcAt(20, 0, "b")))
	assert.Equal(t, "tile-B", state.Tabs["t1"].GetTileId().GetValue())
}

func TestApply_TabTypeMismatchDropsSilently(t *testing.T) {
	state := crdt.NewState("user-1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "t1",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-A"},
	}, hlcAt(10, 0, "a")))
	// A Set with the wrong TabType must drop. The validator rejects
	// such ops upstream; Apply's defense-in-depth behavior is silent
	// drop so byte-equal parity holds even on malformed inputs.
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabId:   "t1",
		Field:   &leapmuxv1.SetTabRegisterOp_TileId{TileId: "tile-B"},
	}, hlcAt(20, 0, "b")))
	assert.Equal(t, "tile-A", state.Tabs["t1"].GetTileId().GetValue())
}

func TestHLCCmp(t *testing.T) {
	a := hlcAt(10, 0, "a")
	b := hlcAt(10, 1, "a")
	c := hlcAt(11, 0, "a")
	d := hlcAt(10, 0, "b")
	assert.Equal(t, -1, crdt.HLCCmp(a, b))
	assert.Equal(t, 1, crdt.HLCCmp(b, a))
	assert.Equal(t, -1, crdt.HLCCmp(b, c))
	assert.Equal(t, -1, crdt.HLCCmp(a, d))
	assert.Equal(t, 0, crdt.HLCCmp(a, hlcAt(10, 0, "a")))
}

func TestClock_TickMonotonic(t *testing.T) {
	c := crdt.NewClock("client-1")
	first := c.Tick(100)
	second := c.Tick(100) // same physical → logical bumps
	third := c.Tick(200)  // physical advances → logical resets
	assert.Equal(t, int64(0), first.GetLogical())
	assert.Equal(t, int64(1), second.GetLogical())
	assert.Equal(t, int64(0), third.GetLogical())
	assert.Equal(t, int64(200), third.GetPhysical())
}

func TestClock_ObserveAdvancesPast(t *testing.T) {
	c := crdt.NewClock("client-1")
	c.Tick(100)
	c.Observe(hlcAt(500, 7, "other"))
	next := c.Tick(100)
	// Now's still 100 but the clock observed 500 — must produce a
	// strictly-greater HLC.
	assert.Equal(t, int64(500), next.GetPhysical())
	assert.Equal(t, int64(8), next.GetLogical())
}
