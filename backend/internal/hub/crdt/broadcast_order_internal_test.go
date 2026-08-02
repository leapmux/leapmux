package crdt

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Go map iteration is randomized, so the ordering `batchFanout.sendTo`
// depends on cannot be pinned from the black-box side -- a test that submitted a
// batch and inspected the frames would pass by luck. This asserts the ordering
// function directly.
func TestOrderedAffectedRefs_NodesBeforeWindowsBeforeTabs(t *testing.T) {
	affected := map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindTab, TabID: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}: {},
		{Kind: EntityKindNode, NodeID: "n2"}:                                          {},
		{Kind: EntityKindFloatingWindow, WindowID: "w1"}:                              {},
		{Kind: EntityKindNode, NodeID: "n1"}:                                          {},
		{Kind: EntityKindTab, TabID: "t0", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}: {},
	}

	// Run it repeatedly: a single pass could match by chance under one map seed.
	for range 32 {
		got := orderedAffectedRefs(affected)
		require.Len(t, got, 5)

		kinds := make([]EntityKind, len(got))
		for i, a := range got {
			kinds[i] = a.ref.Kind
		}
		assert.Equal(t, []EntityKind{
			EntityKindNode, EntityKindNode,
			EntityKindFloatingWindow,
			EntityKindTab, EntityKindTab,
		}, kinds, "a tab's tile_id names a node, so every node must materialize first")

		// And the order is total, not merely grouped -- otherwise two runs of the
		// same broadcast could still emit frames in different orders.
		assert.Equal(t, "n1", got[0].ref.NodeID)
		assert.Equal(t, "n2", got[1].ref.NodeID)
		assert.Equal(t, "t0", got[3].ref.TabID)
		assert.Equal(t, "t1", got[4].ref.TabID)
	}
}

// WorkspaceID is the only identity a WorkspaceRoot ref carries, so without a
// tie-break on it two such refs compare equal and sort.Slice -- which is not
// stable -- orders them arbitrarily. No frame is emitted for that kind today, so
// this pins the comparator's totality by construction rather than by the accident
// of which kinds currently have frame arms.
func TestOrderedAffectedRefs_TotalOrderAcrossWorkspaceRoots(t *testing.T) {
	affected := map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws-c"}: {},
		{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws-a"}: {},
		{Kind: EntityKindWorkspaceRoot, WorkspaceID: "ws-b"}: {},
	}

	for range 32 {
		got := orderedAffectedRefs(affected)
		require.Len(t, got, 3)

		ids := make([]string, len(got))
		for i, a := range got {
			ids[i] = a.ref.WorkspaceID
		}
		assert.Equal(t, []string{"ws-a", "ws-b", "ws-c"}, ids,
			"refs that differ only in WorkspaceID must still have a total order")
	}
}

func TestOrderedAffectedRefs_CarriesTheTransition(t *testing.T) {
	// The two passes read `trans` off the slice rather than re-hashing the map;
	// if the pairing were lost, every visibility test would silently see the
	// zero transition (invisible on both sides) and no frame would be sent.
	ref := EntityRef{Kind: EntityKindNode, NodeID: "n1"}
	got := orderedAffectedRefs(map[EntityRef]EntityWorkspaceTransition{
		ref: {Pre: "", Post: "w1"},
	})

	require.Len(t, got, 1)
	assert.Equal(t, ref, got[0].ref)
	assert.Equal(t, "w1", got[0].trans.Post)
}

// The >64-workspace guard on batchEventCache is a SOUNDNESS bound, not an
// allocation one.
//
// subscriberWorkspaceMask packs bit i per workspace key, and in Go a uint64
// shift of >= 64 evaluates to 0. So past 64 keys the mask stops distinguishing
// subscribers: one that sees ONLY the 65th workspace collapses onto the same
// mask as one that sees nothing, and a cache keyed on it would hand the first
// subscriber the second's (empty) filtered batch -- or the reverse. That is why
// broadcastBatch leaves the cache nil there and batchEventFor keeps a separate
// arm for it.
//
// This asserts the aliasing directly, so the arm cannot be "simplified" away on
// the assumption that the threshold is about allocation.
func TestSubscriberWorkspaceMask_AliasesPast64Keys(t *testing.T) {
	keys := make([]string, 65)
	for i := range keys {
		keys[i] = fmt.Sprintf("ws-%d", i)
	}
	only65th := &Subscriber{Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"ws-64": true}}}
	none := &Subscriber{Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{}}}

	require.NotEqual(t, only65th.Filter.IsAllowed("ws-64"), none.Filter.IsAllowed("ws-64"),
		"the two subscribers really do disagree about ws-64")
	assert.Equal(t, subscriberWorkspaceMask(none, keys), subscriberWorkspaceMask(only65th, keys),
		"bit 64 is unrepresentable, which is exactly why the batch cache must be disabled past 64 keys")
}
