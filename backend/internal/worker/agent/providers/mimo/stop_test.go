package mimo

import (
	"net/http"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interruptOnClock runs Interrupt while the abort-grace timer is trapped, and
// returns the grace that the timer asked the clock for. Interrupt arms the timer
// inside the call, and a trapped call waits for the test, so the call runs on
// its own goroutine.
func interruptOnClock(t *testing.T, a *Agent, clock *quartz.Mock) time.Duration {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	grace := clock.Trap().AfterFunc(mimoAbortGraceTimerTag)
	defer grace.Close()
	result := make(chan error, 1)
	go func() { result <- a.Interrupt() }()
	delay := testutil.WaitForTimer(t, ctx, grace)
	require.NoError(t, <-result)
	return delay
}

// assertNoTimer asserts that the agent armed no timer on its clock.
func assertNoTimer(t *testing.T, clock *quartz.Mock, message string) {
	t.Helper()
	_, pending := clock.Peek()
	assert.False(t, pending, message)
}

func TestInterruptWithoutATurnSendsNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	require.NoError(t, a.Interrupt())
	assert.Empty(t, server.allRequests())
	assertNoTimer(t, clock, "no abort means no check after the grace")
}

func TestInterruptAbortsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))

	assert.Equal(t, mimoAbortGrace, interruptOnClock(t, a, clock))
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1)

	// MiMo ends an aborted turn at once, with an abort error and an idle.
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)

	// The grace ends, and the check reads the server's status: no turn runs.
	clock.Advance(mimoAbortGrace).MustWait(testutil.DeadlineContext(t))
	assert.Len(t, server.requestsTo("GET /session/status"), 1, "the check after the grace reads the server's status")
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "the check after the grace finds the turn already ended")
}

// An abort whose idle never arrives still ends the turn: the check after the
// grace compares the turn with the server's own status.
func TestInterruptEndsATurnWhoseIdleNeverArrives(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))

	interruptOnClock(t, a, clock)
	clock.Advance(mimoAbortGrace - time.Nanosecond).MustWait(testutil.DeadlineContext(t))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the grace has not ended yet")

	clock.Advance(time.Nanosecond).MustWait(testutil.DeadlineContext(t))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.True(t, messages[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[0].Completion)
	assert.Equal(t, contracts.MiMoEventSessionStatus, rowTypes(t, messages)[0], "the divider reads as the idle MiMo would send")
}

func TestInterruptThatFailsKeepsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	server.respond("POST /session/ses_test/abort", http.StatusInternalServerError, `{}`)

	assert.ErrorContains(t, a.Interrupt(), "abort the MiMo turn")
	assert.False(t, a.interruptRequested)
	assertNoTimer(t, clock, "the abort stopped nothing, so no check follows")
	assert.Equal(t, []bool{true}, sink.TurnActives())
}

// A turn with no session has nothing that an abort could address.
func TestInterruptWithoutASessionSendsNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	a.turnActive = true
	a.sessionID = ""

	require.NoError(t, a.Interrupt())
	assert.Empty(t, server.allRequests())
	assert.False(t, a.interruptRequested)
	assertNoTimer(t, clock, "no abort means no check after the grace")
}

func TestInterruptOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.Interrupt(), "stopped")
	assert.Empty(t, server.allRequests())
}

// feedUnfinishedTurn feeds a turn that stops in the middle: the main agent and
// a background subagent each stream part of a text and run a command that did
// not end.
func feedUnfinishedTurn(t *testing.T, a *Agent) {
	t.Helper()
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a,
		textPartEvent(t, partTypeText, "prt_t", "msg_p", "", false),
		deltaEvent(t, "prt_t", "msg_p", "Half a sen"),
		toolPartEvent(t, "prt_1", "msg_p", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "sleep 100"}}),
		messageEvent(t, "msg_c2", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "prt_c", "msg_c2", "", false),
		deltaEvent(t, "prt_c", "msg_c2", "Sub half"),
		toolPartEvent(t, "prt_c3", "msg_c2", contracts.MiMoToolBash, "call-sub-01", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "sleep 200"}}),
	)
}

// assertUnfinishedTurnClosed asserts that the rows of feedUnfinishedTurn were
// finished with completion, that the turn ended, and that the subagent's row
// took status.
func assertUnfinishedTurnClosed(t *testing.T, sink *agenttest.Sink, completion agent.MessageCompletion, status bgtask.Status) {
	t.Helper()
	messages := sink.Messages()
	require.Len(t, messages, 5, "the spawn call's two rows, the command's opener, the unfinished text, the command's closer")
	assert.Equal(t, "call-1", messages[2].SpanID)
	assert.False(t, messages[2].Closing)
	_, text, textCompletion := assembledText(t, messages[3].Content)
	assert.Equal(t, "Half a sen", text)
	assert.Equal(t, string(completion), textCompletion)
	assert.Equal(t, "call-1", messages[4].SpanID)
	assert.True(t, messages[4].Closing)
	assert.Equal(t, completion, messages[4].Completion)
	assert.NotContains(t, sink.OpenSpans(), "call-1")

	childRows := sink.Child("child-of-" + spawnCallID).Messages()
	require.Len(t, childRows, 4, "the prompt, the command's opener, the unfinished text, the command's closer")
	_, text, textCompletion = assembledText(t, childRows[2].Content)
	assert.Equal(t, "Sub half", text)
	assert.Equal(t, string(completion), textCompletion)
	assert.Equal(t, "call-sub-01", childRows[3].SpanID)
	assert.True(t, childRows[3].Closing)
	assert.Equal(t, completion, childRows[3].Completion)
	assert.Equal(t, status, backgroundTask(t, sink, spawnCallID).Status)

	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "a process that ended runs no turn")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

// Stop ends the turn that runs. What the turn streamed and never finished
// stays in the transcript, marked as interrupted.
func TestStopFinishesTheUnfinishedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	// Stop waits for the exit that its signal causes.
	a.SimulateExitForTest()
	feedUnfinishedTurn(t, a)
	require.Len(t, sink.Messages(), 3, "before the stop, only the command's opener follows the spawn")

	a.Stop()
	assertUnfinishedTurnClosed(t, sink, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	assert.False(t, a.turnActive)
}

// A process that exits on its own leaves its turn unfinished too. Its rows
// state the failure.
func TestUnexpectedExitFinishesTheUnfinishedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feedUnfinishedTurn(t, a)
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	assertUnfinishedTurnClosed(t, sink, agent.MessageCompletionError, bgtask.StatusFailed)
}

// The worker calls Wait after Stop. The second call finds nothing left open.
func TestWaitAfterStopFinishesNothingTwice(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.SimulateExitForTest()
	feedUnfinishedTurn(t, a)

	a.Stop()
	rows, childRows := len(sink.Messages()), len(sink.Child("child-of-"+spawnCallID).Messages())
	require.NoError(t, a.Wait())
	assert.Len(t, sink.Messages(), rows)
	assert.Len(t, sink.Child("child-of-"+spawnCallID).Messages(), childRows)
}

// A failure that the turn end would state as its divider has no turn end when
// the process exits first. It stays in the transcript as a notification, since
// it can be the reason for the exit.
func TestExitPersistsAFailureThatNoTurnEndReported(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "the provider closed the connection")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), failure)
	require.Zero(t, sink.NotificationCount(), "inside a turn, the failure waits for the turn end")
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	assert.Nil(t, a.unattributed)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

// The main agent's failed message claims its failure for the turn end. When the
// process exits first, that claimed failure is the notification.
func TestExitPersistsTheTurnsOwnFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "bad request")
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		failure,
		failedMessageEvent(t, "msg_1", mainActorID, "APIError", "bad request"),
	)
	require.NotNil(t, a.turnFailure, "the message claimed the failure for the turn end")
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	assert.Nil(t, a.turnFailure)
	assert.Empty(t, sink.Messages(), "no turn end follows the exit")
}

// Stop waits for the stream goroutine, so no event reaches a handler after the
// finish. A goroutine that does not end holds Stop only up to the wait's limit,
// and Stop then finishes the turn all the same.
func TestStopWaitsForTheStreamOnlyUpToItsLimit(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	a.SimulateExitForTest()
	// A stream goroutine that its cancel does not end.
	a.streamCancel = func() {}
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
		deltaEvent(t, "prt_t", "msg_1", "Half"),
	)
	wait := clock.Trap().NewTimer(mimoStreamStopTimerTag)
	defer wait.Close()

	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	assert.Equal(t, mimoStreamStopWait, testutil.WaitForTimer(t, ctx, wait))
	clock.Advance(mimoStreamStopWait - time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
		t.Fatal("Stop returned before the wait's limit")
	default:
	}
	assert.Empty(t, sink.Messages(), "nothing is finished while the stream can still dispatch")

	clock.Advance(time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not return at the wait's limit")
	}
	messages := sink.Messages()
	require.Len(t, messages, 1)
	_, text, completion := assembledText(t, messages[0].Content)
	assert.Equal(t, "Half", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.False(t, a.turnActive)
}

// A stream goroutine that ends at once ends the wait at once: Stop arms the
// limit and stops it again.
func TestStopDoesNotWaitForAStreamThatEnded(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, _, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	startTestStream(t, a, server)
	a.SimulateExitForTest()
	wait, stopWait := testutil.NewTimerTraps(t, clock, mimoStreamStopTimerTag)

	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	testutil.WaitForTimer(t, ctx, wait)
	stopWait.MustWait(ctx).MustRelease(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not return when the stream ended")
	}
	_, pending := clock.Peek()
	assert.False(t, pending)
}

// A stop with no turn persists nothing.
func TestStopWithoutATurnPersistsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.SimulateExitForTest()

	a.Stop()
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ChildAgentIDs(), "a flush opens no transcript")
}
