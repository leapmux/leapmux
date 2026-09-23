package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Kilo's `/goal` is the one text route that carries all four verbs. The command is
// Kilo's own addition to the OpenCode base it forks, which is why OpenCode has none.
func TestKiloGoal_UsesTheAdvertisedGoalCommandForEveryVerb(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	ag := &Agent{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))

	assert.Empty(t, ag.SupportedGoalActions(), "a build that lists no goal command offers no goal control")
	acptest.AdvertiseCommands(t, ag, "compact", "goal")
	assert.Equal(t,
		[]agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
		ag.SupportedGoalActions())

	setOutcome, err := ag.PerformGoalAction(agent.GoalActionSet, "  keep\n NOTES.md accurate ")
	require.NoError(t, err)
	assert.Equal(t, "/goal keep NOTES.md accurate", setOutcome.QueuedInput)
	ag.ObserveGoalCommand(agent.GoalDeliverySend, setOutcome.QueuedInput)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "keep NOTES.md accurate", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)

	// A pause keeps the objective and moves the status alone.
	pauseOutcome, err := ag.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal pause", pauseOutcome.QueuedInput)
	ag.ObserveGoalCommand(agent.GoalDeliverySend, pauseOutcome.QueuedInput)
	paused, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusPaused, paused.Status)
	assert.Equal(t, "keep NOTES.md accurate", paused.Objective, "a pause must not lose the objective")

	resumeOutcome, err := ag.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal resume", resumeOutcome.QueuedInput)
	ag.ObserveGoalCommand(agent.GoalDeliverySend, resumeOutcome.QueuedInput)
	resumed, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, resumed.Status)
	assert.Equal(t, "keep NOTES.md accurate", resumed.Objective)

	clearOutcome, err := ag.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal clear", clearOutcome.QueuedInput)
	ag.ObserveGoalCommand(agent.GoalDeliverySend, clearOutcome.QueuedInput)
	assert.Equal(t, 1, sink.GoalClears())
}

// The command is positional, so a one-word objective that equals a verb would reach Kilo
// as that verb. Refuse it rather than store a goal Kilo does not hold.
func TestKiloGoal_RefusesAnObjectiveThatReadsAsAVerb(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	for _, objective := range []string{"pause", "Resume", "CLEAR"} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, "objective %q", objective)
	}
}

// A goal command that Kilo never advertised changes nothing, the same rule every text
// route follows.
func TestKiloGoal_ObservesNothingWithoutTheAdvertisedCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}
