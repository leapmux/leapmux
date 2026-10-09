package service

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goalCacheReplay(t *testing.T, dispatcher *channel.Dispatcher) []*leapmuxv1.AgentEvent {
	t.Helper()
	writer := &replayIdentityWriter{testResponseWriter: &testResponseWriter{channelID: "goal-cache-replay"}}
	openReplayIdentityWatch(t, dispatcher, &leapmuxv1.WatchEventsRequest{
		UpdateId: 83, Agents: []*leapmuxv1.WatchAgentEntry{replayOriginEntry("goal-publication-owner", 83)},
	}, writer)
	require.Eventually(t, func() bool { return countCatchUpCompletes(writer.testResponseWriter) == 1 }, 30*time.Second, time.Millisecond)
	return decodeAgentEvents(writer.testResponseWriter)
}

func goalCacheReplayedGoal(t *testing.T, events []*leapmuxv1.AgentEvent) *leapmuxv1.AgentGoalChanged {
	t.Helper()
	var goal *leapmuxv1.AgentGoalChanged
	for _, event := range events {
		if event.GetGoalChanged() != nil {
			goal = event.GetGoalChanged()
		}
	}
	require.NotNil(t, goal, "the actual catch-up stream must project the stored goal")
	return goal
}

func goalCacheProgressStages(t *testing.T, events []*leapmuxv1.AgentEvent) []string {
	t.Helper()
	var progress []string
	for _, stage := range goalPublicationStages(t, events) {
		if strings.HasPrefix(stage, "progress:") {
			progress = append(progress, stage)
		}
	}
	return progress
}

func TestGoalCacheReplacementPublishesEqualCounters(t *testing.T) {
	for _, tokens := range []int64{0, -1, 23, math.MaxInt64} {
		t.Run(strconv.FormatInt(tokens, 10), func(t *testing.T) {
			svc, dispatcher, writer, services, _ := goalPublicationFixture(t)
			services.UpsertGoal(goalPublicationUpdate("Goal A", tokens))
			before := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(goalPublicationUpdate("Goal B", tokens))
			assert.Equal(t, "Goal B", mustGoalRow(t, svc).GoalObjective)
			assert.Equal(t, []string{"goal:Goal B", "notification:Goal B", "progress:" + strconv.FormatInt(tokens, 10)},
				goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]),
				"a replacement card needs its counters even when their bytes equal the previous goal's counters")
			replay := goalCacheReplay(t, dispatcher)
			assert.Equal(t, "Goal B", goalCacheReplayedGoal(t, replay).GetGoal().GetObjective())
			assert.Equal(t, []string{"progress:" + strconv.FormatInt(tokens, 10)}, goalCacheProgressStages(t, replay))
			assert.Equal(t, 2, goalNotificationCount(t, svc, "goal-publication-owner"))
		})
	}
}

func TestGoalCacheReplacementWithoutCountersDropsPreviousProgress(t *testing.T) {
	for _, replacement := range []string{"objective", "native identity", "creation time"} {
		t.Run(replacement, func(t *testing.T) {
			svc, dispatcher, writer, services, _ := goalPublicationFixture(t)
			original := goalPublicationUpdate("Standing goal", 23)
			if replacement == "creation time" {
				original.NativeID = ""
			}
			services.UpsertGoal(original)
			update := original
			update.TokensUsed = nil
			switch replacement {
			case "objective":
				update.Objective = "Replacement goal"
			case "native identity":
				update.NativeID = "replacement-native-id"
			case "creation time":
				update.CreatedAt = original.CreatedAt.Add(time.Second)
			}
			before := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(update)
			assert.Empty(t, goalCacheProgressStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			assert.NotContains(t, sink.sessionInfoSnapshot(), contracts.SessionInfoKeyGoalProgress,
				"an accepted replacement without counters must remove the previous goal's cached counters")
			replay := goalCacheReplay(t, dispatcher)
			replayed := goalCacheReplayedGoal(t, replay).GetGoal()
			require.NotNil(t, replayed)
			assert.Equal(t, update.Objective, replayed.GetObjective())
			assert.Equal(t, update.NativeID, replayed.GetNativeId())
			assert.Empty(t, goalCacheProgressStages(t, replay), "the real replay must not attach old counters to the replacement goal")
		})
	}
}

func TestGoalCacheClearDropsProgressFromActualReplay(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(strconv.FormatBool(snapshot), func(t *testing.T) {
			svc, dispatcher, _, services, _ := goalPublicationFixture(t)
			services.UpsertGoal(goalPublicationUpdate("Clear this goal", 23))
			services.ClearGoal(snapshot)
			assert.Empty(t, mustGoalRow(t, svc).GoalObjective)
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			assert.NotContains(t, sink.sessionInfoSnapshot(), contracts.SessionInfoKeyGoalProgress)
			replay := goalCacheReplay(t, dispatcher)
			assert.Nil(t, goalCacheReplayedGoal(t, replay).GetGoal())
			assert.Empty(t, goalCacheProgressStages(t, replay), "a clear must remove the cached counters before real catch-up")
		})
	}
}

func TestGoalCacheSameGoalChangesPreserveProgress(t *testing.T) {
	for _, change := range []string{"detail", "status", "native identity enrichment"} {
		t.Run(change, func(t *testing.T) {
			svc, dispatcher, writer, services, _ := goalPublicationFixture(t)
			original := goalPublicationUpdate("Standing goal", 23)
			if change == "native identity enrichment" {
				original.NativeID = ""
			}
			services.UpsertGoal(original)
			before := len(decodeAgentEvents(writer.testResponseWriter))
			update := original
			update.TokensUsed = nil
			switch change {
			case "detail":
				update.StatusDetail = "new detail"
				services.UpsertGoal(update)
			case "status":
				services.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
			case "native identity enrichment":
				update.NativeID = "learned-native-id"
				services.UpsertGoal(update)
			}
			assert.Empty(t, goalCacheProgressStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			assert.Contains(t, sink.sessionInfoSnapshot(), contracts.SessionInfoKeyGoalProgress)
			replay := goalCacheReplay(t, dispatcher)
			assert.Equal(t, "Standing goal", goalCacheReplayedGoal(t, replay).GetGoal().GetObjective())
			assert.Equal(t, []string{"progress:23"}, goalCacheProgressStages(t, replay),
				"same-goal status, detail, and identity enrichment preserve the actual cached progress")
		})
	}
}
