package cline

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestInterruptAbortsTheRunningTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.Interrupt(), "no turn, nothing to abort")
	assert.Empty(t, r.hub.commandsNamed(commandRunAbort))

	requestID := r.startTurn(t, "Hello.")
	require.NoError(t, r.agent.Interrupt())
	abort, ok := r.hub.waitCommand(commandRunAbort)
	require.True(t, ok)
	assert.Equal(t, r.sessionID(), abort.SessionID)
	// Cline ends an aborted run with an error when a tool failed first; the
	// user's interrupt still reads as an interruption.
	r.endRun(t, requestID, contracts.ClineRunReasonError)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
}

func TestAnInterruptThatClineRefusesLeavesTheTurnAsItWas(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.hub.handle(commandRunAbort, func(fakeCommand) fakeReply { return fakeReply{Code: "busy", Message: "no"} })
	require.Error(t, r.agent.Interrupt())
	r.endRun(t, requestID, contracts.ClineRunReasonError)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionError, messages[len(messages)-1].Completion, "a failed interrupt interrupted nothing")
}

func TestInterruptRefusesAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Stop()
	require.ErrorIs(t, r.agent.Interrupt(), errAgentStopped)
}

func TestStopShutsTheDaemonDownAndEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	dir := r.agent.dir.Path()
	r.agent.Stop()
	assert.True(t, r.agent.IsStopped())
	assert.Len(t, r.hub.commandsNamed(commandRunAbort), 1, "the stop aborts the run first")
	assert.Equal(t, 1, r.hub.shutdownCount(), "the stop asks the daemon to shut down with its token")
	assert.False(t, r.turnActive())
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.True(t, last.TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, last.Completion)
	assert.Equal(t, contracts.ClineEventRunAborted, decode(t, last.Content)["event"])
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "the stop removes the agent's directory")
	require.NoError(t, claimSession(r.sessionID(), "someone"), "the stop releases the session")
	releaseSession(r.sessionID(), "someone")

	r.agent.Stop()
	assert.Equal(t, 1, r.hub.shutdownCount(), "a second stop does nothing")
}

func TestWaitEndsATurnThatTheDaemonsExitCutShort(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	r.exit()
	_ = r.agent.Wait()
	assert.False(t, r.turnActive())
	messages := r.sink.Messages()
	last := messages[len(messages)-1]
	assert.True(t, last.TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, last.Completion)
	assert.Equal(t, contracts.ClineEventRunFailed, decode(t, last.Content)["event"])
	assert.Contains(t, payloadOf(t, last)["error"], "exited")

	// The manager calls Wait alone after an exit that nobody asked for, so Wait
	// releases the session and removes the agent's directory.
	require.NoError(t, claimSession(r.sessionID(), "other-agent"), "no dead daemon keeps the session claimed")
	releaseSession(r.sessionID(), "other-agent")
	assert.NoDirExists(t, r.agent.dir.Path(), "the directory of the dead daemon goes")
}

// Stop and Wait each run the teardown, and it runs once: a second one would end
// the turn twice or release a session that another agent claimed since.
func TestStopAfterWaitTearsDownOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	r.exit()
	_ = r.agent.Wait()
	turnEnds := 0
	for _, message := range r.sink.Messages() {
		if message.TurnEnd {
			turnEnds++
		}
	}
	require.NoError(t, claimSession(r.sessionID(), "other-agent"))
	t.Cleanup(func() { releaseSession(r.sessionID(), "other-agent") })
	r.agent.Stop()
	after := 0
	for _, message := range r.sink.Messages() {
		if message.TurnEnd {
			after++
		}
	}
	assert.Equal(t, turnEnds, after, "the turn ends once")
	assert.ErrorIs(t, claimSession(r.sessionID(), "third-agent"), errSessionHosted, "the stop leaves another agent's claim alone")
}

// A context clear that claims a new session while a stop runs keeps no claim.
// The order is the one that leaked: the stop reads the old session, then the
// clear claims the new one and switches to it, and then the stop tears down.
func TestAStopDuringAContextClearReleasesTheNewSession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	oldID := r.sessionID()
	newID := "cleared-" + oldID
	createArrived := make(chan string, 1)
	r.hub.handle(commandSessionCreate, func(c fakeCommand) fakeReply {
		createArrived <- c.RequestID
		return fakeReply{Hold: true}
	})
	aborts := 0
	var createID string
	r.hub.handle(commandRunAbort, func(fakeCommand) fakeReply {
		aborts++
		if aborts == 2 {
			// The stop's abort: it read the old session already. Let the clear
			// finish its switch before the stop goes on.
			r.hub.reply(createID, fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": newID}}})
			waitFor(t, func() bool { return r.agent.currentSession() == newID }, "the clear switches to the new session")
		}
		return fakeReply{}
	})
	r.startTurn(t, "Hello.")
	cleared := make(chan struct{})
	go func() {
		defer close(cleared)
		_, _ = r.agent.ClearContext()
	}()
	createID = <-createArrived
	r.agent.Stop()
	<-cleared
	require.NoError(t, claimSession(newID, "other-agent"), "the stop released the session that the clear claimed")
	releaseSession(newID, "other-agent")
	require.NoError(t, claimSession(oldID, "other-agent"))
	releaseSession(oldID, "other-agent")
}

// A backfill that the stop starts for a parallel subagent ends inside the stop,
// so no registry row changes after Stop returns.
func TestStopFinishesTheRowsOfParallelSubagents(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Delegate twice.")
	r.emit(contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look one."))
	r.emit(contracts.ClineEventToolStarted, spawnStart("spawn_2", "Look two."))
	waitFor(t, func() bool {
		_, one := r.sink.BackgroundTask("spawn_1")
		_, two := r.sink.BackgroundTask("spawn_2")
		return one && two
	}, "both spawns run")
	r.emit(eventIterationStarted, map[string]any{"iteration": 1})
	r.agent.Stop()
	for _, key := range []string{"spawn_1", "spawn_2"} {
		item, ok := r.sink.BackgroundTask(key)
		require.True(t, ok)
		assert.True(t, item.Status.IsFinished(), "%s is final when Stop returns", key)
	}
}

func TestStopEndsASubagentThatStillRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Delegate.")
	r.emit(contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	waitFor(t, r.agent.spawnRuns, "the spawn runs")
	r.agent.Stop()
	item, _ := r.sink.BackgroundTask("spawn_1")
	assert.True(t, item.Status.IsFinished())
}

// An interrupt before the run starts finds no run to abort, and Cline still
// answers `applied`. The worker aborts the run when it starts instead, so the
// user's interrupt is not lost.
func TestAnInterruptBeforeTheRunStartsAbortsItWhenItStarts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sent := make(chan string, 1)
	r.hub.handle(commandSessionSendInput, func(c fakeCommand) fakeReply {
		sent <- c.RequestID
		return fakeReply{Hold: true}
	})
	sendErr := make(chan error, 1)
	go func() { sendErr <- r.agent.SendInput("Hello.", nil) }()
	requestID := <-sent
	require.NoError(t, r.agent.Interrupt())
	assert.Empty(t, r.hub.commandsNamed(commandRunAbort), "no run exists to abort yet")

	r.emit(eventRunStarted, map[string]any{"requestId": requestID, "clientId": r.agent.clientID})
	require.NoError(t, <-sendErr)
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandRunAbort)) == 1 }, "the run is aborted once it starts")
	r.endRun(t, requestID, contracts.ClineRunReasonAborted)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
}

// A settling turn holds the input queue while a mode change applies, and no run
// of Cline's belongs to it. A stop during it ends no turn of the user's, so it
// writes no turn-end row, and the turn is no longer active.
func TestAStopDuringAModeChangeWritesNoTurnEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Mu.Lock()
	r.agent.turn = turnState{active: true, settling: true}
	r.agent.Mu.Unlock()
	r.agent.finishOutput(agent.MessageCompletionInterrupted, "")
	assert.Empty(t, r.sink.Messages(), "no turn of the user's ended")
	assert.False(t, r.turnActive())
}

// A turn that stray output opened has no run, so no run end ends it. An
// interrupt ends it at once, after an abort for a run that the worker did not
// see start.
func TestAnInterruptEndsATurnThatHasNoRun(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventAssistantDelta, map[string]any{"text": "late"})
	require.True(t, r.turnActive(), "stray output opens a turn")
	require.NoError(t, r.agent.Interrupt())
	assert.False(t, r.turnActive(), "the interrupt ends the turn")
	assert.Len(t, r.hub.commandsNamed(commandRunAbort), 1)
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
}

// An abort that fails when the run starts stopped nothing, so the run's end
// states its own completion, not an interruption.
func TestADeferredAbortThatFailsLeavesTheTurnAsItWas(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sent := make(chan string, 1)
	r.hub.handle(commandSessionSendInput, func(c fakeCommand) fakeReply {
		sent <- c.RequestID
		return fakeReply{Hold: true}
	})
	r.hub.handle(commandRunAbort, func(fakeCommand) fakeReply { return fakeReply{Code: "busy", Message: "no"} })
	sendErr := make(chan error, 1)
	go func() { sendErr <- r.agent.SendInput("Hello.", nil) }()
	requestID := <-sent
	require.NoError(t, r.agent.Interrupt())
	r.emit(eventRunStarted, map[string]any{"requestId": requestID, "clientId": r.agent.clientID})
	require.NoError(t, <-sendErr)
	_, ok := r.hub.waitCommand(commandRunAbort)
	require.True(t, ok, "the run is aborted once it starts")
	waitFor(t, func() bool {
		r.agent.Mu.Lock()
		defer r.agent.Mu.Unlock()
		return !r.agent.turn.interruptRequested
	}, "the failed abort withdraws the interrupt")
	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionComplete, messages[len(messages)-1].Completion)
}

// A crash of the daemon fails the subagents that its turn left running: no
// report states how they ended.
func TestWaitFailsTheSubagentsThatACrashCutShort(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Delegate.")
	r.emit(contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	waitFor(t, r.agent.spawnRuns, "the spawn runs")
	r.exit()
	_ = r.agent.Wait()
	item, ok := r.sink.BackgroundTask("spawn_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, item.Status)
}

// A stop of an agent with no session aborts nothing, and still shuts the daemon
// down and removes the agent's directory.
func TestStopOfAnAgentWithNoSession(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	dir := r.agent.dir.Path()
	r.agent.Stop()
	assert.Empty(t, r.hub.commandsNamed(commandRunAbort))
	assert.Equal(t, 1, r.hub.shutdownCount())
	assert.NoDirExists(t, dir)
	assert.Empty(t, r.sink.Messages(), "no turn ended")
}

// describeWith states the end of a daemon whose stderr held stderr.
func describeWith(t *testing.T, stderr string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "cline-exit", ProviderName: "cline", Ctx: ctx, Cancel: cancel})
	p.DrainStderr(strings.NewReader(stderr))
	return (&Agent{Process: &p}).describeExit()
}

// The turn-end row of a crash states the daemon's stderr, trimmed, and at most
// its last 2000 bytes: the end of the output holds the reason.
func TestDescribeExitKeepsTheEndOfTheStderr(t *testing.T) {
	t.Parallel()
	const exited = "agent process exited unexpectedly"
	assert.Equal(t, exited, describeWith(t, ""))
	assert.Equal(t, exited, describeWith(t, " \n "), "blank output adds nothing")
	assert.Equal(t, exited+": boom", describeWith(t, "\n boom \n"))
	exact := strings.Repeat("a", 2000)
	assert.Equal(t, exited+": "+exact, describeWith(t, exact), "output of the limit stays whole")
	long := "HEAD" + strings.Repeat("b", 1999) + "END"
	assert.Equal(t, exited+": ..."+long[len(long)-2000:], describeWith(t, long))
	assert.NotContains(t, describeWith(t, long), "HEAD", "the start of a long output goes")
}
