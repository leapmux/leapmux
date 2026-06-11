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
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	createAgent := func(id string) {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:            id,
			WorkspaceID:   "ws-1",
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
	svc.Output.spanTracker(openID)

	// A CLOSED agent whose state was orphaned (never routed through cleanup).
	closedID := "agent-closed"
	createAgent(closedID)
	require.NoError(t, svc.Queries.CloseAgent(ctx, closedID))
	svc.Output.spanTracker(closedID)

	// A DELETED agent (no DB row at all) with leftover state.
	deletedID := "agent-deleted"
	svc.Output.spanTracker(deletedID)

	require.ElementsMatch(t, []string{openID, closedID, deletedID}, svc.Output.TrackedAgentIDs())

	svc.SweepOrphanedAgentState()

	tracked := svc.Output.TrackedAgentIDs()
	assert.Contains(t, tracked, openID, "an open agent's state is retained for a possible relaunch")
	assert.NotContains(t, tracked, closedID, "a closed agent's orphaned state is reclaimed")
	assert.NotContains(t, tracked, deletedID, "a deleted agent's orphaned state is reclaimed")
}
