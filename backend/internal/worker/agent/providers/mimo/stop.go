package mimo

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
// The server ends an aborted turn with an error event, then idle.
// These events can arrive before the HTTP reply. Exact message evidence or an
// accepted reply confirms the interruption. The timer checks the server's own
// status so a turn whose idle never arrives still ends.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	stopped, active, sessionID := a.StoppedLocked(), a.turnActive, a.sessionID
	var attempt uint64
	if !stopped && active && sessionID != "" {
		attempt = a.noteInterruptLocked()
	}
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !active || sessionID == "" {
		return nil
	}
	// Record the pending attempt before the request leaves. Native abort events
	// can confirm the interruption before this request returns.
	if err := a.rpc.abort(a.Context(), sessionID); err != nil {
		a.Mu.Lock()
		delete(a.interruptRequests, attempt)
		a.Mu.Unlock()
		return fmt.Errorf("abort the MiMo turn: %w", err)
	}
	a.Mu.Lock()
	a.confirmInterruptLocked(attempt)
	a.Mu.Unlock()
	a.clock.AfterFunc(mimoAbortGrace, a.reconcileTurn, mimoAbortGraceTimerTag)
	return nil
}

// noteInterruptLocked records a pending stop attempt against the current turn.
// The caller holds a.Mu. Attempt IDs continue across turns, so an old failure
// cannot remove a later turn's stop.
func (a *Agent) noteInterruptLocked() uint64 {
	a.interruptAttempt++
	if a.interruptRequests == nil {
		a.interruptRequests = make(map[uint64]bool)
	}
	a.interruptRequests[a.interruptAttempt] = false
	return a.interruptAttempt
}

// confirmInterruptLocked confirms only an attempt that still belongs to this turn.
// The caller holds a.Mu. An ended turn removes its attempts before a replacement starts.
func (a *Agent) confirmInterruptLocked(attempt uint64) {
	if _, current := a.interruptRequests[attempt]; current {
		a.interruptRequests[attempt] = true
	}
}

// noteNativeAbortLocked records independent evidence that the native turn stopped.
// The caller holds a.Mu. A transport failure cannot remove this native evidence.
func (a *Agent) noteNativeAbortLocked() {
	a.confirmInterruptLocked(a.noteInterruptLocked())
}

// hasConfirmedInterruptLocked derives the outcome from confirmed attempts alone.
// The caller holds a.Mu. A pending request states no completed outcome.
func (a *Agent) hasConfirmedInterruptLocked() bool {
	for _, confirmed := range a.interruptRequests {
		if confirmed {
			return true
		}
	}
	return false
}

// Stop ends the server and every process below it, and then finishes what the
// turn left unfinished, as interrupted.
//
// mimo is a Node script that starts the native binary as a child.
// It forwards no signal. A signal to that script alone leaves the server as an orphan.
// On Unix, the process group holds both processes. SIGTERM to that group ends the server.
// The native server does not exit when stdin closes.
// Process.Stop sends mimoStopSignal, closes stdin, then waits for exit.
// Its fallback kills every process that the owner still holds.
//
// Process.Stop records the process tree before the signal.
// MiMo runs each bash command in a separate session and leaves it live after server exit.
// The group signal cannot reach that command. The captured process identity permits cleanup after server exit.
// A signal before Process.Stop would end the server before its children enter that record.
// The command would then outlive this agent.
//
// Stop cancels the stream before the signal. The stream cannot reconnect to the stopped server.
// No later idle event can end the turn. finishOutput ends it instead.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	if a.streamCancel != nil {
		a.streamCancel()
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

// finishOutput persists unfinished native output with completion:
//
//   - Retained text from each actor.
//   - Unfinished tool calls from each actor.
//   - A failure that no turn end reported.
//
// It closes subagent and workflow rows, then clears the turn and live progress.
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
	a.readyUnreportedFailuresLocked()
	a.turnActive = false
	a.interruptRequests = nil
	a.awaitingAbortOutcome = false
	a.sessionSwitching = false
	a.compactionAck = nil
	a.manualCompactionID = ""
	a.manualCompactionReady = false
	a.manualFollowupSending = false
	a.manualFollowupBusy = false
	a.Mu.Unlock()
	// A rejected notification remains available to the next Stop or Wait call.
	a.flushFailureNotifications()
	a.sink.ReportProgress(agent.ResetProgress())
	// Process completion ends live spans even when a native closing row waits for persistence.
	a.sink.ResetSpans()
	a.PublishTurnActive()
}
