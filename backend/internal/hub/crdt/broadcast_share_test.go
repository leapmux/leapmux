package crdt_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestBroadcast_EntityMaterialized_SharesEventPointerAcrossSubscribers
// pins the dedup optimization: when N subscribers observe the same
// visibility-becoming-visible transition, they receive the IDENTICAL
// `*WatchOrgEvent` pointer rather than per-subscriber allocations of
// the same body. Without dedup the entity clone alone scales with sub
// count.
func TestBroadcast_EntityMaterialized_SharesEventPointerAcrossSubscribers(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 1_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1 BEFORE the destination-only subscribers attach,
	// so the move op below is the only event that fires across both
	// subscribers.
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed", "tA", "root1", "wkr", "p1")},
	})
	require.NoError(t, err)

	cap1 := &captureSubscriber{}
	sub1 := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w2": true}},
		Send:   cap1.send,
	}
	_, unsub1 := mgr.Subscribe(sub1)
	defer unsub1()

	cap2 := &captureSubscriber{}
	sub2 := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w2": true}},
		Send:   cap2.send,
	}
	_, unsub2 := mgr.Subscribe(sub2)
	defer unsub2()

	// Move tA from w1 to w2; both destination-only subs see this as a
	// becoming-visible transition.
	mv := &leapmuxv1.OpBatch{
		BatchId: "move-shared",
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

	evt1 := findEntityMaterialized(cap1.snapshot())
	evt2 := findEntityMaterialized(cap2.snapshot())
	require.NotNil(t, evt1, "subscriber 1 must receive EntityMaterialized")
	require.NotNil(t, evt2, "subscriber 2 must receive EntityMaterialized")
	assert.Same(t, evt1, evt2, "broadcast must share the same *WatchOrgEvent pointer across subscribers")
}

// TestBroadcast_Presence_SharesEventPointerAcrossSubscribers pins the
// same optimization for presence broadcasts. UpdatePresence fans out
// to every subscriber whose filter includes the workspace; with dedup
// they all receive the same envelope.
func TestBroadcast_Presence_SharesEventPointerAcrossSubscribers(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 1_000)
	seedRootInternal(t, mgr, "w1", "root1")

	cap1 := &captureSubscriber{}
	sub1 := &crdt.Subscriber{
		ClientID: "c1",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:     cap1.send,
	}
	_, unsub1 := mgr.Subscribe(sub1)
	defer unsub1()

	cap2 := &captureSubscriber{}
	sub2 := &crdt.Subscriber{
		ClientID: "c2",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:     cap2.send,
	}
	_, unsub2 := mgr.Subscribe(sub2)
	defer unsub2()

	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "active-client"))

	// HeartbeatPresence is async — the manager goroutine processes the
	// presenceCh job and only THEN broadcasts. Poll for the event with
	// a reasonable cap so the test stays fast in the happy path and
	// fails fast otherwise.
	var evt1, evt2 *leapmuxv1.WatchOrgEvent
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		evt1 = findPresence(cap1.snapshot(), "w1")
		evt2 = findPresence(cap2.snapshot(), "w1")
		if evt1 != nil && evt2 != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NotNil(t, evt1)
	require.NotNil(t, evt2)
	assert.Same(t, evt1, evt2, "presence broadcasts must share the same *WatchOrgEvent pointer across subscribers")
}

func findEntityMaterialized(events []*leapmuxv1.WatchOrgEvent) *leapmuxv1.WatchOrgEvent {
	for _, evt := range events {
		if evt.GetEntityMaterialized() != nil {
			return evt
		}
	}
	return nil
}

func findPresence(events []*leapmuxv1.WatchOrgEvent, workspaceID string) *leapmuxv1.WatchOrgEvent {
	for _, evt := range events {
		p := evt.GetPresence()
		if p != nil && p.GetWorkspaceId() == workspaceID {
			return evt
		}
	}
	return nil
}
