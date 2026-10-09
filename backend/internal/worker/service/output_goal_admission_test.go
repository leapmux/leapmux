package service

import (
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedGoalColumns(t *testing.T, svc *Service) GoalColumns {
	t.Helper()
	row, err := svc.Queries.GetAgentGoal(t.Context(), "goal-publication-owner")
	require.NoError(t, err)
	return goalColumnsOfRow(row)
}

func TestGoalAdmissionRejectsObsoleteRoot(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		for _, operation := range []string{"set", "clear", "status", "capabilities"} {
			t.Run(operation+func() string {
				if adopted {
					return " after adoption"
				}
				return " before adoption"
			}(), func(t *testing.T) {
				svc, _, writer, services, _ := goalPublicationFixture(t)
				services.UpsertGoal(goalPublicationUpdate("Original goal", 11))
				if adopted {
					services.SetTurnState(agent.TurnState{Active: true}, 1)
				}
				sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
				before := storedGoalColumns(t, svc)
				beforeRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
				require.NoError(t, err)
				svc.Output.NewSink(sink.agentID, sink.agentProvider)
				beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
				performGoalOperation(services, operation)
				assert.Equal(t, before, storedGoalColumns(t, svc), "an obsolete root must change no stored goal field")
				afterRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
				require.NoError(t, err)
				assert.Equal(t, beforeRows, afterRows)
				assert.Empty(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
			})
		}
	}
}

func TestGoalAdmissionRejectsReplacementDuringCapabilities(t *testing.T) {
	for _, replacement := range []string{"root", "session", "A-B-A"} {
		for _, operation := range []string{"set", "clear", "status", "capabilities"} {
			t.Run(replacement+" "+operation, func(t *testing.T) {
				svc, _, writer, services, process := goalPublicationFixture(t)
				services.UpsertGoal(goalPublicationUpdate("Original goal", 11))
				sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
				before := storedGoalColumns(t, svc)
				beforeRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
				require.NoError(t, err)
				beforeFact := sink.currentMessageSessionFact()
				entered, release := make(chan struct{}), make(chan struct{})
				resume := sync.OnceFunc(func() { close(release) })
				defer resume()
				process.observeNext(func() { close(entered); <-release })
				finished := make(chan struct{})
				beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
				go func() { defer close(finished); performGoalOperation(services, operation) }()
				ctx := testutil.DeadlineContext(t)
				select {
				case <-entered:
				case <-ctx.Done():
					resume()
					waitGoalOperation(t, finished)
					t.Fatal("the actual provider capability read did not reach its controlled hold")
				}
				switch replacement {
				case "root":
					svc.Output.NewSink(sink.agentID, sink.agentProvider)
				case "session", "A-B-A":
					services.UpdateSessionID("replacement-goal-session")
					if replacement == "A-B-A" {
						services.UpdateSessionID("original-goal-session")
					}
					assert.NotSame(t, beforeFact, sink.currentMessageSessionFact())
				}
				resume()
				waitGoalOperation(t, finished)
				assert.Equal(t, before, storedGoalColumns(t, svc), "replacement during provider work must precede any old storage admission")
				afterRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
				require.NoError(t, err)
				assert.Equal(t, beforeRows, afterRows)
				assert.Empty(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
			})
		}
	}
}

func TestGoalAdmissionKeepsSameSessionAcrossTurns(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	services.UpsertGoal(goalPublicationUpdate("Standing goal", 11))
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	originalFact := sink.currentMessageSessionFact()
	before := storedGoalColumns(t, svc)
	services.UpdateSessionID("original-goal-session")
	assert.Same(t, originalFact, sink.currentMessageSessionFact())
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	services.SetTurnState(agent.TurnState{}, 2)
	services.SetTurnState(agent.TurnState{Active: true}, 3)
	beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
	services.UpsertGoal(goalPublicationUpdate("Standing goal", 23))
	assert.Equal(t, before, storedGoalColumns(t, svc), "a same-goal report keeps its durable identity and stamp")
	assert.Equal(t, []string{"progress:23"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
	assert.Equal(t, 1, goalNotificationCount(t, svc, sink.agentID))
}

func TestGoalAdmissionRejectsExpiredQueuedEvents(t *testing.T) {
	for _, queued := range []string{"goal", "capabilities", "progress"} {
		t.Run(queued, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			var entered, free bool
			writer.onEvent = func(event *leapmuxv1.AgentEvent) {
				if event.GetGoalChanged() == nil || entered {
					return
				}
				entered = true
				free = goalPublicationLocksFree(sink)
				if !free {
					return
				}
				switch queued {
				case "goal":
					services.UpsertGoal(goalPublicationUpdate("Queued goal", 47))
				case "capabilities":
					services.PublishGoalCapabilities()
				case "progress":
					services.UpsertGoal(goalPublicationUpdate("Original goal", 47))
				}
				replacement := svc.Output.NewSink(sink.agentID, sink.agentProvider)
				replacement.UpdateSessionID("replacement-goal-session")
			}
			beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(goalPublicationUpdate("Original goal", 23))
			assert.True(t, entered)
			assert.True(t, free, "the callback must execute both actual queueing and replacement")
			stages := goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:])
			assert.NotContains(t, stages, "progress:23")
			assert.NotContains(t, stages, "progress:47")
			assert.NotContains(t, stages, "goal:Queued goal")
			var goals int
			for _, stage := range stages {
				if len(stage) >= 5 && stage[:5] == "goal:" {
					goals++
				}
			}
			assert.Equal(t, 1, goals, "only the first already admitted goal may reach the stream")
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
			require.NoError(t, err)
			for _, row := range rows {
				assert.Equal(t, "original-goal-session", row.AgentSessionID)
				assert.True(t, row.TranscriptOnly)
			}
			expected := 1
			if queued == "goal" {
				expected = 2
			}
			assert.Equal(t, expected, goalNotificationCount(t, svc, sink.agentID))
		})
	}
}

func TestGoalAdmissionPreservesExpiredNotificationCapture(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	refuseGoalNotificationWrites(t, svc)
	services.UpsertGoal(goalPublicationUpdate("Original retained goal", 23))
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	replacement := svc.Output.NewSink(sink.agentID, sink.agentProvider)
	replacement.UpdateSessionID("replacement-goal-session")
	before := len(decodeAgentEvents(writer.testResponseWriter))
	allowGoalNotificationWrites(t, svc)
	replacement.PublishGoalCapabilities()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the exact failed original notification must survive replacement")
	assert.Equal(t, "original-goal-session", rows[0].AgentSessionID)
	assert.True(t, rows[0].TranscriptOnly)
	assert.Equal(t, []string{"set"}, goalTransitionKinds(t, svc, sink.agentID))
	assert.NotContains(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]), "progress:23")
	assert.Equal(t, "Original retained goal", mustGoalRow(t, svc).GoalObjective)
}

func TestGoalAdmissionRejectsChildrenBeforeCallbacks(t *testing.T) {
	svc, _, writer, services, process := goalPublicationFixture(t)
	services.UpsertGoal(goalPublicationUpdate("Root goal", 11))
	childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "child-goal-spawn", ProviderChildKey: "child-goal-key", Title: "Child goal"})
	require.NoError(t, err)
	child := services.ChildSink(childID)
	before := storedGoalColumns(t, svc)
	beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
	var callbacks int
	process.observeNext(func() { callbacks++ })
	for _, operation := range []string{"set", "clear", "status", "capabilities"} {
		performGoalOperation(child, operation)
	}
	assert.Zero(t, callbacks, "the root-only guard must precede any provider capability read")
	assert.Equal(t, before, storedGoalColumns(t, svc))
	assert.Empty(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
	childGoal, err := svc.Queries.GetAgentGoal(t.Context(), childID)
	require.NoError(t, err)
	assert.Empty(t, childGoal.GoalObjective)
	assert.Zero(t, goalNotificationCount(t, svc, childID))
	services.PublishGoalCapabilities()
	assert.Equal(t, 1, callbacks, "the real root capability control must reach the same provider callback")
}
