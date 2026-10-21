package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFreshLaunchClearsGoal(t *testing.T, planRestart, withGoal bool) {
	t.Helper()
	svc, _, agentID := prepareNativeRestartAgent(t)
	if withGoal {
		sink := requireRootOutputSink(t, svc.Output, agentID)
		sink.UpsertGoal(agent.GoalUpdate{Objective: "Finish the old task", Status: agent.GoalStatusActive})
	}
	before, err := svc.Queries.GetAgentByID(t.Context(), agentID)
	require.NoError(t, err)
	if withGoal {
		require.NotEmpty(t, before.GoalObjective)
	}
	launched := false
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		launched = true
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		require.NoError(t, err)
		assert.Empty(t, row.GoalObjective, "the old goal must leave before the launch")
		assert.Zero(t, row.GoalStatus)
		assert.False(t, row.GoalCreatedAt.Valid)
		assert.Empty(t, opts.ResumeSessionID)
		return svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newNativeRestartProbeAgent(agentID), nil
			})
	}
	if planRestart {
		_, err = svc.restartPlanContextLocked(agentID, "acceptEdits", before)
		require.NoError(t, err)
	} else {
		finish, err := svc.prepareClearContext(agentID)
		require.NoError(t, err)
		finish()
	}
	require.True(t, launched)
	messages, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	cleared := 0
	for _, message := range messages {
		for _, kind := range decodeMessageTypes(t, &leapmuxv1.AgentChatMessage{Content: message.Content, ContentCompression: message.ContentCompression}) {
			if kind == "goal_cleared" {
				cleared++
			}
		}
	}
	if withGoal {
		assert.Equal(t, 1, cleared)
	} else {
		assert.Zero(t, cleared)
	}
}

func TestClearContextClearsTheSessionGoal(t *testing.T) {
	t.Parallel()
	testFreshLaunchClearsGoal(t, false, true)
}

func TestPlanContextRestartClearsTheSessionGoal(t *testing.T) {
	t.Parallel()
	testFreshLaunchClearsGoal(t, true, true)
}

func TestClearContextWithoutAGoalWritesNoGoalRow(t *testing.T) {
	t.Parallel()
	testFreshLaunchClearsGoal(t, false, false)
}
