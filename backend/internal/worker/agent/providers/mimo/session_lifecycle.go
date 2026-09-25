package mimo

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// openSession creates a session, or resumes the one resumeID names. A resume
// that fails is fatal, as it is for every provider: the alternative starts an
// empty session under the name of the one the user asked to continue.
func (a *Agent) openSession(ctx context.Context, resumeID string) (mimoSession, error) {
	if resumeID == "" {
		return a.rpc.createSession(ctx)
	}
	session, err := a.rpc.getSession(ctx, resumeID)
	if err != nil {
		if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			return mimoSession{}, providerkit.ResumeFailedError(resumeID, fmt.Errorf("MiMo holds no such session"))
		}
		return mimoSession{}, providerkit.ResumeFailedError(resumeID, err)
	}
	return session, nil
}

// restoreResumedSession reads what the worker keeps about a resumed session
// that the new process does not report again: the subagents the session's own
// spawn calls started, and the session's cost so far.
//
// Both are best effort. The session is open either way. A history that cannot
// be read leaves each earlier subagent without its link, so a later row of one
// opens a second tab under a new key.
func (a *Agent) restoreResumedSession(ctx context.Context, sessionID string) {
	messages, err := a.rpc.messages(ctx, sessionID)
	if err != nil {
		slog.Warn("mimo read resumed history", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.restoreActorLinks(messages)
	a.Mu.Lock()
	for _, message := range messages {
		if message.Info.Role == roleAssistant && message.Info.Cost > 0 {
			if a.usage.costs == nil {
				a.usage.costs = map[string]float64{}
			}
			a.usage.costs[message.Info.ID] = message.Info.Cost
		}
	}
	a.Mu.Unlock()
}

// ClearContext starts a fresh session on the same server.
//
// These things of the old session end with it, because its events name a
// session that this agent no longer reads, and nothing else would end them:
//
//   - Its running turn is aborted.
//   - What each of its actors streamed and never finished is persisted as
//     interrupted.
//   - A failure that no turn end persisted is persisted as a notification.
//   - Its pending requests are refused, and their cards are withdrawn.
//   - Its subagents' rows and its workflow runs' rows are closed.
//
// The goal goes with it too, because MiMo holds a goal per session.
func (a *Agent) ClearContext() (string, error) {
	a.Mu.Lock()
	stopped, oldID, active := a.StoppedLocked(), a.sessionID, a.turnActive
	a.Mu.Unlock()
	if stopped {
		return "", fmt.Errorf("agent is stopped")
	}
	ctx := a.Context()
	session, err := a.rpc.createSession(ctx)
	if err != nil {
		return "", fmt.Errorf("create a MiMo session: %w", err)
	}
	if oldID != "" && active {
		if err := a.rpc.abort(ctx, oldID); err != nil {
			slog.Warn("mimo abort the cleared session", "agent_id", a.AgentID(), "session_id", oldID, "error", err)
		}
	}

	a.dispatchMu.Lock()
	a.flushUnfinishedOutput(agent.MessageCompletionInterrupted)
	a.retireAllControls()
	a.closeSessionActors(bgtask.StatusStopped)
	a.closeSessionWorkflows(bgtask.StatusStopped)
	a.Mu.Lock()
	held := a.takeUnreportedFailureLocked()
	a.sessionID = session.ID
	a.turnActive = false
	a.interruptRequested = false
	a.lastTurnFailed = false
	a.TurnToolUses = 0
	clear(a.messages)
	clear(a.parts)
	clear(a.tools)
	clear(a.compactions)
	clear(a.buffers)
	a.usage = mimoUsage{}
	a.goal = mimoGoalState{}
	a.Mu.Unlock()
	if held != nil {
		// No turn end of the old session can persist this failure now, because
		// the old session's events no longer reach the agent.
		a.persistFailureRow(held)
	}
	a.dispatchMu.Unlock()

	a.spawnPrompts.Clear()
	a.ResetCumulativeOutput()
	a.sink.ReportProgress(agent.ResetProgress())
	a.sink.ResetSpans()
	a.sink.ClearGoal(false)
	a.PublishTurnActive()
	a.sink.UpdateSessionID(session.ID)
	return session.ID, nil
}

// closeSessionWorkflows ends the rows of the workflow runs that still run, with
// status: at a session this agent leaves, and at the end of the process.
func (a *Agent) closeSessionWorkflows(status bgtask.Status) {
	a.Mu.Lock()
	var rows []string
	for _, workflow := range a.workflows {
		if !workflow.finished {
			rows = append(rows, workflow.rowKey())
		}
	}
	clear(a.workflows)
	a.Mu.Unlock()
	for _, rowKey := range rows {
		providerkit.LogRegistryRefusal("mimo", "close", a.sink.CloseBackgroundTask(rowKey, status))
	}
}

// mimoCompactionStartWait limits how long CompactContext waits for the
// compaction to start. The compaction itself runs for as long as the model
// takes, so the call returns at its start, as Codex's does.
const mimoCompactionStartWait = 30 * time.Second

// CompactContext asks MiMo to compact the session. It returns once the
// compaction started: the route answers only after the compaction and the turn
// after it finish, so the request runs on its own goroutine, and the first
// compaction part on the stream is the confirmation.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	model, ok := splitModelID(a.model)
	if !ok {
		a.Mu.Unlock()
		return fmt.Errorf("MiMo compacts with a model, and the session has none")
	}
	if a.compactionAck != nil {
		a.Mu.Unlock()
		return fmt.Errorf("a context compaction is already pending")
	}
	ack := make(chan struct{})
	a.compactionAck = ack
	a.Mu.Unlock()
	release := func() {
		a.Mu.Lock()
		if a.compactionAck == ack {
			a.compactionAck = nil
		}
		a.Mu.Unlock()
	}
	if sessionID == "" {
		release()
		return fmt.Errorf("agent has no MiMo session")
	}

	result := make(chan error, 1)
	go func() {
		err := a.rpc.summarize(a.Context(), sessionID, model)
		if err != nil {
			slog.Warn("mimo compaction failed", "agent_id", a.AgentID(), "error", err)
		}
		result <- err
	}()
	timer := a.clock.NewTimer(mimoCompactionStartWait, mimoCompactionStartTimerTag)
	defer timer.Stop(mimoCompactionStartTimerTag)
	select {
	case <-ack:
		return nil
	case err := <-result:
		release()
		if err != nil {
			return classifyDeliveryError("compaction", err)
		}
		return nil
	case <-timer.C:
		release()
		return fmt.Errorf("MiMo did not start the compaction within %s", mimoCompactionStartWait)
	case <-a.ProcessDone():
		release()
		return a.ProcessExitError()
	}
}
