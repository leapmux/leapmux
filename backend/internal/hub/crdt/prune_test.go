package crdt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// helper: tombstone an entity at hlc by setting tombstone_at directly,
// bypassing Apply. Tests use this to assemble pruning fixtures without
// driving the full op pipeline.
func tombstoneNodeAt(state *leapmuxv1.OrgCrdtState, nodeID string, hlc *leapmuxv1.HLC) {
	rec := state.GetNodes()[nodeID]
	if rec == nil {
		rec = &leapmuxv1.NodeRecord{NodeId: nodeID}
		state.Nodes[nodeID] = rec
	}
	rec.TombstoneAt = hlc
}

func tombstoneTabAt(state *leapmuxv1.OrgCrdtState, tabID string, hlc *leapmuxv1.HLC) {
	rec := state.GetTabs()[tabID]
	if rec == nil {
		rec = &leapmuxv1.TabRecord{TabId: tabID}
		state.Tabs[tabID] = rec
	}
	rec.TombstoneAt = hlc
}

func tombstoneFwAt(state *leapmuxv1.OrgCrdtState, windowID string, hlc *leapmuxv1.HLC) {
	rec := state.GetFloatingWindows()[windowID]
	if rec == nil {
		rec = &leapmuxv1.FloatingWindowRecord{WindowId: windowID}
		state.FloatingWindows[windowID] = rec
	}
	rec.TombstoneAt = hlc
}

func TestPruneTombstones_DropsRecordsAtOrBelowWatermark(t *testing.T) {
	state := crdt.NewState("org")
	state.Nodes["live"] = &leapmuxv1.NodeRecord{NodeId: "live"}
	tombstoneNodeAt(state, "old-node", hlcAt(5, 0, "a"))
	tombstoneNodeAt(state, "boundary-node", hlcAt(10, 0, "a"))
	tombstoneNodeAt(state, "future-node", hlcAt(20, 0, "a"))
	tombstoneTabAt(state, "old-tab", hlcAt(7, 0, "a"))
	tombstoneFwAt(state, "old-fw", hlcAt(3, 0, "a"))

	pruned := crdt.PruneTombstonesAtOrBelow(state, hlcAt(10, 0, "a"))

	assert.Equal(t, 4, pruned)
	assert.NotContains(t, state.GetNodes(), "old-node")
	assert.NotContains(t, state.GetNodes(), "boundary-node")
	assert.Contains(t, state.GetNodes(), "future-node", "above-watermark tombstones survive")
	assert.Contains(t, state.GetNodes(), "live", "live records survive")
	assert.NotContains(t, state.GetTabs(), "old-tab")
	assert.NotContains(t, state.GetFloatingWindows(), "old-fw")
}

func TestPruneTombstones_ZeroWatermarkIsNoOp(t *testing.T) {
	state := crdt.NewState("org")
	tombstoneNodeAt(state, "n1", hlcAt(5, 0, "a"))

	// Zero HLC ≤ any tombstone HLC, but pruning with a zero watermark
	// is the "nothing to do yet" case (manager has never compacted).
	pruned := crdt.PruneTombstonesAtOrBelow(state, &leapmuxv1.HLC{})
	assert.Equal(t, 0, pruned)
	assert.Contains(t, state.GetNodes(), "n1")

	pruned = crdt.PruneTombstonesAtOrBelow(state, nil)
	assert.Equal(t, 0, pruned)
	assert.Contains(t, state.GetNodes(), "n1")
}

func TestPruneTombstones_PreservesLiveRecords(t *testing.T) {
	state := crdt.NewState("org")
	// Node with a live (non-tombstoned) record but a never-applied
	// tombstone_at zero value — must not be pruned regardless of
	// watermark.
	state.Nodes["live"] = &leapmuxv1.NodeRecord{NodeId: "live"}
	state.Tabs["live-tab"] = &leapmuxv1.TabRecord{TabId: "live-tab"}
	state.FloatingWindows["live-fw"] = &leapmuxv1.FloatingWindowRecord{WindowId: "live-fw"}

	pruned := crdt.PruneTombstonesAtOrBelow(state, hlcAt(1_000_000, 0, "any"))
	assert.Equal(t, 0, pruned)
	assert.Len(t, state.GetNodes(), 1)
	assert.Len(t, state.GetTabs(), 1)
	assert.Len(t, state.GetFloatingWindows(), 1)
}

func TestPruneTombstones_HLCTiebreakerByClientId(t *testing.T) {
	state := crdt.NewState("org")
	// Same physical/logical, different client ids. HLC ordering uses
	// client_id as the tiebreaker; pruning must apply the same rule
	// so "≤ watermark" is consistent with the rest of the CRDT.
	tombstoneNodeAt(state, "n-lo", hlcAt(10, 0, "a"))
	tombstoneNodeAt(state, "n-eq", hlcAt(10, 0, "m"))
	tombstoneNodeAt(state, "n-hi", hlcAt(10, 0, "z"))

	pruned := crdt.PruneTombstonesAtOrBelow(state, hlcAt(10, 0, "m"))
	assert.Equal(t, 2, pruned)
	assert.NotContains(t, state.GetNodes(), "n-lo")
	assert.NotContains(t, state.GetNodes(), "n-eq")
	assert.Contains(t, state.GetNodes(), "n-hi", "client_id > watermark survives")
}

func TestPruneTombstones_NilStateIsSafe(t *testing.T) {
	require.NotPanics(t, func() {
		crdt.PruneTombstonesAtOrBelow(nil, hlcAt(10, 0, "a"))
	})
}

// TestManager_CompactionPrunesTombstonedTab verifies that after a
// committed-then-tombstoned tab, a TickHousekeeping pass drops the
// tombstoned record entirely so a fresh bootstrap sees the tab as
// never-existed.
func TestManager_CompactionPrunesTombstonedTab(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 1_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Add a tab.
	_, err := mgr.Submit(t.Context(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "add", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// Tombstone the tab in a separate batch (new canonical HLC).
	tomb := &leapmuxv1.OpBatch{
		BatchId: "tomb-tA",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-tomb-tA",
			Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:   "tA",
			}},
		}},
	}
	_, err = mgr.Submit(t.Context(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tomb},
	})
	require.NoError(t, err)

	// Sanity: tombstone is set, but the record is still present.
	stateBefore := mgr.State()
	require.Contains(t, stateBefore.GetTabs(), "tA", "tombstoned tab is still present pre-compaction")
	require.False(t, crdt.HLCIsZero(stateBefore.GetTabs()["tA"].GetTombstoneAt()))

	// One housekeeping pass advances compactionWatermark up to maxHlc;
	// pruning drops the tombstoned record.
	mgr.TickHousekeeping(t.Context())

	stateAfter := mgr.State()
	assert.NotContains(t, stateAfter.GetTabs(), "tA", "compaction must prune tombstoned tabs")
	assert.NotNil(t, stateAfter.GetCompactionWatermark())
	assert.False(t, crdt.HLCIsZero(stateAfter.GetCompactionWatermark()))
}

// TestManager_CompactionDoesNotPruneLiveRecords pins that compaction is
// LWW-safe: live records sharing a workspace with tombstoned siblings
// must survive the prune pass.
func TestManager_CompactionDoesNotPruneLiveRecords(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 1_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Two tabs, only one gets tombstoned.
	_, err := mgr.Submit(t.Context(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{
			addTabBatch(t, "addA", "tA", "root1", "wkr", "p1"),
			addTabBatch(t, "addB", "tB", "root1", "wkr", "p2"),
		},
	})
	require.NoError(t, err)

	tomb := &leapmuxv1.OpBatch{
		BatchId: "tomb-tA",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-tomb-tA",
			Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:   "tA",
			}},
		}},
	}
	_, err = mgr.Submit(t.Context(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tomb},
	})
	require.NoError(t, err)

	mgr.TickHousekeeping(t.Context())

	state := mgr.State()
	assert.NotContains(t, state.GetTabs(), "tA")
	assert.Contains(t, state.GetTabs(), "tB")
	// The workspace root NodeRecord is live and must NOT be pruned.
	assert.Contains(t, state.GetNodes(), "root1")
}
