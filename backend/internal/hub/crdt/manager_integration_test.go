package crdt_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// runManager wires up a manager with a fake journal and starts it in a
// goroutine. The returned cleanup stops the manager and cancels the
// context. The injected `now` advances by 1ms per call so every
// heartbeat / submit gets a strictly-fresh timestamp; this keeps the
// presence-leader logic (which requires strict-ahead) deterministic.
func runManager(t *testing.T, orgID string, auth crdt.AuthChecker, nowSeed int64, opts ...crdt.ManagerOption) (*crdt.Manager, *fakeJournal, context.CancelFunc) {
	t.Helper()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = nowSeed
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	mgr := crdt.NewManager(orgID, j, auth, nil, now, opts...)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})
	return mgr, j, cancel
}

// seedRootInternal seeds the workspace + root node via the lifecycle
// path (manager-internal mutation + SubmitInternal SetWorkspaceRootNode).
func seedRootInternal(t *testing.T, mgr *crdt.Manager, workspaceID, rootID string) {
	t.Helper()
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
			WorkspaceId: workspaceID,
			RootNodeId:  "",
		}
		if s.GetEpochStartedAt() == nil {
			s.EpochStartedAt = nil
		}
	})
	rootKind := &leapmuxv1.OrgOp{
		OpId: "seed-root-kind-" + workspaceID,
		Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: rootID,
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}},
	}
	rootRegister := &leapmuxv1.OrgOp{
		OpId: "seed-root-register-" + workspaceID,
		Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: workspaceID,
			RootNodeId:  rootID,
		}},
	}
	results, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		OrgID:   "org",
		Batches: []*leapmuxv1.OpBatch{{BatchId: "seed-" + workspaceID, Ops: []*leapmuxv1.OrgOp{rootKind, rootRegister}}},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].GetCommitted(), "seed root should commit; got %v", results[0])
}

// addTabBatch builds a 3-op SetTabRegister batch (tile_id + worker_id + position).
func addTabBatch(t *testing.T, batchID string, tabID, tileID, workerID, position string) *leapmuxv1.OpBatch {
	t.Helper()
	return &leapmuxv1.OpBatch{
		BatchId: batchID,
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-" + batchID + "-tile", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID},
			}}},
			{OpId: "op-" + batchID + "-worker", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID},
			}}},
			{OpId: "op-" + batchID + "-pos", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: position},
			}}},
		},
	}
}

func TestManager_TwoClients_InterleavedOps_Converge(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 1_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// Bootstrap epoch from the manager's materialized.
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Client A: add tab tA at root1.
	resA, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "userA", OriginClient: "clientA",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "bA", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	require.NotNil(t, resA[0].GetCommitted())

	// Client B: add tab tB at root1.
	resB, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "userB", OriginClient: "clientB",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "bB", "tB", "root1", "wkr1", "p2")},
	})
	require.NoError(t, err)
	require.NotNil(t, resB[0].GetCommitted())

	state := mgr.State()
	require.Contains(t, state.GetTabs(), "tA")
	require.Contains(t, state.GetTabs(), "tB")
	assert.Equal(t, "root1", state.GetTabs()["tA"].GetTileId().GetValue())
	assert.Equal(t, "root1", state.GetTabs()["tB"].GetTileId().GetValue())
}

func TestManager_RestartReplay(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(2_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	mgr := crdt.NewManager("org", j, allowAll{}, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()

	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	cancel()
	mgr.Stop()

	// Reload from journal.
	mgr2 := crdt.NewManager("org", j, allowAll{}, nil, now)
	require.NoError(t, mgr2.Bootstrap(context.Background()))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = mgr2.Start(ctx2) }()
	defer mgr2.Stop()

	state := mgr2.State()
	require.Contains(t, state.GetTabs(), "tA", "tab must survive replay")
	assert.Equal(t, "root1", state.GetTabs()["tA"].GetTileId().GetValue())
}

// TestManager_RestartReplay_PreservesWorkspaceRecord pins the bug
// "after restart, workspace appears in sidebar but the agent tab never
// renders and the frontend times out at 30s":
//
// The workspace's WorkspaceContentsRecord placeholder is added to
// `state.Workspaces` via non-journaled MutateInternal in
// applyLifecycleCreate. The seed batch's SetWorkspaceRootNodeOp is
// journaled and re-applied on Bootstrap replay; but its apply path
// (applySetWorkspaceRootNode) silently dropped the op when the
// placeholder was missing. So on Bootstrap-from-empty-snapshot the
// workspace record vanished and the frontend's awaitWorkspaceBootstrap
// polled `workspaces[wsID].rootNodeId` forever.
//
// The contract: after restart, the workspace record (and its
// root_node_id register) MUST survive replay; the projection
// the subscriber reads is the only authoritative source the frontend
// has for "this workspace exists with this root node".
func TestManager_RestartReplay_PreservesWorkspaceRecord(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(2_500)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	mgr := crdt.NewManager("org", j, allowAll{}, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()

	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)

	// Sanity: pre-restart, the workspace record carries the seeded root.
	preState := mgr.State()
	require.Contains(t, preState.GetWorkspaces(), "w1", "precondition: workspace seeded")
	require.Equal(t, "root1", preState.GetWorkspaces()["w1"].GetRootNodeId(),
		"precondition: root_node_id register populated")

	cancel()
	mgr.Stop()

	// Restart with a fresh manager pointing at the same journal. The
	// fakeJournal has no snapshot yet (no CompactBatch ran), so this
	// hits the cold-boot path: LoadState returns nil state + the full
	// tail, Bootstrap re-applies every op via Apply.
	mgr2 := crdt.NewManager("org", j, allowAll{}, nil, now)
	require.NoError(t, mgr2.Bootstrap(context.Background()))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = mgr2.Start(ctx2) }()
	defer mgr2.Stop()

	state := mgr2.State()
	require.Contains(t, state.GetWorkspaces(), "w1",
		"workspace record must survive replay; without it, the frontend's awaitWorkspaceBootstrap polls forever and surfaces a 30s timeout")
	assert.Equal(t, "root1", state.GetWorkspaces()["w1"].GetRootNodeId(),
		"root_node_id register must survive replay so subscribers can resolve the workspace's root tile")
}

func TestManager_DedupeByOpID(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 3_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	batch := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	first, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	committed := first[0].GetCommitted()
	require.NotNil(t, committed)

	// Resubmit the same batch (same op_ids) — dedup hit.
	rebatch := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	second, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{rebatch},
	})
	require.NoError(t, err)
	dup := second[0].GetCommitted()
	require.NotNil(t, dup, "dedup retry must return committed, not rejection")

	require.Len(t, dup.GetCommitted(), 3)
	for i, op := range committed.GetCommitted() {
		assert.Equal(t, op.GetOpId(), dup.GetCommitted()[i].GetOpId())
		assert.Equal(t, crdt.HLCCmp(op.GetCanonicalHlc(), dup.GetCommitted()[i].GetCanonicalHlc()), 0,
			"dedup must echo the original canonical HLC")
	}
}

func TestManager_DedupeByOpID_DifferentBody_RejectsCollision(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 4_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	first := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	r1, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{first},
	})
	require.NoError(t, err)
	require.NotNil(t, r1[0].GetCommitted())

	// Reuse op_ids but with a different body (different position).
	mutated := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p2")
	r2, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{mutated},
	})
	require.NoError(t, err)
	rejected := r2[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_OP_ID_COLLISION, rejected.GetReason())
}

func TestManager_DedupeByOpID_DifferentPrincipal_RejectsUnauthorized(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 5_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	batch := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	r1, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "userA", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	require.NotNil(t, r1[0].GetCommitted())

	// Different principal retries the same op_ids — must reject as
	// op_id_collision_unauthorized (principal-before-body precedence).
	r2, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "userB", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	rejected := r2[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_OP_ID_COLLISION_UNAUTHORIZED, rejected.GetReason())
}

func TestManager_Epoch0_RejectedAsEpochRequired(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 6_000)
	seedRootInternal(t, mgr, "w1", "root1")

	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: 0, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	rejected := r[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_EPOCH_REQUIRED, rejected.GetReason())
}

func TestManager_StaleEpoch_Rejected(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 7_000)
	seedRootInternal(t, mgr, "w1", "root1")
	currentEpoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Bump the in-memory epoch by 2 directly; emulates 28 days of advance.
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.CurrentEpoch = currentEpoch + 2
	})

	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: currentEpoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	rejected := r[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, rejected.GetReason())
}

func TestManager_PresenceActiveClient(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 8_000)

	// First client heartbeats — becomes active.
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientA"))
	// Second client heartbeats — becomes active (most-recent wins).
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientB"))
	// Subscribe to capture broadcasts.
	var (
		mu     sync.Mutex
		events []*leapmuxv1.WatchOrgEvent
	)
	sub := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send: func(evt *crdt.MarshaledEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt.Event)
			return nil
		},
	}
	_, unsub := mgr.Subscribe(sub)
	defer unsub()

	// New heartbeat from C — should broadcast presence.
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientC"))
	// Allow goroutine to process.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events, "expected at least one broadcast")
	last := events[len(events)-1]
	pres := last.GetPresence()
	require.NotNil(t, pres)
	assert.Equal(t, "w1", pres.GetWorkspaceId())
	assert.Equal(t, "clientC", pres.GetActiveClientId())
}

func TestManager_PresenceDeferredClear_ClearsOnFinalUnsub(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 7_000, crdt.WithPresenceClearGrace(20*time.Millisecond))

	// Subscribe two presence-tracked clients to w1.
	var (
		muA     sync.Mutex
		eventsA []*leapmuxv1.WatchOrgEvent
	)
	subA := &crdt.Subscriber{
		ClientID: "clientA",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send: func(evt *crdt.MarshaledEvent) error {
			muA.Lock()
			defer muA.Unlock()
			eventsA = append(eventsA, evt.Event)
			return nil
		},
	}
	_, unsubA := mgr.Subscribe(subA)

	subB := &crdt.Subscriber{
		ClientID: "clientB",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:     func(*crdt.MarshaledEvent) error { return nil },
	}
	_, unsubB := mgr.Subscribe(subB)

	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientA"))
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientB"))
	time.Sleep(30 * time.Millisecond)

	// B is the most recent heartbeater. Disconnect B and wait past
	// the grace window — A should observe presence flipping to A.
	muA.Lock()
	eventsA = nil
	muA.Unlock()
	unsubB()
	time.Sleep(100 * time.Millisecond)

	muA.Lock()
	var sawAfterB string
	for _, evt := range eventsA {
		if p := evt.GetPresence(); p != nil {
			sawAfterB = p.GetActiveClientId()
		}
	}
	muA.Unlock()
	assert.Equal(t, "clientA", sawAfterB, "after B's grace clears, A should be the active client")

	// Now disconnect A as well; the broadcast goes only to remaining
	// subscribers — none in this test — but we can verify the entry is
	// gone by re-querying the manager state via a fresh subscriber.
	unsubA()
	time.Sleep(100 * time.Millisecond)

	var (
		muC     sync.Mutex
		eventsC []*leapmuxv1.WatchOrgEvent
	)
	subC := &crdt.Subscriber{
		ClientID: "clientC",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send: func(evt *crdt.MarshaledEvent) error {
			muC.Lock()
			defer muC.Unlock()
			eventsC = append(eventsC, evt.Event)
			return nil
		},
	}
	_, unsubC := mgr.Subscribe(subC)
	defer unsubC()
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientC"))
	time.Sleep(30 * time.Millisecond)

	muC.Lock()
	defer muC.Unlock()
	require.NotEmpty(t, eventsC)
	last := eventsC[len(eventsC)-1].GetPresence()
	require.NotNil(t, last)
	assert.Equal(t, "clientC", last.GetActiveClientId(), "C heartbeats into a workspace with no other active entries → C is the sole active client")
}

func TestManager_PresenceDeferredClear_ReconnectCancelsClear(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 7_500, crdt.WithPresenceClearGrace(150*time.Millisecond))

	var (
		muObs     sync.Mutex
		eventsObs []*leapmuxv1.WatchOrgEvent
	)
	observer := &crdt.Subscriber{
		ClientID: "observer",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send: func(evt *crdt.MarshaledEvent) error {
			muObs.Lock()
			defer muObs.Unlock()
			eventsObs = append(eventsObs, evt.Event)
			return nil
		},
	}
	_, unsubObs := mgr.Subscribe(observer)
	defer unsubObs()

	subA1 := &crdt.Subscriber{
		ClientID: "clientA",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:     func(*crdt.MarshaledEvent) error { return nil },
	}
	_, unsubA1 := mgr.Subscribe(subA1)
	require.NoError(t, mgr.HeartbeatPresence(context.Background(), "w1", "clientA"))
	time.Sleep(30 * time.Millisecond)

	// Reset observer event log so we only inspect post-reconnect events.
	muObs.Lock()
	eventsObs = nil
	muObs.Unlock()

	// "Reconnect" cycle: drop A's subscription, reattach under the
	// same ClientID inside the grace window.
	unsubA1()
	time.Sleep(30 * time.Millisecond)
	subA2 := &crdt.Subscriber{
		ClientID: "clientA",
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:     func(*crdt.MarshaledEvent) error { return nil },
	}
	_, unsubA2 := mgr.Subscribe(subA2)
	defer unsubA2()

	// Wait well past the original grace deadline.
	time.Sleep(300 * time.Millisecond)

	muObs.Lock()
	defer muObs.Unlock()
	for _, evt := range eventsObs {
		if p := evt.GetPresence(); p != nil {
			t.Fatalf("unexpected PresenceUpdate during reconnect: active=%q", p.GetActiveClientId())
		}
	}
}

func TestManager_TabIndexInSync(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 9_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)

	owned, rendered := j.snapshotIndex()
	require.Contains(t, owned, "tA")
	require.Contains(t, rendered, "tA")
	assert.Equal(t, "w1", owned["tA"].WorkspaceID)
	assert.Equal(t, "root1", owned["tA"].TileID)
	assert.Equal(t, "wkr1", owned["tA"].WorkerID)
	// _rendered also carries worker_id so ListTabs can route without
	// joining _owned.
	assert.Equal(t, "wkr1", rendered["tA"].WorkerID)

	// Tombstone the tab; both views must drop the row.
	tombstoneBatch := &leapmuxv1.OpBatch{
		BatchId: "b2",
		Ops: []*leapmuxv1.OrgOp{{OpId: "op-tomb-tA", Body: &leapmuxv1.OrgOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		}}}},
	}
	_, err = mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tombstoneBatch},
	})
	require.NoError(t, err)

	owned, rendered = j.snapshotIndex()
	assert.NotContains(t, owned, "tA")
	assert.NotContains(t, rendered, "tA")
}

func TestManager_AtomicBatch_RejectAll_OnAnyOpFailure(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 10_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	preBatchCount := j.batchCount()

	// Build a batch that fails: tab references a non-existent tile.
	batch := &leapmuxv1.OpBatch{
		BatchId: "fail-batch",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-tile-bad", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tBad",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost-tile"},
			}}},
			{OpId: "op-worker-bad", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tBad",
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr1"},
			}}},
			{OpId: "op-pos-bad", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tBad",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p1"},
			}}},
		},
	}
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	rejected := r[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID, rejected.GetReason())

	// None of the batch's ops should have been journaled.
	assert.Equal(t, preBatchCount, j.batchCount(), "rejected batch must not write any ops")
}

func TestManager_IndependentBatches_OneRejection_DoesNotAffectOthers(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 11_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	good := addTabBatch(t, "good", "tGood", "root1", "wkr1", "p1")
	bad := &leapmuxv1.OpBatch{
		BatchId: "bad",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "op-bad-tile", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tBad",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "ghost"},
			}}},
		},
	}
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{good, bad},
	})
	require.NoError(t, err)
	require.Len(t, r, 2)
	require.NotNil(t, r[0].GetCommitted(), "good batch must commit")
	require.NotNil(t, r[1].GetRejected(), "bad batch must reject")

	state := mgr.State()
	require.Contains(t, state.GetTabs(), "tGood")
	assert.NotContains(t, state.GetTabs(), "tBad")
}

func TestManager_AssignsContiguousMonotonicCanonicalHLCs(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 12_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	committed := r[0].GetCommitted()
	require.NotNil(t, committed)

	// Each successive op must have a strictly greater canonical HLC.
	ops := committed.GetCommitted()
	require.Len(t, ops, 3)
	for i := 1; i < len(ops); i++ {
		assert.Greater(t, crdt.HLCCmp(ops[i].GetCanonicalHlc(), ops[i-1].GetCanonicalHlc()), 0,
			"canonical HLCs must be strictly increasing within a batch")
	}
}

func TestManager_HubOnlyOp_FromClient_Rejected(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 13_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Client tries to call SetWorkspaceRootNode (hub-only).
	batch := &leapmuxv1.OpBatch{
		BatchId: "evil",
		Ops: []*leapmuxv1.OrgOp{{OpId: "op-evil", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{
			SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{WorkspaceId: "w1", RootNodeId: "stolen"},
		}}},
	}
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	rejected := r[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_HUB_ONLY_OP, rejected.GetReason())
}

func TestManager_SameBatch_RegisterConflict_LastWinsBySubmissionOrder(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 14_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Build a tab batch where two SetTabRegister(tile_id=…) ops target
	// the same tab. Hub assigns canonical HLCs in submission order →
	// second wins LWW.
	tabA := &leapmuxv1.OpBatch{
		BatchId: "init",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "init-1", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
			}}},
			{OpId: "init-2", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr1"},
			}}},
			{OpId: "init-3", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p1"},
			}}},
		},
	}
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{tabA},
	})
	require.NoError(t, err)
	require.NotNil(t, r[0].GetCommitted())

	// Now build a batch that writes tile_id twice in succession.
	// Submission order matters: 'p2' is written first then 'p3' — the
	// batch contains two tile_id writes targeting the same tab.
	conflict := &leapmuxv1.OpBatch{
		BatchId: "conflict",
		Ops: []*leapmuxv1.OrgOp{
			{OpId: "c-1", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "first"},
			}}},
			{OpId: "c-2", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "second"},
			}}},
		},
	}
	r2, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{conflict},
	})
	require.NoError(t, err)
	require.NotNil(t, r2[0].GetCommitted())

	state := mgr.State()
	assert.Equal(t, "second", state.GetTabs()["tA"].GetPosition().GetValue(),
		"second op in submission order must win LWW")
}

func TestManager_Subscribe_AlwaysBootstraps(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 15_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// Subscriber asks for w1.
	sub := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:   func(*crdt.MarshaledEvent) error { return nil },
	}
	initial, unsub := mgr.Subscribe(sub)
	defer unsub()
	require.NotNil(t, initial)
	require.Contains(t, initial.GetWorkspaces(), "w1")
	assert.Equal(t, "root1", initial.GetWorkspaces()["w1"].GetRootNodeId())
	require.Contains(t, initial.GetNodes(), "root1")
}

func TestManager_BroadcastFiltering_AllowedSet(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 16_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Subscriber sees w1 only.
	var (
		mu     sync.Mutex
		events []*leapmuxv1.WatchOrgEvent
	)
	sub := &crdt.Subscriber{
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send: func(evt *crdt.MarshaledEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt.Event)
			return nil
		},
	}
	_, unsub := mgr.Subscribe(sub)
	defer unsub()

	// Submit a tab on w2 — subscriber must NOT see the raw op.
	r, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "tabW2", "tInW2", "root2", "wkr1", "p1")},
	})
	require.NoError(t, err)
	require.NotNil(t, r[0].GetCommitted())

	mu.Lock()
	defer mu.Unlock()
	for _, evt := range events {
		assert.Nil(t, evt.GetBatch(), "subscriber must not see batch events containing w2 ops")
	}
}

// TestManager_Compaction_DropsOldOps_RetainsDedup asserts the canonical
// compaction contract: after TickHousekeeping runs, every op committed
// at or below the new compaction_watermark has been dropped from the
// op log, but the per-op dedup row is retained — so a retry of any of
// those op_ids still returns the original canonical HLC.
func TestManager_Compaction_DropsOldOps_RetainsDedup(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 20_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Commit a batch — three ops journaled, three dedup rows written.
	batch := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	res, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	committed := res[0].GetCommitted()
	require.NotNil(t, committed)
	require.Len(t, committed.GetCommitted(), 3)
	originalHLCs := make([]*leapmuxv1.HLC, 3)
	for i, op := range committed.GetCommitted() {
		originalHLCs[i] = op.GetCanonicalHlc()
	}

	preOps := j.batchCount()
	preDedup := j.dedupCount()
	require.Greater(t, preOps, 0, "ops must be journaled before compaction")
	require.Greater(t, preDedup, 0, "dedup rows must exist before compaction")

	mgr.TickHousekeeping(context.Background())

	postOps := j.batchCount()
	postDedup := j.dedupCount()
	assert.Equal(t, 0, postOps, "compaction must drop ops at or below watermark")
	assert.Equal(t, preDedup, postDedup, "dedup rows must survive compaction (until TTL expiry)")

	// Resubmit the same batch — should return canonical HLCs from the
	// dedup row, NOT a fresh apply (which would have minted new HLCs).
	retry := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	res2, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{retry},
	})
	require.NoError(t, err)
	dup := res2[0].GetCommitted()
	require.NotNil(t, dup, "post-compaction retry must dedup, not reject")
	require.Len(t, dup.GetCommitted(), 3)
	for i, op := range dup.GetCommitted() {
		assert.Equal(t, crdt.HLCCmp(originalHLCs[i], op.GetCanonicalHlc()), 0,
			"post-compaction retry must echo the original canonical HLC")
	}
}

// TestManager_DedupHitOldEpoch_FallsThroughToStaleEpoch covers the
// retention-window edge: even though a dedup row still exists for an
// op_id (TTL ~14d), if the row's stored epoch is more than one epoch
// behind the manager's current epoch, the retry rejects as stale_epoch
// instead of replaying the canonical HLC.
func TestManager_DedupHitOldEpoch_FallsThroughToStaleEpoch(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 21_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Commit a batch at the initial epoch — dedup rows now carry that epoch.
	first, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")},
	})
	require.NoError(t, err)
	require.NotNil(t, first[0].GetCommitted())

	// Advance current_epoch by 2; the dedup row's stored epoch is now
	// `current_epoch - 2`, outside the one-epoch grace window.
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) { s.CurrentEpoch = epoch + 2 })
	newEpoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	require.Equal(t, epoch+2, newEpoch)

	// Retry with the *new* epoch (so the request-level stale check passes)
	// but a stored dedup row that's now too old.
	retry := addTabBatch(t, "b1", "tA", "root1", "wkr1", "p1")
	res, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: newEpoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{retry},
	})
	require.NoError(t, err)
	rejected := res[0].GetRejected()
	require.NotNil(t, rejected)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, rejected.GetReason(),
		"dedup hit on a stored-epoch outside the retention window must reject as stale_epoch, not echo")
}

// TestRendered_HasWorkerID_ListTabsCanRouteWithoutOwnedJoin asserts the
// fundamental contract that `workspace_tab_rendered` carries `worker_id`
// — so the hub's ListTabs / GetTab handlers can serve clients without
// joining `_owned`. The validation is also covered as a sub-check inside
// TestManager_TabIndexInSync; this test calls it out explicitly so a
// future contributor wouldn't accidentally collapse the rendered row
// schema down to (tab_id, tile_id, workspace_id) and break routing.
func TestRendered_HasWorkerID_ListTabsCanRouteWithoutOwnedJoin(t *testing.T) {
	mgr, j, _ := runManager(t, "org", allowAll{}, 22_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	_, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "b1", "tA", "root1", "wkr-rendered", "p1")},
	})
	require.NoError(t, err)

	_, rendered := j.snapshotIndex()
	require.Contains(t, rendered, "tA")
	assert.Equal(t, "wkr-rendered", rendered["tA"].WorkerID,
		"rendered row must carry worker_id so ListTabs can route without joining _owned")
	assert.Equal(t, "w1", rendered["tA"].WorkspaceID)
	assert.Equal(t, "root1", rendered["tA"].TileID)
}
