package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupRootSink creates a root main-agent row and binds a sink to it. The
// per-test CreateAgent + NewSink preamble is identical across the child-agent
// tests; this helper keeps each test focused on the behavior under test. The
// sink is NOT registered with the agent manager (these tests drive the sink
// directly), unlike setupAgentWithWatcher.
func setupRootSink(t *testing.T, rootID string) (*Service, agent.OutputSink) {
	t.Helper()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:            rootID,
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	return svc, svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
}

func TestEnsureChildAgent_CreatesOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	// First call creates the child agent row.
	id1, err := sink.EnsureChildAgent("span-1", "task-1", "build feature")
	require.NoError(t, err)
	assert.NotEmpty(t, id1)

	// Second call is idempotent: same child key resolves to the same agent id.
	id2, err := sink.EnsureChildAgent("span-1", "task-1", "build feature")
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "EnsureChildAgent is idempotent")

	// The child row exists with the right parent linkage.
	child, err := svc.Queries.GetAgentByID(ctx, id1)
	require.NoError(t, err)
	require.True(t, child.ParentAgentID.Valid)
	assert.Equal(t, "root-1", child.ParentAgentID.String)
	assert.Equal(t, "span-1", child.SpawnSpanID)
	assert.Equal(t, "build feature", child.Title)
}

func TestEnsureChildAgent_RegistryRowLinksChild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "build feature")
	require.NoError(t, err)

	// The registry row under the root owner links to the child.
	rows, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
		OwnerAgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "task-1", rows[0].RowKey)
	assert.Equal(t, childID, rows[0].ChildAgentID, "registry row links to the child agent id")
}

// TestCleanupChildAgent_ReclaimsPerChildState verifies a terminal child close
// reclaims the per-child service state (span tracker, cached child sink) so a
// long-running root that cycles many subagents does not accumulate a stale
// entry per closed child. The child AGENT row and transcript survive.
func TestCleanupChildAgent_ReclaimsPerChildState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "build feature")
	require.NoError(t, err)
	// Touch the child sink + a span so the per-child caches are populated.
	childSink := sink.ChildSink(childID)
	childSink.OpenSpan("item-1", "span-1")
	_, _, spanLoaded := svc.Output.trackers.get(childID)
	require.True(t, spanLoaded, "child span tracker populated")

	// A terminal close drives CleanupChildAgent via the provider's sink.
	sink.CleanupChildAgent(childID)

	_, _, spanLoaded = svc.Output.trackers.get(childID)
	assert.False(t, spanLoaded, "child span tracker reclaimed on terminal close")
	// The child AGENT row and its transcript survive (only in-memory caches are reclaimed).
	child, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err, "child agent row survives the in-memory cleanup")
	require.True(t, child.ParentAgentID.Valid, "child linkage survives")

	// Idempotent: a second cleanup is a no-op.
	sink.CleanupChildAgent(childID)
}

func TestEnsureChildAgent_SpawnSpanFallbackAfterRegistryLoss(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")

	// First spawn: creates the child.
	id1, err := sink.EnsureChildAgent("span-1", "task-1", "first")
	require.NoError(t, err)

	// Simulate a worker restart between the agent-row insert and the registry
	// upsert: a fresh OutputHandler with no cache. The child row is in the DB
	// but the in-memory registry cache is gone.
	svc.Output = NewOutputHandler(svc.DB, svc.Queries, svc.Watchers, svc.Agents, nil)
	sink2 := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// The spawn-span fallback reattaches the same child row.
	id2, err := sink2.EnsureChildAgent("span-1", "task-1", "first")
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "spawn-span fallback reattaches the existing child")
}

func TestChildSink_PersistsIntoChildSeqSpace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)

	// Persist into the child transcript via ChildSink.
	childSink := sink.ChildSink(childID)
	require.NotNil(t, childSink)
	require.NoError(t, childSink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"assistant"}`), agent.SpanInfo{
			SpanID: "child-span-1", SpanType: "text",
		}))

	// The message lands under the child agent id, with its own seq space.
	msgs, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: childID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, int64(1), msgs[0].Seq, "child has its own seq space starting at 1")

	// The root transcript is untouched.
	rootMsgs, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	assert.Empty(t, rootMsgs, "child message does not leak into the root transcript")
}

func TestChildSink_SpanTrackerIndependentOfParent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)

	// Open a span on the root and the child with the SAME id. Each transcript
	// has its own tracker, so closing the child span must not close the root's.
	// We assert independence by persisting a closing message on the child and a
	// non-closing message on the root under the same span id, then checking both
	// transcripts persist independently.
	sink.OpenSpan("shared-span", "")
	childSink := sink.ChildSink(childID)
	childSink.OpenSpan("shared-span", "")
	childSink.CloseSpan("shared-span")

	// The root span is still open: persisting a closing message on the root
	// under the same id succeeds (its tracker still tracks it).
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"result"}`), agent.SpanInfo{
			SpanID: "shared-span", SpanType: "tool_result", Closing: true,
		}))

	rootMsgs, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, rootMsgs, 1, "root transcript persists its own message despite the child close")
}

func TestNestedChild_RegistersUnderRoot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")

	// First-level child.
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)
	childSink := sink.ChildSink(childID)

	// Grandchild: spawned from the child's transcript. Its registry row lives
	// under the ROOT owner, but parent_agent_id is the child.
	grandchildID, err := childSink.EnsureChildAgent("span-2", "task-2", "grandchild")
	require.NoError(t, err)
	assert.NotEmpty(t, grandchildID)
	assert.NotEqual(t, childID, grandchildID)

	// The grandchild's immediate parent is the child.
	grandchild, err := svc.Queries.GetAgentByID(ctx, grandchildID)
	require.NoError(t, err)
	require.True(t, grandchild.ParentAgentID.Valid)
	assert.Equal(t, childID, grandchild.ParentAgentID.String, "grandchild's immediate parent is the child")

	// The registry row for the grandchild lives under the ROOT owner.
	rows, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
		OwnerAgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	// Two rows: the child and the grandchild, both under root-1.
	keys := make(map[string]db.AgentBackgroundTask, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = r
	}
	assert.Contains(t, keys, "task-1")
	assert.Contains(t, keys, "task-2", "grandchild registry row lives under the root owner")
	assert.Equal(t, grandchildID, keys["task-2"].ChildAgentID)
}

// TestChildSink_TrackerRegisteredAsChildKind verifies the typed registry
// records root vs child distinctly: NewSink seeds a root-kind tracker,
// ChildSink seeds a child-kind tracker. This is the invariant cleanup and the
// orphan sweep rely on to reason about each population.
func TestChildSink_TrackerRegisteredAsChildKind(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")

	// require (not assert) so a seeding regression fails here, not by skipping
	// the kind check below.
	_, rootKind, ok := svc.Output.trackers.get("root-1")
	require.True(t, ok, "root tracker seeded by NewSink")
	assert.Equal(t, spanTrackerRoot, rootKind, "NewSink registers the root kind")

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)
	_ = sink.ChildSink(childID) // drive child-tracker creation

	_, childKind, ok := svc.Output.trackers.get(childID)
	require.True(t, ok, "child tracker seeded by ChildSink")
	assert.Equal(t, spanTrackerChild, childKind, "ChildSink registers the child kind")

	// The child id must NOT collide with a root registration. A subsequent
	// rootTracker for the SAME child id is a programming error.
	assert.Panics(t, func() { svc.Output.rootTracker(childID) },
		"registering an existing child id as root is a kind conflict")
}

// TestCleanupChildAgent_OrphanedTrackerPointerIsBenign codifies the documented
// "benign orphan" invariant: after CleanupChildAgent deletes a child's registry
// entry, a provider goroutine still holding the retained child sink may call
// OpenSpan on it without panicking. The call mutates a GC-retained tracker
// that the registry no longer owns, so the orphan is unreachable from
// TrackedAgentIDs — exactly the intended behavior. This is benign by design
// (see spanTrackerRegistry's doc comment); a drain would serialize provider
// read-loops for no correctness gain.
func TestCleanupChildAgent_OrphanedTrackerPointerIsBenign(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	rootSink := sink.(*agentOutputSink)

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)
	retainedChildSink := sink.ChildSink(childID).(*agentOutputSink)
	retainedChildSink.OpenSpan("item-1", "span-1")

	// Terminal close reaps the registry entry and prunes the cached child sink
	// from the direct parent in O(1).
	rootSink.CleanupChildAgent(childID)
	_, _, ok := svc.Output.trackers.get(childID)
	require.False(t, ok, "registry entry deleted")

	// A late provider call on the retained sink must not panic. Its write lands
	// on the orphaned tracker, which the registry no longer reaches.
	assert.NotPanics(t, func() { retainedChildSink.OpenSpan("item-2", "span-1") })

	// The orphan is invisible to the orphan sweep candidate set.
	for _, id := range svc.Output.TrackedAgentIDs() {
		assert.NotEqual(t, childID, id, "orphaned child tracker is unreachable from the registry")
	}

	// Snapshot resolution now returns a fresh empty tracker for the child id,
	// NOT the orphan. The orphan's late writes never reach a snapshot — the
	// load-bearing half of "benign". A regression that reattached the orphan
	// here would leak its span state back into the timeline.
	fallback := svc.Output.trackerForSnapshot(childID)
	_, lines, _ := fallback.Snapshot("", "", false)
	assert.Equal(t, "[]", lines, "the orphan's span state is unobservable via snapshot")
	assert.NotSame(t, retainedChildSink.tracker, fallback,
		"snapshot resolves to a fresh empty tracker, not the orphaned child tracker")
}

// TestCleanupChildAgent_PrunesChildSinkFromDirectParent verifies that a
// terminal child close prunes the cached child sink from the DIRECT PARENT in
// O(1) (the parent sink is the receiver), without scanning every root sink.
// Closes one of two children and asserts the sibling stays cached. A regression
// that reverted to the O(roots) rootSinks scan would still pass today (one
// root), so the assertion also pins that the surviving child sink is the SAME
// pointer the parent cached — i.e. the prune touched only the closed child.
func TestCleanupChildAgent_PrunesChildSinkFromDirectParent(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	rootSink := sink.(*agentOutputSink)

	closedID, err := sink.EnsureChildAgent("span-1", "task-1", "closed-child")
	require.NoError(t, err)
	survivingID, err := sink.EnsureChildAgent("span-2", "task-2", "surviving-child")
	require.NoError(t, err)

	// Drive ChildSink for both so both are cached on the root sink.
	closedSink := sink.ChildSink(closedID).(*agentOutputSink)
	survivingSink := sink.ChildSink(survivingID).(*agentOutputSink)

	// Terminal close of one child prunes ONLY that child's cached sink.
	sink.CleanupChildAgent(closedID)

	rootSink.childMu.Lock()
	_, closedPresent := rootSink.childSinks[closedID]
	surviving, survivingPresent := rootSink.childSinks[survivingID]
	rootSink.childMu.Unlock()
	assert.False(t, closedPresent, "closed child's cached sink pruned from the parent")
	require.True(t, survivingPresent, "surviving sibling stays cached")
	assert.Same(t, survivingSink, surviving, "surviving sink is the same pointer the parent cached")

	// The closed child's per-agent state is reclaimed; the sibling's survives.
	_, _, closedTracked := svc.Output.trackers.get(closedID)
	_, _, survivingTracked := svc.Output.trackers.get(survivingID)
	assert.False(t, closedTracked, "closed child's tracker reclaimed")
	assert.True(t, survivingTracked, "surviving sibling's tracker untouched")

	// The orphaned closed sink is still usable (benign orphan) but unreachable.
	assert.NotPanics(t, func() { closedSink.OpenSpan("late", "span-1") })
}

// TestCleanupChildAgent_PrunesGrandchildFromIntermediateParent verifies the
// direct-parent prune works at nesting depth 2: a grandchild's cached sink
// lives on its INTERMEDIATE child parent (not the root), so closing the
// grandchild must prune from the intermediate parent and leave the root's
// childSinks untouched. The old O(roots) scan only inspected root sinks, so it
// silently missed grandchildren cached on intermediate children; the direct
// parent prune is depth-agnostic and correct at any depth.
func TestCleanupChildAgent_PrunesGrandchildFromIntermediateParent(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	rootSink := sink.(*agentOutputSink)

	// child -> grandchild (depth 2). The grandchild's cached sink lives on the
	// child's childSinks, not the root's.
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)
	childSink := sink.ChildSink(childID).(*agentOutputSink)

	grandchildID, err := childSink.EnsureChildAgent("span-2", "task-2", "grandchild")
	require.NoError(t, err)
	grandchildSink := childSink.ChildSink(grandchildID).(*agentOutputSink)
	_ = grandchildSink

	// Close the grandchild via the intermediate parent (childSink).
	childSink.CleanupChildAgent(grandchildID)

	// The grandchild is pruned from the INTERMEDIATE parent (childSink), which is
	// where ChildSink cached it. The root's childSinks still holds the child.
	childSink.childMu.Lock()
	_, grandchildOnChild := childSink.childSinks[grandchildID]
	childSink.childMu.Unlock()
	assert.False(t, grandchildOnChild, "grandchild pruned from its intermediate parent")

	rootSink.childMu.Lock()
	_, childOnRoot := rootSink.childSinks[childID]
	rootSink.childMu.Unlock()
	assert.True(t, childOnRoot, "the child (intermediate parent) stays cached on the root")

	// The grandchild's per-agent state is reclaimed; the child's survives.
	_, _, grandchildTracked := svc.Output.trackers.get(grandchildID)
	_, _, childTracked := svc.Output.trackers.get(childID)
	assert.False(t, grandchildTracked, "grandchild tracker reclaimed")
	assert.True(t, childTracked, "intermediate parent tracker untouched")
}

// TestTrackerForSnapshot_ReadOnlyNoSeed verifies trackerForSnapshot resolves
// an existing tracker WITHOUT seeding one for an unknown agent. This is the
// contract the persistAndBroadcast nil-fallback and snapshotPassthroughSpanLines
// rely on: creating an entry here would mask a missing-sink bug, so an unknown
// agent gets a fallback empty tracker (rendering "[]") and the registry stays
// untouched.
func TestTrackerForSnapshot_ReadOnlyNoSeed(t *testing.T) {
	t.Parallel()

	svc, _ := setupRootSink(t, "root-1")

	// Known agent: returns the registered tracker (same pointer as the registry).
	registered, _, ok := svc.Output.trackers.get("root-1")
	require.True(t, ok)
	assert.Same(t, registered, svc.Output.trackerForSnapshot("root-1"),
		"known agent resolves to its registered tracker")

	// Unknown agent: returns a non-nil tracker (so Snapshot is safe) but does
	// NOT add an entry to the registry.
	before := svc.Output.trackers.len()
	unknown := svc.Output.trackerForSnapshot("never-registered")
	assert.NotNil(t, unknown, "unknown agent gets a fallback tracker, not nil")
	_, lines, _ := unknown.Snapshot("", "", false)
	assert.Equal(t, "[]", lines, "fallback tracker renders empty span lines")
	assert.Equal(t, before, svc.Output.trackers.len(), "no entry seeded for the unknown agent")
}

// TestCleanupChildAgent_ReChildSinkGetsFreshTracker pins the ordering fix:
// CleanupChildAgent runs cleanupChildMaps (deletes the registry entry) BEFORE
// it deletes the cached child sink under childMu. A ChildSink that re-caches
// the child AFTER cleanup therefore calls childTracker, which creates a FRESH
// registry entry. The re-cached sink binds to a live tracker that the orphan
// sweep reaches — not an orphan. A regression that reversed the order (delete
// the cache, then delete the registry) would let a racing ChildSink re-cache a
// sink whose tracker cleanupChildMaps then orphans.
func TestCleanupChildAgent_ReChildSinkGetsFreshTracker(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	rootSink := sink.(*agentOutputSink)

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "child")
	require.NoError(t, err)
	first := sink.ChildSink(childID).(*agentOutputSink)
	firstTracker := first.tracker

	// Terminal close: registry entry deleted, cached sink pruned.
	rootSink.CleanupChildAgent(childID)
	_, _, ok := svc.Output.trackers.get(childID)
	require.False(t, ok, "registry entry deleted by cleanup")

	// Re-resolve the child sink. The registry entry is gone, so childTracker
	// creates a fresh one.
	second := sink.ChildSink(childID).(*agentOutputSink)

	// The re-cached sink's tracker is a distinct allocation that the registry
	// owns and the orphan sweep reaches.
	assert.NotSame(t, firstTracker, second.tracker,
		"re-cached sink binds to a fresh tracker, not the orphaned one")
	regTracker, _, regOK := svc.Output.trackers.get(childID)
	require.True(t, regOK, "fresh registry entry exists for the re-cached child")
	assert.Same(t, second.tracker, regTracker,
		"the re-cached sink's tracker is the one the registry owns")
}
