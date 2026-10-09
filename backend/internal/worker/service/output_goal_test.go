package service

import (
	"bytes"
	"context"
	"database/sql"
	"regexp"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupGoalTest creates one stored agent and returns these values:
//   - The Worker service.
//   - The registered root sink.
//   - The agent ID.
//   - A reader for the stored row.
func setupGoalTest(t *testing.T) (*Service, agent.ProviderServices, string, func() db.Agent) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	readRow := func() db.Agent {
		t.Helper()
		row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
		require.NoError(t, err)
		return row
	}
	return svc, sink, "agent-1", readRow
}

// goalNotificationCount counts goal transitions in the stored transcript.
// Adjacent notifications share one notification_thread row, which retains each notification as a separate entry.
// Count type tokens to detect duplicate announcements within that row.
func goalNotificationCount(t *testing.T, svc *Service, agentID string) int {
	t.Helper()
	msgs, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{
		AgentID: agentID, Seq: 0, Limit: 1000,
	})
	require.NoError(t, err)
	n := 0
	for _, m := range msgs {
		body, err := msgcodec.Decompress(m.Content, m.ContentCompression)
		require.NoError(t, err)
		n += bytes.Count(body, []byte(`"`+contracts.NotificationTypeGoalUpdated+`"`))
		n += bytes.Count(body, []byte(`"`+contracts.NotificationTypeGoalCleared+`"`))
	}
	return n
}

// goalTransitionKinds reads each stored goal_transition token in transcript order.
// The test must read the actual notification envelope that the browser receives.
func goalTransitionKinds(t *testing.T, svc *Service, agentID string) []string {
	t.Helper()
	msgs, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{
		AgentID: agentID, Seq: 0, Limit: 1000,
	})
	require.NoError(t, err)
	var kinds []string
	for _, m := range msgs {
		body, err := msgcodec.Decompress(m.Content, m.ContentCompression)
		require.NoError(t, err)
		// A notification row can hold one notification or a thread of notifications.
		// Read tokens in byte order to compare transcript order across both shapes.
		for _, match := range goalTransitionPattern.FindAllSubmatch(body, -1) {
			kinds = append(kinds, string(match[1]))
		}
	}
	return kinds
}

// goalTransitionPattern reads the transition token from the persisted payload.
// json.Marshal writes the map's key and value without a space between them.
var goalTransitionPattern = regexp.MustCompile(`"goal_transition":"([a-z]+)"`)

func activeGoal(objective string, tokensUsed int64, createdAt time.Time) agent.GoalUpdate {
	return agent.GoalUpdate{
		Objective:    objective,
		Status:       agent.GoalStatusActive,
		StatusDetail: "active",
		CreatedAt:    createdAt,
		TokensUsed:   &tokensUsed,
	}
}

func TestGoal_FirstReportStoresAndAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Make the tests pass", 0, created))

	row := readRow()
	assert.Equal(t, "Make the tests pass", row.GoalObjective)
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusActive), row.GoalStatus)
	assert.Equal(t, "active", row.GoalStatusDetail)
	require.True(t, row.GoalCreatedAt.Valid)
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID))
}

func TestGoalUnknownStatusPublishesTheNeutralUpdatedTransition(t *testing.T) {
	svc, _, writer, sink, _ := goalPublicationFixture(t)
	update := goalPublicationUpdate("Native objective", 11)
	sink.UpsertGoal(update)
	update.Status = agent.GoalStatusUnknown
	update.StatusDetail = "futureState"
	sink.UpsertGoal(update)
	stored := mustGoalRow(t, svc)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN, stored.GoalStatus)
	assert.Equal(t, "futureState", stored.GoalStatusDetail)
	assert.Equal(t, []string{"set", "updated"}, goalTransitionKinds(t, svc, stored.ID))
	snapshot, err := svc.Output.LoadGoal(t.Context(), stored.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Goal)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN, snapshot.Goal.GetStatus())
	assert.Equal(t, "futureState", snapshot.Goal.GetStatusDetail())
	stamp := stored.GoalUpdatedAt
	before := len(decodeAgentEvents(writer.testResponseWriter))
	sink.UpsertGoal(update)
	assert.Equal(t, stamp, mustGoalRow(t, svc).GoalUpdatedAt)
	assert.Empty(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]))
	update.StatusDetail = "anotherFutureState"
	sink.UpsertGoal(update)
	assert.Equal(t, "anotherFutureState", mustGoalRow(t, svc).GoalStatusDetail)
	assert.Equal(t, 2, goalNotificationCount(t, svc, stored.ID), "a detail-only update must not add another transition")
}

func TestGoalUnknownSnapshotAndStatusUpdatePreserveStoredIdentity(t *testing.T) {
	for _, operation := range []string{"snapshot", "status"} {
		t.Run(operation, func(t *testing.T) {
			svc, _, _, sink, _ := goalPublicationFixture(t)
			update := goalPublicationUpdate("Native objective", 11)
			sink.UpsertGoal(update)
			before := mustGoalRow(t, svc)
			if operation == "snapshot" {
				update.Status, update.StatusDetail, update.Snapshot = agent.GoalStatusUnknown, "futureState", true
				sink.UpsertGoal(update)
			} else {
				sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusUnknown)
			}
			after := mustGoalRow(t, svc)
			assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN, after.GoalStatus)
			assert.Equal(t, before.GoalObjective, after.GoalObjective)
			assert.Equal(t, before.GoalNativeID, after.GoalNativeID)
			assert.Equal(t, before.GoalCreatedAt, after.GoalCreatedAt)
			expected := 2
			if operation == "snapshot" {
				expected = 1
			}
			assert.Equal(t, expected, goalNotificationCount(t, svc, after.ID))
			if operation == "status" {
				assert.Equal(t, []string{"set", "updated"}, goalTransitionKinds(t, svc, after.ID))
			}
		})
	}
}

func TestGoalUnknownProcessExitAndReplayDeriveDormantWithoutAWrite(t *testing.T) {
	svc, dispatcher, writer, sink, _ := goalPublicationFixture(t)
	update := goalPublicationUpdate("Native objective", 11)
	update.Status, update.StatusDetail = agent.GoalStatusUnknown, "futureState"
	sink.UpsertGoal(update)
	before := mustGoalRow(t, svc)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN, before.GoalStatus)
	beforeNotifications := goalNotificationCount(t, svc, before.ID)
	svc.Agents.StopAndWaitAgent(before.ID)
	svc.Output.publishGoalCapabilities(before.ID)
	after := mustGoalRow(t, svc)
	assert.Equal(t, before, after, "the process-exit projection must change no stored goal field or stamp")
	assert.Equal(t, beforeNotifications, goalNotificationCount(t, svc, before.ID))
	snapshot, err := svc.Output.LoadGoal(t.Context(), before.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Goal)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT, snapshot.Goal.GetStatus())
	assert.Empty(t, snapshot.Goal.GetStatusDetail())
	var projected *leapmuxv1.AgentGoalChanged
	for _, event := range decodeAgentEvents(writer.testResponseWriter) {
		if event.GetGoalChanged() != nil {
			projected = event.GetGoalChanged()
		}
	}
	require.NotNil(t, projected)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT, projected.GetGoal().GetStatus())
	assert.Empty(t, projected.GetSupportedActions())
	replayed := &replayIdentityWriter{testResponseWriter: &testResponseWriter{channelID: "unknown-goal-replay"}}
	openReplayIdentityWatch(t, dispatcher, &leapmuxv1.WatchEventsRequest{UpdateId: 71, Agents: []*leapmuxv1.WatchAgentEntry{replayOriginEntry(before.ID, 71)}}, replayed)
	require.Eventually(t, func() bool { return countCatchUpCompletes(replayed.testResponseWriter) == 1 }, 30*time.Second, time.Millisecond)
	var replayGoal *leapmuxv1.AgentGoalChanged
	for _, event := range decodeAgentEvents(replayed.testResponseWriter) {
		if event.GetGoalChanged() != nil {
			assert.Equal(t, before.ID, readReplayOrigin(event))
			replayGoal = event.GetGoalChanged()
		}
	}
	require.NotNil(t, replayGoal)
	assert.True(t, proto.Equal(snapshot.Goal, replayGoal.GetGoal()))
	assert.Equal(t, snapshot.UpdatedAt, replayGoal.GetGoalUpdatedAt())
	assert.Equal(t, before, mustGoalRow(t, svc))
}

// Codex reports after each completed tool call, even when only its counters change.
// Those reports must produce only one goal transition in the transcript.
func TestGoal_ProgressOnlyReportsWriteNoTranscriptRow(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	for i := 0; i < 25; i++ {
		sink.UpsertGoal(activeGoal("Make the tests pass", int64(100*i), created))
	}

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"25 progress reports are ONE transition")
	assert.Equal(t, "Make the tests pass", readRow().GoalObjective)
}

func TestGoal_StatusChangeAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Ship it", 10, created))
	done := activeGoal("Ship it", 900, created)
	done.Status = agent.GoalStatusDone
	done.StatusDetail = "complete"
	sink.UpsertGoal(done)

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID))
	row := readRow()
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusDone), row.GoalStatus)
	assert.Equal(t, "complete", row.GoalStatusDetail)
}

// Codex supplies no native goal ID. A restarted objective receives a new createdAt.
// A comparison of objective and status alone would omit that restart from the transcript.
func TestGoal_SameObjectiveWithNewCreatedAtIsANewGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	first := time.Unix(1_700_000_000, 0).UTC()
	second := time.Unix(1_700_009_999, 0).UTC()

	sink.UpsertGoal(activeGoal("Fix the flake", 500, first))
	sink.UpsertGoal(activeGoal("Fix the flake", 0, second))

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID),
		"a restarted goal is a transition, not a repeat")
	assert.Equal(t, second.Unix(), readRow().GoalCreatedAt.Time.Unix())
}

func TestGoal_ReplacingAnObjectiveWithoutNativeTimeStartsANewIdentity(t *testing.T) {
	t.Parallel()
	svc, sink, _, readRow := setupGoalTest(t)
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	svc.Output.now = func() time.Time { return now }
	sink.UpsertGoal(agent.GoalUpdate{Objective: "First objective", Status: agent.GoalStatusActive})
	first := readRow().GoalCreatedAt
	now = now.Add(time.Minute)
	sink.UpsertGoal(agent.GoalUpdate{Objective: "Second objective", Status: agent.GoalStatusActive})
	second := readRow().GoalCreatedAt
	assert.False(t, first.Time.Equal(second.Time))
	assert.True(t, now.Equal(second.Time))
}

func TestGoal_NativeIdentityDistinguishesEqualObjectives(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	svc.Output.now = func() time.Time { return now }
	sink.UpsertGoal(agent.GoalUpdate{NativeID: "first", Objective: "Same objective", Status: agent.GoalStatusActive})
	first := readRow()
	now = now.Add(time.Minute)
	sink.UpsertGoal(agent.GoalUpdate{NativeID: "second", Objective: "Same objective", Status: agent.GoalStatusActive})
	second := readRow()
	assert.Equal(t, "second", second.GoalNativeID)
	assert.False(t, first.GoalCreatedAt.Time.Equal(second.GoalCreatedAt.Time))
	assert.Equal(t, []string{contracts.GoalTransitionSet, contracts.GoalTransitionReplaced}, goalTransitionKinds(t, svc, agentID))
	assert.Equal(t, "second", goalProto(GoalColumnsOfAgent(second)).NativeId)
	sink.ClearGoal(false)
	assert.Empty(t, readRow().GoalNativeID)
}

func TestGoal_LearningNativeIdentityPreservesTheExistingGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(agent.GoalUpdate{Objective: "Same objective", Status: agent.GoalStatusActive})
	created := readRow().GoalCreatedAt
	sink.UpsertGoal(agent.GoalUpdate{NativeID: "native", Objective: "Same objective", Status: agent.GoalStatusActive})
	row := readRow()
	assert.Equal(t, "native", row.GoalNativeID)
	assert.True(t, created.Time.Equal(row.GoalCreatedAt.Time))
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID))
	sink.UpsertGoal(agent.GoalUpdate{Objective: "Same objective", Status: agent.GoalStatusPaused})
	assert.Equal(t, "native", readRow().GoalNativeID)
}

// A resume snapshot can describe a goal that the provider created hours earlier.
// It updates the stored row and card without a new transcript notification.
// A new notification would incorrectly announce the old goal at resume time.
func TestGoal_SnapshotUpdatesStateWithoutAnnouncing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	snapshot := activeGoal("Resumed objective", 4200, created)
	snapshot.Snapshot = true
	sink.UpsertGoal(snapshot)

	assert.Equal(t, 0, goalNotificationCount(t, svc, agentID))
	assert.Equal(t, "Resumed objective", readRow().GoalObjective,
		"the panel still needs the goal a resume restated")
}

// Codex sends thread/goal/cleared on resume when the native thread has no goal.
// The stored row can still hold the previous process's goal while the Worker cache is empty.
// The clear must use stored state even when a new sink holds no cached goal.
func TestGoal_ClearIssuesTheWriteFromStoredState(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Old objective", 100, created))
	// A new sink represents a restarted Worker. The original goal remains in its stored row.
	coldSink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	coldSink.ClearGoal(false)

	row := readRow()
	assert.Empty(t, row.GoalObjective)
	assert.Empty(t, row.GoalStatus)
	assert.False(t, row.GoalCreatedAt.Valid)
	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID), "set, then cleared")
}

// A clear of an absent goal changes its stamp but announces no goal transition.
func TestGoal_ClearWithNoGoalAnnouncesNothing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)

	sink.ClearGoal(false)

	assert.Equal(t, 0, goalNotificationCount(t, svc, agentID))
}

// A status detail can change after every turn while the goal remains the same.
// Claude Code reports its evaluator's reason, and Reasonix reports lastReason.
// Those details update the card without a repeated transcript announcement.
func TestGoal_StatusDetailChangeStoresButNeverAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Keep the suite green", 10, created))
	for i, reason := range []string{
		"the suite still has one failure",
		"two specs now fail",
		"the lint target has not run",
	} {
		report := activeGoal("Keep the suite green", int64(100*(i+1)), created)
		report.StatusDetail = reason
		sink.UpsertGoal(report)
	}

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"three new reasons for the same goal are ONE transition")
	assert.Equal(t, "the lint target has not run", readRow().GoalStatusDetail,
		"the card still reads the latest reason")
}

// A changed objective after a restart is a new goal and must reach the transcript.
// The restatement rule must not suppress it.
func TestGoal_ANewObjectiveAfterARestartStillAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("The old objective", 10, created))

	sink.UpsertGoal(activeGoal("A brand new objective", 0, time.Unix(1_700_050_000, 0).UTC()))

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID),
		"a different objective after a restart is a new goal")
	assert.Equal(t, "A brand new objective", readRow().GoalObjective)
}

// A clear requires its stamp even when its goal is nil.
// The event carries that stamp so an older cold-load reply cannot restore the cleared goal.
func TestGoal_ClearedAgentStillCarriesAnOrderingStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)

	sink.UpsertGoal(activeGoal("Something to clear", 10, time.Unix(1_700_000_000, 0).UTC()))
	set, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, set.Goal)
	require.NotEmpty(t, set.UpdatedAt)

	sink.ClearGoal(false)

	cleared, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, cleared.Goal)
	assert.NotEmpty(t, cleared.UpdatedAt, "a clear is an answer and needs a stamp too")
	assert.GreaterOrEqual(t, cleared.UpdatedAt, set.UpdatedAt,
		"the layout is fixed-width UTC, so a string compare orders the two")
}

// LoadGoal supplies the cold-start projection. A child owns no goal and must not inherit its root's goal.
func TestGoal_LoadGoalAnswersNilForAChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, rootID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Root objective", 10, time.Unix(1_700_000_000, 0).UTC()))

	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: rootID, Valid: true},
		SpawnSpanID:   "span-1",
		Title:         "a subagent",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	rootGoal, err := svc.Output.LoadGoal(ctx, rootID)
	require.NoError(t, err)
	require.NotNil(t, rootGoal.Goal)
	assert.Equal(t, "Root objective", rootGoal.Goal.GetObjective())
	assert.NotEmpty(t, rootGoal.UpdatedAt, "the cold-load answer carries its ordering stamp")

	childGoal, err := svc.Output.LoadGoal(ctx, "child-1")
	require.NoError(t, err)
	assert.Nil(t, childGoal.Goal, "a subagent has no session goal of its own")
	assert.Empty(t, childGoal.UpdatedAt, "and no stamp to order one with")
}

// A Codex collaboration child is a native thread and can report its own goal.
// Its child sink must not replace the root's session objective with that goal.
func TestGoal_ChildSinkCannotWriteAGoal(t *testing.T) {
	t.Parallel()
	svc, sink, _, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Root objective", 10, time.Unix(1_700_000_000, 0).UTC()))

	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "span-1", ProviderChildKey: "child-key-1", Title: "A subagent"})
	require.NoError(t, err)
	childSink := svc.Output.NewSink(childID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	childSink.UpsertGoal(activeGoal("Subagent objective", 1, time.Unix(1_700_005_000, 0).UTC()))
	childSink.ClearGoal(false)
	childSink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusActive), readRow().GoalStatus)

	assert.Equal(t, "Root objective", readRow().GoalObjective,
		"a child's goal must not overwrite the session's, and a child's clear must not erase it")
}

// These producers share the pure builder for the event that the browser reads:
//   - A committed goal mutation.
//   - A capability projection.
//   - WatchEvents catch-up.
func TestGoal_ChangedEventCarriesTheGoalAndItsStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	stored, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)

	event := GoalChangedEvent(agentID, stored, svc.Output.SupportedGoalActions(agentID))
	changed := event.GetGoalChanged()
	require.NotNil(t, changed)
	assert.Equal(t, agentID, changed.GetAgentId())
	assert.Equal(t, "Ship it", changed.GetGoal().GetObjective())
	assert.Equal(t, stored.UpdatedAt, changed.GetGoalUpdatedAt())
	assert.NotEmpty(t, changed.GetGoalUpdatedAt())
}

// A cleared goal is nil, so the event itself must retain the stamp.
// The stamp orders this answer against an older reply with a present goal.
func TestGoal_ChangedEventStampsAnAbsentGoalToo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))
	sink.ClearGoal(false)

	stored, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.Nil(t, stored.Goal)

	changed := GoalChangedEvent(agentID, stored, svc.Output.SupportedGoalActions(agentID)).GetGoalChanged()
	require.NotNil(t, changed)
	assert.Nil(t, changed.GetGoal())
	assert.NotEmpty(t, changed.GetGoalUpdatedAt(),
		"a cleared goal is the answer a stale reply resurrects, so it needs a stamp")
}

// The projection repairs bytes that a previous writer could store.
// One invalid byte makes proto.Marshal reject the complete ListAgentMessagesResponse and its chat history page.
func TestGoal_ProjectionNeverEmitsBytesProtoCannotMarshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, agentID, _ := setupGoalTest(t)

	// Write invalid bytes directly because GoalUpdate.Clean removes them from every sink input.
	require.NoError(t, svc.Queries.UpdateAgentGoal(ctx, db.UpdateAgentGoalParams{
		GoalObjective:    "ship \xff it",
		GoalStatus:       leapmuxv1.AgentGoalStatus(agent.GoalStatusActive),
		GoalStatusDetail: "wait\xfe",
		GoalCreatedAt:    sqltime.SQLiteNullTime{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true},
		GoalUpdatedAt:    sqltime.SQLiteNullTime{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true},
		ID:               agentID,
	}))

	// Require the actual invalid bytes in SQLite before the projection runs.
	// Otherwise, database sanitization could make the test pass without a projection repair.
	stored, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.False(t, utf8.ValidString(stored.GoalObjective),
		"the column holds the bad byte, so the repair has real work to do")

	loaded, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded.Goal)
	assert.True(t, utf8.ValidString(loaded.Goal.GetObjective()))
	assert.True(t, utf8.ValidString(loaded.Goal.GetStatusDetail()))

	// Require serialization of the complete response that carries the projection.
	_, marshalErr := proto.Marshal(&leapmuxv1.ListAgentMessagesResponse{
		Goal:       loaded.Goal,
		GoalLoaded: true,
	})
	assert.NoError(t, marshalErr, "one bad byte would fail the entire message page")
}

// An absent counter differs from an explicit zero.
// Keep an unreported counter absent because a displayed zero would claim usage that the provider never reported.
func TestGoalProgressInfo_OmitsWhatTheProviderDidNotReport(t *testing.T) {
	t.Parallel()

	tokens := int64(1200)
	seconds := int64(45)

	// Codex reports tokens and seconds, never an iteration count.
	codexLike := goalProgressInfo(agent.GoalUpdate{TokensUsed: &tokens, TimeUsedSeconds: &seconds})
	require.NotNil(t, codexLike)
	assert.Contains(t, codexLike, "tokens_used")
	assert.Contains(t, codexLike, "time_used_seconds")
	assert.NotContains(t, codexLike, "iterations", "Codex states no iteration count")
	assert.NotContains(t, codexLike, "token_budget", "a null budget is absent, not zero")

	// Preserve explicit zero because a newly started goal can report zero usage.
	zero := int64(0)
	assert.Equal(t, map[string]interface{}{"tokens_used": int64(0)},
		goalProgressInfo(agent.GoalUpdate{TokensUsed: &zero}))

	// A provider that reports no counter at all broadcasts nothing.
	assert.Nil(t, goalProgressInfo(agent.GoalUpdate{Objective: "no counters here"}))
}

// Concurrent identical reports must announce only one transition.
// Serial calls cannot prove that comparison and storage form one serialized operation.
// Run with -race to detect concurrent access defects also.
func TestGoal_ConcurrentIdenticalReportsAnnounceOnce(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	// The shared creation time identifies one goal. Only the first report is a transition.
	const reporters = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range reporters {
		wg.Add(1)
		go func(tokens int64) {
			defer wg.Done()
			<-start
			sink.UpsertGoal(activeGoal("Ship it", tokens, created))
		}(int64(i * 100))
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"eight racing reports of one goal are ONE transition")
	assert.Equal(t, "Ship it", readRow().GoalObjective)
}

// Codex sends thread/goal/cleared on resume to restate the native thread's absent goal.
// A snapshot clear removes a stale stored goal without announcing a new user action.
// Without the snapshot flag, restart would produce a false clear notification.
func TestGoal_SnapshotClearRemovesTheGoalWithoutAnnouncingIt(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Survived a restart", 10, time.Unix(1_700_000_000, 0).UTC()))
	require.Equal(t, 1, goalNotificationCount(t, svc, agentID))

	sink.ClearGoal(true)

	row := readRow()
	assert.Empty(t, row.GoalObjective, "the write still runs")
	assert.Empty(t, row.GoalStatus)
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"a restatement of the absence announces nothing")
}

// A user-requested clear still reaches the transcript.
// The snapshot flag suppresses only a restatement of native state.
func TestGoal_RealClearAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	sink.ClearGoal(false)

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID))
}

// A goal can finish while the Worker is down.
// Its changed status must produce a real transition when the provider reports it after restart.
func TestGoal_AGoalThatFinishedDuringTheRestartStillAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()
	sink.UpsertGoal(activeGoal("Ship it", 10, created))

	done := activeGoal("Ship it", 900, created)
	done.Status = agent.GoalStatusDone
	done.StatusDetail = "complete"
	sink.UpsertGoal(done)

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID),
		"an achievement that happened during the restart is still news")
}

// A goal without a readable objective must not produce a card with live controls.
// The write path and read projection both enforce that presence rule.
func TestGoal_AStatusWithNoObjectiveIsNoGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)

	sink.UpsertGoal(agent.GoalUpdate{
		Objective:    "",
		Status:       agent.GoalStatusActive,
		StatusDetail: "active",
		CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
	})

	row := readRow()
	assert.Empty(t, row.GoalObjective)
	assert.Empty(t, row.GoalStatus, "Clean resolves the contradiction to no goal")
	assert.Equal(t, 0, goalNotificationCount(t, svc, agentID))

	snapshot, err := svc.Output.LoadGoal(context.Background(), agentID)
	require.NoError(t, err)
	assert.Nil(t, snapshot.Goal, "the read path refuses a goal with no text too")
}

// A report whose cleaner removes the objective must perform a complete clear and announce the removal.
// A partial empty write would leave the native identity stored without a removal notification.
// A later present goal would then produce a new set notification without the preceding clear.
func TestGoal_AReportThatEmptiesTheObjectiveClearsTheGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()
	sink.UpsertGoal(activeGoal("Ship it", 10, created))
	require.Equal(t, 1, goalNotificationCount(t, svc, agentID))

	// Reasonix sends an absent objective as "" while its state machine starts.
	sink.UpsertGoal(agent.GoalUpdate{Objective: "", Status: agent.GoalStatusActive, CreatedAt: created})

	row := readRow()
	assert.Empty(t, row.GoalObjective)
	assert.False(t, row.GoalCreatedAt.Valid, "the clear drops the identity, so no phantom goal survives")
	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID),
		"the removal is announced rather than happening silently")

	// The goal that returns is a new set after the transcript's clear notification.
	sink.UpsertGoal(activeGoal("Ship it", 0, created))
	assert.Equal(t, []string{"set", "set"}, goalTransitionKinds(t, svc, agentID))
}

// A first set and a resume both end in Active.
// The explicit transition token lets the transcript distinguish those operations.
func TestGoal_TransitionKindDistinguishesAResumeFromASet(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Ship it", 10, created))
	paused := activeGoal("Ship it", 20, created)
	paused.Status = agent.GoalStatusPaused
	paused.StatusDetail = "paused"
	sink.UpsertGoal(paused)
	sink.UpsertGoal(activeGoal("Ship it", 30, created))

	assert.Equal(t, []string{"set", "paused", "resumed"}, goalTransitionKinds(t, svc, agentID),
		"the third report returns to active, and that is a RESUME, not a new goal")
}

// A replacement carries its own kind, so the transcript never reports a
// restarted objective as an update of the previous one.
func TestGoal_TransitionKindMarksAReplacement(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)

	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))
	sink.UpsertGoal(activeGoal("Ship something else", 0, time.Unix(1_700_009_000, 0).UTC()))

	assert.Equal(t, []string{"set", "replaced"}, goalTransitionKinds(t, svc, agentID))
}

// startGoalAgentProcess registers a real test process through the Manager's startup path.
// The live-goal fixture needs that process because an absent process correctly projects a stored goal as Dormant.
func startGoalAgentProcess(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{
		AgentID:       agentID,
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}, agent.NewProviderServices(requireRootOutputSink(t, svc.Output, agentID)), claudetest.StartEcho)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	require.True(t, svc.Agents.AgentAlive(agentID))
}

// A delivered text command reaches the same durable sink as a provider goal
// report. The command observer must not stop at in-memory manager state.
func TestGoal_DeliveredTextCommandPersistsThroughTheOutputSink(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupGoalTest(t)
	startGoalAgentProcess(t, svc, agentID)
	require.NoError(t, svc.Agents.SendRawInput(agentID, []byte(
		"{\"type\":\"system\",\"subtype\":\"init\",\"slash_commands\":[\"goal\"]}\n"), agent.StopContext{}))
	require.Eventually(t, func() bool {
		return len(svc.Agents.SupportedGoalActions(agentID)) > 0
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, svc.Agents.SendInput(agentID, "/goal ship the release", nil))
	stored, err := svc.Output.LoadGoal(t.Context(), agentID)
	require.NoError(t, err)
	require.NotNil(t, stored.Goal)
	assert.Equal(t, "ship the release", stored.Goal.GetObjective())
}

// Dormant is derived. The stored provider status remains unchanged after its process exits.
// The card must show no active indicator or live controls for that absent process.
// Dormant retains the stored objective, while an empty goal means no objective exists.
func TestGoal_AGoalWithNoRunningProcessProjectsDormant(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	report := activeGoal("Survived a restart", 10, created)
	report.StatusDetail = "verifying"
	sink.UpsertGoal(report)

	snapshot, err := svc.Output.LoadGoal(context.Background(), agentID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Goal, "the objective survives, so the panel can still say it")
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT, snapshot.Goal.GetStatus())
	assert.Equal(t, "Survived a restart", snapshot.Goal.GetObjective())
	assert.Empty(t, snapshot.Goal.GetStatusDetail(),
		"the provider's word described the running state, and nothing runs")

	// The projection changes no stored status or detail.
	row := readRow()
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusActive), row.GoalStatus, "the row still holds the provider's last word")
	assert.Equal(t, "verifying", row.GoalStatusDetail)
}

// A live process's goal keeps the provider's reported status.
func TestGoal_AGoalWithARunningProcessKeepsItsStatus(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)
	startGoalAgentProcess(t, svc, agentID)

	report := activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC())
	report.StatusDetail = "verifying"
	sink.UpsertGoal(report)

	snapshot, err := svc.Output.LoadGoal(context.Background(), agentID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Goal)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE, snapshot.Goal.GetStatus())
	assert.Equal(t, "verifying", snapshot.Goal.GetStatusDetail())
}

// An absent objective never projects as Dormant because no goal exists.
func TestGoal_AnAgentWithNoGoalNeverProjectsDormant(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupGoalTest(t)

	snapshot, err := svc.Output.LoadGoal(context.Background(), agentID)
	require.NoError(t, err)
	assert.Nil(t, snapshot.Goal)
}

// A same-goal report after restart must not announce a new goal.
// Codex can mark the report as a snapshot. Claude Code and Reasonix need the stored-row comparison instead.
// Derived dormancy leaves the stored status unchanged, so an ordinary restatement still matches it.
func TestGoal_TheFirstReportAfterARestartAnnouncesNothing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Survived a restart", 10, created))
	require.Equal(t, 1, goalNotificationCount(t, svc, agentID))

	// Reasonix restates its goal without a snapshot flag.
	restatement := activeGoal("Survived a restart", 20, created)
	restatement.Snapshot = false
	sink.UpsertGoal(restatement)

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"restating a goal that outlived the worker is not a new transition")
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusActive), readRow().GoalStatus)
}

// A process exit projects Dormant so an open browser sees its goal controls become unavailable.
// The projection changes no stored goal status or stamp.
func TestGoal_ProcessExitPublishesTheDormantGoalWithoutWriting(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))
	before := readRow()

	svc.Output.publishGoalCapabilities(agentID)

	row := readRow()
	assert.Equal(t, before.GoalStatus, row.GoalStatus, "the exit writes no status")
	assert.Equal(t, before.GoalUpdatedAt.Time, row.GoalUpdatedAt.Time, "nor moves the stamp")
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"settling the card is a state change, not a transcript event")
}

// Every process exit publishes capabilities, including providers without a goal feature.
// An agent with no stored goal must remain unchanged.
func TestGoal_ProcessExitDoesNothingForAnAgentWithNoGoal(t *testing.T) {
	t.Parallel()
	svc, _, agentID, readRow := setupGoalTest(t)

	svc.Output.publishGoalCapabilities(agentID)

	row := readRow()
	assert.Empty(t, row.GoalStatus)
	assert.False(t, row.GoalUpdatedAt.Valid, "no stamp, so nothing was written")
}

// Relaunch stops the previous process and repeats the exit projection.
// A repeated projection must not change the stored stamp.
func TestGoal_ProcessExitIsIdempotent(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	svc.Output.publishGoalCapabilities(agentID)
	first := readRow().GoalUpdatedAt
	svc.Output.publishGoalCapabilities(agentID)

	assert.Equal(t, first.Time, readRow().GoalUpdatedAt.Time, "the stamp does not move")
}

// The full-row adapter supplies GoalColumns for cold loads.
// It must retain the row's agent ID so the projection can find its live process.
// An omitted ID would incorrectly project every stored goal as Dormant.
func TestGoal_TheFullRowAdapterProjectsALiveGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	startGoalAgentProcess(t, svc, agentID)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	snapshot := svc.Output.GoalSnapshotFrom(GoalColumnsOfAgent(readRow()))

	require.NotNil(t, snapshot.Goal)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE, snapshot.Goal.GetStatus(),
		"the adapter carries the id, so the projection can find the process")
}

// A child owns no goal, so its projection returns before the process-liveness check.
// The child runs inside its root's process. A child-ID lookup would incorrectly imply a dormant child goal.
func TestGoal_AChildProjectsNoGoalAtAll(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupGoalTest(t)

	snapshot := svc.Output.GoalSnapshotFrom(GoalColumns{
		AgentID:   "child-1",
		IsChild:   true,
		Objective: "the root's objective",
		Status:    agent.GoalStatusActive,
	})

	assert.Nil(t, snapshot.Goal)
}

func TestGoalStatusUpdatePreservesStoredIdentityAfterSinkRestart(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	original := activeGoal("Keep this objective", 12, time.Unix(1_700_000_000, 0).UTC())
	original.NativeID = "native-goal"
	sink.UpsertGoal(original)
	before := readRow()

	restarted := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
	restarted.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)

	after := readRow()
	assert.Equal(t, before.GoalObjective, after.GoalObjective)
	assert.Equal(t, before.GoalNativeID, after.GoalNativeID)
	assert.Equal(t, before.GoalCreatedAt, after.GoalCreatedAt)
	assert.Equal(t, leapmuxv1.AgentGoalStatus(agent.GoalStatusPaused), after.GoalStatus)
	assert.Empty(t, after.GoalStatusDetail)
	assert.Equal(t, []string{"set", "paused"}, goalTransitionKinds(t, svc, agentID))

	restarted.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	assert.Equal(t, after.GoalUpdatedAt, readRow().GoalUpdatedAt)
	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID))
}

func TestGoalStatusUpdateLeavesAbsentAndClearedGoalsAbsent(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	assert.Empty(t, readRow().GoalObjective)
	assert.Zero(t, goalNotificationCount(t, svc, agentID))

	sink.UpsertGoal(activeGoal("Remove this objective", 0, time.Unix(123, 0)))
	sink.ClearGoal(false)
	before := readRow()
	sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	assert.Equal(t, before, readRow())
	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID))
}

func TestGoalStatusUpdateRejectsInvalidStatuses(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Keep this objective", 0, time.Unix(123, 0)))
	before := readRow()
	for _, status := range []agent.GoalStatus{agent.GoalStatusNone, agent.GoalStatusDormant, -1, 99} {
		sink.UpdateGoalStatus(agent.GoalStatusActive, status)
		assert.Equal(t, before, readRow())
	}
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID))
}

func TestGoalStatusUpdateRacesClearWithoutRestoringTheGoal(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Keep this objective", 0, time.Unix(123, 0)))
	var writers sync.WaitGroup
	writers.Go(func() { sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused) })
	writers.Go(func() { sink.ClearGoal(false) })
	writers.Wait()
	assert.Empty(t, readRow().GoalObjective)
	assert.Empty(t, readRow().GoalStatus)
	assert.LessOrEqual(t, goalNotificationCount(t, svc, agentID), 3)
}

// TestGoal_AnAbsentManagerNeverClaimsDormant pins the construction-time check.
// Every provider plugin comes from the manager's registry. A handler without a
// manager fails during construction and cannot claim a dormant goal.
func TestGoal_AnAbsentManagerNeverClaimsDormant(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, "service: NewOutputHandler requires an agent manager", func() {
		NewOutputHandler(nil, nil, nil, nil, nil)
	})
}
