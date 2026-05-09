package crdt_test

import (
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// canonicalize returns a deterministic byte representation of a
// post-Apply state. Walks each map in sorted key order and binary-
// marshals each entry with proto.MarshalOptions{Deterministic: true}.
// The hashes match across permutations IFF the state is invariant
// under op order — that's the property the parity test asserts.
func canonicalize(t *testing.T, state *leapmuxv1.OrgCrdtState) []byte {
	t.Helper()
	var sb strings.Builder
	marshaler := proto.MarshalOptions{Deterministic: true}

	sb.WriteString("nodes:")
	keys := make([]string, 0, len(state.GetNodes()))
	for k := range state.GetNodes() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k + "=")
		b, err := marshaler.Marshal(state.GetNodes()[k])
		require.NoError(t, err)
		sb.Write(b)
		sb.WriteString(";")
	}

	sb.WriteString("|tabs:")
	keys = keys[:0]
	for k := range state.GetTabs() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k + "=")
		b, err := marshaler.Marshal(state.GetTabs()[k])
		require.NoError(t, err)
		sb.Write(b)
		sb.WriteString(";")
	}

	sb.WriteString("|fws:")
	keys = keys[:0]
	for k := range state.GetFloatingWindows() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k + "=")
		b, err := marshaler.Marshal(state.GetFloatingWindows()[k])
		require.NoError(t, err)
		sb.Write(b)
		sb.WriteString(";")
	}
	return []byte(sb.String())
}

// applyAll applies an op stream to a fresh state.
func applyAll(t *testing.T, ops []*leapmuxv1.OrgOp) *leapmuxv1.OrgCrdtState {
	t.Helper()
	state := crdt.NewState("org")
	for _, op := range ops {
		crdt.Apply(state, op)
	}
	return state
}

// shuffleOps returns a fresh slice with `ops` permuted using the
// supplied seed. Each op is cloned so per-permutation Apply calls
// don't share mutable state through the proto map fields.
func shuffleOps(seed int64, ops []*leapmuxv1.OrgOp) []*leapmuxv1.OrgOp {
	r := rand.New(rand.NewSource(seed))
	out := make([]*leapmuxv1.OrgOp, len(ops))
	copy(out, ops)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	cloned := make([]*leapmuxv1.OrgOp, len(out))
	for i, op := range out {
		cloned[i] = proto.Clone(op).(*leapmuxv1.OrgOp)
	}
	return cloned
}

// TestCommute_PermutedSetsConverge verifies that every permutation of
// a validated op log produces byte-equal canonicalized state. This
// is the headline CRDT property.
func TestCommute_PermutedSetsConverge(t *testing.T) {
	ops := []*leapmuxv1.OrgOp{
		stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n1", Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "A"}}, hlcAt(10, 0, "a")),
		stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n1", Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "B"}}, hlcAt(20, 0, "b")),
		stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n2", Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}}, hlcAt(15, 0, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1", Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "n2"}}, hlcAt(25, 0, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1", Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "z"}}, hlcAt(30, 0, "b")),
	}

	baseline := canonicalize(t, applyAll(t, shuffleOps(0, ops)))
	for seed := int64(1); seed < 50; seed++ {
		shuffled := shuffleOps(seed, ops)
		got := canonicalize(t, applyAll(t, shuffled))
		assert.Equal(t, string(baseline), string(got), "permutation seed=%d diverged", seed)
	}
}

// TestCommute_TombstonePairs verifies the remove-wins property
// across all (Set, Tombstone) pairings: applied in either order, the
// result should be a tombstoned record with all other registers
// cleared.
func TestCommute_TombstonePairs(t *testing.T) {
	cases := []struct {
		name string
		ops  []*leapmuxv1.OrgOp
	}{
		{
			name: "node set then tombstone",
			ops: []*leapmuxv1.OrgOp{
				stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n1", Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "A"}}, hlcAt(5, 0, "a")),
				stamped(&leapmuxv1.TombstoneNodeOp{NodeId: "n1"}, hlcAt(10, 0, "a")),
			},
		},
		{
			name: "tab set then tombstone",
			ops: []*leapmuxv1.OrgOp{
				stamped(&leapmuxv1.SetTabRegisterOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1", Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "n2"}}, hlcAt(5, 0, "a")),
				stamped(&leapmuxv1.TombstoneTabOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t1"}, hlcAt(10, 0, "a")),
			},
		},
		{
			name: "floating window set then tombstone",
			ops: []*leapmuxv1.OrgOp{
				stamped(&leapmuxv1.SetFloatingWindowRegisterOp{WindowId: "w1", Field: &leapmuxv1.SetFloatingWindowRegisterOp_X{X: 0.5}}, hlcAt(5, 0, "a")),
				stamped(&leapmuxv1.TombstoneFloatingWindowOp{WindowId: "w1"}, hlcAt(10, 0, "a")),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := canonicalize(t, applyAll(t, tc.ops))
			reversed := []*leapmuxv1.OrgOp{tc.ops[1], tc.ops[0]}
			b := canonicalize(t, applyAll(t, reversed))
			assert.Equal(t, string(a), string(b), "remove-wins must be order-independent")
		})
	}
}

// TestCommute_ConcurrentSetsAtSameHLC verifies that two ops with
// identical (physical, logical) but different client_ids tie-break
// deterministically by client_id.
func TestCommute_ConcurrentSetsAtSameHLC(t *testing.T) {
	a := stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n1", Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "from-a"}}, hlcAt(10, 0, "alpha"))
	b := stamped(&leapmuxv1.SetNodeRegisterOp{NodeId: "n1", Field: &leapmuxv1.SetNodeRegisterOp_Position{Position: "from-b"}}, hlcAt(10, 0, "bravo"))

	state1 := applyAll(t, []*leapmuxv1.OrgOp{a, b})
	state2 := applyAll(t, []*leapmuxv1.OrgOp{b, a})
	assert.Equal(t, "from-b", state1.GetNodes()["n1"].GetPosition().GetValue(), "client_id=bravo should win the tie-break")
	assert.Equal(t, "from-b", state2.GetNodes()["n1"].GetPosition().GetValue())
}
