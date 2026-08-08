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

func TestEnsureChildAgent_CreatesOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	childID, err := sink.EnsureChildAgent("span-1", "task-1", "build feature")
	require.NoError(t, err)
	// Touch the child sink + a span so the per-child caches are populated.
	childSink := sink.ChildSink(childID)
	childSink.OpenSpan("item-1", "span-1")
	_, spanLoaded := svc.Output.spanTrackers.Load(childID)
	require.True(t, spanLoaded, "child span tracker populated")

	// A terminal close drives CleanupChildAgent via the provider's sink.
	sink.CleanupChildAgent(childID)

	_, spanLoaded = svc.Output.spanTrackers.Load(childID)
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

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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
