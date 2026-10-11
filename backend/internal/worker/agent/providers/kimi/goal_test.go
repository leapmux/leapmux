package kimi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func goalSnapshot(status string) map[string]any {
	return map[string]any{
		"goalId": "goal_1", "objective": "Ship the release", "status": status,
		"turnsUsed": 3, "tokensUsed": 12000, "wallClockMs": 95500, "budget": map[string]any{"tokenBudget": 50000},
	}
}

func TestKimiGoalUpdated(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})

	goal, ok := rig.sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "goal_1", goal.NativeID)
	assert.Equal(t, "Ship the release", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, "active", goal.StatusDetail)
	require.NotNil(t, goal.Iterations)
	assert.EqualValues(t, 3, *goal.Iterations)
	require.NotNil(t, goal.TokensUsed)
	assert.EqualValues(t, 12000, *goal.TokensUsed)
	require.NotNil(t, goal.TokenBudget)
	assert.EqualValues(t, 50000, *goal.TokenBudget)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.EqualValues(t, 95, *goal.TimeUsedSeconds)
	assert.False(t, goal.Snapshot)
	assert.False(t, goal.CreatedAt.IsZero())
	created := goal.CreatedAt

	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("paused")})
	goal, _ = rig.sink.LastGoal()
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, created, goal.CreatedAt, "the same goal keeps the time the worker first saw it")
}

func TestKimiGoalRemoval(t *testing.T) {
	t.Parallel()

	t.Run("a goal that completed leaves quietly", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("complete")})
		goal, _ := rig.sink.LastGoal()
		assert.Equal(t, agent.GoalStatusDone, goal.Status)
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": nil})
		assert.Equal(t, []bool{true}, rig.sink.GoalClearSnapshots(), "the completion is already in the transcript")
	})

	t.Run("a cancelled goal leaves with a row", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": nil})
		assert.Equal(t, []bool{false}, rig.sink.GoalClearSnapshots())
	})

	// GoalServices.ClearGoal forbids a skip because the provider's own copy is
	// empty: the worker's store can hold a goal from a previous process, which the
	// provider's copy never saw. A store with no goal writes no row.
	t.Run("a removal clears the stored goal even when the provider knew of none", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": nil})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": map[string]any{"objective": "  "}})
		assert.Equal(t, []bool{false, false}, rig.sink.GoalClearSnapshots())
	})

	t.Run("a subagent's goal is not the session's", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "agentId": "agent-0", "snapshot": goalSnapshot("active")})
		_, ok := rig.sink.LastGoal()
		assert.False(t, ok)
	})
}

func TestKimiGoalStatus(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.GoalStatusActive, kimiGoalStatus("active"))
	assert.Equal(t, agent.GoalStatusPaused, kimiGoalStatus("paused"))
	assert.Equal(t, agent.GoalStatusDone, kimiGoalStatus("complete"))
	assert.Equal(t, agent.GoalStatusBlocked, kimiGoalStatus("blocked"))
	assert.Equal(t, agent.GoalStatusBlocked, kimiGoalStatus("dreaming"), "a state LeapMux cannot read offers no Pause")
}

func TestKimiGoalActions(t *testing.T) {
	t.Parallel()

	profileBodies := func(t *testing.T, rig *kimiTestRig) []string {
		t.Helper()
		var out []string
		for _, request := range rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile")) {
			out = append(out, string(request.Body))
		}
		return out
	}

	t.Run("every action once the engine runs goals", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
			rig.agent.SupportedGoalActions())
	})

	t.Run("none without the goal feature", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.setFeatures()
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{})
		assert.Empty(t, rig.agent.SupportedGoalActions())
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Ship it")
		require.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	})

	t.Run("a new goal starts its work as the user's message", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		before := len(profileBodies(t, rig))
		outcome, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "  Ship the release  ")
		require.NoError(t, err)
		assert.Equal(t, "Ship the release", outcome.QueuedInput)
		bodies := profileBodies(t, rig)[before:]
		require.Len(t, bodies, 1)
		assert.JSONEq(t, `{"agent_config":{"goal_objective":"Ship the release"}}`, bodies[0])
	})

	t.Run("a new objective replaces the running goal", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})
		before := len(profileBodies(t, rig))
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Another goal")
		require.NoError(t, err)
		bodies := profileBodies(t, rig)[before:]
		require.Len(t, bodies, 2)
		assert.JSONEq(t, `{"agent_config":{"goal_control":"cancel"}}`, bodies[0])
		assert.JSONEq(t, `{"agent_config":{"goal_objective":"Another goal"}}`, bodies[1])
	})

	t.Run("a goal the server refuses because one exists", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: kimiCodeGoalExists, Msg: "a goal exists"})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Ship it")
		require.ErrorContains(t, err, "already runs a goal")
	})

	t.Run("a goal needs an objective", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, " \n ")
		require.ErrorContains(t, err, "objective")
	})

	t.Run("clear, pause and resume are goal controls", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		before := len(profileBodies(t, rig))
		for _, action := range []agent.GoalAction{agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume} {
			outcome, err := rig.agent.PerformGoalAction(action, "")
			require.NoError(t, err)
			assert.Empty(t, outcome.QueuedInput)
		}
		bodies := profileBodies(t, rig)[before:]
		require.Len(t, bodies, 3)
		for i, control := range []string{"cancel", "pause", "resume"} {
			var body struct {
				Config map[string]string `json:"agent_config"`
			}
			require.NoError(t, json.Unmarshal([]byte(bodies[i]), &body))
			assert.Equal(t, control, body.Config[kimiConfigGoalControl])
		}
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)), "an idle goal needs no abort")
		assert.Empty(t, rig.sink.GoalClearSnapshots(), "a cancel that the server took is reported by its goal.updated, not by the action")
	})

	t.Run("a new objective is not created when the running goal cannot be cancelled", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})
		before := len(profileBodies(t, rig))
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: 50000, Msg: "goal store locked"})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Another goal")
		require.ErrorContains(t, err, "goal store locked")
		bodies := profileBodies(t, rig)[before:]
		require.Len(t, bodies, 1, "the create never runs")
		assert.JSONEq(t, `{"agent_config":{"goal_control":"cancel"}}`, bodies[0])
	})

	t.Run("a pause succeeds when the abort of the running turn fails", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "system_trigger"}})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort), fakeKapReply{Code: 50000, Msg: "no turn"})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionPause, "")
		require.NoError(t, err, "the goal is paused, which is what the reader asked for")
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)), 1)
	})

	t.Run("a resume the server refuses fails", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: kimiCodeGoalNotFound, Msg: "No current goal"})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionResume, "")
		require.ErrorContains(t, err, "No current goal")
	})

	t.Run("an agent with no session refuses every action", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.agent.Mu.Lock()
		rig.agent.sessionID = ""
		rig.agent.Mu.Unlock()
		before := len(rig.fake.routes())
		_, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Ship it")
		require.ErrorContains(t, err, "session id")
		assert.Len(t, rig.fake.routes(), before)
	})

	t.Run("a pause stops the running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "system_trigger"}})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionPause, "")
		require.NoError(t, err)
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), kimiActionAbort)), 1)
	})

	// The server removes a goal by itself once it completes, so a Clear or a Set
	// can reach a session whose goal is gone. cancelGoal refuses that with
	// GOAL_NOT_FOUND.
	t.Run("a clear of a goal the server already removed succeeds", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: kimiCodeGoalNotFound, Msg: "No current goal"})
		outcome, err := rig.agent.PerformGoalAction(agent.GoalActionClear, "")
		require.NoError(t, err, "the session has no goal, which is the state the reader asked for")
		assert.Empty(t, outcome.QueuedInput)
		assert.Equal(t, []bool{false}, rig.sink.GoalClearSnapshots(), "LeapMux's copy of the goal goes too")
	})

	t.Run("a new objective replaces a goal the server already removed", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})
		before := len(profileBodies(t, rig))
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"),
			fakeKapReply{Code: kimiCodeGoalNotFound, Msg: "No current goal"}, fakeKapReply{})
		outcome, err := rig.agent.PerformGoalAction(agent.GoalActionSet, "Another goal")
		require.NoError(t, err, "there was nothing to replace")
		assert.Equal(t, "Another goal", outcome.QueuedInput)
		bodies := profileBodies(t, rig)[before:]
		require.Len(t, bodies, 2)
		assert.JSONEq(t, `{"agent_config":{"goal_control":"cancel"}}`, bodies[0])
		assert.JSONEq(t, `{"agent_config":{"goal_objective":"Another goal"}}`, bodies[1])
	})

	t.Run("a pause of a goal the server already removed fails", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: kimiCodeGoalNotFound, Msg: "No current goal"})
		_, err := rig.agent.PerformGoalAction(agent.GoalActionPause, "")
		require.ErrorContains(t, err, "No current goal", "no goal is left to pause, and the reader must learn it")
	})

	t.Run("an action this build does not know", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		_, err := rig.agent.PerformGoalAction(agent.GoalAction(99), "")
		require.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	})
}

func TestKimiReadsTheGoalOfAResumedSession(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual", Goal: goalSnapshot("paused")})
	rig := connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
	goal, ok := rig.sink.LastGoal()
	require.True(t, ok)
	assert.True(t, goal.Snapshot, "a resume restates the goal; it is not a change")
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
}

// Each field of a goal snapshot beside its id, objective and status is
// optional, and an absent one must read as unknown, never as zero.
func TestKimiGoalWithAbsentFields(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": map[string]any{
		"goalId": "goal_1", "objective": "Ship the release", "status": "active",
	}})
	goal, ok := rig.sink.LastGoal()
	require.True(t, ok)
	assert.Nil(t, goal.Iterations)
	assert.Nil(t, goal.TokensUsed)
	assert.Nil(t, goal.TokenBudget)
	assert.Nil(t, goal.TimeUsedSeconds)

	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": map[string]any{
		"goalId": "goal_1", "objective": "Ship the release", "status": "active", "budget": map[string]any{}, "wallClockMs": 999,
	}})
	goal, _ = rig.sink.LastGoal()
	assert.Nil(t, goal.TokenBudget, "a budget that states no token limit sets none")
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Zero(t, *goal.TimeUsedSeconds, "the time is whole seconds, rounded down")
}

func TestKimiReadGoalOfAResumedSession(t *testing.T) {
	t.Parallel()

	t.Run("a session with no goal clears the stored one quietly", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual"})
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
		assert.Equal(t, []bool{true}, rig.sink.GoalClearSnapshots(),
			"the worker's store can hold a goal from a previous process, and a restatement writes no row")
		_, ok := rig.sink.LastGoal()
		assert.False(t, ok)
	})

	t.Run("an engine with no goal feature reads no goal", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.setFeatures()
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual", Goal: goalSnapshot("active")})
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
		assert.Empty(t, fake.requestsTo("GET "+kimiSessionPath("session_stored", "/goal")))
		_, ok := rig.sink.LastGoal()
		assert.False(t, ok)
		assert.Empty(t, rig.sink.GoalClearSnapshots())
	})

	t.Run("a goal read that fails changes nothing", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual", Goal: goalSnapshot("active")})
		fake.reply("GET "+kimiSessionPath("session_stored", "/goal"), fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "goal store down"})
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
		_, ok := rig.sink.LastGoal()
		assert.False(t, ok)
		assert.Empty(t, rig.sink.GoalClearSnapshots(), "a read that failed proves nothing about the goal")
		assert.Equal(t, "session_stored", rig.sessionID(), "the resume itself succeeds")
	})
}

// The server states no creation time, so the worker stamps a goal from its
// clock the first time it sees the goal's id. A later report of the same goal
// keeps the stamp. A new goal id, or the same id after a removal, takes a new
// one.
func TestKimiGoalCreatedAtFollowsTheGoalID(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	first := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	clock.Set(first).MustWait(ctx)
	rig := newKimiOutputRig(t)
	rig.agent.clock = clock
	report := func(goalID, status string) agent.GoalUpdate {
		t.Helper()
		snapshot := goalSnapshot(status)
		snapshot["goalId"] = goalID
		rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": snapshot})
		goal, ok := rig.sink.LastGoal()
		require.True(t, ok)
		return goal
	}

	assert.Equal(t, first, report("goal_1", "active").CreatedAt, "the first sight stamps the goal")
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Equal(t, first, report("goal_1", "paused").CreatedAt, "the same goal keeps its stamp")
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Equal(t, first.Add(2*time.Minute), report("goal_2", "active").CreatedAt, "a new goal id takes a new stamp")

	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": nil})
	clock.Advance(time.Minute).MustWait(ctx)
	assert.Equal(t, first.Add(3*time.Minute), report("goal_2", "active").CreatedAt, "a goal that returns after a removal takes a new stamp")
}
