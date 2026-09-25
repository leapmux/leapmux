package ohmypi

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoalsAreDisplayOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	assert.Empty(t, r.agent.SupportedGoalActions(), "omp's RPC reaches no goal runtime")
}

func TestGoalUpdatedReportsTheGoal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"goal_updated","goal":{"id":"g1","objective":"Ship the release","status":"active","tokenBudget":50000,"tokensUsed":1200,"timeUsedSeconds":42,"createdAt":1790187118735,"updatedAt":1790187119000},"state":{"enabled":true}}`)

	goal, ok := r.sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "g1", goal.NativeID)
	assert.Equal(t, "Ship the release", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, "active", goal.StatusDetail)
	assert.Equal(t, time.UnixMilli(1790187118735).UTC(), goal.CreatedAt, "omp states milliseconds")
	require.NotNil(t, goal.TokenBudget)
	assert.Equal(t, int64(50000), *goal.TokenBudget)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(1200), *goal.TokensUsed)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(42), *goal.TimeUsedSeconds)
	assert.False(t, goal.Snapshot, "a goal that changed while the session runs is news")
}

func TestAGoalWithNoBudgetStatesNone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"goal_updated","goal":{"id":"g1","objective":"Ship","status":"paused","tokensUsed":0,"timeUsedSeconds":0,"createdAt":0}}`)
	goal, ok := r.sink.LastGoal()
	require.True(t, ok)
	assert.Nil(t, goal.TokenBudget)
	assert.True(t, goal.CreatedAt.IsZero(), "an absent time keeps the identity the sink has")
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
}

func TestAGoalReportedAtStartupIsASnapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.startupSnapshot.Store(true)
	r.emit(`{"type":"goal_updated","goal":{"id":"g1","objective":"Ship","status":"active","tokensUsed":0,"timeUsedSeconds":0,"createdAt":1}}`, `{"type":"goal_updated","goal":null}`)
	goal, ok := r.sink.LastGoal()
	require.True(t, ok)
	assert.True(t, goal.Snapshot)
	assert.Equal(t, []bool{true}, r.sink.GoalClearSnapshots())
}

func TestAGoalThatEndsIsCleared(t *testing.T) {
	t.Parallel()
	for name, frame := range map[string]string{
		"a null goal":        `{"type":"goal_updated","goal":null}`,
		"a dropped goal":     `{"type":"goal_updated","goal":{"id":"g1","objective":"Ship","status":"dropped"}}`,
		"a goal with no aim": `{"type":"goal_updated","goal":{"id":"g1","objective":"","status":"active"}}`,
	} {
		r := newRig(t)
		r.emit(frame)
		assert.Equal(t, 1, r.sink.GoalClears(), name)
		assert.Equal(t, []bool{false}, r.sink.GoalClearSnapshots(), name)
	}
}

func TestAMalformedGoalFrameChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"goal_updated","goal":"soon"}`)
	_, ok := r.sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, r.sink.GoalClears())
}

func TestGoalTime(t *testing.T) {
	t.Parallel()
	assert.True(t, goalTime(0).IsZero(), "an absent time")
	assert.True(t, goalTime(-1790187118735).IsZero(), "a time before 1970 is no creation time omp writes")
	assert.Equal(t, time.Date(2026, 9, 23, 18, 11, 58, 735000000, time.UTC), goalTime(1790187118735))
	assert.Equal(t, time.UTC, goalTime(1).Location())
}

func TestGoalStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.GoalStatusActive, goalStatus("active"))
	assert.Equal(t, agent.GoalStatusPaused, goalStatus("paused"))
	assert.Equal(t, agent.GoalStatusDone, goalStatus("complete"))
	assert.Equal(t, agent.GoalStatusBlocked, goalStatus("budget-limited"))
	assert.Equal(t, agent.GoalStatusBlocked, goalStatus("hibernating"), "a status this build cannot read")
}
