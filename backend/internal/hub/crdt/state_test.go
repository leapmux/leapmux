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

// stamped wraps a body in a fully-stamped OrgOp.
func stamped(body any, hlc *leapmuxv1.HLC) *leapmuxv1.OrgOp {
	op := &leapmuxv1.OrgOp{OrgId: "org", OpId: "op-" + hlc.GetClientId(), CanonicalHlc: hlc}
	switch b := body.(type) {
	case *leapmuxv1.SetNodeRegisterOp:
		op.Body = &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: b}
	case *leapmuxv1.TombstoneNodeOp:
		op.Body = &leapmuxv1.OrgOp_TombstoneNode{TombstoneNode: b}
	case *leapmuxv1.SetTabRegisterOp:
		op.Body = &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: b}
	case *leapmuxv1.TombstoneTabOp:
		op.Body = &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: b}
	case *leapmuxv1.SetFloatingWindowRegisterOp:
		op.Body = &leapmuxv1.OrgOp_SetFloatingWindowRegister{SetFloatingWindowRegister: b}
	case *leapmuxv1.TombstoneFloatingWindowOp:
		op.Body = &leapmuxv1.OrgOp_TombstoneFloatingWindow{TombstoneFloatingWindow: b}
	}
	return op
}

func TestApply_SetNodeKind_FreshAndIdempotent(t *testing.T) {
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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

func TestApply_ParentIdSetOnce(t *testing.T) {
	state := crdt.NewState("org")
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

func TestApply_NegativeZeroNormalization(t *testing.T) {
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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
	state := crdt.NewState("org")
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
