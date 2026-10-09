package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type goalCapabilityObserver struct {
	agent.Agent
	mu      sync.Mutex
	observe func()
	actions []agent.GoalAction
}

func (process *goalCapabilityObserver) SupportedGoalActions() []agent.GoalAction {
	process.mu.Lock()
	observe := process.observe
	process.observe = nil
	actions := append([]agent.GoalAction(nil), process.actions...)
	process.mu.Unlock()
	if observe != nil {
		observe()
	}
	return actions
}

func (process *goalCapabilityObserver) observeNext(observe func()) {
	process.mu.Lock()
	process.observe = observe
	process.mu.Unlock()
}

func goalPublicationFixture(t *testing.T) (*Service, *channel.Dispatcher, *turnAdmissionWatchingWriter, agent.ProviderServices, *goalCapabilityObserver) {
	t.Helper()
	svc, dispatcher, base := setupTestService(t)
	const agentID = "goal-publication-owner"
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}))
	services := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	var process *goalCapabilityObserver
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{AgentID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}, services,
		func(ctx context.Context, options agent.Options, output agent.ProviderServices) (agent.Agent, error) {
			underlying, err := claudetest.StartSilent(ctx, options, output)
			if err != nil {
				return nil, err
			}
			process = &goalCapabilityObserver{Agent: underlying, actions: []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}}
			return process, nil
		})
	require.NoError(t, err)
	services.UpdateSessionID("original-goal-session")
	svc.Output.WaitActivityRefreshes()
	writer := &turnAdmissionWatchingWriter{testResponseWriter: base, onEvent: func(*leapmuxv1.AgentEvent) {}}
	registerAgentWatch(svc, base.ChannelID(), agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	return svc, dispatcher, writer, services, process
}

func goalPublicationLocksFree(sink *agentOutputSink) bool {
	goal := sink.h.goalMutex(sink.agentID)
	goalFree := goal.TryLock()
	if goalFree {
		goal.Unlock()
	}
	mutation := sink.h.transcriptMutationMutex(sink.rootAgentID)
	mutationFree := mutation.TryLock()
	if mutationFree {
		mutation.Unlock()
	}
	return goalFree && mutationFree
}

func goalPublicationUpdate(objective string, tokens int64) agent.GoalUpdate {
	return agent.GoalUpdate{NativeID: "native-" + objective, Objective: objective, Status: agent.GoalStatusActive, CreatedAt: time.Unix(1700000000, 0).UTC(), TokensUsed: &tokens}
}

func performGoalOperation(services agent.ProviderServices, operation string) {
	switch operation {
	case "set":
		services.UpsertGoal(goalPublicationUpdate("Outer goal", 23))
	case "clear":
		services.ClearGoal(false)
	case "status":
		services.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	case "capabilities":
		services.PublishGoalCapabilities()
	default:
		panic("invalid test goal operation")
	}
}

func goalPublicationStages(t *testing.T, events []*leapmuxv1.AgentEvent) []string {
	t.Helper()
	var stages []string
	for _, event := range events {
		if goal := event.GetGoalChanged(); goal != nil {
			objective := "none"
			if goal.GetGoal() != nil {
				objective = goal.GetGoal().GetObjective()
			}
			stages = append(stages, "goal:"+objective)
			continue
		}
		message := event.GetAgentMessage()
		if message == nil {
			continue
		}
		data, err := msgcodec.Decompress(message.GetContent(), message.GetContentCompression())
		require.NoError(t, err)
		var payload struct {
			Type      string                     `json:"type"`
			Objective string                     `json:"objective"`
			Messages  []json.RawMessage          `json:"messages"`
			Info      map[string]json.RawMessage `json:"info"`
		}
		require.NoError(t, json.Unmarshal(data, &payload))
		if payload.Type == contracts.NotificationTypeAgentSessionInfo {
			if progress, exists := payload.Info[contracts.SessionInfoKeyGoalProgress]; exists {
				var values map[string]int64
				require.NoError(t, json.Unmarshal(progress, &values))
				if tokens, present := values[contracts.GoalProgressFieldTokensUsed]; present {
					stages = append(stages, "progress:"+strconv.FormatInt(tokens, 10))
				}
			}
			continue
		}
		if len(payload.Messages) != 0 {
			require.NoError(t, json.Unmarshal(payload.Messages[len(payload.Messages)-1], &payload))
		}
		if payload.Type == contracts.NotificationTypeGoalUpdated || payload.Type == contracts.NotificationTypeGoalCleared {
			stages = append(stages, "notification:"+payload.Objective)
		}
	}
	return stages
}

func TestGoalPublicationCallbacksCanReenterOperations(t *testing.T) {
	for _, operation := range []string{"set", "clear", "status", "capabilities"} {
		t.Run(operation, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			services.UpsertGoal(goalPublicationUpdate("Original goal", 11))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			var entered bool
			var callbacks int
			var locksFree bool
			writer.onEvent = func(event *leapmuxv1.AgentEvent) {
				if event.GetGoalChanged() == nil || entered {
					return
				}
				entered = true
				locksFree = goalPublicationLocksFree(sink)
				if locksFree {
					callbacks++
					services.UpsertGoal(goalPublicationUpdate("Nested goal", 37))
				}
			}
			before := len(decodeAgentEvents(writer.testResponseWriter))
			performGoalOperation(services, operation)
			assert.True(t, locksFree, "the actual goal watcher must release both goal and root mutation locks")
			assert.Equal(t, 1, callbacks, "the watcher must execute the real nested goal mutation")
			row := mustGoalRow(t, svc)
			assert.Equal(t, "Nested goal", row.GoalObjective)
			stages := goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:])
			assert.Contains(t, stages, "goal:Nested goal")
			assert.Contains(t, stages, "notification:Nested goal")
			assert.Contains(t, stages, "progress:37")
		})
	}
}

func TestGoalCapabilitiesCanReenterOperations(t *testing.T) {
	for _, operation := range []string{"set", "clear", "status", "capabilities"} {
		t.Run(operation, func(t *testing.T) {
			svc, _, writer, services, process := goalPublicationFixture(t)
			services.UpsertGoal(goalPublicationUpdate("Original goal", 11))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			var callbacks int
			var locksFree bool
			process.observeNext(func() {
				locksFree = goalPublicationLocksFree(sink)
				if locksFree {
					callbacks++
					services.UpsertGoal(goalPublicationUpdate("Capability goal", 41))
				}
			})
			before := len(decodeAgentEvents(writer.testResponseWriter))
			performGoalOperation(services, operation)
			assert.True(t, locksFree, "the real provider capability callback must run outside both service locks")
			assert.Equal(t, 1, callbacks)
			changes := decodeAgentEvents(writer.testResponseWriter)[before:]
			var goals int
			for _, event := range changes {
				if goal := event.GetGoalChanged(); goal != nil {
					goals++
					assert.Equal(t, []leapmuxv1.AgentGoalAction{leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_RESUME}, goal.GetSupportedActions())
				}
			}
			assert.Equal(t, 2, goals, "both actual committed operations must publish their complete action data")
		})
	}
}

func mustGoalRow(t *testing.T, svc *Service) db.Agent {
	t.Helper()
	row, err := svc.Queries.GetAgentByID(t.Context(), "goal-publication-owner")
	require.NoError(t, err)
	return row
}

func TestGoalPublicationKeepsCommittedRecordOrder(t *testing.T) {
	for _, mode := range []string{"reentrant", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			var callbacks int
			var free bool
			var nestedDone chan struct{}
			var nestedCompleted bool
			ctx := testutil.DeadlineContext(t)
			writer.onEvent = func(event *leapmuxv1.AgentEvent) {
				if event.GetGoalChanged().GetGoal().GetObjective() != "Goal A" || callbacks != 0 {
					return
				}
				free = goalPublicationLocksFree(sink)
				if !free {
					return
				}
				callbacks++
				if mode == "concurrent" {
					nestedDone = make(chan struct{})
					go func() { services.UpsertGoal(goalPublicationUpdate("Goal B", 47)); close(nestedDone) }()
					select {
					case <-nestedDone:
						nestedCompleted = true
					case <-ctx.Done():
					}
				} else {
					services.UpsertGoal(goalPublicationUpdate("Goal B", 47))
				}
			}
			before := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(goalPublicationUpdate("Goal A", 23))
			if nestedDone != nil {
				waitGoalOperation(t, nestedDone)
				assert.True(t, nestedCompleted, "the nested mutation must finish before the original watcher returns")
			}
			assert.True(t, free)
			assert.Equal(t, 1, callbacks)
			assert.Equal(t, "Goal B", mustGoalRow(t, svc).GoalObjective)
			assert.Equal(t, []string{"goal:Goal A", "notification:Goal A", "goal:Goal B", "notification:Goal B", "progress:47"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]))
		})
	}
}

func TestGoalPublicationRetainsImmutableInputs(t *testing.T) {
	svc, _, writer, services, process := goalPublicationFixture(t)
	update := goalPublicationUpdate("Immutable goal", 23)
	budget, seconds, iterations := int64(24), int64(25), int32(26)
	update.TokenBudget, update.TimeUsedSeconds, update.Iterations = &budget, &seconds, &iterations
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if event.GetGoalChanged().GetGoal().GetObjective() == update.Objective {
			*update.TokensUsed = 99
			budget, seconds, iterations = 100, 101, 102
			process.mu.Lock()
			process.actions[0] = agent.GoalActionClear
			process.mu.Unlock()
		}
	}
	before := len(decodeAgentEvents(writer.testResponseWriter))
	services.UpsertGoal(update)
	assert.Equal(t, "Immutable goal", mustGoalRow(t, svc).GoalObjective)
	assert.Equal(t, []string{"goal:Immutable goal", "notification:Immutable goal", "progress:23"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]))
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	value, present := sink.sessionInfoSnapshot()[contracts.SessionInfoKeyGoalProgress]
	require.True(t, present, "the accepted real progress stage must populate the existing cache")
	progress, valid := value.(json.RawMessage)
	require.True(t, valid)
	var counters map[string]int64
	require.NoError(t, json.Unmarshal(progress, &counters))
	assert.Equal(t, map[string]int64{
		contracts.GoalProgressFieldTokensUsed: 23, contracts.GoalProgressFieldTokenBudget: 24,
		contracts.GoalProgressFieldTimeUsedSeconds: 25, contracts.GoalProgressFieldIterations: 26,
	}, counters, "every copied counter must retain the value before the actual watcher callback")
	goal := &leapmuxv1.AgentGoal{Objective: "Original snapshot", NativeId: "original-id"}
	explicitActions := []leapmuxv1.AgentGoalAction{leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET}
	event := GoalChangedEvent("goal-publication-owner", GoalSnapshot{Goal: goal, UpdatedAt: "original-stamp"}, explicitActions)
	goal.Objective = "Mutated snapshot"
	goal.NativeId = "mutated-id"
	explicitActions[0] = leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR
	assert.Equal(t, "Original snapshot", event.GetGoalChanged().GetGoal().GetObjective())
	assert.Equal(t, "original-id", event.GetGoalChanged().GetGoal().GetNativeId())
	assert.Equal(t, "original-stamp", event.GetGoalChanged().GetGoalUpdatedAt())
	assert.Equal(t, []leapmuxv1.AgentGoalAction{leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET}, event.GetGoalChanged().GetSupportedActions())
}

func TestGoalPublicationProgressUsesStoredCreationTimePrecision(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	update := goalPublicationUpdate("Precise goal", 23)
	update.NativeID = ""
	update.CreatedAt = time.Unix(1700000000, 123456789).In(time.FixedZone("provider", 9*60*60))
	before := len(decodeAgentEvents(writer.testResponseWriter))
	services.UpsertGoal(update)
	row := mustGoalRow(t, svc)
	require.True(t, row.GoalCreatedAt.Valid)
	assert.Equal(t, sqltime.FloorMillis(update.CreatedAt), row.GoalCreatedAt.Time)
	assert.Equal(t, []string{"goal:Precise goal", "notification:Precise goal", "progress:23"},
		goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]),
		"the stored timestamp precision must not suppress the original accepted counters")
}

func refuseGoalNotificationWrites(t *testing.T, svc *Service) {
	t.Helper()
	// Consolidation updates an existing notification row instead of inserting another row.
	// Refuse both paths so the fixture retains the actual failed publication stage.
	for _, operation := range []struct{ trigger, event string }{
		{"refuse_goal_notification", "INSERT"},
		{"refuse_goal_notification_update", "UPDATE"},
	} {
		_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON messages
WHEN NEW.agent_id = 'goal-publication-owner' AND NEW.source = %d
BEGIN SELECT RAISE(ABORT, 'goal notification refused'); END`,
			operation.trigger, operation.event, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX))
		require.NoError(t, err)
	}
}

func allowGoalNotificationWrites(t *testing.T, svc *Service) {
	t.Helper()
	for _, trigger := range []string{"refuse_goal_notification", "refuse_goal_notification_update"} {
		_, err := svc.DB.ExecContext(t.Context(), "DROP TRIGGER "+trigger)
		require.NoError(t, err)
	}
}

func TestGoalPublicationRetainsFailedNotificationForRetry(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	refuseGoalNotificationWrites(t, svc)
	before := len(decodeAgentEvents(writer.testResponseWriter))
	services.UpsertGoal(goalPublicationUpdate("Retained goal", 23))
	assert.Equal(t, "Retained goal", mustGoalRow(t, svc).GoalObjective)
	assert.Zero(t, goalNotificationCount(t, svc, "goal-publication-owner"))
	assert.Equal(t, []string{"goal:Retained goal"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]), "progress must wait behind the failed captured notification")
	allowGoalNotificationWrites(t, svc)
	before = len(decodeAgentEvents(writer.testResponseWriter))
	services.PublishGoalCapabilities()
	assert.Equal(t, 1, goalNotificationCount(t, svc, "goal-publication-owner"))
	stages := goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:])
	assert.Equal(t, []string{"notification:Retained goal", "progress:23", "goal:Retained goal"}, stages)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "original-goal-session", rows[0].AgentSessionID)
	beforeRow := rows[0]
	services.PublishGoalCapabilities()
	afterRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	require.Len(t, afterRows, 1)
	assert.Equal(t, beforeRow, afterRows[0])
}

func TestGoalPublicationRetriedNotificationDoesNotAttachProgressToReplayedReplacement(t *testing.T) {
	svc, dispatcher, writer, services, _ := goalPublicationFixture(t)
	refuseGoalNotificationWrites(t, svc)
	services.UpsertGoal(goalPublicationUpdate("Goal A", 23))
	services.UpsertGoal(goalPublicationUpdate("Goal B", 47))
	assert.Equal(t, "Goal B", mustGoalRow(t, svc).GoalObjective)
	assert.Zero(t, goalNotificationCount(t, svc, "goal-publication-owner"))
	replayWriter := &replayIdentityWriter{testResponseWriter: &testResponseWriter{channelID: "goal-replacement-replay"}}
	openReplayIdentityWatch(t, dispatcher, &leapmuxv1.WatchEventsRequest{UpdateId: 59, Agents: []*leapmuxv1.WatchAgentEntry{replayOriginEntry("goal-publication-owner", 59)}}, replayWriter)
	require.Eventually(t, func() bool { return countCatchUpCompletes(replayWriter.testResponseWriter) == 1 }, 30*time.Second, time.Millisecond)
	var replayedGoal *leapmuxv1.AgentGoalChanged
	for _, event := range decodeAgentEvents(replayWriter.testResponseWriter) {
		if event.GetGoalChanged() != nil {
			replayedGoal = event.GetGoalChanged()
		}
	}
	require.NotNil(t, replayedGoal)
	assert.Equal(t, "Goal B", replayedGoal.GetGoal().GetObjective())
	beforeLive := len(decodeAgentEvents(writer.testResponseWriter))
	beforeReplay := len(decodeAgentEvents(replayWriter.testResponseWriter))
	allowGoalNotificationWrites(t, svc)
	services.PublishGoalCapabilities()
	assert.Equal(t, 2, goalNotificationCount(t, svc, "goal-publication-owner"))
	liveStages := goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeLive:])
	assert.NotContains(t, liveStages, "progress:23", "the retained Goal A counters must not attach to stored Goal B")
	assert.Equal(t, []string{"notification:Goal A", "goal:Goal B", "notification:Goal B", "progress:47", "goal:Goal B"}, liveStages)
	replayStages := goalPublicationStages(t, decodeAgentEvents(replayWriter.testResponseWriter)[beforeReplay:])
	assert.NotContains(t, replayStages, "progress:23", "a real replay restored Goal B before the original notification retry")
	assert.Contains(t, replayStages, "progress:47")
	assert.Equal(t, "Goal B", mustGoalRow(t, svc).GoalObjective)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "original-goal-session", rows[0].AgentSessionID)
	assert.Equal(t, []string{"set", "replaced"}, goalTransitionKinds(t, svc, "goal-publication-owner"))
	stored := rows[0]
	services.PublishGoalCapabilities()
	rows, err = svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, stored, rows[0])
}

type goalReadFailureStore struct {
	db.DBTX
	failed atomic.Bool
}

func (store *goalReadFailureStore) QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row {
	if strings.HasPrefix(query, "-- name: GetAgentGoal :one") && store.failed.CompareAndSwap(false, true) {
		return store.DBTX.QueryRowContext(ctx, "SELECT missing_goal_column")
	}
	return store.DBTX.QueryRowContext(ctx, query, arguments...)
}

func TestGoalPublicationProgressReadFailureKeepsDedupUnchanged(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	var installed bool
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if event.GetGoalChanged() != nil && !installed {
			installed = true
			svc.Output.queries = db.New(&goalReadFailureStore{DBTX: svc.DB})
		}
	}
	var logs int
	var free bool
	previous := slog.Default()
	slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
		if record.Message != "The Worker failed to read the goal before progress publication." || record.Level != slog.LevelWarn {
			return
		}
		logs++
		free = goalPublicationLocksFree(sink)
		svc.Output.queries = db.New(svc.DB)
	}}))
	defer slog.SetDefault(previous)
	before := len(decodeAgentEvents(writer.testResponseWriter))
	services.UpsertGoal(goalPublicationUpdate("Read guarded goal", 23))
	assert.Equal(t, 1, logs)
	assert.True(t, free)
	assert.NotContains(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]), "progress:23")
	sink.sessionInfoMu.Lock()
	_, cached := sink.lastSessionInfo[contracts.SessionInfoKeyGoalProgress]
	sink.sessionInfoMu.Unlock()
	assert.False(t, cached, "a refused progress read must leave the existing dedup cache unchanged")
	svc.Output.queries = db.New(svc.DB)
	services.PublishGoalCapabilities()
	assert.Contains(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]), "progress:23")
}

func TestGoalPublicationRejectsExpiredLiveEffects(t *testing.T) {
	for _, change := range []string{"root", "session", "turn"} {
		t.Run(change, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			var entered bool
			writer.onEvent = func(event *leapmuxv1.AgentEvent) {
				if event.GetGoalChanged() == nil || entered {
					return
				}
				entered = true
				switch change {
				case "root":
					replacement := svc.Output.NewSink(sink.agentID, sink.agentProvider)
					replacement.UpdateSessionID("replacement-goal-session")
				case "session":
					services.UpdateSessionID("replacement-goal-session")
				case "turn":
					services.SetTurnState(agent.TurnState{Active: true}, 1)
					services.SetTurnState(agent.TurnState{}, 2)
					services.SetTurnState(agent.TurnState{Active: true}, 3)
				}
			}
			before := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(goalPublicationUpdate("Original captured goal", 23))
			assert.True(t, entered)
			stages := goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:])
			if change == "turn" {
				assert.Contains(t, stages, "progress:23", "a later turn in the same session does not invalidate its goal")
			} else {
				assert.NotContains(t, stages, "progress:23")
			}
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, "original-goal-session", rows[0].AgentSessionID)
			assert.True(t, rows[0].TranscriptOnly, "the retained notification keeps its old transcript owner after each replacement")
		})
	}
}

func TestGoalPublicationQueueReleasesExactEmptyIdentity(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	var originalQueue *goalPublicationQueue
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if event.GetGoalChanged() != nil && originalQueue == nil {
			svc.Output.goalPublicationMu.Lock()
			originalQueue = svc.Output.goalPublications["goal-publication-owner"]
			svc.Output.goalPublicationMu.Unlock()
		}
	}
	for i := range 12 {
		services.UpsertGoal(goalPublicationUpdate("Goal "+strconv.Itoa(i), int64(i)))
	}
	assert.Equal(t, 12, goalNotificationCount(t, svc, "goal-publication-owner"))
	assert.Equal(t, "Goal 11", mustGoalRow(t, svc).GoalObjective)
	assert.Len(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)), 36)
	svc.Output.goalPublicationMu.Lock()
	assert.Empty(t, svc.Output.goalPublications, "the actual publication registry must remove the exact empty queue")
	svc.Output.goalPublicationMu.Unlock()
	require.NotNil(t, originalQueue)
	refuseGoalNotificationWrites(t, svc)
	services.UpsertGoal(goalPublicationUpdate("Replacement queue", 23))
	svc.Output.goalPublicationMu.Lock()
	replacementQueue := svc.Output.goalPublications["goal-publication-owner"]
	svc.Output.goalPublicationMu.Unlock()
	require.NotNil(t, replacementQueue)
	require.NotSame(t, originalQueue, replacementQueue)
	svc.Output.releaseGoalPublicationClaim("goal-publication-owner", originalQueue)
	svc.Output.goalPublicationMu.Lock()
	assert.Same(t, replacementQueue, svc.Output.goalPublications["goal-publication-owner"])
	assert.Len(t, replacementQueue.records, 1, "old queue cleanup must preserve the new captured notification")
	svc.Output.goalPublicationMu.Unlock()
	allowGoalNotificationWrites(t, svc)
	beforeRetry := len(decodeAgentEvents(writer.testResponseWriter))
	services.PublishGoalCapabilities()
	assert.Equal(t, 13, goalNotificationCount(t, svc, "goal-publication-owner"))
	assert.Equal(t, []string{"notification:Replacement queue", "progress:23", "goal:Replacement queue"},
		goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeRetry:]))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	beforeRetry = len(decodeAgentEvents(writer.testResponseWriter))
	services.PublishGoalCapabilities()
	assert.Equal(t, []string{"goal:Replacement queue"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeRetry:]))
	afterRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "goal-publication-owner"})
	require.NoError(t, err)
	assert.Equal(t, rows, afterRows, "a repeated real drain must repeat no accepted notification or progress effect")
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	assert.True(t, goalPublicationLocksFree(sink))
}

func TestGoalPublicationReleasesClaimAfterPanic(t *testing.T) {
	svc, _, writer, services, _ := goalPublicationFixture(t)
	var panicked bool
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if event.GetGoalChanged() != nil && !panicked {
			panicked = true
			panic("controlled goal watcher panic")
		}
	}
	assert.PanicsWithValue(t, "controlled goal watcher panic", func() { services.UpsertGoal(goalPublicationUpdate("Panic goal", 23)) })
	assert.Equal(t, "Panic goal", mustGoalRow(t, svc).GoalObjective)
	writer.onEvent = func(*leapmuxv1.AgentEvent) {}
	services.PublishGoalCapabilities()
	assert.Equal(t, 1, goalNotificationCount(t, svc, "goal-publication-owner"))
	assert.Equal(t, []string{"goal:Panic goal", "notification:Panic goal", "progress:23", "goal:Panic goal"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)))
	assert.True(t, goalPublicationLocksFree(requireRootOutputSink(t, svc.Output, "goal-publication-owner")))
}

func TestGoalFailureLogsReleaseLocksBeforeCallbacks(t *testing.T) {
	for _, test := range []struct {
		name, message, operation string
		write                    bool
	}{
		{name: "update read", message: "failed to fetch agent for goal update", operation: "set"},
		{name: "update write", message: "failed to update agent goal", operation: "set", write: true},
		{name: "clear read", message: "failed to fetch agent for goal clear", operation: "clear"},
		{name: "clear write", message: "failed to clear agent goal", operation: "clear", write: true},
		{name: "capability read", message: "failed to read agent goal for capability broadcast", operation: "capabilities"},
		{name: "status read", message: "failed to read agent goal for status update", operation: "status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			services.UpsertGoal(goalPublicationUpdate("Original goal", 11))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			before := mustGoalRow(t, svc)
			beforeProgress := sink.sessionInfoSnapshot()[contracts.SessionInfoKeyGoalProgress]
			if test.write {
				_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_goal_mutation BEFORE UPDATE OF goal_objective ON agents
WHEN NEW.id = 'goal-publication-owner' BEGIN SELECT RAISE(ABORT, 'goal mutation refused'); END`)
				require.NoError(t, err)
			} else {
				svc.Output.queries = db.New(&goalReadFailureStore{DBTX: svc.DB})
			}
			var logs, callbacks int
			var free bool
			var repairError error
			previous := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				if record.Message != test.message || record.Level != slog.LevelWarn {
					return
				}
				belongs := false
				var failure error
				record.Attrs(func(attribute slog.Attr) bool {
					if attribute.Key == "agent_id" {
						belongs = attribute.Value.String() == sink.agentID
					}
					if attribute.Key == "error" {
						failure, _ = attribute.Value.Any().(error)
					}
					return true
				})
				if !belongs {
					return
				}
				logs++
				assert.Error(t, failure)
				assert.Equal(t, before, mustGoalRow(t, svc), "the refused original mutation must keep every stored field before its logger")
				assert.Equal(t, beforeProgress, sink.sessionInfoSnapshot()[contracts.SessionInfoKeyGoalProgress],
					"a refused goal read or write must preserve the existing progress cache before its logger")
				free = goalPublicationLocksFree(sink)
				if !free {
					return
				}
				svc.Output.queries = db.New(svc.DB)
				if test.write {
					_, repairError = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_goal_mutation")
					if repairError != nil {
						return
					}
				}
				callbacks++
				services.ClearGoal(false)
			}}))
			defer slog.SetDefault(previous)
			beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
			performGoalOperation(services, test.operation)
			assert.Equal(t, 1, logs)
			assert.True(t, free)
			assert.NoError(t, repairError)
			assert.Equal(t, 1, callbacks, "the actual logger must execute the same sink's real clear operation")
			assert.Empty(t, mustGoalRow(t, svc).GoalObjective)
			assert.Contains(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]), "goal:none")
		})
	}
}

// The deadline applies only to controlled test coordination.
func waitGoalOperation(t *testing.T, done <-chan struct{}) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the goal operation did not complete")
	}
}
