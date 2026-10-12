package codebuddy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// goalInitFrame is the init frame of a build that advertises /goal.
func goalInitFrame(commands string) string {
	return `{"type":"system","subtype":"init","session_id":"session-1","model":"m","permissionMode":"default","slash_commands":[` + commands + `]}`
}

func TestCodebuddyGoalOffersNothingUntilTheInitFrame(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	assert.Empty(t, a.SupportedGoalActions(),
		"the controls stay hidden until the CLI says this build has the command")

	a.HandleOutput([]byte(goalInitFrame(`"compress","goal"`)))

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the capability change re-publishes after registration")
}

func TestCodebuddyGoalHidesControlsWhenTheBuildHasNoGoalCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.HandleOutput([]byte(goalInitFrame(`"compress"`)))

	assert.Empty(t, a.SupportedGoalActions())
	// The route builds the text whatever the build supports; it is the observer
	// that refuses a delivery the CLI would read as a prompt.
	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
	require.NoError(t, err)
	assert.Equal(t, "/goal Ship it", outcome.QueuedInput)
}

// A frame that carries no command list at all leaves the answer UNKNOWN rather
// than clearing it, so a shape change does not overwrite a known "yes" with a
// silent "no". The observer keeps the same rule: only a POSITIVE "no" refuses a
// delivery.
func TestCodebuddyGoalInitWithoutACommandListLeavesTheCapabilityUnknown(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.HandleOutput([]byte(goalInitFrame(`"goal"`)))
	published := sink.GoalCapabilityPublishes()
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1"}`))

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions(),
		"a frame with no list answers nothing, so the known answer stands")
	assert.Equal(t, published, sink.GoalCapabilityPublishes(), "nothing changed, so nothing re-publishes")
}

func TestCodebuddyGoalActionsBuildTheGoalCommand(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(goalInitFrame(`"goal"`)))
	for _, tc := range []struct {
		action    agent.GoalAction
		objective string
		want      string
	}{
		{action: agent.GoalActionSet, objective: "Ship the\nrelease", want: "/goal Ship the release"},
		{action: agent.GoalActionSet, objective: "all tests pass", want: "/goal all tests pass"},
		{action: agent.GoalActionClear, want: "/goal clear"},
	} {
		outcome, err := a.PerformGoalAction(tc.action, tc.objective)
		require.NoError(t, err)
		assert.Equal(t, tc.want, outcome.QueuedInput)
	}
}

func TestCodebuddyGoalHasNoPauseAndNoResume(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(goalInitFrame(`"goal"`)))

	assert.NotContains(t, a.SupportedGoalActions(), agent.GoalActionPause)
	assert.NotContains(t, a.SupportedGoalActions(), agent.GoalActionResume)

	_, err := a.PerformGoalAction(agent.GoalActionPause, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	_, err = a.PerformGoalAction(agent.GoalActionResume, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
}

// A one-word objective that equals a clear alias would reach CodeBuddy as that
// clear, because the aliases are recognized on an exact single-token match.
func TestCodebuddyGoalRefusesAnObjectiveThatClears(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(goalInitFrame(`"goal"`)))
	providerkittest.AssertRefusesAnObjectiveThatClears(t, codebuddyGoalRoute)
}

// The observer is the only writer: no later frame restates the goal, so the row
// a delivered command writes stands until the next command.
func TestCodebuddyGoalObserverWritesAndClearsTheRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal all tests pass")

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "all tests pass", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)

	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal clear")
	assert.Equal(t, 1, sink.GoalClears())
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "the user just cleared it, so it is a real transition")
}

// CodeBuddy's steer channel is its own control request, not the user-message
// channel the command parser reads, so a steered command is not observed.
func TestCodebuddyGoalObserverIgnoresASteeredCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.ObserveGoalCommand(agent.GoalDeliverySteer, "/goal all tests pass")

	_, ok := sink.LastGoal()
	assert.False(t, ok)
}

// Before the init frame the capability is unknown, so an observed command still
// writes its row: a cold-started process can accept the command before its
// first stdout frame.
func TestCodebuddyGoalObserverAcceptsACommandBeforeInit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")

	_, ok := sink.LastGoal()
	assert.True(t, ok)
}

// Once the CLI positively reports a command list WITHOUT /goal, the observer
// refuses the delivery: the write would be text to the model, not a command.
func TestCodebuddyGoalObserverRefusesAfterABuildWithoutTheCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.HandleOutput([]byte(goalInitFrame(`"compress"`)))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")

	_, ok := sink.LastGoal()
	assert.False(t, ok)
}
