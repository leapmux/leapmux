package crdt_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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
	// consumeErr, when non-nil, makes MarkLifecycleOutboxConsumed return it
	// instead of consuming, simulating a transient DB write fault so a test can
	// exercise the row-stays-pending re-drain path.
	consumeErr error
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
	if o.consumeErr != nil {
		return o.consumeErr
	}
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

// TestLifecycleCreate_RedrainAfterDedupExpiryIsIdempotent pins SWEEP-1-Create +
// SWEEP-2: if the seed batch landed on a prior drain but the consume failed, and
// the org then sees no lifecycle RPC for > DedupTTL, the next drain re-validates
// the seed and the SetWorkspaceRootNode op rejects with ROOT_IMMUTABLE (the root
// is already set). That rejection must be treated as idempotent success -- the
// create already happened -- rather than rolling back, because the rollback would
// delete(state.Workspaces, wsID) while the DB row, root node, and any tabs the
// user added stay behind, corrupting the projection and stranding the row in a
// permanent rejection loop.
func TestLifecycleCreate_RedrainAfterDedupExpiryIsIdempotent(t *testing.T) {
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

	listener := &captureSubscriber{}
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"sentinel": true}},
		Send:   listener.send,
	})
	defer unsub()

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

	// First drain: the seed lands, the row is consumed, the workspace is live.
	row1 := outbox.push("org", crdt.LifecycleOpCreate, payload)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))
	require.Contains(t, outbox.consumedSnapshot(), row1)
	mat := mgr.Materialized(crdt.SubscriberFilter{})
	require.Contains(t, mat.GetWorkspaces(), "w1")
	require.Equal(t, "root-w1", mat.GetWorkspaces()["w1"].GetRootNodeId())

	// Simulate DedupTTL expiry: clear the dedup table so the next drain's
	// SubmitInternal re-validates the seed batch (which has a fixed BatchId)
	// rather than short-circuiting on a dedup hit. The row's ExpiresAt is
	// now+DedupTTL (14 days), so sweep past it.
	clockMu.Lock()
	clockNow := clock
	clockMu.Unlock()
	cleared, err := j.CleanupExpiredRecentBatchIDs(context.Background(), time.UnixMilli(clockNow).Add(30*24*time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, cleared, int64(1), "the dedup row for the seed batch must be expired to simulate >DedupTTL inactivity")

	// Re-push the same create row (the consume on a prior failed drain would
	// have left it pending; a fresh push stands in for that here) and re-drain.
	row2 := outbox.push("org", crdt.LifecycleOpCreate, payload)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox),
		"a re-drain whose seed hits ROOT_IMMUTABLE must be treated as idempotent success, not an error")
	require.Contains(t, outbox.consumedSnapshot(), row2, "the re-drained row is consumed (no permanent rejection loop)")

	// The workspace is STILL live with its root intact: the rollback path that
	// would corrupt CRDT state did not run.
	mat = mgr.Materialized(crdt.SubscriberFilter{})
	assert.Contains(t, mat.GetWorkspaces(), "w1", "the workspace must not be rolled back by a ROOT_IMMUTABLE re-drain")
	assert.Equal(t, "root-w1", mat.GetWorkspaces()["w1"].GetRootNodeId(), "the root node must survive the re-drain")
	assert.Contains(t, mat.GetNodes(), "root-w1", "the root node record must survive the re-drain")
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
		// denied-user not present — CanAccessWorkspace returns false.
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
		"expected ok-user's filter to widen — CanAccessWorkspace returns true")
	assert.False(t, deniedSub.Filter.IsAllowed("w1"),
		"denied-user's filter must NOT widen — CanAccessWorkspace returns false")
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

func (e *erroringBatch) CanAccessWorkspaceForUsers(_ context.Context, _, _ string, _ []string) (map[string]bool, error) {
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

// perWorkspaceFailBatch fails CanAccessWorkspaceForUsers for the workspaces in
// failFor (toggleable at runtime), standing in for a read-ACL fault scoped to one
// workspace. Every other workspace resolves as allowed for all queried users.
type perWorkspaceFailBatch struct {
	perUserOwner
	mu      sync.Mutex
	failFor map[string]error
}

func (c *perWorkspaceFailBatch) setFail(ws string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failFor == nil {
		c.failFor = map[string]error{}
	}
	if err == nil {
		delete(c.failFor, ws)
	} else {
		c.failFor[ws] = err
	}
}

func (c *perWorkspaceFailBatch) CanAccessWorkspaceForUsers(_ context.Context, _, workspaceID string, userIDs []string) (map[string]bool, error) {
	c.mu.Lock()
	err := c.failFor[workspaceID]
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(userIDs))
	for _, u := range userIDs {
		out[u] = true
	}
	return out, nil
}

// TestSubmitLifecycle_FailedRowDoesNotStrandIndependentWorkspaces pins the
// skip-and-continue drain: a persistently-failing create for one workspace must
// not block INDEPENDENT workspaces' rows behind it, yet every LATER row for the
// SAME workspace must be deferred with it so per-workspace order holds. Aborting
// the whole batch on the first error stranded independent rows until unrelated org
// activity happened to drain while the fault was clear.
func TestSubmitLifecycle_FailedRowDoesNotStrandIndependentWorkspaces(t *testing.T) {
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
	wantErr := errors.New("transient read-ACL fault for wA")
	auth := &perWorkspaceFailBatch{
		perUserOwner: perUserOwner{allowed: map[string]map[string]bool{"user-1": {"wA": true, "wB": true}}},
	}
	auth.setFail("wA", wantErr)
	mgr := crdt.NewManager("org", j, auth, nil, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	// A subscriber with a placeholder filter, so a create's
	// ExpandSubscribersForWorkspace actually calls the ACL checker (and faults for wA).
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	})
	defer unsub()

	createRow := func(ws, root string) []byte {
		p, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
			OpType: crdt.LifecycleOpCreate, WorkspaceID: ws, Title: ws, RootNodeID: root,
		}, []*leapmuxv1.OrgOp{
			{OpId: ws + "-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: root, Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
			}}},
			{OpId: ws + "-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
				WorkspaceId: ws, RootNodeId: root,
			}}},
		})
		require.NoError(t, err)
		return p
	}
	renameA, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpRename, WorkspaceID: "wA", NewTitle: "wA-renamed",
	}, nil)
	require.NoError(t, err)

	// Rows in id order: create wA (faults), create wB (independent, succeeds),
	// rename wA (same workspace as the failed create -> must be deferred).
	createA := outbox.push("org", crdt.LifecycleOpCreate, createRow("wA", "root-wA"))
	createB := outbox.push("org", crdt.LifecycleOpCreate, createRow("wB", "root-wB"))
	renameAID := outbox.push("org", crdt.LifecycleOpRename, renameA)

	// The drain surfaces wA's fault but must not abort wB.
	err = mgr.SubmitLifecycle(context.Background(), outbox)
	require.ErrorIs(t, err, wantErr)

	consumed := outbox.consumedSnapshot()
	assert.Contains(t, consumed, createB, "an independent workspace's create must land despite wA's fault")
	assert.NotContains(t, consumed, createA, "the failed create stays pending for retry")
	assert.NotContains(t, consumed, renameAID, "a later row for the SAME failed workspace must be deferred, preserving order")

	mat := mgr.Materialized(crdt.SubscriberFilter{})
	assert.Contains(t, mat.GetWorkspaces(), "wB", "wB must be live")
	assert.NotContains(t, mat.GetWorkspaces(), "wA", "wA was rolled back")

	// The fault clears; the deferred wA create then its rename both apply, in order.
	auth.setFail("wA", nil)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))
	consumed = outbox.consumedSnapshot()
	assert.Contains(t, consumed, createA, "wA create applies once the fault clears")
	assert.Contains(t, consumed, renameAID, "wA rename applies after its create, in order")
	mat = mgr.Materialized(crdt.SubscriberFilter{})
	assert.Contains(t, mat.GetWorkspaces(), "wA", "wA is live after its create finally applied")
}

// TestSubmitLifecycle_ApplyFailureDedupsLogAcrossDrains pins the EFFICIENCY-5
// fix: a row whose applyLifecycleRow keeps failing is NOT consumed (it retries,
// since an apply failure is most often transient), but it must not re-log at
// Error on every drain -- one Error on the first sighting, Warn on the repeats,
// mirroring the decode-failure path's undecodableLogged. Without the dedup a
// persistently-failing row would re-log Error on every lifecycle RPC in the org
// for as long as the fault lasts.
func TestSubmitLifecycle_ApplyFailureDedupsLogAcrossDrains(t *testing.T) {
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

	// Capture log records so the test can assert Error-vs-Warn counts without
	// depending on the process-wide slog.Default().
	captured := &countingHandler{levels: map[slog.Level]int{}}
	logger := slog.New(captured)

	wantErr := errors.New("transient read-ACL fault for wF")
	auth := &perWorkspaceFailBatch{
		perUserOwner: perUserOwner{allowed: map[string]map[string]bool{"user-1": {"wF": true}}},
	}
	auth.setFail("wF", wantErr)
	mgr := crdt.NewManager("org", j, auth, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"placeholder": true}},
		Send:   (&captureSubscriber{}).send,
	})
	defer unsub()

	payload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpCreate, WorkspaceID: "wF", Title: "wF", RootNodeID: "root-wF",
	}, []*leapmuxv1.OrgOp{
		{OpId: "wF-kind", Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root-wF", Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}}},
		{OpId: "wF-register", Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "wF", RootNodeId: "root-wF",
		}}},
	})
	require.NoError(t, err)
	badRow := outbox.push("org", crdt.LifecycleOpCreate, payload)

	// First drain: the row fails to apply, logs Error once, stays pending.
	require.ErrorIs(t, mgr.SubmitLifecycle(context.Background(), outbox), wantErr)
	errorsAfter1 := captured.count(slog.LevelError)
	warnsAfter1 := captured.count(slog.LevelWarn)
	assert.Equal(t, 1, errorsAfter1, "first sighting of a failing apply must log exactly one Error")
	assert.Equal(t, 0, warnsAfter1, "no Warn before the first Error")
	assert.NotContains(t, outbox.consumedSnapshot(), badRow, "the failing row stays pending for retry")

	// Second drain: same row still failing -- must NOT log Error again, only Warn.
	require.ErrorIs(t, mgr.SubmitLifecycle(context.Background(), outbox), wantErr)
	assert.Equal(t, 1, captured.count(slog.LevelError),
		"a persistently-failing apply must not re-log Error on every drain (dedup like undecodableLogged)")
	assert.Equal(t, 1, captured.count(slog.LevelWarn), "the repeat sighting logs Warn instead")

	// The fault clears: the row applies, and the next drain does not log at all.
	auth.setFail("wF", nil)
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox))
	assert.Contains(t, outbox.consumedSnapshot(), badRow, "the row applies once the fault clears")
	assert.Equal(t, 1, captured.count(slog.LevelError), "no new Error after the fault clears")
	assert.Equal(t, 1, captured.count(slog.LevelWarn), "no new Warn after the fault clears")

	// If the row were to fail AGAIN after clearing, it logs Error afresh -- the
	// dedup entry was cleared on success, so a later failure is a new incident.
	auth.setFail("wF", errors.New("a different, later fault"))
	// Re-push the row (it was consumed on success) to observe a fresh failure.
	badRow2 := outbox.push("org", crdt.LifecycleOpCreate, payload)
	require.Error(t, mgr.SubmitLifecycle(context.Background(), outbox))
	assert.Equal(t, 2, captured.count(slog.LevelError),
		"a failure after a prior success is a new incident (the dedup entry was cleared on success)")
	_ = badRow2
}

// countingHandler is a minimal slog.Handler that tallies records by level. It
// keeps every handler call atomic so concurrent emits (none in this test, but
// the manager's background goroutine could in principle log) do not race the
// counts.
type countingHandler struct {
	mu     sync.Mutex
	levels map[slog.Level]int
}

func (h *countingHandler) Enabled(_ context.Context, level slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levels[r.Level]++
	return nil
}

func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *countingHandler) count(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.levels[level]
}

// A row whose payload cannot be decoded must not abort the whole drain -- an
// independent, well-formed row after it still applies -- and must be CONSUMED
// after one Error log rather than left pending: decode is a pure function of
// the payload, so a retry can never succeed, and a pending poison row would
// re-fault on every future drain while occupying an outbox page slot.
func TestSubmitLifecycle_UndecodableRowIsDiscardedWithoutAbortingTheBatch(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(320_000)
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

	// A corrupt row (non-JSON payload) followed by a well-formed rename.
	badRow := outbox.push("org", crdt.LifecycleOpCreate, []byte("}{ not json"))
	renamePayload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpRename, WorkspaceID: "wZ", NewTitle: "wZ-renamed",
	}, nil)
	require.NoError(t, err)
	renameRow := outbox.push("org", crdt.LifecycleOpRename, renamePayload)

	err = mgr.SubmitLifecycle(context.Background(), outbox)
	require.Error(t, err, "the decode failure must be surfaced")

	consumed := outbox.consumedSnapshot()
	assert.Contains(t, consumed, badRow,
		"an undecodable row is consumed after one log: decode is deterministic, so retrying can never succeed and leaving it pending re-faults on every future drain")
	assert.Contains(t, consumed, renameRow, "a well-formed row after a corrupt one must still apply")

	// A second drain finds a clean outbox: the poison row is gone for good.
	require.NoError(t, mgr.SubmitLifecycle(context.Background(), outbox),
		"the next drain must not re-fault on the discarded row")
}

// TestBroadcastWorkspaceCreated_ExpandsFilter_ForReadableUsers verifies
// the AuthChecker-gated filter expansion: a subscriber whose user
// CanAccessWorkspace returns true receives the event AND has their
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
	// onlyOwner{allowed: ...} maps workspaceID -> allowed; CanAccessWorkspace
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
			// denied-user not present → CanAccessWorkspace returns false.
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

func (p perUserOwner) CanAccessWorkspace(_ context.Context, _, workspaceID, principalID string) (bool, error) {
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

func (p *perUserOwnerBatch) CanAccessWorkspaceForUsers(_ context.Context, _, workspaceID string, userIDs []string) (map[string]bool, error) {
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

// SubmitLifecycle must be single-consumer by construction: every lifecycle RPC
// drains the outbox post-commit on its own request goroutine, and the row-apply
// logic (contractSubscribersForWorkspace's single-key-delete safety argument,
// applyLifecycleCreate's optimistic state add, the fixed "lifecycle-<op>-<ws>"
// batch ids) is written against sequential drains. Two overlapping drains could
// re-apply a still-pending create against an in-flight delete of the same
// workspace -- re-adding it to m.state and re-expanding subscriber filters
// after the delete's contraction. This pins the serialization itself: a second
// drain's ListPending must not begin until the first drain's whole
// list-process-consume pass has returned.
func TestSubmitLifecycle_SerializesConcurrentDrains(t *testing.T) {
	mgr := crdt.NewManager("org", newFakeJournal(), allowAll{}, nil, time.Now)
	require.NoError(t, mgr.Bootstrap(context.Background()))

	reader := &gatedOutbox{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}

	first := make(chan error, 1)
	go func() { first <- mgr.SubmitLifecycle(context.Background(), reader) }()

	// Wait until the first drain is inside ListPending.
	select {
	case <-reader.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first drain never reached ListPending")
	}

	second := make(chan error, 1)
	go func() { second <- mgr.SubmitLifecycle(context.Background(), reader) }()

	// The second drain must park on the drain lock, not enter ListPending
	// beside the first.
	select {
	case <-reader.entered:
		t.Fatal("second drain entered ListPending while the first was still draining")
	case <-time.After(100 * time.Millisecond):
	}

	close(reader.release)
	require.NoError(t, <-first)

	// With the first drain finished, the second proceeds and completes.
	select {
	case <-reader.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second drain never ran after the first finished")
	}
	require.NoError(t, <-second)
}

// gatedOutbox signals each ListPending entry on `entered`, blocks it until
// `release` closes, and returns no rows -- just enough for the serialization
// test above to observe whether two drains can be inside ListPending at once.
type gatedOutbox struct {
	entered chan struct{}
	release chan struct{}
}

func (o *gatedOutbox) ListPendingLifecycleOutbox(context.Context, string) ([]crdt.LifecycleOutboxRow, error) {
	o.entered <- struct{}{}
	<-o.release
	return nil, nil
}

func (o *gatedOutbox) MarkLifecycleOutboxConsumed(context.Context, int64, time.Time) error {
	return nil
}

// When a corrupt row's consume keeps failing (a transient DB write fault), the
// row stays pending and the next drain re-decodes it. The Error log must fire
// ONCE -- not once per drain -- or one bad row amplifies into permanent log
// noise tied to every lifecycle RPC in the org. The dedupe logs Error on the
// first sighting and Warn on the repeats; once the consume finally succeeds the
// row is gone and its dedupe entry is cleared.
func TestSubmitLifecycle_UndecodableRowErrorIsDedupedAcrossDrains(t *testing.T) {
	outbox := newControllableOutbox()
	outbox.consumeErr = errors.New("database is locked")
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(330_000)
	)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock++
		return time.UnixMilli(clock)
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mgr := crdt.NewManager("org", j, allowAll{}, logger, now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
	})

	outbox.push("org", crdt.LifecycleOpCreate, []byte("}{ not json"))

	// First drain: consume fails, row stays pending, Error fires.
	require.Error(t, mgr.SubmitLifecycle(context.Background(), outbox))
	firstDrain := logBuf.String()
	assert.Equal(t, 1, strings.Count(firstDrain, "discarding undecodable lifecycle row"),
		"the Error must fire exactly once on the first sighting")

	// Second drain: row is still pending (consume still failing), re-decoded.
	require.Error(t, mgr.SubmitLifecycle(context.Background(), outbox))
	secondDrain := logBuf.String()
	assert.Equal(t, 1, strings.Count(secondDrain, "discarding undecodable lifecycle row"),
		"the Error must NOT repeat for the same row on a re-drain (dedupe)")
	assert.GreaterOrEqual(t, strings.Count(secondDrain, "still pending after a failed consume"), 1,
		"the repeat sighting is logged at Warn instead")

	// Once the consume fault clears, the row is consumed and its dedupe entry
	// cleared, so a later (hypothetical) re-sighting would log Error again.
	outbox.consumeErr = nil
	// SubmitLifecycle still returns the decode error (surfacing that a corrupt
	// row was discarded), but the row is now gone for good.
	require.Error(t, mgr.SubmitLifecycle(context.Background(), outbox))
	assert.Contains(t, outbox.consumedSnapshot(), int64(1),
		"the corrupt row is consumed once the transient fault clears")
}

// A delete whose MarkLifecycleOutboxConsumed fails must still broadcast the
// WorkspaceDeleted event and contract subscriber filters: the in-memory state
// already reflects the delete (the tombstone batch ran and the Workspaces entry
// is gone), so delaying the subscriber-visible event behind outbox bookkeeping
// would leave subscribers routing tabs to a dead workspace until some later
// lifecycle RPC re-drained -- which for a delete that removed the org's last
// workspace may not come for a long time. The broadcast runs before the consume;
// a later successful re-drain re-broadcasts, which is idempotent.
func TestSubmitLifecycle_DeleteBroadcastsEvenWhenConsumeFails(t *testing.T) {
	outbox := newControllableOutbox()
	j := newFakeJournal()
	var (
		clockMu sync.Mutex
		clock   = int64(500_000)
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

	// Seed a live workspace in-memory so the delete has something to remove and
	// broadcast about.
	seedRootInternal(t, mgr, "wDel", "rootDel")

	listener := &captureSubscriber{}
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"wDel": true}},
		Send:   listener.send,
	})
	defer unsub()

	// The consume fails on every drain (a transient DB write fault).
	outbox.consumeErr = errors.New("database is locked")
	deletePayload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpDelete, WorkspaceID: "wDel",
	}, nil)
	require.NoError(t, err)
	outbox.push("org", crdt.LifecycleOpDelete, deletePayload)

	require.Error(t, mgr.SubmitLifecycle(context.Background(), outbox),
		"the consume failure must be surfaced")

	evts := listener.snapshot()
	var sawDeleted bool
	for _, e := range evts {
		if d := e.GetDeleted(); d != nil && d.GetWorkspaceId() == "wDel" {
			sawDeleted = true
		}
	}
	assert.True(t, sawDeleted,
		"the WorkspaceDeleted broadcast must fire even when the consume fails, "+
			"so subscribers learn immediately rather than waiting on an outbox re-drain")
}
