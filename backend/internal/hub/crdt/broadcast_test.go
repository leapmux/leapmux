package crdt_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// pointerCaptureSubscriber records the *MarshaledEvent pointers it is sent, so a
// test can assert that co-visible subscribers SHARE one marshaled batch event
// (a single proto marshal) rather than each receiving a freshly built copy.
type pointerCaptureSubscriber struct {
	mu     sync.Mutex
	events []*crdt.MarshaledEvent
}

func (c *pointerCaptureSubscriber) send(evt *crdt.MarshaledEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
	return nil
}

// batchEvents returns only the WatchOrgEvent_Batch events (dropping the initial
// materialized snapshot Subscribe delivers).
func (c *pointerCaptureSubscriber) batchEvents() []*crdt.MarshaledEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*crdt.MarshaledEvent
	for _, e := range c.events {
		if e.Event.GetBatch() != nil {
			out = append(out, e)
		}
	}
	return out
}

// subscribeCapturing installs a subscriber on `mgr` whose Send appends
// to a private events slice. Returns a snapshot accessor that copies
// the events under a mutex, plus an unsubscribe callback. The
// snapshot func is safe to call from any goroutine.
func subscribeCapturing(t *testing.T, mgr *crdt.Manager, filter map[string]bool) (snapshot func() []*leapmuxv1.WatchOrgEvent, unsub func()) {
	t.Helper()
	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: filter},
		Send:   cap.send,
	}
	_, u := mgr.Subscribe(sub)
	return cap.snapshot, u
}

// countOpsInBatches sums the ops across every WatchOrgEvent_Batch in
// the captured stream. The wire shape carries one filtered OpBatch per
// committed batch — per-subscriber filtering may strip ops the
// subscriber can't see, so the count is the post-filter visible total.
func countOpsInBatches(events []*leapmuxv1.WatchOrgEvent) int {
	n := 0
	for _, evt := range events {
		if b := evt.GetBatch(); b != nil {
			n += len(b.GetOps())
		}
	}
	return n
}

// TestBroadcast_BatchEventSharedAcrossCoVisibleSubscribers pins the batch-event
// marshal-dedup: two subscribers with the same visibility over a batch's
// workspaces must receive the SAME *MarshaledEvent (so the proto is marshaled
// once for both, not once per subscriber), while a subscriber that can't see the
// batch's workspace receives no batch event at all.
func TestBroadcast_BatchEventSharedAcrossCoVisibleSubscribers(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 180_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1 so a later position edit is a stable-visibility op for w1
	// watchers (kept in the filtered batch, not redacted).
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// Two subscribers that both see w1 (identical mask), one that sees only w2.
	subA := &pointerCaptureSubscriber{}
	subB := &pointerCaptureSubscriber{}
	subC := &pointerCaptureSubscriber{}
	_, ua := mgr.Subscribe(&crdt.Subscriber{Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}, Send: subA.send})
	defer ua()
	_, ub := mgr.Subscribe(&crdt.Subscriber{Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}, Send: subB.send})
	defer ub()
	_, uc := mgr.Subscribe(&crdt.Subscriber{Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w2": true}}, Send: subC.send})
	defer uc()

	// A stable-visibility edit on the w1 tab (a position write that keeps it in w1).
	edit := &leapmuxv1.OpBatch{
		BatchId: "edit",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-edit",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{edit},
	})
	require.NoError(t, err)

	aEvents := subA.batchEvents()
	bEvents := subB.batchEvents()
	require.Len(t, aEvents, 1, "w1 watcher A must receive the batch")
	require.Len(t, bEvents, 1, "w1 watcher B must receive the batch")
	// The optimization: co-visible subscribers share ONE MarshaledEvent pointer,
	// so proto.Marshal runs once for both rather than once per subscriber.
	assert.Same(t, aEvents[0], bEvents[0], "co-visible subscribers must share one MarshaledEvent (single marshal)")
	// The shared event still carries the visible op.
	assert.Equal(t, 1, countOpsInBatches([]*leapmuxv1.WatchOrgEvent{aEvents[0].Event}),
		"the shared batch event must carry the w1 edit op")

	// The w2-only watcher has a different mask (cannot see w1), so it gets no batch
	// event -- confirming the shared event is keyed by visibility, not handed to all.
	assert.Empty(t, subC.batchEvents(), "w2-only watcher must not receive the w1 edit batch")
}

// TestBroadcast_BecomingVisible_SendsEntityMaterialized_NotRawOp
// covers the visibility-transition redaction rule: a subscriber whose
// allowed set does NOT include the source workspace must receive
// `EntityMaterialized` (not the raw move op) when an entity arrives in
// a destination workspace they CAN see.
func TestBroadcast_BecomingVisible_SendsEntityMaterialized_NotRawOp(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 100_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// Subscriber is destination-only (sees w2).
	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w2": true})
	defer unsub()

	// Move the tab to w2 via a single tile_id write.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-move",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	var materialized int
	for _, evt := range events {
		if evt.GetEntityMaterialized() != nil {
			materialized++
		}
	}
	assert.Equal(t, 1, materialized, "destination-only subscriber must get exactly one EntityMaterialized")
	assert.Equal(t, 0, countOpsInBatches(events), "destination-only subscriber must not see the raw move op (would leak source workspace)")
}

// TestBroadcast_BecomingHidden_SendsEntityRemoved_NotRawOp covers the
// inverse: a subscriber whose allowed set does NOT include the
// destination workspace must receive `EntityRemoved` (not the raw
// move op) when an entity leaves a workspace they were watching.
func TestBroadcast_BecomingHidden_SendsEntityRemoved_NotRawOp(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 110_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// Subscriber is source-only (sees w1).
	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w1": true})
	defer unsub()

	// Move the tab to w2.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-move",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	var removed int
	for _, evt := range events {
		if er := evt.GetEntityRemoved(); er != nil {
			removed++
			// Tab redaction must carry only the tab identity — no
			// destination info leaks back to source-only subscribers.
			tab := er.GetTab()
			require.NotNil(t, tab)
			assert.Equal(t, "tA", tab.GetTabId())
		}
	}
	assert.Equal(t, 1, removed, "source-only subscriber must get exactly one EntityRemoved")
	assert.Equal(t, 0, countOpsInBatches(events), "source-only subscriber must not see the raw move op (would leak destination workspace)")
}

// TestBroadcast_AlwaysVisible_ForwardsRawOps covers the always-visible
// arm: a subscriber whose allowed set includes BOTH workspaces sees
// raw move ops inside the filtered batch.
func TestBroadcast_AlwaysVisible_ForwardsRawOps(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 120_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w1": true, "w2": true})
	defer unsub()

	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-move",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	var materializedOrRemoved int
	for _, evt := range events {
		if evt.GetEntityMaterialized() != nil || evt.GetEntityRemoved() != nil {
			materializedOrRemoved++
		}
	}
	assert.Equal(t, 1, countOpsInBatches(events), "always-visible subscriber must receive the raw move op")
	assert.Equal(t, 0, materializedOrRemoved, "always-visible subscriber must NOT receive a redacted event")
}

// TestBroadcast_NilFilter_SeesAllWorkspaces covers the all-workspaces
// (nil filter) arm: a subscriber whose Filter.WorkspaceIDs is nil must
// see raw ops across every workspace, exactly like an explicit
// {w1, w2} filter. This pins the lazily-built shared `allVis` map —
// broadcastBatch defers its construction to the first nil-filter
// subscriber seen on a commit, so a regression there (leaving visMap
// nil, or building the wrong entries) would silently drop
// cross-workspace ops for every all-workspaces subscriber.
func TestBroadcast_NilFilter_SeesAllWorkspaces(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 125_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// nil filter == "all workspaces".
	snapshot, unsub := subscribeCapturing(t, mgr, nil)
	defer unsub()

	// Move the tab from w1 to w2. A nil-filter subscriber sees both the
	// source and destination workspace, so this is the always-visible
	// arm: it must receive the raw move op, not a redacted
	// EntityMaterialized/Removed.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-move",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	var materializedOrRemoved int
	for _, evt := range events {
		if evt.GetEntityMaterialized() != nil || evt.GetEntityRemoved() != nil {
			materializedOrRemoved++
		}
	}
	assert.Equal(t, 1, countOpsInBatches(events),
		"nil-filter subscriber must receive the raw move op across workspaces via the shared allVis path")
	assert.Equal(t, 0, materializedOrRemoved,
		"nil-filter subscriber sees both workspaces, so no redacted event should be emitted")
}

// TestBroadcast_PerEntity_NoDuplicateMaterialized verifies that a
// multi-op batch touching the same entity multiple times emits exactly
// ONE EntityMaterialized to a becoming-visible subscriber.
func TestBroadcast_PerEntity_NoDuplicateMaterialized(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 130_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w2": true})
	defer unsub()

	// Move + reposition + worker change — three ops on the same tab.
	mv := &leapmuxv1.OpBatch{
		BatchId: "multi",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-mv-tile", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}}},
			{OpId: "op-mv-pos", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "newp"},
			}}},
			{OpId: "op-mv-wkr", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr-new"},
			}}},
		},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	var materializedForTab int
	for _, evt := range events {
		if m := evt.GetEntityMaterialized(); m != nil {
			if m.GetTab() != nil && m.GetTab().GetTabId() == "tA" {
				materializedForTab++
			}
		}
	}
	assert.Equal(t, 1, materializedForTab,
		"a multi-op batch on the same tab must emit exactly one EntityMaterialized to a becoming-visible subscriber; got %d", materializedForTab)
}

// TestBroadcast_AlwaysHidden_NoEvents covers the always-hidden case:
// a subscriber who can see neither workspace gets nothing from the move.
func TestBroadcast_AlwaysHidden_NoEvents(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 140_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	seedRootInternal(t, mgr, "w3", "root3")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w3": true})
	defer unsub()

	// Move tab between w1 and w2 — w3 should see nothing.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-move",
			Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mv},
	})
	require.NoError(t, err)

	events := snapshot()
	for _, evt := range events {
		assert.Nil(t, evt.GetBatch(), "w3-only subscriber must not receive a batch event for w1↔w2 ops")
		assert.Nil(t, evt.GetEntityMaterialized(), "w3-only subscriber must not see EntityMaterialized for w1↔w2 moves")
		assert.Nil(t, evt.GetEntityRemoved(), "w3-only subscriber must not see EntityRemoved for w1↔w2 moves")
	}
}

// TestBroadcast_TombstoneTab_SendsRawOp_NotEntityRemoved covers the
// originator-feedback case: a subscriber watching the workspace where a
// tab was tombstoned must receive the raw TombstoneTab op via the
// visible-to-visible path, NOT EntityRemoved. EntityRemoved is reserved
// for true visibility transitions (workspace moves that push an entity
// out of the subscriber's allowed set); a tombstone is not such a
// transition. Before the fix, `workspaceForEntities` returned "" for
// tombstoned tabs (since TombstoneTab wipes tile_id), so the broadcast
// layer classified the originator as "becoming hidden" and fired
// EntityRemoved at them — which then triggered the frontend's pending-
// dropped path and showed a misleading "pending change was discarded"
// toast on every tab close.
func TestBroadcast_TombstoneTab_SendsRawOp_NotEntityRemoved(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 150_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w1": true})
	defer unsub()

	// Close the tab — this is the user's "close tab" path.
	tombstone := &leapmuxv1.OpBatch{
		BatchId: "close",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-close",
			Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tombstone},
	})
	require.NoError(t, err)

	events := snapshot()
	var rawTombstones, entityRemoved int
	for _, evt := range events {
		if b := evt.GetBatch(); b != nil {
			for _, op := range b.GetOps() {
				if _, isTomb := op.GetBody().(*leapmuxv1.OrgOp_TombstoneTab); isTomb {
					rawTombstones++
				}
			}
		}
		if evt.GetEntityRemoved() != nil {
			entityRemoved++
		}
	}
	assert.Equal(t, 1, rawTombstones, "subscriber watching the tab's workspace must receive the raw TombstoneTab op")
	assert.Equal(t, 0, entityRemoved, "tombstone-within-workspace must NOT fire EntityRemoved (that's reserved for workspace-move redactions)")
}

// TestBroadcast_TombstoneTab_NoEventsForUnrelatedSubscriber confirms
// the other half of the contract: a subscriber whose filter does NOT
// include the tab's workspace must see nothing — neither the raw op
// nor EntityRemoved. The fix preserves the pre-workspace for
// tombstones, so an unrelated workspace's subscriber still has
// pre/post both invisible.
func TestBroadcast_TombstoneTab_NoEventsForUnrelatedSubscriber(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 160_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	// Subscriber watches w2 only — never sees the tab.
	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w2": true})
	defer unsub()

	tombstone := &leapmuxv1.OpBatch{
		BatchId: "close",
		Ops: []*leapmuxv1.OrgOp{{
			OpId: "op-close",
			Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			}},
		}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tombstone},
	})
	require.NoError(t, err)

	events := snapshot()
	for _, evt := range events {
		assert.Nil(t, evt.GetBatch(), "w2-only subscriber must not receive a batch event for a w1 tab tombstone")
		assert.Nil(t, evt.GetEntityRemoved(), "w2-only subscriber must not see EntityRemoved for a w1 tab tombstone")
		assert.Nil(t, evt.GetEntityMaterialized(), "w2-only subscriber must not see EntityMaterialized for a w1 tab tombstone")
	}
}

// TestBroadcast_TombstoneFloatingWindow_SendsRawOp_NotEntityRemoved
// covers the floating-window arm of the same fix. TombstoneFloatingWindow
// wipes the workspace_id register, so without the fix the broadcast
// layer would classify the originator as becoming-hidden and fire
// EntityRemoved.
func TestBroadcast_TombstoneFloatingWindow_SendsRawOp_NotEntityRemoved(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 170_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a complete floating window in w1 via MutateInternal — bypasses
	// completeness validation, which would otherwise demand all 7 register
	// writes in the seeding batch.
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Nodes["fwroot"] = &leapmuxv1.NodeRecord{
			NodeId: "fwroot",
			Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "seed"}},
		}
		s.FloatingWindows["fw"] = &leapmuxv1.FloatingWindowRecord{
			WindowId:    "fw",
			RootNodeId:  "fwroot",
			WorkspaceId: &leapmuxv1.LWWString{Value: "w1", Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 1, ClientId: "seed"}},
			X:           &leapmuxv1.LWWDouble{Value: 0, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 2, ClientId: "seed"}},
			Y:           &leapmuxv1.LWWDouble{Value: 0, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 3, ClientId: "seed"}},
			Width:       &leapmuxv1.LWWDouble{Value: 100, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 4, ClientId: "seed"}},
			Height:      &leapmuxv1.LWWDouble{Value: 100, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 5, ClientId: "seed"}},
			Opacity:     &leapmuxv1.LWWDouble{Value: 1, Hlc: &leapmuxv1.HLC{Physical: 1, Logical: 6, ClientId: "seed"}},
		}
	})

	snapshot, unsub := subscribeCapturing(t, mgr, map[string]bool{"w1": true})
	defer unsub()

	// Same-batch close: the floating-window-root tombstone is allowed
	// only when paired with the window tombstone (Rule 12 exception).
	close := &leapmuxv1.OpBatch{
		BatchId: "close-fw",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-close-root", Body: &leapmuxv1.OrgOp_TombstoneNode{TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: "fwroot"}}},
			{OpId: "op-close-fw", Body: &leapmuxv1.OrgOp_TombstoneFloatingWindow{TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{WindowId: "fw"}}},
		},
	}
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{close},
	})
	require.NoError(t, err)

	events := snapshot()
	var rawWindowTombstones, rawNodeTombstones, entityRemoved int
	for _, evt := range events {
		if b := evt.GetBatch(); b != nil {
			for _, op := range b.GetOps() {
				switch op.GetBody().(type) {
				case *leapmuxv1.OrgOp_TombstoneFloatingWindow:
					rawWindowTombstones++
				case *leapmuxv1.OrgOp_TombstoneNode:
					rawNodeTombstones++
				}
			}
		}
		if evt.GetEntityRemoved() != nil {
			entityRemoved++
		}
	}
	assert.Equal(t, 1, rawWindowTombstones, "subscriber must receive the raw TombstoneFloatingWindow op")
	assert.Equal(t, 1, rawNodeTombstones, "subscriber must also receive the raw TombstoneNode op for the window root")
	assert.Equal(t, 0, entityRemoved, "tombstone-within-workspace must not fire EntityRemoved for either the window or its root node")
}
