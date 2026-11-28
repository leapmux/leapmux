package kimi

import (
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeGoalFeedPreservesRESTMetadata(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual", Goal: goalSnapshot("active")})
	rig := connectKimiTestRig(t, fake, server.URL, agent.Options{Session: agent.ResumeSession("session_stored")})
	waitFor(t, func() bool {
		goal, present := rig.sink.LastGoal()
		return present && goal.NativeID == "goal_1"
	}, "the optional REST snapshot enriches the native subscription goal")
	goal, present := rig.sink.LastGoal()
	require.True(t, present)
	createdAt := goal.CreatedAt
	rig.agent.HandleOutput([]byte(`{"type":"goal","session_id":"session_stored","agent_id":"main","goal":{"objective":"Ship the release","status":"paused","turns_used":4,"tokens_used":12001,"wall_clock_ms":96000}}`))
	goal, present = rig.sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "goal_1", goal.NativeID)
	assert.Equal(t, createdAt, goal.CreatedAt)
	require.NotNil(t, goal.TokenBudget)
	assert.EqualValues(t, 50000, *goal.TokenBudget)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	require.NotNil(t, goal.TokensUsed)
	assert.EqualValues(t, 12001, *goal.TokensUsed)

	applyKimiRESTGoalForTest(t, rig.agent, map[string]any{"goalId": "goal_1", "objective": "Ship the release", "status": "paused"})
	goal, _ = rig.sink.LastGoal()
	assert.Nil(t, goal.TokenBudget, "a complete native REST snapshot can remove the budget")
	rig.agent.HandleOutput([]byte(`{"type":"goal","session_id":"session_stored","agent_id":"main"}`))
	assert.Empty(t, rig.agent.goal.snapshot.Objective)
}

func TestKimiNativeGoalClearsBeforeItsLateMetadataReply(t *testing.T) {
	t.Parallel()
	testKimiGoalMetadataOrdering(t, true)
}

func TestKimiNativeGoalMetadataRetainsNewerUsage(t *testing.T) {
	t.Parallel()
	testKimiGoalMetadataOrdering(t, false)
}

func testKimiGoalMetadataOrdering(t *testing.T, clear bool) {
	t.Helper()
	fake, server := newFakeKap(t)
	ctx := testutil.DeadlineContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseResponse)
	fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual", Goal: goalSnapshot("active")})
	fake.reply("GET "+kimiSessionPath("session_stored", "/goal"), fakeKapReply{
		Data: goalSnapshot("active"),
		BeforeReply: func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	})
	rig := connectKimiTestRig(t, fake, server.URL, agent.Options{Session: agent.ResumeSession("session_stored")})
	defer releaseResponse()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the flat goal did not start an optional metadata read")
	}
	goal, present := rig.sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "Ship the release", goal.Objective, "the flat state is published before the optional read")
	if clear {
		rig.agent.HandleOutput([]byte(`{"type":"goal","session_id":"session_stored","agent_id":"main"}`))
		assert.Equal(t, []bool{false}, rig.sink.GoalClearSnapshots())
	} else {
		rig.agent.HandleOutput([]byte(`{"type":"goal","session_id":"session_stored","agent_id":"main","goal":{"objective":"Ship the release","status":"active","turns_used":9,"tokens_used":54321,"wall_clock_ms":123000}}`))
	}
	releaseResponse()
	waitFor(t, rig.agent.goalMetadataWork.Idle, "the delayed metadata request finishes")
	if clear {
		rig.agent.Mu.Lock()
		objective := rig.agent.goal.snapshot.Objective
		rig.agent.Mu.Unlock()
		assert.Empty(t, objective, "the delayed metadata cannot restore a cleared goal")
		assert.Len(t, rig.sink.Goals(), 1, "the delayed metadata cannot republish a cleared goal")
		return
	}
	goal, present = rig.sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "goal_1", goal.NativeID)
	require.NotNil(t, goal.TokenBudget)
	assert.EqualValues(t, 50000, *goal.TokenBudget)
	require.NotNil(t, goal.TokensUsed)
	assert.EqualValues(t, 54321, *goal.TokensUsed, "older REST usage cannot replace newer feed usage")
}

func TestKimiReconnectPublishesOnlyTheLatestGoalState(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.update(rig.sessionID(), func(session *fakeKapSession) {
		session.Goal = map[string]any{"objective": "Old objective", "status": "active"}
	})
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(sub fakeKapSubscribe) {
		rig.fake.update(sub.SessionID, func(session *fakeKapSession) {
			session.Goal = map[string]any{"objective": "Current objective", "status": "paused"}
		})
	}
	rig.fake.mu.Unlock()
	finished := beginNativeKimiReconnect(t, rig)
	waitFor(t, finished, "the native subscription dispatches its current goal")
	goals := rig.sink.Goals()
	require.Len(t, goals, 1)
	assert.Equal(t, "Current objective", goals[0].Objective)
	assert.Equal(t, agent.GoalStatusPaused, goals[0].Status)
}
