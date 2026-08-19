package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "task-1", rows[0].RowKey)
	assert.Equal(t, childID, rows[0].ChildAgentID, "registry row links to the child agent id")
}

// TestCleanupChildAgent_ReclaimsPerChildState verifies a final child close
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

	// A closing update drives CleanupChildAgent via the provider's sink.
	sink.CleanupChildAgent(childID)

	_, _, spanLoaded = svc.Output.trackers.get(childID)
	assert.False(t, spanLoaded, "child span tracker reclaimed on final close")
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
	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
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

	// Final close reaps the registry entry and prunes the cached child sink
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
// final child close prunes the cached child sink from the DIRECT PARENT in
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

	// Final close of one child prunes ONLY that child's cached sink.
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

	// Final close: registry entry deleted, cached sink pruned.
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

// transcriptMessages reads a child transcript in seq order, decoding each row's
// content so a test can assert on the envelope the frontend receives.
func transcriptMessages(t *testing.T, svc *Service, childID string) []map[string]any {
	t.Helper()
	rows, err := svc.Queries.ListAllMessagesByAgentID(context.Background(),
		db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		raw, err := msgcodec.Decompress(r.Content, r.ContentCompression)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		m["__source"] = float64(r.Source)
		out = append(out, m)
	}
	return out
}

// The subagent tab must open on the instruction the subagent was given. The
// prompt is persisted as a plain USER message so it renders as markdown, in
// full, with no provider-specific shape for the frontend to learn.
func TestPersistChildPrompt_IsTheFirstMessage(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.PersistChildPrompt(childID, "Review the diff."))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "Review the diff.", msgs[0]["content"])
	assert.Equal(t, float64(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER), msgs[0]["__source"])
}

func TestPersistChildPrompt_SkipsABlankPrompt(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.PersistChildPrompt(childID, ""))
	require.NoError(t, sink.PersistChildPrompt(childID, "   \n  "))
	assert.Empty(t, transcriptMessages(t, svc, childID))
}

// Idempotency is by emptiness, not a flag: a replayed spawn (or a re-attach
// after a worker restart, which loses any in-memory marker but not the
// transcript) must not stack a second copy of the prompt.
func TestPersistChildPrompt_DoesNotAppendOnceTheChildHasSpoken(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.PersistChildPrompt(childID, "Review the diff."))
	require.NoError(t, sink.PersistChildMessage(childID,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"text","text":"ok"}`), agent.SpanInfo{}))

	// A replay of the same spawn, and a late prompt that would otherwise land
	// BELOW the work it asked for.
	require.NoError(t, sink.PersistChildPrompt(childID, "Review the diff."))
	require.NoError(t, sink.PersistChildPrompt(childID, "A different prompt."))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 2)
	assert.Equal(t, "Review the diff.", msgs[0]["content"])
}

// Closing a subagent's registry row is the one provider-neutral moment the
// subagent is known to be over, so that is where the child transcript gets its
// closing divider -- otherwise the tab shows a thinking indicator forever.
func TestCloseBackgroundTask_WritesTheSubagentEndDivider(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	assert.Equal(t, "completed", msgs[0]["status"])
	assert.Equal(t, float64(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX), msgs[0]["__source"])
}

// Every final status is carried through, so the divider can say WHY the
// subagent stopped rather than just that it did.
func TestCloseBackgroundTask_CarriesTheFinalStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []bgtask.Status{
		bgtask.StatusFailed, bgtask.StatusStopped, bgtask.StatusInterrupted,
	} {
		t.Run(bgtask.StatusWire(status), func(t *testing.T) {
			t.Parallel()
			svc, sink := setupRootSink(t, "root-1")
			childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
			require.NoError(t, err)
			require.NoError(t, sink.CloseBackgroundTask("task-1", status))

			msgs := transcriptMessages(t, svc, childID)
			require.Len(t, msgs, 1)
			assert.Equal(t, bgtask.StatusWire(status), msgs[0]["status"])
		})
	}
}

// The divider follows the mutation that actually moved the row into a final
// status, NOT CloseBackgroundTask specifically. Every real provider reaches the
// final status through a different applier first and only then closes:
//
//   - Claude: UpdateBackgroundTaskStatus(final) then CloseBackgroundTask
//   - Codex:  UpsertBackgroundTask(final) then CloseBackgroundTask
//   - Pi:     UpsertBackgroundTask(final) and never closes at all
//
// A close-driven divider fires for none of these, because the close early-
// returns on an already-final row. Each sequence below is one provider's real
// order, and each must produce exactly one divider.
func TestSubagentEndDivider_FollowsWhicheverMutationEndsTheRow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		end  func(t *testing.T, sink agent.OutputSink)
	}{
		{
			// Claude's handleClaudeTaskNotification order.
			name: "status update then close",
			end: func(t *testing.T, sink agent.OutputSink) {
				require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusStopped, ""))
				require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusStopped))
			},
		},
		{
			// Codex's collabAgentsStatesToRegistry order.
			name: "final upsert then close",
			end: func(t *testing.T, sink agent.OutputSink) {
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: "task-1", Kind: bgtask.KindSubagent, Status: bgtask.StatusStopped,
				}))
				require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusStopped))
			},
		},
		{
			// Pi's piApplySubagentEnd: no close at all.
			name: "final upsert with no close",
			end: func(t *testing.T, sink agent.OutputSink) {
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: "task-1", Kind: bgtask.KindSubagent, Status: bgtask.StatusStopped,
				}))
			},
		},
		{
			// The ACP close-only path (Goose, OpenCode, Kilo).
			name: "close only",
			end: func(t *testing.T, sink agent.OutputSink) {
				require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusStopped))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, sink := setupRootSink(t, "root-1")
			childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
			require.NoError(t, err)

			tc.end(t, sink)

			msgs := transcriptMessages(t, svc, childID)
			require.Len(t, msgs, 1, "exactly one closing divider, whichever applier ended the row")
			assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
			assert.Equal(t, "stopped", msgs[0]["status"])
		})
	}
}

// Exactly one divider closes the transcript in EITHER arrival order.
//
// A provider that forwards its subagent's own closing envelope (Claude's
// result) writes it through the child sink's PersistTurnEnd, while the registry
// row goes final on a separate event (task_notification). Nothing serializes
// the two, and the file's own pendingTaskEnd machinery exists because Claude's
// stream reorders -- so both orders are reachable and neither may stack two
// rules saying the same thing.
func TestSubagentEndDivider_ExactlyOneInEitherArrivalOrder(t *testing.T) {
	t.Parallel()

	result := []byte(`{"type":"result","duration_ms":12}`)

	t.Run("registry closes first, then the forwarded result", func(t *testing.T) {
		t.Parallel()
		svc, sink := setupRootSink(t, "root-1")
		childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
		require.NoError(t, err)

		require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
		require.NoError(t, sink.ChildSink(childID).PersistTurnEnd(result, agent.SpanInfo{}))

		msgs := transcriptMessages(t, svc, childID)
		require.Len(t, msgs, 1, "the forwarded result must stand down, not stack")
		assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	})

	t.Run("forwarded result first, then the registry close", func(t *testing.T) {
		t.Parallel()
		svc, sink := setupRootSink(t, "root-1")
		childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
		require.NoError(t, err)

		require.NoError(t, sink.ChildSink(childID).PersistTurnEnd(result, agent.SpanInfo{}))
		require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

		msgs := transcriptMessages(t, svc, childID)
		require.Len(t, msgs, 1, "the neutral divider must yield to the richer result")
		assert.Equal(t, "result", msgs[0]["type"])
	})
}

// The claim is what makes "exactly one divider" hold, so it must hold when both
// writers run CONCURRENTLY -- the case the old last-message probes could not
// cover, because each side read before either wrote.
func TestSubagentEndDivider_ConcurrentWritersProduceOne(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	result := []byte(`{"type":"result","duration_ms":12}`)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		assert.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	}()
	go func() {
		defer wg.Done()
		assert.NoError(t, sink.ChildSink(childID).PersistTurnEnd(result, agent.SpanInfo{}))
	}()
	wg.Wait()

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1, "the claim admits exactly one writer, whichever won the race")
}

// A REPEATED final status update must not write a second divider. The two
// guards in applyBackgroundTaskStatus exclude an already-final row only when the
// incoming status is non-final, or when BOTH the status and the activeForm
// repeat -- and Claude sends exactly the case that slips through: a
// summary-bearing final update followed by a bare one.
func TestSubagentEndDivider_RepeatedFinalStatusWritesOnlyOne(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusCompleted, "wrote the summary"))
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusCompleted, ""))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1, "only the active -> final transition owes a divider")
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
}

// A ROOT turn end is not a subagent boundary: it must never be suppressed, and
// it must not pay the child-transcript read that the suppression needs.
func TestPersistTurnEnd_RootTurnEndIsNeverSuppressed(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	result := []byte(`{"type":"result","duration_ms":12}`)
	require.NoError(t, sink.PersistTurnEnd(result, agent.SpanInfo{}))
	require.NoError(t, sink.PersistTurnEnd(result, agent.SpanInfo{}))

	assert.Len(t, transcriptMessages(t, svc, "root-1"), 2, "every root turn end persists")
}

// applyBackgroundTaskClose reports changed=true only on the first
// pending/running -> final transition, and the DB's own status guard makes
// that hold across a restart -- so a re-close cannot stack a second divider.
func TestCloseBackgroundTask_DividerIsWrittenOnce(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusFailed))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	assert.Len(t, transcriptMessages(t, svc, childID), 1)
}

// A shell row has no transcript to close; the divider must not be written into
// the root's own transcript by mistake.
func TestCloseBackgroundTask_ShellRowWritesNoDivider(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-1", Kind: bgtask.KindShell, Title: "npm test", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("shell-1", bgtask.StatusCompleted))

	assert.Empty(t, transcriptMessages(t, svc, "root-1"))
}

// The owner process dying is the other way a subagent ends. The exit sweep
// gives it a final status every still-active row in bulk, so it must close those
// transcripts too -- otherwise a subagent whose owner crashed keeps a
// transcript that simply stops.
func TestMarkBackgroundTasksExited_WritesTheSubagentEndDivider(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	// stopped=false is the crash path, which labels the row 'interrupted'.
	svc.Output.MarkAgentBackgroundTasksExited("root-1", false)

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	assert.Equal(t, "interrupted", msgs[0]["status"])
}

func TestMarkBackgroundTasksExited_LabelsAnExplicitStopAsStopped(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	svc.Output.MarkAgentBackgroundTasksExited("root-1", true)

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "stopped", msgs[0]["status"])
}

// The sweep skips rows that already reached a final status, so a subagent
// that finished before its owner exited keeps its original divider and does not
// get a second one.
func TestMarkBackgroundTasksExited_SkipsAnAlreadyClosedSubagent(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	svc.Output.MarkAgentBackgroundTasksExited("root-1", false)

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "completed", msgs[0]["status"], "the original outcome survives the sweep")
}

// Exactly one divider closes a subagent transcript. Claude forwards the
// subagent's own `result`, which the frontend already draws as a turn-end
// divider (and which carries the duration, plus the error label and detail on
// failure), so the neutral divider must not stack a second rule under it.
func TestCloseBackgroundTask_SkipsTheDividerWhenTheProviderAlreadyEndedIt(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	// The forwarded subagent result, persisted as the child's turn end.
	require.NoError(t, sink.PersistChildTurnEnd(childID,
		[]byte(`{"type":"result","duration_ms":5100,"is_error":false}`), agent.SpanInfo{}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1, "the provider's own turn-end divider is the only one")
	assert.Equal(t, "result", msgs[0]["type"])
}

// The same subagent stopped mid-flight forwards no result, so its transcript
// does NOT close itself and still needs the neutral divider. This is why the
// check is content-based rather than a static per-provider capability.
func TestCloseBackgroundTask_WritesTheDividerWhenTheSubagentStoppedMidFlight(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	// Work, but no closing envelope.
	require.NoError(t, sink.PersistChildMessage(childID,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"text","text":"working"}`), agent.SpanInfo{}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusStopped))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 2)
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[1]["type"])
	assert.Equal(t, "stopped", msgs[1]["status"])
}

// The divider is written by the HANDLER, which has no sink to borrow a
// provider from, so it resolves one from the child's own agent row. Getting
// this wrong is silent-ish: createMessageRow refuses an UNSPECIFIED provider,
// so the divider is simply dropped and the transcript never closes.
func TestCloseBackgroundTask_DividerCarriesTheChildsProvider(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	rows, err := svc.Queries.ListAllMessagesByAgentID(context.Background(),
		db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, rows[0].AgentProvider,
		"the divider inherits the child agent's provider, not UNSPECIFIED")
}

// The exit sweep gives it a final status EVERY still-active row in one pass, so every
// child transcript it ends must get its own divider -- not just the first.
func TestMarkBackgroundTasksExited_ClosesEveryChildTranscript(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childA, err := sink.EnsureChildAgent("span-a", "task-a", "A")
	require.NoError(t, err)
	childB, err := sink.EnsureChildAgent("span-b", "task-b", "B")
	require.NoError(t, err)

	svc.Output.MarkAgentBackgroundTasksExited("root-1", false)

	for _, childID := range []string{childA, childB} {
		msgs := transcriptMessages(t, svc, childID)
		require.Len(t, msgs, 1, "child %s", childID)
		assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
		assert.Equal(t, "interrupted", msgs[0]["status"])
	}
}

// The sweep must not write a divider into a transcript that already closes
// itself, for the same reason CloseBackgroundTask does not.
func TestMarkBackgroundTasksExited_SkipsATranscriptThatAlreadyEnded(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.PersistChildTurnEnd(childID,
		[]byte(`{"type":"result","duration_ms":5100}`), agent.SpanInfo{}))

	svc.Output.MarkAgentBackgroundTasksExited("root-1", false)

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1)
	assert.Equal(t, "result", msgs[0]["type"])
}

// --- Revive ---
//
// Claude restarts a finished subagent when the parent messages it. The registry
// row has to reopen, and the transcript has to become closeable again -- the
// first completion durably claimed its closing divider, so without a release the
// second run would end with no divider at all.

// registrySnapshotRow reads one row back through the snapshot clients receive,
// which is the cache. Distinct from registryRow in title_cleaning_test.go, which
// reads the DB directly -- the revive writes BOTH, so the two together are what
// catch a cache that drifted from the durable row.
func registrySnapshotRow(t *testing.T, svc *Service, rootID, rowKey string) bgtask.Item {
	t.Helper()
	items, err := svc.Output.LoadBackgroundTasks(context.Background(), rootID)
	require.NoError(t, err)
	for _, it := range items {
		if it.RowKey == rowKey {
			return it
		}
	}
	t.Fatalf("no registry row %q", rowKey)
	return bgtask.Item{}
}

func TestReviveBackgroundTask_ReturnsAFinishedRowToRunning(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	// A finished Claude subagent carries its output file in Description, written
	// by the same task_notification that ended it.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:      "task-1",
		Kind:        bgtask.KindSubagent,
		Description: "/tmp/task-1.output",
		Status:      bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusCompleted, "wrote the report"))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.True(t, registrySnapshotRow(t, svc, "root-1", "task-1").Status.IsFinished())

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	row := registrySnapshotRow(t, svc, "root-1", "task-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero(), "a running row carries no end time")
	assert.Empty(t, row.ActiveForm,
		"active_form described the finished run; the new one has reported nothing yet")
	assert.Empty(t, row.Description,
		"description held the finished run's output file; the new run has not named one")

	// The durable row too, not only the cache. The revive writes both, and a
	// cache that says running over a DB row that still says completed would
	// survive until the next worker restart and then silently un-revive.
	stored := registryRow(t, svc)
	assert.Equal(t, "running", stored.Status)
	assert.False(t, stored.EndedAt.Valid, "ended_at is cleared to NULL, not left stamped")
	assert.Empty(t, stored.ActiveForm)
	assert.Empty(t, stored.Description)
}

// Idempotent in both directions, so a duplicate revive and a revive for a task
// this root never ran are both harmless.
func TestReviveBackgroundTask_IsANoOpForAnActiveOrAbsentRow(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	// Still running: nothing to reopen, and the row must not be disturbed.
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "reading files"))
	require.NoError(t, sink.ReviveBackgroundTask("task-1"))
	row := registrySnapshotRow(t, svc, "root-1", "task-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "reading files", row.ActiveForm, "an active row keeps its live activity text")

	// Absent: no row, no error.
	assert.NoError(t, sink.ReviveBackgroundTask("task-nope"))
}

// The claim release is the half that is easy to forget, and forgetting it costs
// the second run its closing divider entirely.
func TestReviveBackgroundTask_ReleasesTheTranscriptCloseClaim(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.False(t, svc.Output.claimSubagentTranscriptClose(context.Background(), childID),
		"the first completion holds the claim")

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	assert.True(t, svc.Output.claimSubagentTranscriptClose(context.Background(), childID),
		"the revive gives the claim back so the next completion can take it")
}

// End to end: a transcript holds one divider per completion, not one for its
// whole life.
func TestSubagentEndDivider_WrittenAgainAfterARevive(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.NoError(t, sink.ReviveBackgroundTask("task-1"))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusFailed))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 2, "one divider for each run")
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	assert.Equal(t, "completed", msgs[0]["status"])
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[1]["type"])
	assert.Equal(t, "failed", msgs[1]["status"], "the second divider reports the SECOND run's outcome")
}

// Regression lock. Nothing seals a child transcript: a divider is a message, not
// a terminator, and a revived subagent's output has to keep landing below it. A
// future "stop persisting once it ended" shortcut would break the revive
// silently, so the behavior is pinned here rather than left implicit.
func TestChildTranscript_AppendsBelowTheEndDivider(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	require.NoError(t, sink.PersistChildMessage(childID,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		[]byte(`{"content":"keep going"}`), agent.SpanInfo{}))

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 2)
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	assert.Equal(t, "keep going", msgs[1]["content"], "the later message sits below the divider")
}

func TestLookupBackgroundTask_ResolvesTheChildAndStatus(t *testing.T) {
	t.Parallel()

	_, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	gotChild, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, childID, gotChild)
	assert.Equal(t, bgtask.StatusRunning, status)

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	_, status, ok, _ = sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status)
}

// A row key that identifies nothing misses, which is what every recipient outside
// this session's subagents does -- a display name, another session, an address.
func TestLookupBackgroundTask_ReportsAMissForAnUnknownKey(t *testing.T) {
	t.Parallel()

	_, sink := setupRootSink(t, "root-1")
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)

	for _, key := range []string{"", "task-nope", "bridge:another-machine"} {
		childID, _, ok, _ := sink.LookupBackgroundTask(key)
		assert.False(t, ok, "key %q identifies no row", key)
		assert.Empty(t, childID)
	}
}

// A shell row owns no transcript, so there is no close claim to give back. The
// revive still reopens the row, and the release must not run against an empty
// child id.
func TestReviveBackgroundTask_RowWithNoTranscript(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-1",
		Kind:   bgtask.KindShell,
		Title:  "npm test",
		Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("shell-1", bgtask.StatusCompleted))

	require.NoError(t, sink.ReviveBackgroundTask("shell-1"))
	assert.Equal(t, bgtask.StatusRunning, registrySnapshotRow(t, svc, "root-1", "shell-1").Status)
}

// The cache and the durable row are two sources, and the query is the one that
// decides. When the DB says the row is already active the UPDATE matches nothing,
// and reporting a revive anyway would release a transcript-close claim for a
// transcript that never reopened -- letting a SECOND divider be written for the
// FIRST completion.
func TestReviveBackgroundTask_TrustsTheDatabaseOverAStaleCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink := setupRootSink(t, "root-1")
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.False(t, svc.Output.claimSubagentTranscriptClose(ctx, childID))

	// Put the DB back to running behind the cache's back, so the two disagree
	// exactly as a concurrent revive would leave them.
	require.NoError(t, svc.Queries.UpdateAgentBackgroundTaskStatus(ctx, db.UpdateAgentBackgroundTaskStatusParams{
		Status:       bgtask.StatusWire(bgtask.StatusRunning),
		UpdatedAt:    sqltime.NewSQLiteTime(nowMillis()),
		OwnerAgentID: "root-1",
		RowKey:       "task-1",
	}))

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	assert.False(t, svc.Output.claimSubagentTranscriptClose(ctx, childID),
		"the claim is still held: no revive was reported, so none was released")
}
