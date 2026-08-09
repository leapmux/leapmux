package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSweepOrphanedAgentState verifies the orphan sweep reclaims in-memory tracker
// state for agents the DB no longer lists as open (closed or deleted), while leaving an
// open-but-inactive agent's state intact (it is retained for a possible relaunch).
func TestSweepOrphanedAgentState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	createAgent := func(id string) {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:            id,
			WorkingDir:    t.TempDir(),
			HomeDir:       t.TempDir(),
			Title:         id,
			Options:       marshalOptions(nil),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
	}

	// An OPEN agent with in-memory state (e.g. crashed but not closed).
	openID := "agent-open"
	createAgent(openID)
	svc.Output.rootTracker(openID)

	// A CLOSED agent whose state was orphaned (never routed through cleanup).
	closedID := "agent-closed"
	createAgent(closedID)
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, closedID)))
	svc.Output.rootTracker(closedID)

	// A DELETED agent (no DB row at all) with leftover state.
	deletedID := "agent-deleted"
	svc.Output.rootTracker(deletedID)

	require.ElementsMatch(t, []string{openID, closedID, deletedID}, svc.Output.TrackedAgentIDs())

	svc.SweepOrphanedAgentState()

	tracked := svc.Output.TrackedAgentIDs()
	assert.Contains(t, tracked, openID, "an open agent's state is retained for a possible relaunch")
	assert.NotContains(t, tracked, closedID, "a closed agent's orphaned state is reclaimed")
	assert.NotContains(t, tracked, deletedID, "a deleted agent's orphaned state is reclaimed")
}

// TestTrackedAgentIDsIncludesRootSinks verifies the orphan sweep can see a root
// sink (keyed in rootSinks) so a root closed through a path that bypassed
// ClearAgentRuntimeState is reclaimable. Without rootSinks in the tracked set,
// the sink (and every cached child sink) would leak for the worker's lifetime.
func TestTrackedAgentIDsIncludesRootSinks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-leak",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	// NewSink registers the root sink in rootSinks.
	_ = svc.Output.NewSink("root-leak", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	assert.Contains(t, svc.Output.TrackedAgentIDs(), "root-leak",
		"a root sink is tracked so the orphan sweep can reclaim it")
}

// TestCleanupChildAgentsBatchPrunesWithoutRootScan verifies the root-teardown
// batch path (ForgetChildSinks + CleanupChildAgents) prunes per-child tracker
// state and clears the root's childSinks map in one pass, without the
// per-child rootSinks.Range scan that CleanupAgent would do for each child.
func TestCleanupChildAgentsBatchPrunesWithoutRootScan(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	// Seed a root sink + per-child span trackers (the state CleanupChildAgents
	// reaps).
	rootSink := svc.Output.NewSink("root-batch", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE).(*agentOutputSink)
	for _, childID := range []string{"child-a", "child-b", "child-c"} {
		svc.Output.childTracker(childID)
	}
	// Cache a child sink on the root so ForgetChildSinks has something to clear.
	rootSink.childMu.Lock()
	rootSink.childSinks = map[string]*agentOutputSink{
		"child-a": {agentID: "child-a", rootAgentID: "root-batch"},
	}
	rootSink.childMu.Unlock()

	tracked := svc.Output.TrackedAgentIDs()
	for _, childID := range []string{"child-a", "child-b", "child-c"} {
		assert.Contains(t, tracked, childID, "child tracker seeded")
	}

	svc.Output.ForgetChildSinks("root-batch")
	svc.Output.CleanupChildAgents([]string{"child-a", "child-b", "child-c"})

	tracked = svc.Output.TrackedAgentIDs()
	for _, childID := range []string{"child-a", "child-b", "child-c"} {
		assert.NotContains(t, tracked, childID, "child tracker reaped by the batch cleanup")
	}
	rootSink.childMu.Lock()
	assert.Nil(t, rootSink.childSinks, "root childSinks map cleared by ForgetChildSinks")
	rootSink.childMu.Unlock()
}
