package mimo

import (
	"fmt"
	"log/slog"
	"syscall"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

// mimoAbortGrace is how long an abort has to end its turn before the worker
// asks the server whether the session is still busy. MiMo ends an aborted turn
// with an idle status at once; the check is for the idle that never arrives.
const mimoAbortGrace = 5 * time.Second

// mimoStreamStopWait limits how long Stop waits for the stream goroutine,
// which ends as soon as its connection closes.
const mimoStreamStopWait = 5 * time.Second

// Interrupt aborts the main agent's running turn. It is a no-op with no turn.
//
// The server answers the abort at once and ends the turn on the event stream:
// an aborted-message error, then idle. A timer then compares the turn with the
// server's own status, so a turn whose idle never arrives still ends.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	stopped, active, sessionID := a.StoppedLocked(), a.turnActive, a.sessionID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !active || sessionID == "" {
		return nil
	}
	if err := a.rpc.abort(a.Context(), sessionID); err != nil {
		// The abort stopped nothing, so the turn still runs.
		return fmt.Errorf("abort the MiMo turn: %w", err)
	}
	a.Mu.Lock()
	if a.turnActive {
		a.interruptRequested = true
	}
	a.Mu.Unlock()
	a.clock.AfterFunc(mimoAbortGrace, a.reconcileTurn, mimoAbortGraceTimerTag)
	return nil
}

// Stop ends the server and every process below it, and then finishes what the
// turn left unfinished, as interrupted.
//
// `mimo` is a Node script that runs the real binary as a child and forwards no
// signal to it, so a signal to the script alone leaves the server running as
// an orphan. The process group holds both, and SIGTERM to the group ends the
// server at once: it closes on SIGTERM, and it does not end when its stdin
// closes. Process.Stop then closes stdin and waits for the exit, and its
// fallback kills whatever the group still holds.
//
// The stream is cancelled BEFORE the signal, so it does not connect again to a
// server that is going away. Thus no idle event can end the turn, and
// finishOutput ends it instead.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	if err := procutil.SignalProcessGroup(a.Cmd(), syscall.SIGTERM); err != nil {
		slog.Debug("mimo signal the server group", "agent_id", a.AgentID(), "error", err)
	}
	a.Process.Stop()
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	a.awaitEventStream()
	a.finishOutput(agent.MessageCompletionInterrupted)
}

// Wait blocks until the process exits, and then finishes what the turn left
// unfinished. An exit that nothing asked for marks that output as failed.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.awaitEventStream()
	a.finishOutput(a.ProcessExitCompletion())
	return err
}

// awaitEventStream waits for the stream goroutine to return, so that no event
// reaches a handler after finishOutput. The goroutine returns as soon as its
// connection closes, and the wait is limited all the same. An agent whose start
// failed before the stream opened has no goroutine to wait for.
func (a *Agent) awaitEventStream() {
	if a.streamCancel == nil {
		return
	}
	timer := a.clock.NewTimer(mimoStreamStopWait, mimoStreamStopTimerTag)
	defer timer.Stop(mimoStreamStopTimerTag)
	select {
	case <-a.streamDone:
	case <-timer.C:
		slog.Warn("mimo event stream did not end", "agent_id", a.AgentID())
	}
}

// finishOutput persists what the process left unfinished when it ended, with
// completion: the text that each actor streamed, the tool calls that each actor
// never finished, and a failure that no turn end reported. It closes the rows
// of the subagents and the workflow runs, and then clears the turn and the live
// progress.
//
// It holds dispatchMu, so an event that the stream goroutine still dispatches
// cannot come between two of its steps. Stop and Wait both call it, and the
// second call finds nothing left to finish.
func (a *Agent) finishOutput(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.flushUnfinishedOutput(completion)
	status := actorStatusForCompletion(completion)
	a.closeSessionActors(status)
	a.closeSessionWorkflows(status)

	a.Mu.Lock()
	held := a.takeUnreportedFailureLocked()
	a.turnActive = false
	a.interruptRequested = false
	a.Mu.Unlock()
	if held != nil {
		// No turn end persists this failure now, and it can be the reason why the
		// process ended.
		a.persistFailureRow(held)
	}
	a.sink.ReportProgress(agent.ResetProgress())
	a.PublishTurnActive()
}
