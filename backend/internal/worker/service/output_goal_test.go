package service

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupGoalTest provisions a worker service with one agent and returns the
// sink, the agent id, and a reader for the stored goal columns.
func setupGoalTest(t *testing.T) (*Service, agent.OutputSink, string, func() db.Agent) {
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

// goalNotificationCount counts the goal TRANSITIONS in the transcript.
//
// It counts occurrences of the type token, not message rows: adjacent
// notifications are folded into one notification_thread wrapper that carries
// each entry inside it, so two transitions with nothing between them are two
// entries in one row. Counting rows would report that as one and hide a
// duplicate announcement.
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
		n += bytes.Count(body, []byte(`"`+agent.NotificationTypeGoalUpdated+`"`))
		n += bytes.Count(body, []byte(`"`+agent.NotificationTypeGoalCleared+`"`))
	}
	return n
}

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
	assert.Equal(t, "active", row.GoalStatus)
	assert.Equal(t, "active", row.GoalStatusDetail)
	require.True(t, row.GoalCreatedAt.Valid)
	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID))
}

// The regression the ticket is about. Codex reports after every completed tool
// call, and only the counters move; the transcript must stay at one row.
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
	assert.Equal(t, "done", row.GoalStatus)
	assert.Equal(t, "complete", row.GoalStatusDetail)
}

// Codex puts NO goal id on the wire. A user who restarts the same objective
// gets a fresh createdAt and nothing else, so a transition test over
// (objective, status) alone would read a restart as no change and never
// announce it.
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

// A resume snapshot restates a goal that may be hours old. It must update the
// row so the panel is right, and write nothing, or every resume prints
// "Goal set: X" as though it just happened.
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

// Codex sends thread/goal/cleared on resume to mean "this thread has no goal".
// That arrives when the worker's memory is cold and the ROW may still hold a
// goal from a previous process, so the write must be issued from what the
// database holds -- never skipped because the in-memory copy looks empty.
func TestGoal_ClearIssuesTheWriteFromStoredState(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Old objective", 100, created))
	// A fresh sink stands in for a worker that restarted: it has no memory of
	// the goal above, and the row still holds it.
	coldSink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	coldSink.ClearGoal()

	row := readRow()
	assert.Empty(t, row.GoalObjective)
	assert.Empty(t, row.GoalStatus)
	assert.False(t, row.GoalCreatedAt.Valid)
	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID), "set, then cleared")
}

// Clearing a goal that was never set changes nothing and announces nothing.
func TestGoal_ClearWithNoGoalAnnouncesNothing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)

	sink.ClearGoal()

	assert.Equal(t, 0, goalNotificationCount(t, svc, agentID))
}

// A goal that survived a restart is being pursued by nobody. Leaving its status
// set would draw live Pause and Clear buttons for a goal no process holds.
func TestGoal_BootSweepBlanksStatusAndKeepsObjective(t *testing.T) {
	t.Parallel()
	svc, sink, _, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Survived a restart", 10, created))
	require.NoError(t, svc.Output.ClearGoalStatusesAtBoot(context.Background()))

	row := readRow()
	assert.Empty(t, row.GoalStatus, "no process is pursuing it")
	assert.Empty(t, row.GoalStatusDetail)
	assert.Equal(t, "Survived a restart", row.GoalObjective,
		"the panel can still say what was being attempted")
}

// The status DETAIL moves far more often than the goal does: Claude Code puts
// the goal evaluator's reason for the last "not yet" there, and Reasonix streams
// a lastReason on a cadence of its own. Announcing those would rebuild the exact
// transcript flood the applier exists to stop.
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

// The boot sweep blanks goal_status, so the provider's first report after a
// worker restart RE-ARMS it. That is a transition by the test's own terms, and
// announcing it would print "Goal set: X" at restart time for a goal set before
// the restart -- the same lie Codex's snapshot flag prevents for Codex, reached
// by a route Claude Code and Reasonix cannot mark for themselves.
func TestGoal_ReArmingAfterABootSweepAnnouncesNothing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Survived a restart", 10, created))
	require.NoError(t, svc.Output.ClearGoalStatusesAtBoot(context.Background()))
	require.Equal(t, 1, goalNotificationCount(t, svc, agentID))

	// The provider reports the same goal again, with no snapshot marking of its
	// own -- Reasonix never sets one.
	rearm := activeGoal("Survived a restart", 20, created)
	rearm.Snapshot = false
	sink.UpsertGoal(rearm)

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"re-arming a goal that outlived the worker is not a new transition")
	assert.Equal(t, "active", readRow().GoalStatus, "but the card is armed again")
}

// The re-arm rule must not swallow a real change. A DIFFERENT objective after a
// restart is a new goal and has to reach the transcript.
func TestGoal_ANewObjectiveAfterABootSweepStillAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("The old objective", 10, created))
	require.NoError(t, svc.Output.ClearGoalStatusesAtBoot(context.Background()))

	sink.UpsertGoal(activeGoal("A brand new objective", 0, time.Unix(1_700_050_000, 0).UTC()))

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID),
		"a different objective after a restart is a new goal")
	assert.Equal(t, "A brand new objective", readRow().GoalObjective)
}

// The stamp that orders two answers about the goal must survive a CLEAR, which
// is the answer a stale cold-load reply resurrects. It rides the carrier, not
// the goal, for exactly that reason.
func TestGoal_ClearedAgentStillCarriesAnOrderingStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)

	sink.UpsertGoal(activeGoal("Something to clear", 10, time.Unix(1_700_000_000, 0).UTC()))
	set, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, set.Goal)
	require.NotEmpty(t, set.UpdatedAt)

	sink.ClearGoal()

	cleared, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, cleared.Goal)
	assert.NotEmpty(t, cleared.UpdatedAt, "a clear is an answer and needs a stamp too")
	assert.GreaterOrEqual(t, cleared.UpdatedAt, set.UpdatedAt,
		"the layout is fixed-width UTC, so a string compare orders the two")
}

// LoadGoal is the cold-start read. A CHILD agent never owns a goal and must not
// inherit its root's.
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

// A child sink must not write a goal at all: Codex collab children ARE threads
// and can carry one, and storing it under the root would replace the session's
// objective with a subagent's.
func TestGoal_ChildSinkCannotWriteAGoal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, rootID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Root objective", 10, time.Unix(1_700_000_000, 0).UTC()))

	childID, err := sink.EnsureChildAgent("span-1", "child-key-1", "A subagent")
	require.NoError(t, err)
	childSink := svc.Output.NewSink(childID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	_ = ctx

	childSink.UpsertGoal(activeGoal("Subagent objective", 1, time.Unix(1_700_005_000, 0).UTC()))

	assert.Equal(t, "Root objective", readRow().GoalObjective,
		"a child's goal must not overwrite the session's")
	_ = rootID
}

// The event the browser actually reads. Three paths send it -- the applier's
// broadcast, the capability re-publish, and the WatchEvents replay -- so it is
// built in ONE place and asserted here rather than at each of them.
func TestGoal_ChangedEventCarriesTheGoalAndItsStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	stored, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)

	event := svc.Output.GoalChangedEvent(agentID, stored.Goal, stored.UpdatedAt)
	changed := event.GetGoalChanged()
	require.NotNil(t, changed)
	assert.Equal(t, agentID, changed.GetAgentId())
	assert.Equal(t, "Ship it", changed.GetGoal().GetObjective())
	assert.Equal(t, stored.UpdatedAt, changed.GetGoalUpdatedAt())
	assert.NotEmpty(t, changed.GetGoalUpdatedAt())
}

// The stamp must ride the CARRIER, not the goal, because the answer it has to
// order is the one where the goal is absent. An agent with no goal still
// carries a stamp once it has had one cleared.
func TestGoal_ChangedEventStampsAnAbsentGoalToo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))
	sink.ClearGoal()

	stored, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.Nil(t, stored.Goal)

	changed := svc.Output.GoalChangedEvent(agentID, stored.Goal, stored.UpdatedAt).GetGoalChanged()
	require.NotNil(t, changed)
	assert.Nil(t, changed.GetGoal())
	assert.NotEmpty(t, changed.GetGoalUpdatedAt(),
		"a cleared goal is the answer a stale reply resurrects, so it needs a stamp")
}

// The projection speaks for itself rather than trusting every past writer.
// proto.Marshal fails the WHOLE message for one invalid byte, and this
// projection feeds ListAgentMessagesResponse -- so a byte that reached the
// column would fail an entire page of chat history, not merely hide the goal.
func TestGoal_ProjectionNeverEmitsBytesProtoCannotMarshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, agentID, _ := setupGoalTest(t)

	// Write the bad bytes straight to the column, which is the one way they get
	// there: GoalUpdate.Clean strips them on every path into the sink.
	require.NoError(t, svc.Queries.UpdateAgentGoal(ctx, db.UpdateAgentGoalParams{
		GoalObjective:    "ship \xff it",
		GoalStatus:       "active",
		GoalStatusDetail: "wait\xfe",
		GoalCreatedAt:    sqltime.SQLiteNullTime{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true},
		GoalUpdatedAt:    sqltime.SQLiteNullTime{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true},
		ID:               agentID,
	}))

	// sqlite stores the bytes VERBATIM. Without this the test would pass for the
	// wrong reason -- a database that sanitized on its own would leave the
	// projection's repair doing nothing, and the assertions below could not tell
	// the difference.
	stored, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.False(t, utf8.ValidString(stored.GoalObjective),
		"the column holds the bad byte, so the repair has real work to do")

	loaded, err := svc.Output.LoadGoal(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded.Goal)
	assert.True(t, utf8.ValidString(loaded.Goal.GetObjective()))
	assert.True(t, utf8.ValidString(loaded.Goal.GetStatusDetail()))

	// The real assertion: the whole response this projection rides marshals.
	_, marshalErr := proto.Marshal(&leapmuxv1.ListAgentMessagesResponse{
		Goal:       loaded.Goal,
		GoalLoaded: true,
	})
	assert.NoError(t, marshalErr, "one bad byte would fail the entire message page")
}

// Absent and zero are different answers. No two providers report the same
// counters, so a field the provider never mentioned must be OMITTED -- a
// present zero renders as "0 tokens used", which states something false.
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

	// A zero the provider DID report is kept: zero tokens used is a real answer
	// for a goal that has just started.
	zero := int64(0)
	assert.Equal(t, map[string]interface{}{"tokens_used": int64(0)},
		goalProgressInfo(agent.GoalUpdate{TokensUsed: &zero}))

	// A provider that reports no counter at all broadcasts nothing.
	assert.Nil(t, goalProgressInfo(agent.GoalUpdate{Objective: "no counters here"}))
}
