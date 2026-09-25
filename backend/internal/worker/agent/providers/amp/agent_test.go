package amp

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestStartNoProcessBeforeTheFirstMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.Empty(t, h.startedThreads(), "an agent that sends nothing leaves no thread in the account")
	assert.False(t, h.turnActive())
}

func TestFirstMessageStartsANewThreadAndWritesTheLine(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("hello")

	assert.Equal(t, []string{""}, h.startedThreads(), "a new thread")
	lines := fp.lines()
	require.Len(t, lines, 1)
	assert.Equal(t, "user", lines[0]["type"])
	assert.Nil(t, lines[0]["steer"], "a new turn is not a steering line")
	message := lines[0]["message"].(map[string]any)
	assert.Equal(t, "user", message["role"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "hello"}}, message["content"])
	assert.True(t, h.turnActive(), "the turn is armed from the moment the line leaves")
}

func TestSecondMessageReusesTheRunningProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("one")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	require.NoError(t, h.agent.SendInput("two", nil))
	assert.Len(t, h.startedThreads(), 1)
	assert.Len(t, fp.lines(), 2)
}

// The LeapMux input queue owns queueing: a plain line during a turn would go to
// Amp's own server queue and run out of LeapMux's sight.
func TestBusyRefusalRepublishesTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("one")
	err := h.agent.SendInput("two", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, &h.sink.Sink, h.agent, err)
	assert.Len(t, fp.lines(), 1, "the refused message reached no stdin")
}

func TestRisingTurnTokens(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	agenttest.AssertRisingTurnTokens(t, &h.sink.Sink, h.agent)
}

func TestSendInputForSessionRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-current"))
	agenttest.AssertRejectsMissingAndReplacedSessions(t, h.agent)
	assert.Empty(t, h.startedThreads())
	require.NoError(t, h.agent.SendInputForSession("T-current", "go", nil))
	assert.Equal(t, []string{"T-current"}, h.startedThreads(), "a resumed agent continues its thread")
}

func TestSteeringWritesASteerLineIntoTheRunningTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	require.NoError(t, h.agent.SteerInput("also say steered", nil))
	lines := fp.lines()
	require.Len(t, lines, 2)
	assert.Equal(t, true, lines[1]["steer"])
	assert.True(t, h.agent.SupportsSteering())
}

func TestSteeringNeedsARunningTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.ErrorIs(t, h.agent.SteerInput("nothing runs", nil), agent.ErrNoActiveTurn)
	fp := h.send("one")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	assert.ErrorIs(t, h.agent.SteerInput("the turn ended", nil), agent.ErrNoActiveTurn)
}

// A process that an interrupt signalled is on its way out, so a steering line
// has no turn left to join, although the turn stays armed until the exit.
func TestSteeringIsRefusedWhileTheProcessEnds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	require.NoError(t, h.agent.Interrupt())
	require.True(t, h.turnActive(), "the turn ends at Amp's result, not at the signal")

	assert.ErrorIs(t, h.agent.SteerInput("too late", nil), agent.ErrNoActiveTurn)
	assert.Len(t, fp.lines(), 1, "the steering line never reached the process")
}

func TestARefusedMessageStartsNoProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	err := h.agent.SendInput("", []*leapmuxv1.Attachment{{Filename: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
	require.Error(t, err)
	assert.Empty(t, h.startedThreads())
	assert.False(t, h.turnActive())
}

func TestAFailedStartDisarmsNothingAndReportsTheError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startErr = errors.New("amp: command not found")
	err := h.agent.SendInput("go", nil)
	require.ErrorContains(t, err, "command not found")
	assert.False(t, h.turnActive(), "the agent armed no turn for a message that never left")
	assert.Empty(t, h.sink.TurnActives(), "the agent published no turn for a message that never left")
	assert.False(t, h.agent.modeLocked, "a thread that never got a message keeps a mutable mode")
}

// A message whose write fails never reached Amp, so the agent releases the turn
// that it armed for the message, and the thread keeps a mutable mode.
func TestAFailedWriteDisarmsTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.startFn = func(threadID, mode string) (*ampProcess, error) {
		proc, err := h.start(threadID, mode)
		if err != nil {
			return nil, err
		}
		// A stdin that refuses every write, although the process still runs.
		fp := h.nextProc()
		fp.stdin.mu.Lock()
		fp.stdin.closed = true
		fp.stdin.mu.Unlock()
		return proc, nil
	}
	statusBefore := h.sink.StatusActiveCount()

	err := h.agent.SendInput("go", nil)
	require.ErrorContains(t, err, "deliver the message to amp")
	assert.False(t, h.turnActive())
	assert.Equal(t, []bool{true, false}, h.sink.TurnActives(), "the agent armed the turn before the write and released it after the failure")
	assert.False(t, h.agent.modeLocked, "the thread got no message, so its mode can still change")
	assert.Equal(t, statusBefore, h.sink.StatusActiveCount(), "the settings view learns of no fixed mode")
	assert.True(t, groupByID(t, h.agent.OptionGroups(), contracts.AmpOptionAgentMode).GetMutable())
}

// Two messages at the same moment start one turn in one process. The second
// finds the turn of the first, and the agent refuses it as busy, so the input
// queue holds it.
func TestConcurrentMessagesStartOneTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	gate := make(chan struct{})
	errs := make(chan error, 2)
	for _, text := range []string{"one", "two"} {
		go func() {
			<-gate
			errs <- h.agent.SendInput(text, nil)
		}()
	}
	close(gate)
	first, second := <-errs, <-errs

	accepted, busy := 0, 0
	for _, err := range []error{first, second} {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, agent.ErrAgentBusy):
			busy++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, 1, busy)
	assert.Equal(t, []string{""}, h.startedThreads(), "one process starts")
	fp := h.nextProc()
	assert.Len(t, fp.lines(), 1, "one message reached Amp")
}

// nextProcAfter drains started processes until one other than fp arrives.
func (h *harness) nextProcAfter(fp *fakeProc) *fakeProc {
	h.t.Helper()
	for {
		next := h.nextProc()
		if next != fp {
			return next
		}
	}
}

func TestModeLocksAtTheFirstMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.UpdateSettings(map[string]string{contracts.AmpOptionAgentMode: agentModeHigh})
	assert.Equal(t, agentModeHigh, h.agent.agentMode)
	statusBefore := h.sink.StatusActiveCount()

	h.send("go")
	assert.True(t, h.agent.modeLocked)
	assert.Greater(t, h.sink.StatusActiveCount(), statusBefore, "the settings view learns at once that the mode is fixed")

	h.agent.UpdateSettings(map[string]string{contracts.AmpOptionAgentMode: agentModeLow})
	assert.Equal(t, agentModeHigh, h.agent.agentMode, "the thread keeps the mode of its first message")
}

// A mode change that lands while the first process starts does not reach the
// thread, because the process already took its mode. The mode locks at the
// value that the thread really has.
func TestModeLocksAtTheModeThatTheThreadStartedWith(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	entered := make(chan string, 1)
	gate := make(chan struct{})
	h.agent.startFn = func(threadID, mode string) (*ampProcess, error) {
		entered <- mode
		<-gate
		return h.start(threadID, mode)
	}
	sent := make(chan error, 1)
	go func() { sent <- h.agent.SendInput("go", nil) }()
	started := <-entered
	h.agent.UpdateSettings(map[string]string{contracts.AmpOptionAgentMode: agentModeHigh})
	close(gate)
	require.NoError(t, <-sent)

	assert.Equal(t, agentModeMedium, started, "the thread started in the mode that it read")
	group := groupByID(t, h.agent.OptionGroups(), contracts.AmpOptionAgentMode)
	assert.False(t, group.GetMutable())
	assert.Equal(t, started, group.GetCurrentValue(), "the locked mode is the thread's mode")
}

func TestResumedAgentStartsWithTheModeLocked(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-old"))
	assert.True(t, h.agent.modeLocked)
}

func TestInterruptSignalsTheProcessAndTheResultEndsTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep 40")
	require.NoError(t, h.agent.Interrupt())
	assert.True(t, fp.ending(), "no new line goes to a process an interrupt signalled")
	h.feed(fp, errorResult(interruptedMessage))
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.Equal(t, []string{""}, h.startedThreads(), "an interrupt does not resume at once")

	// The next message resumes the thread in a new process.
	h.feed(fp, initLine("T-1"))
	require.NoError(t, h.agent.SendInput("next", nil))
	assert.Equal(t, []string{"", "T-1"}, h.startedThreads())
}

func TestInterruptWithNoTurnIsANoop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, h.agent.Interrupt())
	assert.Empty(t, h.sink.Messages())
}

// A process that dies with no `result` in its interrupted turn -- the stop that
// replaces the signal on Windows -- still ends the turn as an interruption.
func TestExitAfterInterruptWithNoResultEndsTheTurnInterrupted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep 40")
	require.NoError(t, h.agent.Interrupt())
	fp.exit()
	fp.awaitHandled(t)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.Equal(t, interruptedMessage, decodeRow(t, ends[0].Content)["error"])
}

// The agent stops a process that ignores the interrupt after the API timeout.
func TestInterruptThatAmpIgnoresStopsTheProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "interrupt-escalation")
	defer trap.Close()
	fp := h.send("sleep 40")
	require.NoError(t, h.agent.Interrupt())
	call := trap.MustWait(t.Context())
	call.MustRelease(t.Context())
	h.clock.Advance(10 * time.Second).MustWait(t.Context())
	fp.awaitHandled(t)
	assert.Len(t, h.turnEnds(), 1)
}

// An error that ends a turn resumes the thread in a new process at once, so the
// thread has an executor again. It happens once, and never loops.
func TestErrorExitMidTurnResumesTheThreadOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-9"))
	h.feed(fp, errorResult("Model Provider Overloaded"))
	fp.exit()
	fp.awaitHandled(t)

	resumed := h.nextProcAfter(fp)
	assert.Equal(t, []string{"", "T-9"}, h.startedThreads())
	assert.True(t, resumed.resumed)
	assert.Empty(t, resumed.lines(), "the resume writes nothing; the next message does")

	// The resumed process dies while idle: it states the failure and resumes nothing.
	resumed.exit()
	resumed.awaitHandled(t)
	assert.Equal(t, []string{"", "T-9"}, h.startedThreads(), "no retry loop")
	notifications := h.sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Contains(t, notifications[0][contracts.NotificationFieldError], "send /clear", "a resume that fails before its init line says how to start fresh")
}

// A resume after an error that the agent's Stop overtakes writes no notice: the
// start fails because the agent stopped, and a stopped agent reports nothing.
//
// The start fails with an error that is neither errAgentStopped nor a context
// error, as a process that the stop killed fails, so only the check of the
// agent's own state can keep the notice out.
func TestResumeThatStopOvertakesWritesNoNotice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-6"))

	entered := make(chan struct{})
	gate := make(chan struct{})
	h.agent.startFn = func(string, string) (*ampProcess, error) {
		close(entered)
		<-gate
		return nil, errors.New("signal: killed")
	}
	fp.exit()
	<-entered
	h.agent.Stop()
	close(gate)
	// The resume holds sendMu for its whole run, so this waits for its end.
	h.agent.sendMu.Lock()
	stopped := h.agent.IsStopped()
	h.agent.sendMu.Unlock()
	require.True(t, stopped)

	for _, notification := range h.sink.Notifications() {
		assert.NotContains(t, notification[contracts.NotificationFieldError], "could not resume", "a stopped agent reports no failed resume")
	}
}

// A resume after an error that fails while the agent runs states the failure,
// so the reader learns that the thread has no executor until the next message.
func TestAFailedResumeStatesTheFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-6"))
	h.mu.Lock()
	h.startErr = errors.New("amp: command not found")
	h.mu.Unlock()
	fp.exit()
	fp.awaitHandled(t)

	require.Eventually(t, func() bool {
		for _, notification := range h.sink.Notifications() {
			if message, _ := notification[contracts.NotificationFieldError].(string); strings.Contains(message, "could not resume the Amp thread T-6") {
				return strings.Contains(message, "command not found")
			}
		}
		return false
	}, 30*time.Second, 2*time.Millisecond, "the failed resume states the thread and the reason")
	assert.Equal(t, []string{"", "T-6"}, h.startedThreads(), "the resume ran once")
	assert.False(t, h.turnActive(), "a failed resume arms no turn")
}

// A process that printed its `result` and does not exit holds the next
// message only until the API timeout: the agent then stops the process and
// starts the next one. An idle process that the agent stopped on purpose states
// no error.
func TestNewMessageStopsAnOldProcessThatDoesNotExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "await-exit")
	defer trap.Close()
	fp := h.send("go")
	h.feed(fp, initLine("T-4"))
	h.feed(fp, textLine("done", stopReasonEndTurn))
	h.feed(fp, `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"done","session_id":"T-4"}`)
	require.True(t, fp.ending())

	h.drainStarted()
	sent := make(chan error, 1)
	go func() { sent <- h.agent.SendInput("next", nil) }()
	ctx := testutil.DeadlineContext(t)
	trap.MustWait(ctx).MustRelease(ctx)
	h.clock.Advance(10 * time.Second).MustWait(ctx)
	require.NoError(t, <-sent)

	assert.True(t, fp.IntentionalStopRequested(), "the agent stopped the old process")
	next := h.nextProc()
	assert.True(t, next.resumed)
	assert.Equal(t, []string{"", "T-4"}, h.startedThreads())
	assert.True(t, h.turnActive(), "the stopped process ended no new turn")
	assert.Len(t, h.turnEnds(), 1)
	assert.Empty(t, h.sink.Notifications(), "an idle process that the agent stopped states no error")
}

// A stop ends a message that waits for the old process, and no new process
// runs after the stop.
func TestStopEndsAMessageThatWaitsForTheOldProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "await-exit")
	defer trap.Close()
	fp := h.send("go")
	h.feed(fp, initLine("T-4"))
	h.feed(fp, textLine("done", stopReasonEndTurn))
	h.feed(fp, `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"done","session_id":"T-4"}`)

	h.drainStarted()
	sent := make(chan error, 1)
	go func() { sent <- h.agent.SendInput("next", nil) }()
	ctx := testutil.DeadlineContext(t)
	trap.MustWait(ctx).MustRelease(ctx)
	h.agent.Stop()

	assert.ErrorIs(t, <-sent, errAgentStopped)
	fp.awaitHandled(t)
	h.agent.mu.Lock()
	proc := h.agent.proc
	h.agent.mu.Unlock()
	assert.Nil(t, proc, "no process runs after the stop")
	assert.Len(t, h.turnEnds(), 1, "the refused message armed no turn")
}

// Amp answers the stop's SIGINT with an error `result`, which the reader
// handles before the stop ends the turn itself. The stop marks the turn as
// interrupted first, so that result ends the turn as an interruption, with
// Amp's own row, and resumes nothing.
func TestStopMakesAmpsCancelResultAnInterruption(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("hang")
	h.feed(fp, initLine("T-3"))
	// Amp prints its cancellation when the stop closes its input, and then
	// exits.
	fp.stdin.onClose = func() {
		h.feed(fp, errorResult(interruptedMessage))
		fp.exit()
	}

	h.agent.Stop()
	fp.awaitHandled(t)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	row := decodeRow(t, ends[0].Content)
	assert.Equal(t, interruptedMessage, row["error"])
	assert.EqualValues(t, 5, row["duration_ms"], "the turn end is Amp's own result row")
	assert.False(t, fp.resumeAfterExit.Load(), "an interruption resumes nothing")
	assert.Equal(t, []string{""}, h.startedThreads())
	assert.Empty(t, h.sink.Notifications())
}

// Output that the service discards before a restart persists nothing at the
// exit either: no turn-end row, no error notice, and no resume.
func TestDiscardedOutputEndsNoTurnAtTheExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-2"))
	h.agent.DiscardOutput()
	fp.exit()
	fp.awaitHandled(t)

	assert.Empty(t, h.turnEnds())
	assert.Empty(t, h.sink.Notifications())
	assert.Equal(t, []string{""}, h.startedThreads(), "a discarded agent resumes nothing")
}

// A crash mid-turn -- a process that printed no `result` -- ends the turn with
// the reason and resumes the thread.
func TestCrashMidTurnEndsTheTurnWithTheReasonAndResumes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-5"))
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Contains(t, decodeRow(t, ends[0].Content)["error"], "exited unexpectedly")
	h.nextProcAfter(fp)
	assert.Equal(t, []string{"", "T-5"}, h.startedThreads())
}

// A new thread whose process died before its init line has no thread to
// resume: the next message starts a new one.
func TestCrashBeforeTheThreadExistedResumesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	fp.exit()
	fp.awaitHandled(t)
	assert.Len(t, h.turnEnds(), 1)
	assert.Equal(t, []string{""}, h.startedThreads())
}

func TestIdleExitWithAResultStatesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	h.feed(fp, `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"done","session_id":"T-1"}`)
	fp.exit()
	fp.awaitHandled(t)
	assert.Empty(t, h.sink.Notifications())
}

// awaitExitWait returns once a message waits for the exit of the last process:
// the agent arms the timer of that wait. A new process that starts first fails
// the test, because then the message did not wait. The caller drains h.started
// before it sends the message.
func awaitExitWait(t *testing.T, h *harness, trap *quartz.Trap) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	calls := make(chan *quartz.Call, 1)
	go func() {
		if call, err := trap.Wait(ctx); err == nil {
			calls <- call
		}
	}()
	select {
	case call := <-calls:
		call.MustRelease(ctx)
	case <-h.started:
		t.Fatal("a new process started before the last one finished its exit")
	case <-ctx.Done():
		t.Fatal("the message did not wait for the last process")
	}
	select {
	case <-h.started:
		t.Fatal("a new process started while the message waited")
	default:
	}
}

// A message that arrives while the last process is on its way out waits for
// that process's exit handler, so the handler cannot end the new turn.
func TestNewMessageWaitsForTheOldProcessExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "await-exit")
	defer trap.Close()
	fp := h.send("go")
	h.feed(fp, initLine("T-2"))
	require.NoError(t, h.agent.Interrupt())
	h.feed(fp, errorResult(interruptedMessage))

	h.drainStarted()
	sent := make(chan error, 1)
	go func() { sent <- h.agent.SendInput("next", nil) }()
	awaitExitWait(t, h, trap)
	fp.exit()
	require.NoError(t, <-sent)
	assert.Equal(t, []string{"", "T-2"}, h.startedThreads())
	assert.True(t, h.turnActive(), "the old process's exit did not end the new turn")
}

// A message that arrives while the exit handler of the last process still runs
// waits for that handler, although the process already exited. Otherwise the
// handler refuses the new process's permission requests, closes its shell rows,
// and ends its turn with the old exit's error.
func TestNewMessageWaitsForTheWholeExitHandler(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "await-exit")
	defer trap.Close()
	fp := startBackgroundCommand(h, "npm run dev", 4242)
	h.feed(fp, textLine("The server runs.", stopReasonEndTurn))
	require.Len(t, h.turnEnds(), 1)

	// The exit handler blocks in the close of the shell row, after the process
	// exited.
	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	h.sink.OnCloseBackgroundTask = func(string, bgtask.Status) {
		once.Do(func() { close(entered) })
		<-gate
	}
	fp.exit()
	<-entered

	h.drainStarted()
	sent := make(chan error, 1)
	go func() { sent <- h.agent.SendInput("next", nil) }()
	awaitExitWait(t, h, trap)
	close(gate)
	require.NoError(t, <-sent)
	h.nextProc()
	assert.True(t, h.turnActive(), "the old exit handler did not end the new turn")
	assert.Len(t, h.turnEnds(), 1, "no turn ended with the old exit's error")
}

func TestStopEndsTheRunningTurnAndRemovesTheDirectory(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[`+toolUseBlock("TU-a", "shell_command", `{"command":"sleep 40"}`)+`]`, "tool_use"))
	h.agent.Stop()

	assert.True(t, h.agent.IsStopped())
	assert.True(t, fp.stdin.closed)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	_, err := os.Stat(h.agent.launch.stateDir)
	assert.True(t, os.IsNotExist(err), "the agent's directory is gone")
	require.NoError(t, h.agent.Wait())

	assert.ErrorContains(t, h.agent.SendInput("after", nil), "stopped")
	assert.ErrorContains(t, h.agent.Interrupt(), "stopped")
	h.agent.Stop() // idempotent
}

func TestStopWithNoProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.Stop()
	assert.Empty(t, h.turnEnds())
	require.NoError(t, h.agent.Wait())
}

func TestClearContextAsksForARestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, err := h.agent.ClearContext()
	assert.ErrorIs(t, err, agent.ErrContextClearUnsupported)
}

func TestRawInputReachesTheRunningProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userLine := []byte(`{"type":"user","message":{"role":"user","content":[]}}`)
	assert.ErrorContains(t, h.agent.SendRawInput(userLine), "no Amp process")
	fp := h.send("go")
	require.NoError(t, h.agent.SendRawInput(userLine))
	assert.Len(t, fp.lines(), 2)

	// A process that an interrupt signalled takes no new line.
	require.NoError(t, h.agent.Interrupt())
	assert.ErrorContains(t, h.agent.SendRawInput(userLine), "no Amp process")
	assert.Len(t, fp.lines(), 2)

	h.agent.Stop()
	assert.ErrorIs(t, h.agent.SendRawInput(userLine), errAgentStopped)
}

func TestStderrComesFromTheLastProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.Empty(t, h.agent.Stderr())
	fp := h.send("go")
	_, _ = fp.StderrWriterForTest().Write([]byte("Error: boom\n"))
	assert.Equal(t, "Error: boom\n", h.agent.Stderr())
	fp.exit()
	fp.awaitHandled(t)
	assert.Equal(t, "Error: boom\n", h.agent.Stderr(), "the last process's stderr survives its exit")
}

func TestTrimStderrDropsTheCommandPaletteHint(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Error: Thread not found or you don't have access.",
		trimStderr("\nError: Thread not found or you don't have access.\nUse 'feedback: report bug' from the command palette to report it.\n"))
	assert.Empty(t, trimStderr("  \n"))
	assert.Equal(t, "Error: a\nat b", trimStderr("Error: a\r\n\r\n  at b\r\nUse the command palette.\r\n"), "Windows line ends and blank lines go")
	assert.Empty(t, trimStderr(""))
}

// The stderr read of an exited process can wait seconds for the drain, for
// example when an MCP server inherited Amp's stderr and outlives it. The exit
// handler therefore reads it with no lock held, so the agent answers while the
// read waits.
func TestExitHandlerReadsStderrWithNoLockHeld(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var reads, readsUnderLock atomic.Int32
	h.agent.stderrFn = func(*ampProcess) string {
		reads.Add(1)
		if h.agent.mu.TryLock() {
			h.agent.mu.Unlock()
		} else {
			readsUnderLock.Add(1)
		}
		return "Error: boom\n"
	}
	fp := h.send("go")
	h.feed(fp, initLine("T-3"))
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Contains(t, decodeRow(t, ends[0].Content)["error"], "Error: boom")
	assert.Equal(t, int32(1), reads.Load(), "the handler reads stderr once")
	assert.Zero(t, readsUnderLock.Load(), "no read waits while the agent's lock is held")
}

func TestDescribeExitStatesTheStderr(t *testing.T) {
	t.Parallel()
	fp := newFakeProc("agent-1", false)
	_, _ = fp.StderrWriterForTest().Write([]byte("Error: API request for getUserInfo failed: 401\n"))
	assert.True(t, strings.HasSuffix(describeExit(fp.ampProcess, fp.Stderr()), "Error: API request for getUserInfo failed: 401"))
}
