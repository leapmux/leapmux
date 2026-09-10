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
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupGoalTest provisions a worker service with one agent and returns the
// sink, the agent id, and a reader for the stored goal columns.
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
		n += bytes.Count(body, []byte(`"`+contracts.NotificationTypeGoalUpdated+`"`))
		n += bytes.Count(body, []byte(`"`+contracts.NotificationTypeGoalCleared+`"`))
	}
	return n
}

// goalTransitionKinds returns the `goal_transition` token of every goal_updated
// entry, in order. It reads the PERSISTED payload rather than calling
// goalTransitionKind directly, because the value only helps if it survives the
// notification envelope the browser reads.
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
		// Adjacent notifications fold into one thread wrapper, so a row can
		// carry several entries. Scan the raw body IN ORDER rather than
		// decoding a shape that differs between a standalone row and a thread;
		// the order is what the assertions are about.
		for _, match := range goalTransitionPattern.FindAllSubmatch(body, -1) {
			kinds = append(kinds, string(match[1]))
		}
	}
	return kinds
}

// goalTransitionPattern reads the transition token out of a persisted payload.
// The value is written with json.Marshal of a map, so the key and the value sit
// together with no space.
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
	coldSink.ClearGoal(false)

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

	sink.ClearGoal(false)

	assert.Equal(t, 0, goalNotificationCount(t, svc, agentID))
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

// A DIFFERENT objective after a restart is a new goal and has to reach the
// transcript. The restatement rule must not swallow it.
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

	sink.ClearGoal(false)

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
	svc, sink, _, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Root objective", 10, time.Unix(1_700_000_000, 0).UTC()))

	childID, err := sink.EnsureChildAgent("span-1", "child-key-1", "A subagent")
	require.NoError(t, err)
	childSink := svc.Output.NewSink(childID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	childSink.UpsertGoal(activeGoal("Subagent objective", 1, time.Unix(1_700_005_000, 0).UTC()))
	childSink.ClearGoal(false)

	assert.Equal(t, "Root objective", readRow().GoalObjective,
		"a child's goal must not overwrite the session's, and a child's clear must not erase it")
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
	sink.ClearGoal(false)

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

// The mutex the applier holds is the whole reason a burst of identical reports
// announces once. Every other test in this file calls the sink serially, and a
// serial suite stays green with the lock removed: the read-modify-write only
// interleaves when two reports genuinely race.
//
// Run under -race, where a missing lock is also a reported data race.
func TestGoal_ConcurrentIdenticalReportsAnnounceOnce(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	// One shared createdAt, so every report describes the SAME goal and only
	// the first of them is a transition.
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

// A clear that RESTATES the absence must not announce one. Codex pushes
// thread/goal/cleared on every resume for a thread that has no goal, and that
// lands when the worker's copy is cold and the row still holds a goal from the
// previous process -- so without the flag the user opens the chat after a
// restart and reads "Goal cleared: X" for a clear nobody performed.
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

// The mirror of the test above: a clear the user actually performed still
// reaches the transcript, so the flag suppresses a restatement and nothing else.
func TestGoal_RealClearAnnounces(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	sink.ClearGoal(false)

	assert.Equal(t, 2, goalNotificationCount(t, svc, agentID))
}

// The case the old blank-status sentinel swallowed. A goal that FINISHED while
// the worker was down comes back in a different status, and that is a real
// transition the user must see -- the re-arm rule must not absorb it.
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

// A goal with a status and NO objective is a goal the user cannot read. The
// card would draw an empty line with an armed dot and live controls, so the
// write path refuses it and the read path refuses it again.
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

// A report that cleans away the objective is a REMOVAL, and it must reach the
// transcript as one. Reaching the applier's write path with it half-wiped the
// row -- objective, status and detail blanked, the identity left behind -- so
// the card emptied mid-session with nothing said, and the NEXT report carrying
// the objective again read the blank row as a first "Goal set".
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

	// And the goal that comes back is a fresh SET, not a repeat of one the
	// transcript never retracted.
	sink.UpsertGoal(activeGoal("Ship it", 0, created))
	assert.Equal(t, []string{"set", "set"}, goalTransitionKinds(t, svc, agentID))
}

// The transition KIND is what lets the transcript say "Goal resumed" instead of
// guessing from the resulting status. A resume ends `active`, and so does a
// first set, so the status alone cannot tell the two apart.
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

// startGoalAgentProcess registers a live process for the agent through the
// manager's own start path, so AgentAlive answers true exactly as it does in
// production. Without one the projection reads every stored goal as dormant,
// which is the correct answer and the wrong fixture for a live-goal test.
func startGoalAgentProcess(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	_, err := svc.Agents.MockStartAgent(t.Context(), agent.Options{
		AgentID:       agentID,
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
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
		"{\"type\":\"system\",\"subtype\":\"init\",\"slash_commands\":[\"goal\"]}\n")))
	require.Eventually(t, func() bool {
		return len(svc.Agents.SupportedGoalActions(agentID)) > 0
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, svc.Agents.SendInput(agentID, "/goal ship the release", nil))
	stored, err := svc.Output.LoadGoal(t.Context(), agentID)
	require.NoError(t, err)
	require.NotNil(t, stored.Goal)
	assert.Equal(t, "ship the release", stored.Goal.GetObjective())
}

// DORMANT is derived, never stored. No process pursues a goal that outlived its
// process, and reporting the last live status would draw a card with an armed
// dot and working Pause and Clear buttons for a process that is gone.
//
// The projection answers `dormant` rather than the empty token, because the
// empty token means "no goal at all": a client reading that back cannot tell a
// waiting goal from a status this build does not understand, so a waiting goal
// would render as a fault.
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

	// And nothing was written to reach that answer, which is the whole point:
	// there is no stored copy left to go stale.
	row := readRow()
	assert.Equal(t, "active", row.GoalStatus, "the row still holds the provider's last word")
	assert.Equal(t, "verifying", row.GoalStatusDetail)
}

// The other half of the same rule: a goal a live process pursues keeps the
// status that process reported.
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

// An agent with NO goal never reads as dormant. Dormant means "an objective is
// stored and nothing pursues it", so with no objective there is nothing to say.
func TestGoal_AnAgentWithNoGoalNeverProjectsDormant(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupGoalTest(t)

	snapshot, err := svc.Output.LoadGoal(context.Background(), agentID)
	require.NoError(t, err)
	assert.Nil(t, snapshot.Goal)
}

// The first report after a worker restart restates a goal the row already
// holds, and it must announce nothing -- printing "Goal set: X" at restart time
// for a goal set an hour ago is the lie Codex's snapshot flag prevents for
// Codex, reached by a route Claude Code and Reasonix cannot mark for
// themselves.
//
// This needs no rule of its own now. Dormancy is derived, so the restart leaves
// the stored status alone and the restatement matches it.
func TestGoal_TheFirstReportAfterARestartAnnouncesNothing(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	created := time.Unix(1_700_000_000, 0).UTC()

	sink.UpsertGoal(activeGoal("Survived a restart", 10, created))
	require.Equal(t, 1, goalNotificationCount(t, svc, agentID))

	// The provider reports the same goal again, with no snapshot marking of its
	// own -- Reasonix never sets one.
	rearm := activeGoal("Survived a restart", 20, created)
	rearm.Snapshot = false
	sink.UpsertGoal(rearm)

	assert.Equal(t, 1, goalNotificationCount(t, svc, agentID),
		"restating a goal that outlived the worker is not a new transition")
	assert.Equal(t, "active", readRow().GoalStatus)
}

// A process that exits mid-session leaves a goal nothing pursues. The exit
// publishes so a browser holding the tab open sees the controls settle, rather
// than learning on its next cold load -- and it writes NOTHING, because the
// status it would have written is the one the projection already derives.
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

// It runs for EVERY process exit, including providers with no goal feature at
// all, so an agent that never had a goal must come through it untouched.
func TestGoal_ProcessExitDoesNothingForAnAgentWithNoGoal(t *testing.T) {
	t.Parallel()
	svc, _, agentID, readRow := setupGoalTest(t)

	svc.Output.publishGoalCapabilities(agentID)

	row := readRow()
	assert.Empty(t, row.GoalStatus)
	assert.False(t, row.GoalUpdatedAt.Valid, "no stamp, so nothing was written")
}

// A relaunch stops the old process and runs the exit path again. Nothing is
// stored, so a second pass cannot differ from the first.
func TestGoal_ProcessExitIsIdempotent(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, readRow := setupGoalTest(t)
	sink.UpsertGoal(activeGoal("Ship it", 10, time.Unix(1_700_000_000, 0).UTC()))

	svc.Output.publishGoalCapabilities(agentID)
	first := readRow().GoalUpdatedAt
	svc.Output.publishGoalCapabilities(agentID)

	assert.Equal(t, first.Time, readRow().GoalUpdatedAt.Time, "the stamp does not move")
}

// The OTHER adapter. Two of them build GoalColumns -- one per sqlc row type --
// and the cold-load path uses this one. It has to carry the agent id like its
// sibling, or the projection asks the running-agent map about the empty string,
// gets "not alive", and reports every cold-loaded goal as dormant.
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

// A worker with no agent manager cannot observe an exit either, so it must not
// invent the state that observing one would have recorded.
func TestGoal_AnAbsentManagerNeverClaimsDormant(t *testing.T) {
	t.Parallel()
	h := NewOutputHandler(nil, nil, nil, nil, nil)

	snapshot := h.GoalSnapshotFrom(GoalColumns{
		AgentID:    "agent-1",
		Objective:  "Ship it",
		StatusWire: "active",
	})

	require.NotNil(t, snapshot.Goal)
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE, snapshot.Goal.GetStatus())
}

// A CHILD owns no goal, and that answer comes before the liveness question: a
// subagent runs inside its parent's process, so asking the map about the child
// id would report dormant for a goal it does not own in the first place.
func TestGoal_AChildProjectsNoGoalAtAll(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupGoalTest(t)

	snapshot := svc.Output.GoalSnapshotFrom(GoalColumns{
		AgentID:    "child-1",
		IsChild:    true,
		Objective:  "the root's objective",
		StatusWire: "active",
	})

	assert.Nil(t, snapshot.Goal)
}
