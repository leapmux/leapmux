package codewhale

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

var goalRoute = threadPath(testThreadID, threadRouteGoal)

func TestCodewhaleGoalStatus(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]agent.GoalStatus{
		"active":         agent.GoalStatusActive,
		"paused":         agent.GoalStatusPaused,
		"complete":       agent.GoalStatusDone,
		"blocked":        agent.GoalStatusBlocked,
		"usage_limited":  agent.GoalStatusBlocked,
		"budget_limited": agent.GoalStatusBlocked,
		"a_later_word":   agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, codewhaleGoalStatus(word), word)
	}
}

func TestAGoalUpdateReachesTheSink(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "thread_goal_updated", "", "", map[string]any{
		"kind": "thread_goal_updated",
		"goal": map[string]any{
			"thread_id": testThreadID, "goal_id": "goal-1", "objective": "Write a haiku.", "status": "active",
			"tokens_used": 10, "time_used_seconds": 3, "continuation_count": 2, "created_at": 1790187037, "token_budget": nil,
		},
	}))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "goal-1", goal.NativeID)
	assert.Equal(t, "Write a haiku.", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, "active", goal.StatusDetail)
	assert.Equal(t, time.Unix(1790187037, 0).UTC(), goal.CreatedAt)
	require.NotNil(t, goal.TokensUsed)
	assert.EqualValues(t, 10, *goal.TokensUsed)
	require.NotNil(t, goal.Iterations)
	assert.EqualValues(t, 2, *goal.Iterations)
	assert.Nil(t, goal.TokenBudget)
	assert.False(t, goal.Snapshot)
}

func TestAGoalEventOfAnotherThreadIsIgnored(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput([]byte(`{"seq":1,"event":"thread_goal_updated","payload":{"goal":{"thread_id":"thr_other","objective":"Theirs","status":"active"}}}`))
	a.HandleOutput([]byte(`{"seq":2,"event":"thread_goal_cleared","payload":{"thread_id":"thr_other"}}`))
	a.HandleOutput(runtimeEvent(3, "thread_goal_updated", "", "", map[string]any{"goal": nil}))
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())

	a.HandleOutput(runtimeEvent(4, "thread_goal_cleared", "", "", map[string]any{"kind": "thread_goal_cleared", "thread_id": testThreadID}))
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "a cleared goal is a real removal")
}

func TestCodewhaleGoalTime(t *testing.T) {
	t.Parallel()
	assert.True(t, codewhaleGoalTime(0).IsZero())
	assert.True(t, codewhaleGoalTime(-5).IsZero())
	assert.Equal(t, time.Unix(60, 0).UTC(), codewhaleGoalTime(60))
}

func TestGoalActions(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPut, goalRoute, http.StatusOK, map[string]any{"objective": "Ship it."})
	rt.respondJSON(http.MethodDelete, goalRoute, http.StatusOK, map[string]any{})
	a, sink := newTestAgent(t, rt)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions())

	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it.")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"objective": "Ship it."}, rt.lastBody(t, http.MethodPut, goalRoute))
	assert.Empty(t, sink.Goals(), "the runtime's own event updates the goal, not the reply")

	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Len(t, rt.requestsTo(http.MethodDelete, goalRoute), 1)

	_, err = a.PerformGoalAction(agent.GoalActionPause, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported, "the runtime has no pause route")
}

func TestClearingAGoalTheThreadDoesNotHold(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodDelete, goalRoute, http.StatusNotFound, "no goal")
	a, sink := newTestAgent(t, rt)
	_, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err, "no goal is the state the reader asked for")
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots())

	failing := newFakeRuntime(t)
	failing.respondStatus(http.MethodDelete, goalRoute, http.StatusInternalServerError, "boom")
	b, bSink := newTestAgent(t, failing)
	_, err = b.PerformGoalAction(agent.GoalActionClear, "")
	assert.ErrorContains(t, err, "boom")
	assert.Zero(t, bSink.GoalClears(), "a clear that failed leaves the goal as it is")

	c, _ := newTestAgent(t, rt)
	c.threadID = ""
	_, err = c.PerformGoalAction(agent.GoalActionSet, "x")
	assert.ErrorContains(t, err, "no thread")
	assert.Empty(t, rt.requestsTo(http.MethodPut, goalRoute), "an agent with no thread sends nothing")
}

func TestSettingAGoalThatTheRuntimeRefusesFails(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPut, goalRoute, http.StatusConflict, "Thread already has an active turn")
	a, sink := newTestAgent(t, rt)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it.")
	assert.ErrorContains(t, err, "active turn")
	assert.Empty(t, sink.Goals())
}

// A goal event that states no thread is the agent's own: the runtime of one
// agent runs one thread.
func TestAGoalEventThatStatesNoThreadReachesTheSink(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "thread_goal_updated", "", "", map[string]any{"goal": map[string]any{"objective": "Ship", "status": "complete"}}))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusDone, goal.Status)
	assert.True(t, goal.CreatedAt.IsZero(), "an absent created_at states no time")
	assert.Nil(t, goal.TokensUsed)
	assert.Nil(t, goal.Iterations)

	a.HandleOutput(runtimeEvent(2, "thread_goal_cleared", "", "", map[string]any{}))
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots())
}

// A goal read that establishes nothing restates nothing: the goal the store
// holds stays as it is.
func TestAResumedThreadWhoseGoalReadEstablishesNothingRestatesNothing(t *testing.T) {
	t.Parallel()
	failing := newFakeRuntime(t)
	failing.respondStatus(http.MethodGet, goalRoute, http.StatusInternalServerError, "boom")
	a, sink := newTestAgent(t, failing)
	a.syncGoalSnapshot(testThreadID)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())

	empty := newFakeRuntime(t)
	empty.respondJSON(http.MethodGet, goalRoute, http.StatusOK, map[string]any{"goal_id": "goal-1", "objective": ""})
	b, bSink := newTestAgent(t, empty)
	b.syncGoalSnapshot(testThreadID)
	assert.Empty(t, bSink.Goals(), "a goal with no objective is no goal to restate")
	assert.Zero(t, bSink.GoalClears())
}

func TestAResumedThreadRestatesItsGoal(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodGet, goalRoute, http.StatusOK, map[string]any{"goal_id": "goal-1", "objective": "Ship it.", "status": "paused", "created_at": 60})
	a, sink := newTestAgent(t, rt)
	a.syncGoalSnapshot(testThreadID)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.True(t, goal.Snapshot, "a restated goal writes no transcript row")
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)

	none := newFakeRuntime(t)
	none.respondStatus(http.MethodGet, goalRoute, http.StatusNotFound, "no goal")
	b, bSink := newTestAgent(t, none)
	b.syncGoalSnapshot(testThreadID)
	assert.Equal(t, []bool{true}, bSink.GoalClearSnapshots(), "a thread with no goal restates that too")
}
