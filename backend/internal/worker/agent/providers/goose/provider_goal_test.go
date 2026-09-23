package goose

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGooseGoal_UsesTheAdvertisedGoalCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))

	assert.Empty(t, a.SupportedGoalActions())
	acptest.AdvertiseCommands(t, a, "compact", "goal")
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	setOutcome, err := a.PerformGoalAction(agent.GoalActionSet, "  ship\n it ")
	require.NoError(t, err)
	assert.Equal(t, "/goal ship it", setOutcome.QueuedInput)
	a.ObserveGoalCommand(agent.GoalDeliverySend, setOutcome.QueuedInput)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)

	for _, clearArg := range gooseGoalRoute.ClearArgs {
		a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal "+clearArg)
	}
	assert.Equal(t, len(gooseGoalRoute.ClearArgs), sink.GoalClears())
}

func TestACPGoal_CommandRemovalRepublishesCapabilities(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))

	acptest.AdvertiseCommands(t, a, "goal")
	acptest.AdvertiseCommands(t, a, "compact")
	assert.Empty(t, a.SupportedGoalActions())
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())
}

// Goose steers through a separate ACP method whose command handling LeapMux did
// not verify, so a steered command must claim nothing.
func TestGooseGoal_IgnoresASteeredCommand(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	acptest.AdvertiseCommands(t, a, "goal")

	a.ObserveGoalCommand(agent.GoalDeliverySteer, "/goal ship it")

	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// An observed goal command changes nothing while Goose has not advertised the
// command.
func TestGooseTextGoalObservationRequiresTheAdvertisedCapability(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

func TestGooseTextGoal_RefusesAnObjectiveThatClears(t *testing.T) {
	t.Parallel()
	providerkittest.AssertRefusesAnObjectiveThatClears(t, gooseGoalRoute)
}
