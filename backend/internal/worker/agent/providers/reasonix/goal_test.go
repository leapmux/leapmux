package reasonix

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixGoalClearsAnAbsentObjectiveAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			a.SetSessionIDForTest("session")
			a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"goal":{"status":"running","objective":"Keep this goal"}}}`))
			a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"goal":{"status":"` + status + `"}}}`))
			assert.Equal(t, 1, sink.GoalClears())
			assert.Len(t, sink.Goals(), 1, "the last turn status must not restore a cleared objective")
		})
	}
}

func TestReasonixGoalAdvertisesSetAndClearIndependently(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	assert.Empty(t, a.SupportedGoalActions())
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}})
	assert.ElementsMatch(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions())
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "normal"}})
	assert.Equal(t, []agent.GoalAction{agent.GoalActionClear}, a.SupportedGoalActions())
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "goal"}})
	assert.Empty(t, a.SupportedGoalActions(), "Set requires a way to remove the previous objective")
}

func TestReasonixGoalIgnoresAStatusWithoutItsRequiredState(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"status":{"goal":{}}}`,
		`{"status":{"goal":{"objective":"Incomplete snapshot"}}}`,
	} {
		sink := &agenttest.Sink{}
		a := &Agent{}
		a.SetSinkForTest(agent.NewProviderServices(sink))
		a.handleReasonixStatusUpdate(json.RawMessage(payload))
		assert.Zero(t, sink.GoalClears(), payload)
		assert.Empty(t, sink.Goals(), payload)
	}
}

func TestReasonixGoalKeepsAnObjectiveWithAnUnprojectedStoppedStatus(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"status":{"goal":{"status":"none","objective":"Keep the stopped goal","runtime":{"stopCause":"budget_spend"}}}}`))
	goal, present := sink.LastGoal()
	assert.True(t, present)
	assert.Equal(t, "Keep the stopped goal", goal.Objective)
	assert.Equal(t, agent.GoalStatusBlocked, goal.Status)
	assert.Equal(t, "budget_spend", goal.StatusDetail)
	assert.Zero(t, sink.GoalClears())
}

func TestReasonixGoalCapabilitiesRequireTheAdvertisedSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		meta string
		want []agent.GoalAction
	}{
		{"absent", `{}`, nil},
		{"pause", `{"_reasonix.io/session/goal/pause":{"schemaVersion":1}}`, []agent.GoalAction{agent.GoalActionPause}},
		{"resume", `{"_reasonix.io/session/goal/resume":{"schemaVersion":1}}`, []agent.GoalAction{agent.GoalActionResume}},
		{"unknown schema", `{"_reasonix.io/session/goal/pause":{"schemaVersion":2},"_reasonix.io/session/goal/resume":{"schemaVersion":0}}`, nil},
		{"invalid schema", `{"_reasonix.io/session/goal/pause":true,"_reasonix.io/session/goal/resume":{"schemaVersion":"1"}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &Agent{}
			a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}})
			a.observeReasonixCapabilities([]byte(`{"agentCapabilities":{"_meta":` + tc.meta + `}}`))
			want := append([]agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, tc.want...)
			assert.ElementsMatch(t, want, a.SupportedGoalActions())
			a.observeReasonixCapabilities([]byte(`{invalid`))
			assert.ElementsMatch(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions())
		})
	}
}

func TestReasonixManualPauseKeepsTheObjectiveAndCounters(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"blocked", "cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			a.handleReasonixStatusUpdate(json.RawMessage(`{"status":{"goal":{"status":"` + status + `","objective":"Retain this objective","runtime":{"turnsUsed":3,"tokensUsed":12345,"stopCause":"manual"}}}}`))
			goal, present := sink.LastGoal()
			require.True(t, present)
			assert.Equal(t, agent.GoalStatusPaused, goal.Status)
			assert.Equal(t, "Retain this objective", goal.Objective)
			require.NotNil(t, goal.Iterations)
			assert.EqualValues(t, 3, *goal.Iterations)
			require.NotNil(t, goal.TokensUsed)
			assert.EqualValues(t, 12345, *goal.TokensUsed)
			assert.Zero(t, sink.GoalClears())
		})
	}
}
