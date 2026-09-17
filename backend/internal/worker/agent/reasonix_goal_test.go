package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixGoalClearsAnAbsentObjectiveAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			a := &ReasonixAgent{}
			a.sink = sink
			a.sessionID = "session"
			a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"goal":{"status":"running","objective":"Keep this goal"}}}`))
			a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"goal":{"status":"` + status + `"}}}`))
			assert.Equal(t, 1, sink.GoalClears())
			assert.Len(t, sink.Goals(), 1, "the last turn status must not restore a cleared objective")
		})
	}
}

func TestReasonixGoalAdvertisesSetAndClearIndependently(t *testing.T) {
	t.Parallel()
	a := &ReasonixAgent{}
	assert.Empty(t, a.SupportedGoalActions())
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}}
	assert.ElementsMatch(t, []GoalAction{GoalActionSet, GoalActionClear}, a.SupportedGoalActions())
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "normal"}}
	assert.Equal(t, []GoalAction{GoalActionClear}, a.SupportedGoalActions())
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "goal"}}
	assert.Empty(t, a.SupportedGoalActions(), "Set requires a way to remove the previous objective")
}

func TestReasonixGoalIgnoresAStatusWithoutItsRequiredState(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"status":{"goal":{}}}`,
		`{"status":{"goal":{"objective":"Incomplete snapshot"}}}`,
	} {
		sink := &testSink{}
		a := &ReasonixAgent{}
		a.sink = sink
		a.handleReasonixStatusUpdate(json.RawMessage(payload))
		assert.Zero(t, sink.GoalClears(), payload)
		assert.Empty(t, sink.Goals(), payload)
	}
}

func TestReasonixGoalKeepsAnObjectiveWithAnUnprojectedStoppedStatus(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	a := &ReasonixAgent{}
	a.sink = sink
	a.handleReasonixStatusUpdate(json.RawMessage(`{"status":{"goal":{"status":"none","objective":"Keep the stopped goal","runtime":{"stopCause":"budget_spend"}}}}`))
	goal, present := sink.LastGoal()
	assert.True(t, present)
	assert.Equal(t, "Keep the stopped goal", goal.Objective)
	assert.Equal(t, GoalStatusBlocked, goal.Status)
	assert.Equal(t, "budget_spend", goal.StatusDetail)
	assert.Zero(t, sink.GoalClears())
}

// Reasonix reports its whole status on every change, so the same phase and the
// same totals arrive many times per turn. Only what MOVED says anything.
func TestReasonixStatusUpdateReportsUsageAndPhaseChanges(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := &ReasonixAgent{}
	a.sink = sink
	a.sessionID = "session"

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{
		"phase":"implementing",
		"usage":{"cumulative":{"totalTokens":1500,"promptTokens":1200,"completionTokens":300,
		"cacheHitTokens":800,"cacheMissTokens":400,"estimatedCost":0.0042}}}}`))

	// The CUMULATIVE half. The turn half restarts, and a gauge that restarted
	// with it would report a context that shrank.
	last := sink.LastSessionInfo()
	usage, ok := last[contracts.SessionInfoKeyContextUsage].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1500), usage[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, int64(1200), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(800), usage[contracts.ContextUsageFieldCacheReadInputTokens])
	assert.Equal(t, 0.0042, last[contracts.SessionInfoKeyTotalCostUsd])
	// A working phase says what every other surface already says.
	assert.Empty(t, sink.Notifications())

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"waiting_permission"}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"waiting_permission"}}`))
	notifications := sink.Notifications()
	require.Len(t, notifications, 1, "a restated phase states nothing new")
	assert.Equal(t, contracts.NotificationTypeAgentStatus, notifications[0]["type"])
	assert.Equal(t, "Waiting for permission", notifications[0]["text"])
}
