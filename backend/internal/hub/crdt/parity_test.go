package crdt_test

import (
	"bytes"
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// canonicalizeState produces a deterministic byte encoding for an
// OrgCrdtState by sorting maps by key, marshaling each entry, and
// concatenating the marshaled bytes. This is the recipe the plan
// prescribes for parity comparison: protojson is non-deterministic for
// maps, and proto's `Deterministic: true` only orders within a single
// marshal — so we sort and serialize manually.
//
// The exact encoding is:
//
//	[0x01][marshaled NodeRecord per node, sorted by node_id]
//	[0x02][marshaled TabRecord per tab, sorted by tab_id]
//	[0x03][marshaled FloatingWindowRecord per window, sorted by window_id]
//	[0x04][marshaled WorkspaceContentsRecord per workspace, sorted by workspace_id]
//
// Each marshaled record is preceded by its 4-byte big-endian length so
// the consumer can re-walk the stream without ambiguity.
func canonicalizeState(t *testing.T, state *leapmuxv1.OrgCrdtState) []byte {
	t.Helper()
	var buf bytes.Buffer
	mopts := proto.MarshalOptions{Deterministic: true}

	keys := make([]string, 0, len(state.GetNodes()))
	for k := range state.GetNodes() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf.WriteByte(0x01)
	for _, k := range keys {
		bs, err := mopts.Marshal(state.GetNodes()[k])
		require.NoError(t, err)
		writeLenPrefixed(&buf, bs)
	}

	keys = keys[:0]
	for k := range state.GetTabs() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf.WriteByte(0x02)
	for _, k := range keys {
		bs, err := mopts.Marshal(state.GetTabs()[k])
		require.NoError(t, err)
		writeLenPrefixed(&buf, bs)
	}

	keys = keys[:0]
	for k := range state.GetFloatingWindows() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf.WriteByte(0x03)
	for _, k := range keys {
		bs, err := mopts.Marshal(state.GetFloatingWindows()[k])
		require.NoError(t, err)
		writeLenPrefixed(&buf, bs)
	}

	keys = keys[:0]
	for k := range state.GetWorkspaces() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf.WriteByte(0x04)
	for _, k := range keys {
		bs, err := mopts.Marshal(state.GetWorkspaces()[k])
		require.NoError(t, err)
		writeLenPrefixed(&buf, bs)
	}
	return buf.Bytes()
}

func writeLenPrefixed(b *bytes.Buffer, data []byte) {
	n := uint32(len(data))
	b.WriteByte(byte(n >> 24))
	b.WriteByte(byte(n >> 16))
	b.WriteByte(byte(n >> 8))
	b.WriteByte(byte(n))
	b.Write(data)
}

// applyAllParity applies a list of validated, canonical-HLC-stamped
// ops to a fresh state with a workspace seeded. Distinct from
// commute_test.go's `applyAll` because the parity tests require a
// preseeded WorkspaceContentsRecord so node-tree projection is
// observable.
func applyAllParity(ops []*leapmuxv1.OrgOp) *leapmuxv1.OrgCrdtState {
	state := crdt.NewState("org")
	state.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w1", RootNodeId: "root1"}
	for _, op := range ops {
		crdt.Apply(state, op)
	}
	return state
}

// shuffledParity returns a permutation of ops driven by the supplied rng.
func shuffledParity(ops []*leapmuxv1.OrgOp, rng *rand.Rand) []*leapmuxv1.OrgOp {
	out := make([]*leapmuxv1.OrgOp, len(ops))
	copy(out, ops)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestParity_ManyPermutationsConverge runs several hundred random
// permutations of a 12-op log and asserts they all canonicalize to
// the same bytes. This is the by-construction property the plan calls
// "byte-equal post-state for any permutation of a validated committed
// log".
func TestParity_ManyPermutationsConverge(t *testing.T) {
	// Build a 12-op log: a couple of tabs + node mutations + tombstones.
	ops := []*leapmuxv1.OrgOp{
		stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}, hlcAt(1, 0, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
		}, hlcAt(2, 0, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w1"},
		}, hlcAt(2, 1, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p1"},
		}, hlcAt(2, 2, "a")),
		// Concurrent client b: opens a different tab.
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "tB",
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
		}, hlcAt(3, 0, "b")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "tB",
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "w1"},
		}, hlcAt(3, 1, "b")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "tB",
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p2"},
		}, hlcAt(3, 2, "b")),
		// Two concurrent ratio updates (different clients).
		stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field: &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{
				Values: []float64{0.6, 0.4},
			}},
		}, hlcAt(4, 0, "a")),
		stamped(&leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field: &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{
				Values: []float64{0.3, 0.7},
			}},
		}, hlcAt(5, 0, "b")),
		// Tombstone tA at higher HLC (remove-wins).
		stamped(&leapmuxv1.TombstoneTabOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		}, hlcAt(10, 0, "a")),
		// Late SetTab on tA (after tombstone) — must be dropped.
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "late"},
		}, hlcAt(11, 0, "a")),
		// Floating-window double register with -0.0 → +0.0 normalization.
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: "fw1",
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: math.Copysign(0, -1)},
		}, hlcAt(12, 0, "a")),
	}

	canonical := canonicalizeState(t, applyAllParity(ops))

	// 200 random permutations should all canonicalize to the same bytes.
	rng := rand.New(rand.NewSource(0x1234))
	for i := 0; i < 200; i++ {
		got := canonicalizeState(t, applyAllParity(shuffledParity(ops, rng)))
		if !bytes.Equal(canonical, got) {
			t.Fatalf("permutation %d produced different bytes (len=%d vs %d)", i, len(canonical), len(got))
		}
	}
}

// TestParity_NegativeZeroCanonicalizationIsByteEqual asserts that two
// inputs differing only in zero-sign produce byte-equal canonical
// states.
func TestParity_NegativeZeroCanonicalizationIsByteEqual(t *testing.T) {
	posOps := []*leapmuxv1.OrgOp{
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: "fw1",
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 0.0},
		}, hlcAt(1, 0, "a")),
	}
	negOps := []*leapmuxv1.OrgOp{
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: "fw1",
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: math.Copysign(0, -1)},
		}, hlcAt(1, 0, "a")),
	}
	posCanon := canonicalizeState(t, applyAllParity(posOps))
	negCanon := canonicalizeState(t, applyAllParity(negOps))
	assert.True(t, bytes.Equal(posCanon, negCanon),
		"+0.0 and -0.0 canonical bytes must match (sign-bit normalization)")
}
