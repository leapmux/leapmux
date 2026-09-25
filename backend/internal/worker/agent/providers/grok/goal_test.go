package grok

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// advertiseCommands delivers the command set of the test session, as Grok
// sends it on every change.
func advertiseCommands(t *testing.T, a *Agent, names ...string) {
	t.Helper()
	commands := make([]any, len(names))
	for i, name := range names {
		commands[i] = map[string]any{"name": name}
	}
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": commands,
	}))
}

func TestGrokOffersGoalsOnlyWithTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	assert.Empty(t, a.SupportedGoalActions(), "a build without the goal command offers no control")
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)

	advertiseCommands(t, a, "compact", "goal")

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}, a.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the browser learns that the control exists")
}

func TestGrokGoalActionsBuildTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	advertiseCommands(t, a, "goal")

	for _, tc := range []struct {
		action    agent.GoalAction
		objective string
		want      string
	}{
		{action: agent.GoalActionSet, objective: "Ship the\nrelease", want: "/goal Ship the release"},
		{action: agent.GoalActionClear, want: "/goal clear"},
		{action: agent.GoalActionPause, want: "/goal pause"},
		{action: agent.GoalActionResume, want: "/goal resume"},
	} {
		outcome, err := a.PerformGoalAction(tc.action, tc.objective)
		require.NoError(t, err)
		assert.Equal(t, tc.want, outcome.QueuedInput)
	}
}

func TestGrokGoalRefusesAnObjectiveThatGrokReadsAsACommand(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	advertiseCommands(t, a, "goal")

	providerkittest.AssertRefusesAnObjectiveThatClears(t, grokGoalRoute)
	for _, objective := range []string{"status", "STATUS", "pause", "resume", "ship it --budget 5000", "ship it  --budget\t5000"} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, objective)
	}
	// A budget flag that is not the trailing pair stays part of the objective.
	for _, objective := range []string{"document --budget handling", "set --budget 50 then more", "--budget 5000", "cap --budget x5"} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.NoError(t, err, objective)
		assert.Equal(t, "/goal "+objective, outcome.QueuedInput)
	}
}

func TestGrokGoalStatusMapping(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]agent.GoalStatus{
		"active": agent.GoalStatusActive, "user_paused": agent.GoalStatusPaused, "back_off_paused": agent.GoalStatusPaused,
		"no_progress_paused": agent.GoalStatusPaused, "infra_paused": agent.GoalStatusPaused, "doom_loop_paused": agent.GoalStatusPaused,
		"blocked": agent.GoalStatusBlocked, "budget_limited": agent.GoalStatusBlocked, "complete": agent.GoalStatusDone,
		"something-new": agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, grokGoalStatus(wire), wire)
	}
}

func TestGrokGoalUpdateReachesTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "goal_updated", "goal_id": "g-1", "objective": "Create goal.txt", "status": "infra_paused",
		"phase": "planning", "token_budget": 20000, "tokens_used": 1200, "elapsed_ms": 4500, "total_worker_rounds": 2,
		"pause_message": "No plan was produced",
	}))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "g-1", goal.NativeID)
	assert.Equal(t, "Create goal.txt", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "infra paused: No plan was produced", goal.StatusDetail)
	require.NotNil(t, goal.TokenBudget)
	assert.Equal(t, int64(20000), *goal.TokenBudget)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(1200), *goal.TokensUsed)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(4), *goal.TimeUsedSeconds)
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(2), *goal.Iterations)
}

func TestGrokGoalUpdateLeavesAbsentCountersAbsent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "goal_updated", "goal_id": "g-1", "objective": "Ship", "status": "active",
	}))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail, "an active goal needs no detail")
	assert.Nil(t, goal.TokenBudget)
	assert.Nil(t, goal.TokensUsed)
	assert.Nil(t, goal.TimeUsedSeconds)
	assert.Nil(t, goal.Iterations)
}

func TestGrokClearedGoalLeavesTheCard(t *testing.T) {
	t.Parallel()
	for name, update := range map[string]map[string]any{
		"cleared":    {"sessionUpdate": "goal_updated", "goal_id": "", "objective": "", "status": "cleared", "phase": "idle"},
		"no goal id": {"sessionUpdate": "goal_updated", "objective": "x", "status": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(notification(t, grokTestSession, update))
			assert.Equal(t, 1, sink.GoalClears())
			assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "a clear that Grok reports is a real transition")
			_, ok := sink.LastGoal()
			assert.False(t, ok)
		})
	}
}

func TestGrokChildGoalUpdateIsNotTheParentsGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// A row routes the child session, so the agent serves it, and only the
	// main-session rule keeps its goal off the parent's card.
	a.AttachChildSession("child-session", "call_spawn")
	a.HandleOutput(notification(t, "child-session", map[string]any{
		"sessionUpdate": "goal_updated", "goal_id": "g-child", "objective": "sub", "status": "active",
	}))
	_, ok := sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, sink.GoalClears())
}

func TestGrokSeedsItsCommandsFromTheInitializeResponse(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.seedAvailableCommands(json.RawMessage(`{"protocolVersion":1,"_meta":{"availableCommands":[{"name":"compact"},{"name":"goal"}]}}`))

	assert.True(t, a.HasAvailableCommand("goal"))
	assert.Len(t, a.SupportedGoalActions(), 4)
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	for _, response := range []string{`{}`, `{"_meta":{}}`, `{"_meta":{"availableCommands":"goal"}}`, `not json`} {
		a.seedAvailableCommands(json.RawMessage(response))
		assert.True(t, a.HasAvailableCommand("goal"), "a response with no list keeps the known commands: %s", response)
	}
}

func TestGrokGoalStatusDetail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status, message, want string
	}{
		{status: "active", message: "ignored", want: ""},
		{status: "user_paused", message: "ignored", want: ""},
		{status: "doom_loop_paused", message: "the legacy name of a user pause", want: ""},
		{status: "complete", message: "ignored", want: ""},
		{status: "back_off_paused", want: "back off paused"},
		{status: "blocked", message: " Waiting for CI ", want: "blocked: Waiting for CI"},
		{status: "budget_limited", message: "  ", want: "budget limited"},
		{status: "some_new_word", want: "some new word"},
	} {
		assert.Equal(t, tc.want, grokGoalStatusDetail(grokGoalUpdate{Status: tc.status, PauseMessage: tc.message}), tc.status)
	}
}

func TestGrokUnreadableGoalUpdateChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{"sessionUpdate": "goal_updated", "goal_id": 7, "status": "active"}))
	_, ok := sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, sink.GoalClears(), "an update that cannot be read does not state that no goal exists")
}

func TestGrokGoalUpdateCountsWholeSeconds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "goal_updated", "goal_id": "g-1", "objective": "Ship", "status": "active", "elapsed_ms": 999, "total_worker_rounds": 0,
	}))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(0), *goal.TimeUsedSeconds)
	require.NotNil(t, goal.Iterations, "a count of zero is a count, not an absent one")
	assert.Equal(t, int32(0), *goal.Iterations)
}

func TestGrokGoalSetRefusesABlankObjective(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	advertiseCommands(t, a, "goal")
	for _, objective := range []string{"", " \n\t "} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.Error(t, err, "%q", objective)
		assert.Empty(t, outcome.QueuedInput, "a refused goal queues nothing")
	}
}
