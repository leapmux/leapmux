package crdt_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// controllableOutbox is a LifecycleOutboxReader that lets tests queue
// outbox rows and observe the consume order. Mirrors the on-disk
// outbox semantics: rows are listed in insertion order, consume marks
// drop them from the pending list, and re-listing returns the
// remaining pending rows.
type controllableOutbox struct {
	mu       sync.Mutex
	nextID   int64
	pending  []crdt.LifecycleOutboxRow
	consumed []int64
}

func newControllableOutbox() *controllableOutbox {
	return &controllableOutbox{nextID: 0}
}

func (o *controllableOutbox) push(orgID string, opType crdt.LifecycleOpType, payload []byte) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nextID++
	row := crdt.LifecycleOutboxRow{ID: o.nextID, OrgID: orgID, OpType: opType, Payload: payload}
	o.pending = append(o.pending, row)
	return row.ID
}

func (o *controllableOutbox) ListPendingLifecycleOutbox(_ context.Context, orgID string) ([]crdt.LifecycleOutboxRow, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]crdt.LifecycleOutboxRow, 0, len(o.pending))
	for _, row := range o.pending {
		if row.OrgID == orgID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (o *controllableOutbox) MarkLifecycleOutboxConsumed(_ context.Context, id int64, _ time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	remaining := o.pending[:0]
	for _, row := range o.pending {
		if row.ID == id {
			o.consumed = append(o.consumed, id)
			continue
		}
		remaining = append(remaining, row)
	}
	o.pending = remaining
	return nil
}

func (o *controllableOutbox) consumedSnapshot() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]int64, len(o.consumed))
	copy(out, o.consumed)
	return out
}

// captureSubscriber records every event the manager pushes to a
// subscriber, suitable for asserting filter-expansion behavior.
type captureSubscriber struct {
	mu     sync.Mutex
	events []*leapmuxv1.WatchOrgEvent
}

func (c *captureSubscriber) send(evt *crdt.MarshaledEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt.Event)
	return nil
}

func (c *captureSubscriber) snapshot() []*leapmuxv1.WatchOrgEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*leapmuxv1.WatchOrgEvent, len(c.events))
	copy(out, c.events)
	return out
}

// TestLifecycleCreate_ThroughOutbox_SeedsRootAndBroadcasts exercises
// the full lifecycle-outbox path: a CreateWorkspace row is pushed into
// the outbox, the manager drains it, the seed batch commits, and the
// WorkspaceCreated broadcast reaches an already-subscribed listener.
func TestLifecycleCreate_ThroughOutbox_SeedsRootAndBroadcasts(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(100_000)
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
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// Already-subscribed listener with an empty filter (will start
	// excluding the new workspace and require filter expansion).
	listener := &captureSubscriber{}
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"sentinel": true}},
		Send:   listener.send,
	})
	defer unsub()

	// Push a CreateWorkspace lifecycle row carrying the seed-root batch.
	seedOps := []*leapmuxv1.OrgOp{
		{OpId: "seed-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root-w1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}}},
		{OpId: "seed-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "root-w1",
		}}},
	}
	payload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpCreate, WorkspaceID: "w1", Title: "First", RootNodeID: "root-w1",
	}, seedOps)
	require.NoError(t, err)
	rowID := outbox.push("org", crdt.LifecycleOpCreate, payload)

	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))

	// Row is marked consumed.
	consumed := outbox.consumedSnapshot()
	require.Contains(t, consumed, rowID)

	// State has w1 with rootNodeId pointing at root-w1.
	mat := mgr.Materialized(crdt.SubscriberFilter{})
	require.Contains(t, mat.GetWorkspaces(), "w1")
	assert.Equal(t, "root-w1", mat.GetWorkspaces()["w1"].GetRootNodeId())
	require.Contains(t, mat.GetNodes(), "root-w1")

	// Listener received WorkspaceCreated despite their initial filter
	// not containing "w1" — the broadcast path expanded the filter.
	var sawCreated bool
	for _, evt := range listener.snapshot() {
		if c := evt.GetCreated(); c != nil && c.GetWorkspaceId() == "w1" {
			sawCreated = true
			break
		}
	}
	assert.True(t, sawCreated, "expected listener to receive WorkspaceCreated for w1 via filter expansion")
}

// TestLifecycleCreate_SeedOpsReachExistingSubscriber pins the bug
// discovered when investigating "new workspace shows in sidebar but
// the just-opened agent tab never appears": the seed batch
// (`SetNodeRegister` root LEAF + `SetWorkspaceRootNode`) broadcast
// from `applyLifecycleCreate` was filtered out for any subscriber
// whose `Filter.WorkspaceIDs` was populated at connect time and
// therefore did not yet contain the new workspace.
//
// The frontend's `seedTabIntoNewWorkspace` polls
// `state.workspaces[wsID].rootNodeId` and times out without the seed
// ops, so the agent tab the user just opened never lands in the CRDT
// projection. Production browser flow hit this every time; the E2E
// helper masked the bug by opening a fresh `/ws/orgevents` after the
// workspace existed.
//
// The contract:
//   - The subscriber's filter is expanded for the new workspace
//     before SubmitInternal runs (so the seed ops broadcast).
//   - The SetNodeRegister(root LEAF) op surfaces as an
//     EntityMaterialized (the node was previously invisible — pre→
//     post visibility transition).
//   - The SetWorkspaceRootNode op surfaces as a raw op so the
//     frontend's `applySetWorkspaceRootNode` populates
//     `workspaces[wsID].rootNodeId`.
func TestLifecycleCreate_SeedOpsReachExistingSubscriber(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(150_000)
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
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// Mirror the production browser: subscribe with a populated
	// filter that locks the subscriber out of any workspace minted
	// AFTER connect. Pre-fix this was the failure mode — every
	// workspace created from the running session was effectively
	// invisible at the CRDT-state level.
	listener := &captureSubscriber{}
	listenerSub := &crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"unrelated-ws": true}},
		Send:   listener.send,
	}
	_, unsub := mgr.Subscribe(listenerSub)
	defer unsub()

	require.False(t, listenerSub.Filter.IsAllowed("w1"),
		"precondition: subscriber's connect-time filter does NOT include the new workspace")

	seedOps := []*leapmuxv1.OrgOp{
		{OpId: "seed-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root-w1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}}},
		{OpId: "seed-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "root-w1",
		}}},
	}
	payload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpCreate, WorkspaceID: "w1", Title: "Fresh", RootNodeID: "root-w1",
	}, seedOps)
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpCreate, payload)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))

	// Postcondition: filter is now expanded.
	assert.True(t, listenerSub.Filter.IsAllowed("w1"),
		"expected applyLifecycleCreate to expand subscriber's filter to include w1")

	// Walk the captured events. The subscriber must observe enough
	// to reconstruct workspaces[w1].rootNodeId AND state.nodes[root-w1]
	// — the two pieces the frontend's `seedTabIntoNewWorkspace`
	// depends on.
	var sawRootMaterialized, sawWorkspaceRootOp, sawCreated bool
	for _, evt := range listener.snapshot() {
		switch e := evt.GetEvent().(type) {
		case *leapmuxv1.WatchOrgEvent_EntityMaterialized:
			node := e.EntityMaterialized.GetNode()
			if node != nil && node.GetNodeId() == "root-w1" {
				sawRootMaterialized = true
			}
		case *leapmuxv1.WatchOrgEvent_Batch:
			for _, op := range e.Batch.GetOps() {
				if body, ok := op.GetBody().(*leapmuxv1.OrgOp_SetWorkspaceRootNode); ok {
					if body.SetWorkspaceRootNode.GetWorkspaceId() == "w1" &&
						body.SetWorkspaceRootNode.GetRootNodeId() == "root-w1" {
						sawWorkspaceRootOp = true
					}
				}
			}
		case *leapmuxv1.WatchOrgEvent_Created:
			if e.Created.GetWorkspaceId() == "w1" {
				sawCreated = true
			}
		}
	}
	assert.True(t, sawRootMaterialized,
		"expected EntityMaterialized for the root LEAF — the frontend needs state.nodes[root-w1] populated to render the tile")
	assert.True(t, sawWorkspaceRootOp,
		"expected raw SetWorkspaceRootNode op — the frontend needs this to populate state.workspaces[w1].rootNodeId so seedTabIntoNewWorkspace's poll resolves")
	assert.True(t, sawCreated,
		"expected WorkspaceCreated event (sidebar refresh + lifecycle signal)")
}

// TestExpandSubscribersForWorkspace_RespectsACL pins that the helper
// only adds the workspace to subscribers whose user passes the auth
// check. A read-denied subscriber must not have their filter widened.
func TestExpandSubscribersForWorkspace_RespectsACL(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(160_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	auth := perUserOwner{allowed: map[string]map[string]bool{
		"ok-user": {"w1": true},
		// denied-user not present — CanReadWorkspace returns false.
	}}
	mgr := crdt.NewManager("org", j, auth, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	okSub := &crdt.Subscriber{
		UserID: "ok-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	}
	_, unsubOk := mgr.Subscribe(okSub)
	defer unsubOk()

	deniedSub := &crdt.Subscriber{
		UserID: "denied-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	}
	_, unsubDenied := mgr.Subscribe(deniedSub)
	defer unsubDenied()

	require.NoError(t, mgr.ExpandSubscribersForWorkspace(context.Background(), "w1"))

	assert.True(t, okSub.Filter.IsAllowed("w1"),
		"expected ok-user's filter to widen — CanReadWorkspace returns true")
	assert.False(t, deniedSub.Filter.IsAllowed("w1"),
		"denied-user's filter must NOT widen — CanReadWorkspace returns false")
}

// TestExpandSubscribersForWorkspace_BatchPath exercises the batch-capable
// dispatch: it must reach the SAME ACL verdict as the per-candidate fallback,
// widen every subscription of an allowed user (dedup across duplicate user IDs),
// and resolve all candidates in a single batch call rather than one per
// subscriber.
func TestExpandSubscribersForWorkspace_BatchPath(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(170_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	auth := &perUserOwnerBatch{perUserOwner: perUserOwner{allowed: map[string]map[string]bool{
		"ok-user": {"w1": true},
	}}}
	mgr := crdt.NewManager("org", j, auth, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// Two subscriptions for the SAME allowed user (dedup) plus a denied user.
	okSubs := []*crdt.Subscriber{
		{UserID: "ok-user", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}}, Send: (&captureSubscriber{}).send},
		{UserID: "ok-user", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}}, Send: (&captureSubscriber{}).send},
	}
	for _, sub := range okSubs {
		_, unsub := mgr.Subscribe(sub)
		defer unsub()
	}
	deniedSub := &crdt.Subscriber{
		UserID: "denied-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	}
	_, unsubDenied := mgr.Subscribe(deniedSub)
	defer unsubDenied()

	require.NoError(t, mgr.ExpandSubscribersForWorkspace(context.Background(), "w1"))

	for i, sub := range okSubs {
		assert.True(t, sub.Filter.IsAllowed("w1"), "ok-user subscription %d must widen via the batch path", i)
	}
	assert.False(t, deniedSub.Filter.IsAllowed("w1"), "denied-user must NOT widen")
	assert.Equal(t, 1, auth.batchCalls(),
		"the batch checker must resolve every candidate in ONE call, not one per subscriber")
}

// erroringBatch is a batch-capable checker whose read-ACL lookup fails, standing
// in for a transient store error.
type erroringBatch struct {
	perUserOwner
	err error
}

func (e *erroringBatch) CanReadWorkspaceForUsers(_ context.Context, _, _ string, _ []string) (map[string]bool, error) {
	return nil, e.err
}

func TestExpandSubscribersForWorkspace_PropagatesLookupError(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(180_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	wantErr := errors.New("transient store failure")
	auth := &erroringBatch{
		perUserOwner: perUserOwner{allowed: map[string]map[string]bool{"ok-user": {"w1": true}}},
		err:          wantErr,
	}
	mgr := crdt.NewManager("org", j, auth, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	sub := &crdt.Subscriber{
		UserID: "ok-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	}
	_, unsub := mgr.Subscribe(sub)
	defer unsub()

	// A read-ACL LOOKUP failure must surface (so workspace-create retries the
	// seed) rather than being swallowed as "deny" -- and the filter must NOT
	// widen on error.
	err := mgr.ExpandSubscribersForWorkspace(context.Background(), "w1")
	require.ErrorIs(t, err, wantErr)
	assert.False(t, sub.Filter.IsAllowed("w1"), "filter must not widen when the ACL lookup failed")
}

func TestExpandSubscribersForWorkspace_RespectsImmutableWorkspaceScope(t *testing.T) {
	j := newFakeJournal()
	mgr := crdt.NewManager("org", j, allowAll{}, nil, time.Now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	sub := &crdt.Subscriber{
		UserID:           "delegated-user",
		WorkspaceScopeID: "w1",
		Filter:           crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}},
		Send:             (&captureSubscriber{}).send,
	}
	_, unsub := mgr.Subscribe(sub)
	defer unsub()

	require.NoError(t, mgr.ExpandSubscribersForWorkspace(context.Background(), "w2"))
	assert.False(t, sub.Filter.IsAllowed("w2"),
		"a scoped subscriber must not widen to a sibling workspace even when its user can read it")

	require.NoError(t, mgr.ExpandSubscribersForWorkspace(context.Background(), "w1"))
	assert.True(t, sub.Filter.IsAllowed("w1"),
		"the immutable scope must still permit expansion to the pinned workspace")
}

// TestLifecycleDelete_TombstonesAllLeftoverContent ensures that
// deleting a workspace tombstones every live node/tab/floating window
// in its subtree, so subsequent workspaces aren't blocked by Rule 15
// completeness checks (an orphan parentless live node would otherwise
// claim "no registered root" and reject the next seed batch).
func TestLifecycleDelete_TombstonesAllLeftoverContent(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(200_000)
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
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// Seed W1 with root-w1.
	seedRootInternal(t, mgr, "w1", "root-w1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Add a tab + a child leaf under root-w1.
	splitToSplit := &leapmuxv1.OrgOp{OpId: "op-rootkind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root-w1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}}}
	splitDir := &leapmuxv1.OrgOp{OpId: "op-rootdir", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root-w1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Direction{Direction: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL},
	}}}
	splitRatios := &leapmuxv1.OrgOp{OpId: "op-rootratios", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "root-w1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{Values: []float64{1.0}}},
	}}}
	childKind := &leapmuxv1.OrgOp{OpId: "op-childkind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "leaf-1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}}}
	childParent := &leapmuxv1.OrgOp{OpId: "op-childparent", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "leaf-1",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root-w1"},
	}}}
	childPos := &leapmuxv1.OrgOp{OpId: "op-childpos", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
		NodeId: "leaf-1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "N"},
	}}}
	splitBatch := &leapmuxv1.OpBatch{
		BatchId: "split-batch",
		Ops:     []*leapmuxv1.OrgOp{splitToSplit, splitDir, splitRatios, childKind, childParent, childPos},
	}
	res, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{splitBatch},
	})
	require.NoError(t, err)
	require.NotNil(t, res[0].GetCommitted(), "split batch should commit; got %v", res[0])

	tabRes, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		OrgID: "org", Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "tab-batch", "tab-1", "leaf-1", "wkr-1", "p1")},
	})
	require.NoError(t, err)
	require.NotNil(t, tabRes[0].GetCommitted(), "tab batch should commit; got %v", tabRes[0])

	// Push a Delete lifecycle row — manager enumerates and tombstones.
	delPayload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpDelete, WorkspaceID: "w1", WorkerIDs: []string{"wkr-1"},
	}, nil)
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpDelete, delPayload)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))

	// Workspace map entry removed.
	mat := mgr.Materialized(crdt.SubscriberFilter{})
	assert.NotContains(t, mat.GetWorkspaces(), "w1")

	// Every node still on disk must be tombstoned. The materialized
	// view filters by allowed workspaces; reach into the manager's
	// in-memory state directly via a fresh Subscribe + materialized
	// readback (already produced by `mat`). Tombstoned nodes are
	// excluded from `out.Nodes` because their workspace lookup
	// returns "". Use that as the test signal.
	for _, n := range mat.GetNodes() {
		// Live nodes are excluded by the materialized filter once the
		// workspace is gone (resolveNodeWorkspace returns "" so the
		// per-workspace filter rejects them). So the loop should
		// never see a live root-w1 / leaf-1.
		_ = n
	}
	assert.NotContains(t, mat.GetNodes(), "root-w1")
	assert.NotContains(t, mat.GetNodes(), "leaf-1")
	assert.NotContains(t, mat.GetTabs(), "tab-1")
}

// TestLifecycleDelete_ThenCreate_NewSeedSucceeds is the regression
// test for the bug that broke 150 close-tile in the E2E suite: deleting
// workspace W1 left its parentless root node live in state, and the
// next CreateWorkspace's seed batch failed completeness (the leftover
// orphan parentless node was no longer a registered root).
func TestLifecycleDelete_ThenCreate_NewSeedSucceeds(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(300_000)
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
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// Create W1.
	createW1, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpCreate, WorkspaceID: "w1", Title: "First", RootNodeID: "root-w1",
	}, []*leapmuxv1.OrgOp{
		{OpId: "w1-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root-w1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}}},
		{OpId: "w1-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "root-w1",
		}}},
	})
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpCreate, createW1)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))

	// Delete W1.
	deleteW1, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpDelete, WorkspaceID: "w1",
	}, nil)
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpDelete, deleteW1)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))

	// Create W2 with a fresh root.
	createW2, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpCreate, WorkspaceID: "w2", Title: "Second", RootNodeID: "root-w2",
	}, []*leapmuxv1.OrgOp{
		{OpId: "w2-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root-w2",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}}},
		{OpId: "w2-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w2", RootNodeId: "root-w2",
		}}},
	})
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpCreate, createW2)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox), "create W2 must succeed after W1 delete")

	// W2's seed root must be live and registered.
	mat := mgr.Materialized(crdt.SubscriberFilter{})
	require.Contains(t, mat.GetWorkspaces(), "w2")
	assert.Equal(t, "root-w2", mat.GetWorkspaces()["w2"].GetRootNodeId())
	require.Contains(t, mat.GetNodes(), "root-w2")
	// W1 must be fully gone — no orphan parentless live nodes.
	assert.NotContains(t, mat.GetWorkspaces(), "w1")
	assert.NotContains(t, mat.GetNodes(), "root-w1")
}

// TestBroadcastWorkspaceCreated_ExpandsFilter_ForReadableUsers verifies
// the AuthChecker-gated filter expansion: a subscriber whose user
// CanReadWorkspace returns true receives the event AND has their
// allowed-set expanded; a denied user neither receives the event nor
// gets their filter touched.
func TestBroadcastWorkspaceCreated_ExpandsFilter_ForReadableUsers(t *testing.T) {
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(400_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	// onlyOwner{allowed: ...} maps workspaceID -> allowed; CanReadWorkspace
	// shares the same allowed set in this test helper.
	auth := onlyOwner{allowed: map[string]bool{"new-ws": true}}
	mgr := crdt.NewManager("org", j, auth, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	allowed := &captureSubscriber{}
	allowedSub := &crdt.Subscriber{
		UserID: "ok-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   allowed.send,
	}
	_, unsubAllowed := mgr.Subscribe(allowedSub)
	defer unsubAllowed()

	denied := &captureSubscriber{}
	deniedFilter := crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}}
	deniedSub := &crdt.Subscriber{
		UserID: "denied-user",
		Filter: deniedFilter,
		Send:   denied.send,
	}
	_, unsubDenied := mgr.Subscribe(deniedSub)
	defer unsubDenied()

	// onlyOwner ignores principalID — both users hit the same allowed
	// set. To exercise the "denied user doesn't get the event" path,
	// flip the auth checker to a per-user-aware one.
	mgrPerUser := crdt.NewManager("org2", j, perUserOwner{
		allowed: map[string]map[string]bool{
			"ok-user": {"new-ws": true},
			// denied-user not present → CanReadWorkspace returns false.
		},
	}, nil, now)
	require.NoError(t, mgrPerUser.Bootstrap(context.Background()))
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { _ = mgrPerUser.Start(ctx2) }()
	t.Cleanup(func() {
		cancel2()
		mgrPerUser.Stop()
	})

	allowed2 := &captureSubscriber{}
	allowedSub2 := &crdt.Subscriber{
		UserID: "ok-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   allowed2.send,
	}
	_, unsubAllowed2 := mgrPerUser.Subscribe(allowedSub2)
	defer unsubAllowed2()

	denied2 := &captureSubscriber{}
	deniedSub2 := &crdt.Subscriber{
		UserID: "denied-user",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   denied2.send,
	}
	_, unsubDenied2 := mgrPerUser.Subscribe(deniedSub2)
	defer unsubDenied2()

	mgrPerUser.BroadcastWorkspaceCreated(context.Background(), "new-ws", "Sibling", "root-new")

	// Allowed subscriber: filter expanded + event delivered.
	assert.True(t, allowedSub2.Filter.IsAllowed("new-ws"),
		"expected allowed subscriber's filter to expand to include new-ws")
	gotCreated := false
	for _, evt := range allowed2.snapshot() {
		if c := evt.GetCreated(); c != nil && c.GetWorkspaceId() == "new-ws" {
			gotCreated = true
			break
		}
	}
	assert.True(t, gotCreated, "expected allowed subscriber to receive WorkspaceCreated")

	// Denied subscriber: filter unchanged + no event.
	assert.False(t, deniedSub2.Filter.IsAllowed("new-ws"),
		"expected denied subscriber's filter to remain unchanged")
	for _, evt := range denied2.snapshot() {
		if c := evt.GetCreated(); c != nil && c.GetWorkspaceId() == "new-ws" {
			t.Fatalf("denied subscriber should NOT have received WorkspaceCreated for new-ws")
		}
	}
}

// perUserOwner is an AuthChecker variant that keys access by both
// userID and workspaceID, used by the broadcast filter-expansion test
// where the two subscribers share an org but have different access.
type perUserOwner struct {
	allowed map[string]map[string]bool
}

func (p perUserOwner) CanWriteWorkspace(_ context.Context, _, workspaceID, principalID string) (bool, error) {
	return p.allowed[principalID][workspaceID], nil
}
func (p perUserOwner) CanReadWorkspace(_ context.Context, _, workspaceID, principalID string) (bool, error) {
	return p.allowed[principalID][workspaceID], nil
}
func (perUserOwner) CanUseWorker(_ context.Context, _, _, _ string) (bool, error) { return true, nil }

// perUserOwnerBatch adds the optional workspaceReaderBatch capability to
// perUserOwner so the batch-dispatch path in ExpandSubscribersForWorkspace is
// exercised (bare perUserOwner only drives the per-candidate fallback). It
// counts batch calls so a test can assert one query resolves all candidates.
type perUserOwnerBatch struct {
	perUserOwner
	mu    sync.Mutex
	calls int
}

func (p *perUserOwnerBatch) CanReadWorkspaceForUsers(_ context.Context, _, workspaceID string, userIDs []string) (map[string]bool, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	out := make(map[string]bool, len(userIDs))
	for _, userID := range userIDs {
		if p.allowed[userID][workspaceID] {
			out[userID] = true
		}
	}
	return out, nil
}

func (p *perUserOwnerBatch) batchCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
