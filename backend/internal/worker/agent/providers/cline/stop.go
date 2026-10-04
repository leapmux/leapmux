package cline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// abortWait limits the abort that a stop sends before it shuts the daemon
// down. The abort lets Cline end the run and store its conversation.
const abortWait = 2 * time.Second

// Interrupt aborts the running turn. Cline cancels the turn's pending approvals
// and questions, stops the run, and ends it with run.aborted, which ends the
// turn as an interruption. It does nothing when no turn runs.
//
// Three turns have no run to abort yet:
//   - A turn whose message left and whose run did not start. Cline finds no
//     run then, and still answers that it applied the abort (run.abort of
//     Cline 3.0.64), so the worker aborts the run when it starts
//     (markRunStarted).
//   - A turn that has no run (turnState.hasNoRun). No run end will end it, so
//     the interrupt ends it at once. An abort goes first, for a run that the
//     worker did not see start, and its failure does not keep the turn.
//   - A settling turn (turnState.settling), which holds the input queue while
//     a mode change applies. The mode change ends it, and the interrupt stops
//     the plan continuation that would follow it (applyModeChange). No abort
//     goes out: the rebuild detaches the session that it would reach.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return errAgentStopped
	}
	turn, sessionID := a.turn, a.sessionID
	waitsForRun := turn.active && turn.steerable && !turn.runStarted
	switch {
	case waitsForRun:
		a.turn.abortOnStart = true
	case turn.active:
		a.turn.interruptRequested = true
	}
	a.Mu.Unlock()
	if !turn.active || sessionID == "" || waitsForRun || turn.settling {
		return nil
	}
	err := a.abortRun(sessionID)
	if turn.hasNoRun() {
		a.dispatchMu.Lock()
		a.endRunlessTurnLocked(agent.MessageCompletionInterrupted, runReasonAborted)
		a.dispatchMu.Unlock()
		return nil
	}
	return err
}

// abortRun sends run.abort for the session's run. A failed abort stopped
// nothing, so the turn still runs, and its end is not an interruption.
func (a *Agent) abortRun(sessionID string) error {
	ctx, cancel := a.requestContext()
	defer cancel()
	if _, err := a.hub.command(ctx, commandRunAbort, sessionID, map[string]any{"sessionId": sessionID, "reason": "The user interrupted the turn."}); err != nil {
		slog.Warn("cline abort the turn", "agent_id", a.AgentID(), "error", err)
		a.Mu.Lock()
		a.turn.interruptRequested = false
		a.Mu.Unlock()
		return fmt.Errorf("abort the Cline turn: %w", err)
	}
	return nil
}

// endRunlessTurnLocked ends the running turn when it still has no run
// (turnState.hasNoRun), with a turn-end row that states reason. The caller
// holds dispatchMu.
func (a *Agent) endRunlessTurnLocked(completion agent.MessageCompletion, reason string) {
	a.Mu.Lock()
	noRun, sessionID := a.turn.hasNoRun(), a.sessionID
	a.Mu.Unlock()
	if noRun {
		a.endTurn(completion, runEndRow(sessionID, reason, ""))
	}
}

// Stop ends the agent: it aborts the running turn, asks the daemon to shut down
// with its own token, stops the daemon's process group, and then finishes what
// the turn left unfinished, as interrupted. It returns once the daemon exited.
//
// The daemon does not end when its stdin closes or when the worker exits, so
// the shutdown request is what ends it gracefully. Process.Stop then closes
// stdin, waits, and kills the group after its grace period; awaitDaemonExit
// covers a daemon that left the group.
func (a *Agent) Stop() {
	a.stopOnce.Do(a.stop)
	<-a.stopped
}

func (a *Agent) stop() {
	defer close(a.stopped)
	a.NoteIntentionalStop()
	a.Mu.Lock()
	// A settling turn has no run to abort (turnState.settling).
	active, sessionID := a.turn.active && !a.turn.settling, a.sessionID
	if active {
		a.turn.interruptRequested = true
	}
	a.Mu.Unlock()
	if active && sessionID != "" {
		ctx, cancel := context.WithTimeout(a.ctx, abortWait)
		if _, err := a.hub.command(ctx, commandRunAbort, sessionID, map[string]any{"sessionId": sessionID, "reason": "LeapMux stopped the agent."}); err != nil {
			slog.Debug("cline abort before the stop", "agent_id", a.AgentID(), "error", err)
		}
		cancel()
	}
	daemon := stopDaemonAt(a.record)
	a.cancel()
	a.hub.close()
	a.Process.Stop()
	if err := awaitDaemonExit(context.Background(), daemon, a.clock); err != nil {
		slog.Warn("cline stop the hub", "agent_id", a.AgentID(), "error", err)
	}
	a.hub.wait()
	a.background.Wait()
	a.teardown(agent.MessageCompletionInterrupted, "")
}

// Wait blocks until the daemon exits, and then tears the agent down. An exit
// that nothing asked for ends the turn as an error that states why the daemon
// ended. The manager calls Wait alone after such an exit, so Wait releases the
// session and the directory as Stop does.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.cancel()
	a.hub.close()
	a.hub.wait()
	a.background.Wait()
	completion := a.ProcessExitCompletion()
	message := ""
	if completion == agent.MessageCompletionError {
		message = a.describeExit()
	}
	a.teardown(completion, message)
	return err
}

// teardown ends what the daemon's end left, once, whether Stop or Wait comes
// first: the turn with its output and its requests, the subagents and the
// teammate runs, the session claims, and the agent's directory. A second run
// would end the turn twice, or release a session that another agent claimed
// since.
func (a *Agent) teardown(completion agent.MessageCompletion, message string) {
	a.teardownOnce.Do(func() {
		a.finishOutput(completion, message)
		a.releaseClaims()
		a.removeDir()
	})
}

// describeExit states why the daemon ended: the exit status, and the end of
// what it wrote to stderr.
func (a *Agent) describeExit() string {
	message := a.ProcessExitError().Error()
	if stderr := strings.TrimSpace(a.Stderr()); stderr != "" {
		const limit = 2000
		if len(stderr) > limit {
			stderr = "..." + stderr[len(stderr)-limit:]
		}
		message = message + ": " + stderr
	}
	return message
}

// finishOutput ends what the process left unfinished when it ended: the turn,
// with its streamed text, its open tool calls and its requests; the subagents
// and the teammate runs that still ran. The teardown calls it.
func (a *Agent) finishOutput(completion agent.MessageCompletion, message string) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	// A settling turn holds the input queue for a mode change, and no run of
	// Cline's belongs to it, so its end is no turn end of the user's.
	active, sessionID := a.turn.active && !a.turn.settling, a.sessionID
	if a.turn.settling {
		a.turn = turnState{}
	}
	a.modeRebuild = nil
	a.Mu.Unlock()
	if active {
		reason := runReasonError
		if completion == agent.MessageCompletionInterrupted {
			reason = runReasonAborted
		}
		a.endTurn(completion, runEndRow(sessionID, reason, message))
	} else {
		a.withdrawAllControls()
		a.settleSpawns(completion)
	}
	status := bgtask.StatusStopped
	if completion == agent.MessageCompletionError {
		status = bgtask.StatusInterrupted
	}
	a.closeTeamRuns(status)
	a.sink.ReportProgress(agent.ResetProgress())
	a.PublishTurnActive()
}

// removeDir removes the agent's private directory: the discovery record, the
// private task database and the attached files. It releases the directory's
// lock too. The removal runs once; a later call only logs its failure again.
func (a *Agent) removeDir() {
	if err := a.dir.Close(); err != nil {
		slog.Warn("cline remove the agent directory", "agent_id", a.AgentID(), "error", err)
	}
}
