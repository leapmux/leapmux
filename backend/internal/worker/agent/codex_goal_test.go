package agent

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalFrame builds a thread/goal/updated line. turnID is emitted verbatim, so a
// caller passes `null` to make the frame a resume snapshot.
func goalFrame(threadID, turnID, objective, status string, tokensUsed, timeUsed, createdAt int64) string {
	return `{"jsonrpc":"2.0","method":"thread/goal/updated","params":{` +
		`"threadId":"` + threadID + `","turnId":` + turnID + `,"goal":{` +
		`"threadId":"` + threadID + `","objective":"` + objective + `","status":"` + status + `",` +
		`"tokenBudget":null,"tokensUsed":` + strconv.FormatInt(tokensUsed, 10) + `,"timeUsedSeconds":` + strconv.FormatInt(timeUsed, 10) + `,` +
		`"createdAt":` + strconv.FormatInt(createdAt, 10) + `,"updatedAt":` + strconv.FormatInt(createdAt, 10) + `}}}`
}

func TestCodexGoal_UpdatedReportsNeutralGoal(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		goalFrame("main-thread", `"turn-1"`, "Make the tests pass", "active", 1200, 45, 1_700_000_000))))

	got, ok := sink.LastGoal()
	require.True(t, ok, "an active goal must reach the sink")
	assert.Equal(t, "Make the tests pass", got.Objective)
	assert.Equal(t, GoalStatusActive, got.Status)
	// The provider's own word survives beside the neutral status, so the panel
	// can say WHY without the neutral enum growing a value per provider.
	assert.Equal(t, "active", got.StatusDetail)
	require.NotNil(t, got.TokensUsed)
	assert.EqualValues(t, 1200, *got.TokensUsed)
	require.NotNil(t, got.TimeUsedSeconds)
	assert.EqualValues(t, 45, *got.TimeUsedSeconds)
	assert.False(t, got.Snapshot, "a frame with a turnId announces a change")
	// createdAt is Unix SECONDS. Reading it as milliseconds would put the goal
	// in 1970 and make every later report look like a different goal.
	assert.Equal(t, int64(1_700_000_000), got.CreatedAt.Unix())
}

// Codex reports no iteration count. The field must stay ABSENT rather than
// arrive as zero, because the panel renders a present zero as "0 turns" -- a
// number Codex never said.
func TestCodexGoal_OmitsCountersCodexDoesNotReport(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		goalFrame("main-thread", `"turn-1"`, "Ship it", "active", 10, 2, 1_700_000_000))))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Nil(t, got.Iterations, "Codex states no iteration count")
	assert.Nil(t, got.TokenBudget, "a null tokenBudget is absent, not zero")
}

// A report that arrives during the RESUME HANDSHAKE restates a goal that may be
// hours old. It must update state and NOT announce itself, or every resume
// writes "Goal set: X" into the transcript for a goal set long before.
func TestCodexGoal_AReportDuringResumeIsASnapshot(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.resumingThread.Store(true)

	handleCodexOutput(agent, parseLine([]byte(
		goalFrame("main-thread", "null", "Resumed objective", "active", 900, 30, 1_700_000_000))))

	got, ok := sink.LastGoal()
	require.True(t, ok, "a snapshot still updates the stored goal")
	assert.True(t, got.Snapshot)
	assert.Equal(t, "Resumed objective", got.Objective)
}

// The regression this discriminator exists for.
//
// Codex acknowledges a thread/goal/set with a report whose turnId is NULL,
// exactly like the resume snapshot -- so reading the null as "snapshot"
// swallowed every change the USER made: the goal moved on screen and the
// transcript never said so. Only the handshake tells the two apart.
func TestCodexGoal_AReportOutsideAResumeIsAnEvent(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		goalFrame("main-thread", "null", "Set by the user", "active", 0, 0, 1_700_000_000))))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.False(t, got.Snapshot, "a null turnId outside a resume is a real change")
	assert.Equal(t, "Set by the user", got.Objective)
}

func TestCodexGoal_StatusMapping(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]GoalStatus{
		"active":        GoalStatusActive,
		"paused":        GoalStatusPaused,
		"complete":      GoalStatusDone,
		"blocked":       GoalStatusBlocked,
		"usageLimited":  GoalStatusBlocked,
		"budgetLimited": GoalStatusBlocked,
		// A word this build does not know is treated as blocked, never active:
		// an unrecognized status is one the client cannot act on, and calling it
		// active would offer a Pause that the goal may refuse.
		"somethingNew": GoalStatusBlocked,
	} {
		assert.Equal(t, want, codexGoalStatus(wire), "status %q", wire)
	}
}

// A collab CHILD thread can carry its own goal. Reporting it would overwrite the
// session's objective with a subagent's, because goal state is keyed by the root.
func TestCodexGoal_ChildThreadGoalIsDropped(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		goalFrame("child-thread-7", `"turn-1"`, "Subagent objective", "active", 5, 1, 1_700_000_000))))

	assert.Empty(t, sink.Goals(), "a child thread's goal must not reach the session goal")
}

func TestCodexGoal_ClearedReachesTheSink(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		`{"jsonrpc":"2.0","method":"thread/goal/cleared","params":{"threadId":"main-thread"}}`)))

	assert.Equal(t, 1, sink.GoalClears())
}

func TestCodexGoal_ClearedForAChildThreadIsDropped(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	handleCodexOutput(agent, parseLine([]byte(
		`{"jsonrpc":"2.0","method":"thread/goal/cleared","params":{"threadId":"child-thread-7"}}`)))

	assert.Equal(t, 0, sink.GoalClears())
}

// The regression this whole change exists to prevent: a goal frame must never
// reach the transcript as a raw message. Codex sends one after every completed
// tool call, so the `default:` arm turned a long turn into a wall of raw JSON.
func TestCodexGoal_NeverPersistsARawTranscriptRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	for i := 0; i < 5; i++ {
		handleCodexOutput(agent, parseLine([]byte(
			goalFrame("main-thread", `"turn-1"`, "Keep going", "active", int64(100*i), int64(i), 1_700_000_000))))
	}

	assert.Empty(t, sink.Messages(), "goal frames are session state, never chat rows")
	assert.Len(t, sink.Goals(), 5, "every frame still reaches the goal sink")
}

func TestCodexGoal_SupportsEveryAction(t *testing.T) {
	t.Parallel()

	// Codex is the one provider with an acknowledged side-band call for all
	// four; thread/goal/set carries both the objective and the status.
	agent := newCodexAgentWithSink(&testSink{})
	assert.ElementsMatch(t,
		[]GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume},
		agent.SupportedGoalActions())
}

func TestCodexGoal_ControlRefusedWithoutAThread(t *testing.T) {
	t.Parallel()

	agent := newCodexAgentWithSink(&testSink{})
	agent.threadID = ""

	assert.Error(t, agent.SetGoal("anything"))
	assert.Error(t, agent.ClearGoal())
	assert.Error(t, agent.PauseGoal())
	assert.Error(t, agent.ResumeGoal())
}
