package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiGoalToolResultUpdatesSharedGoal(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"create_goal", "get_goal", "update_goal", "set_goal_tasks", "update_goal_task"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			a := &PiAgent{sink: sink}
			a.HandleOutput([]byte(` {"type":"tool_execution_end","toolCallId":"goal-call","toolName":"` + tool + `","result":{"content":[],"details":{"version":3,"goal":{"id":"native-goal","objective":"Keep the exact objective.\nSecond line.","status":"paused","createdAt":"2026-09-12T04:37:25.401Z","usage":{"tokensUsed":0,"activeSeconds":12},"tokenBudget":2000}}}} `))
			goal, ok := sink.LastGoal()
			require.True(t, ok)
			assert.Equal(t, "native-goal", goal.NativeID)
			assert.Equal(t, "Keep the exact objective.\nSecond line.", goal.Objective)
			assert.Equal(t, GoalStatusPaused, goal.Status)
			assert.Equal(t, "paused", goal.StatusDetail)
			assert.Equal(t, time.Date(2026, 9, 12, 4, 37, 25, 401000000, time.UTC), goal.CreatedAt)
			require.NotNil(t, goal.TokensUsed)
			assert.Zero(t, *goal.TokensUsed)
			require.NotNil(t, goal.TimeUsedSeconds)
			assert.Equal(t, int64(12), *goal.TimeUsedSeconds)
			require.NotNil(t, goal.TokenBudget)
			assert.Equal(t, int64(2000), *goal.TokenBudget)
		})
	}
}

func TestPiGoalToolResultRequiresAnExplicitGoal(t *testing.T) {
	t.Parallel()
	for _, details := range []string{`{}`, `{"version":3}`, `{"version":3,"goal":{}}`, `{"version":3,"goal":"invalid"}`} {
		t.Run(details, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			a := &PiAgent{sink: sink}
			a.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"goal-call","toolName":"get_goal","result":{"content":[],"details":` + details + `}}`))
			assert.Empty(t, sink.Goals())
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func TestPiGoalToolResultClearsAnExplicitNullGoal(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	a := &PiAgent{sink: sink}
	a.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"goal-call","toolName":"get_goal","result":{"content":[],"details":{"version":3,"goal":null}}}`))
	assert.Equal(t, 1, sink.GoalClears())
}
