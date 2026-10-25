package ohmypi

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoalActions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	assert.ElementsMatch(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}, r.agent.SupportedGoalActions())
	_, ok := any(r.agent).(agent.GoalWriter)
	require.True(t, ok, "native goal commands require a GoalWriter")
}

func TestGoalActionsUseNativeCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		action    agent.GoalAction
		op        string
		objective string
	}{
		{"set", agent.GoalActionSet, "create", "Ship the release"},
		{"clear", agent.GoalActionClear, "drop", ""},
		{"pause", agent.GoalActionPause, "pause", ""},
		{"resume", agent.GoalActionResume, "resume", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			writer, ok := any(r.agent).(agent.GoalWriter)
			require.True(t, ok)
			r.respond(func(command recordedCommand) *rigReply {
				// ClearContext uses this mutex too, so it cannot replace the session during a goal command.
				unlocked := r.agent.sessionMu.TryLock()
				if unlocked {
					r.agent.sessionMu.Unlock()
				}
				assert.False(t, unlocked)
				return nil
			})
			outcome, err := writer.PerformGoalAction(tc.action, tc.objective)
			require.NoError(t, err)
			assert.Empty(t, outcome.QueuedInput, "the native goal controller starts its own work")
			commands := r.commandsOfType("goal")
			require.Len(t, commands, 1)
			assert.Equal(t, tc.op, commands[0].Payload["op"])
			if tc.action == agent.GoalActionSet {
				assert.Equal(t, tc.objective, commands[0].Payload["objective"])
			} else {
				assert.NotContains(t, commands[0].Payload, "objective")
			}
			_, reported := r.sink.LastGoal()
			assert.False(t, reported, "only native goal events publish state")
			assert.Zero(t, r.sink.GoalClears())
		})
	}
}

func TestGoalActionRejectionPreservesState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	writer, ok := any(r.agent).(agent.GoalWriter)
	require.True(t, ok)
	r.emit(`{"type":"goal_updated","goal":{"id":"g1","objective":"Keep this goal","status":"paused"}}`)
	r.respond(func(recordedCommand) *rigReply {
		return &rigReply{Error: "Resume or drop the paused goal before creating another."}
	})
	outcome, err := writer.PerformGoalAction(agent.GoalActionSet, "Replace it")
	require.ErrorContains(t, err, "Resume or drop the paused goal")
	assert.Empty(t, outcome.QueuedInput)
	goal, exists := r.sink.LastGoal()
	require.True(t, exists)
	assert.Equal(t, "Keep this goal", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Zero(t, r.sink.GoalClears())
}

func TestGoalActionValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		action    agent.GoalAction
		objective string
	}{
		{"empty objective", agent.GoalActionSet, ""},
		{"blank objective", agent.GoalActionSet, " \n\t"},
		{"negative action", agent.GoalAction(-1), "Ship"},
		{"unknown action", agent.GoalAction(1000), "Ship"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			writer, ok := any(r.agent).(agent.GoalWriter)
			require.True(t, ok)
			_, err := writer.PerformGoalAction(tc.action, tc.objective)
			require.Error(t, err)
			assert.Empty(t, r.commandsOfType("goal"))
		})
	}
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
