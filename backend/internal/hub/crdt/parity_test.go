package crdt_test

import (
	"bytes"
	"fmt"
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
// UserCrdtState by sorting maps by key, marshaling each entry, and
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
func canonicalizeState(t *testing.T, state *leapmuxv1.UserCrdtState) []byte {
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
func applyAllParity(ops []*leapmuxv1.CrdtOp) *leapmuxv1.UserCrdtState {
	state := crdt.NewState("user-1")
	state.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w1", RootNodeId: "root1"}
	for _, op := range ops {
		crdt.Apply(state, op)
	}
	return state
}

// shuffledParity returns a permutation of ops driven by the supplied rng.
func shuffledParity(ops []*leapmuxv1.CrdtOp, rng *rand.Rand) []*leapmuxv1.CrdtOp {
	out := make([]*leapmuxv1.CrdtOp, len(ops))
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
	ops := []*leapmuxv1.CrdtOp{
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
		state := applyAllParity(shuffledParity(ops, rng))
		got := canonicalizeState(t, state)
		if !bytes.Equal(canonical, got) {
			t.Fatalf("permutation %d produced different bytes (len=%d vs %d)", i, len(canonical), len(got))
		}
		// Convergence alone doesn't say the projection is sane. Check the
		// rendered-tab invariant on every permuted state too: it costs nothing
		// here and gives the property hundreds of orderings to break on.
		assertRenderedTabsAddressable(t, state, fmt.Sprintf("permutation %d", i))
	}
}

// assertRenderedTabsAddressable is the invariant that makes RenderedTabs safe
// to derive a write target from.
//
// `workspace_tab_rendered` backs LocateTab / GetTab / ListTabs, and the CLI
// turns a `--tab-id` into a `--tile-id` through it before emitting
// SetTabRegister(tile_id). `validateTabPlacement` accepts only a live LEAF, so
// every rendered tile must BE one — and must be the tab's own tile_id, not a
// relabelled stand-in. Relabelling is not hypothetical: an earlier frontend
// projection remapped a collapsed SPLIT's tabs onto the parent's node id, which
// is what made a tab render on no tile at all when a grid was closed beside it.
// Both sides now report the raw tile_id (see the collapse cases in
// testdata/crdt_projection_conformance.json); porting a remap here would break
// `tab open --after-tab` in a way no existing test noticed.
func assertRenderedTabsAddressable(t *testing.T, state *leapmuxv1.UserCrdtState, label string) {
	t.Helper()
	proj := crdt.Project(state)
	for _, tab := range proj.RenderedTabs {
		rec := state.GetTabs()[tab.TabID]
		if rec == nil {
			t.Fatalf("%s: rendered tab %q has no TabRecord", label, tab.TabID)
		}
		if raw := rec.GetTileId().GetValue(); tab.TileID != raw {
			t.Fatalf("%s: rendered tab %q reports tile %q but its tile_id register says %q -- "+
				"a relabelled tile is not writable", label, tab.TabID, tab.TileID, raw)
		}
		node := state.GetNodes()[tab.TileID]
		if node == nil {
			t.Fatalf("%s: rendered tab %q sits on unknown tile %q", label, tab.TabID, tab.TileID)
		}
		if node.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_LEAF {
			t.Fatalf("%s: rendered tab %q sits on tile %q of kind %v -- only a LEAF accepts a tab",
				label, tab.TabID, tab.TileID, node.GetKind().GetValue())
		}
		if !crdt.HLCIsZero(node.GetTombstoneAt()) {
			t.Fatalf("%s: rendered tab %q sits on tombstoned tile %q", label, tab.TabID, tab.TileID)
		}
	}
}

// TestParity_NegativeZeroCanonicalizationIsByteEqual asserts that two
// inputs differing only in zero-sign produce byte-equal canonical
// states.
func TestParity_NegativeZeroCanonicalizationIsByteEqual(t *testing.T) {
	posOps := []*leapmuxv1.CrdtOp{
		stamped(&leapmuxv1.SetFloatingWindowRegisterOp{
			WindowId: "fw1",
			Field:    &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: 0.0},
		}, hlcAt(1, 0, "a")),
	}
	negOps := []*leapmuxv1.CrdtOp{
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
