package qwen

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// advertiseCommands delivers the command set of the test session.
func advertiseCommands(t *testing.T, a *Agent, names ...string) {
	t.Helper()
	commands := make([]any, len(names))
	for i, name := range names {
		commands[i] = map[string]any{"name": name}
	}
	a.HandleOutput(sessionUpdate(t, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": commands}))
}

// goalState is one goal state, on the empty message that carries it.
func goalState(t *testing.T, goal map[string]any, activity string) []byte {
	t.Helper()
	state := map[string]any{"v": 2, "activity": activity}
	if goal != nil {
		state["goal"] = goal
	} else {
		state["goal"] = nil
	}
	return metaChunk(t, "", map[string]any{"goalState": state})
}

func TestQwenOffersGoalsOnlyWithTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	assert.Empty(t, a.SupportedGoalActions())
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)

	advertiseCommands(t, a, "compress", "goal")

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}, a.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())
}

func TestQwenGoalActionsBuildTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	advertiseCommands(t, a, "goal")
	for _, tc := range []struct {
		action    agent.GoalAction
		objective string
		want      string
	}{
		{action: agent.GoalActionSet, objective: "Ship the\nrelease", want: "/goal set Ship the release"},
		// Qwen would read a bare `pause` or `set up` as a verb; the set verb
		// keeps each of them an objective.
		{action: agent.GoalActionSet, objective: "pause", want: "/goal set pause"},
		{action: agent.GoalActionSet, objective: "off", want: "/goal set off"},
		{action: agent.GoalActionSet, objective: "set up the CI", want: "/goal set set up the CI"},
		{action: agent.GoalActionClear, want: "/goal clear"},
		{action: agent.GoalActionPause, want: "/goal pause"},
		{action: agent.GoalActionResume, want: "/goal resume"},
	} {
		outcome, err := a.PerformGoalAction(tc.action, tc.objective)
		require.NoError(t, err)
		assert.Equal(t, tc.want, outcome.QueuedInput)
	}
}

func TestQwenGoalStatusMapping(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]agent.GoalStatus{
		"active": agent.GoalStatusActive, "paused": agent.GoalStatusPaused, "blocked": agent.GoalStatusBlocked,
		"usage_limited": agent.GoalStatusBlocked, "complete": agent.GoalStatusDone, "something-new": agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, qwenGoalStatus(wire), wire)
	}
	assert.Empty(t, qwenGoalStatusDetail("active", "running", "ignored"))
	assert.Equal(t, "verifying", qwenGoalStatusDetail("active", "verifying", ""))
	assert.Equal(t, "usage limited: out of tokens", qwenGoalStatusDetail("usage_limited", "idle", "out of tokens"))
	assert.Equal(t, "Three turns made no progress", qwenGoalStatusDetail("paused", "idle", " Three turns made no progress "))
	assert.Empty(t, qwenGoalStatusDetail("complete", "idle", "done"))
	assert.Equal(t, "Waiting for CI", qwenGoalStatusDetail("blocked", "idle", "Waiting for CI"))
	assert.Equal(t, "usage limited", qwenGoalStatusDetail("usage_limited", "idle", "  "), "a blank reason adds nothing")
	assert.Equal(t, "verifying", qwenGoalStatusDetail("active", "verifying", "a reason of an active goal"),
		"an active goal keeps no reason: the reason belongs to a pause or a block")
	assert.Empty(t, qwenGoalStatusDetail("paused", "verifying", ""), "only an active goal verifies")
}

func TestQwenGoalStateReachesTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	created := time.Date(2026, 9, 23, 18, 16, 49, 930_000_000, time.UTC)
	a.HandleOutput(goalState(t, map[string]any{
		"goalId": "9789df2d", "revision": 1, "objective": "Reply with DONE", "status": "paused",
		"turnCount": 3, "activeTimeMs": 4393, "tokensUsed": 330, "tokenBudget": 30000000,
		"createdAt": created.UnixMilli(), "lastReason": "Three goal turns recorded nothing",
	}, "idle"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "9789df2d", goal.NativeID)
	assert.Equal(t, "Reply with DONE", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Three goal turns recorded nothing", goal.StatusDetail)
	assert.True(t, goal.CreatedAt.Equal(created))
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(3), *goal.Iterations)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(4), *goal.TimeUsedSeconds)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(330), *goal.TokensUsed)
	require.NotNil(t, goal.TokenBudget)
	assert.Equal(t, int64(30000000), *goal.TokenBudget)
}

func TestQwenGoalStateWithNoGoalClearsTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, nil, "idle"))
	a.HandleOutput(goalState(t, map[string]any{"goalId": "", "objective": "x", "status": "active"}, "idle"))
	assert.Equal(t, 2, sink.GoalClears())
	assert.Equal(t, []bool{false, false}, sink.GoalClearSnapshots())
}

func TestQwenUnreadableGoalStateChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(metaChunk(t, "", map[string]any{"goalState": "text"}))
	_, ok := sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, sink.GoalClears())
}

func TestQwenGoalStateLeavesAbsentCountersAbsent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, map[string]any{"goalId": "g-1", "objective": "Ship", "status": "active"}, "running"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.True(t, goal.CreatedAt.IsZero(), "a goal that states no creation time has none")
	assert.Nil(t, goal.Iterations)
	assert.Nil(t, goal.TimeUsedSeconds)
	assert.Nil(t, goal.TokensUsed)
	assert.Nil(t, goal.TokenBudget)
}

func TestQwenGoalStateCountsWholeSeconds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, map[string]any{
		"goalId": "g-1", "objective": "Ship", "status": "active", "activeTimeMs": 999, "createdAt": -5, "turnCount": 0, "tokensUsed": 0,
	}, "running"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(0), *goal.TimeUsedSeconds, "a goal that ran for less than a second used no whole second")
	assert.True(t, goal.CreatedAt.IsZero(), "a creation time before the epoch is no time that Qwen states")
	require.NotNil(t, goal.Iterations, "a count of zero is a count, not an absent one")
	assert.Equal(t, int32(0), *goal.Iterations)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(0), *goal.TokensUsed)
}

func TestQwenGoalSetRefusesABlankObjective(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	advertiseCommands(t, a, "goal")
	for _, objective := range []string{"", " \n\t "} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.Error(t, err, "%q", objective)
		assert.Empty(t, outcome.QueuedInput, "a refused goal queues nothing")
	}
}
