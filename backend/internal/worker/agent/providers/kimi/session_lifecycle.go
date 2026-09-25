package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code's session lifecycle.
//
// The server holds any number of sessions; the agent drives one. A new session
// is POST /sessions, and it has NO model until the profile binds one: the server
// never applies the configured default to a session it creates, and the first
// prompt of an unbound session fails with `model.not_configured`. So every new
// session gets its whole profile before it is used.
//
// A stored session is resumed by reading it: the server loads a session on any
// REST read, and only a loaded session is subscribable.

// kimiSessionReply is the part of a session object the worker reads.
type kimiSessionReply struct {
	ID string `json:"id"`
}

// createSession creates a session in the working directory, binds its profile
// and subscribes to it. It returns the session id.
func (a *Agent) createSession(ctx context.Context, settings kimiSettings) (string, error) {
	var created kimiSessionReply
	body := map[string]any{"metadata": map[string]any{"cwd": a.workingDir}}
	if err := a.api.post(ctx, kimiRouteSessions, body, &created); err != nil {
		return "", fmt.Errorf("create a Kimi Code session: %w", err)
	}
	if err := kimiCheckID("session", created.ID); err != nil {
		return "", err
	}
	if err := a.postProfile(ctx, created.ID, a.profileConfig(settings, nil)); err != nil {
		return "", fmt.Errorf("configure the Kimi Code session: %w", err)
	}
	if _, err := a.stream.subscribe(ctx, created.ID); err != nil {
		return "", fmt.Errorf("subscribe to the Kimi Code session: %w", err)
	}
	return created.ID, nil
}

// resumeSession loads a stored session and subscribes to it. The session keeps
// its own model and history; only the axes the launch states differently are
// written to its profile.
func (a *Agent) resumeSession(ctx context.Context, sessionID string, wanted kimiSettings) (kimiStatus, error) {
	if err := kimiCheckID("session", sessionID); err != nil {
		return kimiStatus{}, err
	}
	status, err := a.readStatus(ctx, sessionID)
	if err != nil {
		return kimiStatus{}, err
	}
	if _, err := a.stream.subscribe(ctx, sessionID); err != nil {
		return kimiStatus{}, err
	}
	stored := kimiSettings{
		model: status.Model, effort: status.ThinkingLevel, permission: status.Permission,
		planMode: status.PlanMode, swarmMode: status.SwarmMode,
	}
	if config := a.profileConfig(wanted, &stored); len(config) > 0 {
		if err := a.postProfile(ctx, sessionID, config); err != nil {
			return kimiStatus{}, fmt.Errorf("configure the resumed Kimi Code session: %w", err)
		}
		if status, err = a.readStatus(ctx, sessionID); err != nil {
			return kimiStatus{}, err
		}
	}
	return status, nil
}

// ClearContext starts a fresh session in the same server, with the same
// settings, and drops everything that belonged to the previous one.
//
// The previous session stays loaded in the server, so its running turn is
// aborted: a turn that keeps working in a session nobody watches would still
// edit files and spend tokens.
//
// The previous session's background tasks and background subagents keep
// running, on purpose. The agent started them to outlive its turn, and a
// context clear ends the conversation, not that work. The ACP providers make
// the same choice. The server does not end them either: a new session leaves
// every other session as it is. Stop ends all of them, because it ends the
// server and kills the process groups that outlive it. After the unsubscribe
// their events no longer reach LeapMux, so their registry rows stay Running
// until the process exits.
func (a *Agent) ClearContext() (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	oldSessionID, settings, busy := a.sessionID, a.settings, a.turnActive
	a.Mu.Unlock()

	ctx, cancel := context.WithTimeout(a.Context(), a.APITimeout()*3)
	defer cancel()
	attachedAt := a.clock.Now()
	sessionID, err := a.createSession(ctx, settings)
	if err != nil {
		return "", err
	}

	// Close out the previous session's output BEFORE the switch, while its rows
	// still belong to the transcript they started in.
	a.dispatchMu.Lock()
	a.Mu.Lock()
	runs := a.runs
	a.runs = nil
	a.sessionID = sessionID
	a.attachedAt = attachedAt
	a.turnActive = false
	a.turnSteerable = false
	a.lastTurnError = ""
	a.tasks = nil
	a.goal = kimiGoalState{}
	a.Mu.Unlock()
	for _, run := range runs {
		if sink := a.runSink(run); sink != nil {
			a.flushRun(run, sink, agent.MessageCompletionInterrupted)
			a.closeOpenTools(run, sink, agent.MessageCompletionInterrupted)
		}
	}
	a.children.clear()
	a.dispatchMu.Unlock()
	a.withdrawAllControls()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
	// A goal belongs to a session, and this one has none yet.
	a.sink.ClearGoal(false)

	if oldSessionID != "" {
		a.stream.unsubscribe(oldSessionID)
		if busy {
			if err := a.api.post(ctx, kimiSessionPath(oldSessionID, kimiActionAbort), nil, nil); err != nil {
				slog.Warn("kimi abort the previous session's turn", "agent_id", a.AgentID(), "error", err)
			}
		}
	}
	a.sink.UpdateSessionID(sessionID)
	return sessionID, nil
}

// CompactContext asks the server to compact the session's context. Progress
// arrives as compaction.* events, which the transcript records.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if err := kimiCheckID("session", sessionID); err != nil {
		return err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	return a.api.post(ctx, kimiSessionPath(sessionID, kimiActionCompact), nil, nil)
}

// kimiSnapshot is the part of GET /sessions/{id}/snapshot that a resync reads.
type kimiSnapshot struct {
	// InFlightTurn is the MAIN agent's running turn, or nil when it runs none.
	InFlightTurn *kimiInFlightTurn `json:"in_flight_turn"`
	// PendingApprovals and PendingQuestions are the interactions that wait for
	// an answer. See kimiInteractionEvent for their shape.
	PendingApprovals []json.RawMessage `json:"pending_approvals"`
	PendingQuestions []json.RawMessage `json:"pending_questions"`
	// Subagents is the server's roster of subagents. See kimiRosterSubagent.
	Subagents []kimiRosterSubagent `json:"subagents"`
}

// kimiInFlightTurn is the part of the snapshot's running turn that a resync
// reads.
type kimiInFlightTurn struct {
	TurnID int64 `json:"turn_id"`
}

// resyncSession restores, after a reconnect, what the event stream could not
// replay. The stream runs it once for each session the server answered for
// (kimiReconnectHandler).
//
// The server replays each durable event that a lost socket missed, but no
// volatile one, and agent.status.updated is volatile. So every reconnect reads
// the status back and reports each axis that moved while the socket was down:
// a plan mode the model entered or left, a swarm mode, a model or a thinking
// level that the server switched.
//
// A server that kept fewer events than the socket missed (resync_required), or
// that no longer had the session loaded (not_found), replays nothing. The
// snapshot then states what the lost events changed: the main agent's running
// turn, the approvals and questions that wait for an answer, and the
// subagents of the current main turn. GET .../tasks states the main agent's
// tasks. The transcript rows that the lost events carried cannot be restated.
//
// The reads and the folds hold dispatchMu. An event that the server emitted
// after a read then dispatches after the fold, so an older read never
// overwrites a newer event, and an event that a read already reflects repeats
// a state the fold set.
func (a *Agent) resyncSession(ctx context.Context, sessionID string, replayed bool) {
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if sessionID != current || kimiCheckID("session", sessionID) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, a.APITimeout())
	defer cancel()
	if !replayed {
		// A read loads a session that the server dropped, and only a loaded
		// session takes a subscribe. A subscribe of a session that the server
		// kept is harmless. The subscribe waits for an ack that the reader
		// delivers, so it runs before the dispatcher is held.
		if _, err := a.readStatus(ctx, sessionID); err != nil {
			slog.Warn("kimi resync load failed", "agent_id", a.AgentID(), "error", err)
			return
		}
		if _, err := a.stream.subscribe(ctx, sessionID); err != nil {
			slog.Warn("kimi resync subscribe failed", "agent_id", a.AgentID(), "error", err)
		}
	}

	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	status, err := a.readStatus(ctx, sessionID)
	if err != nil {
		slog.Warn("kimi resync status read failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.foldStatus(status)
	if replayed {
		return
	}
	var snapshot kimiSnapshot
	if err := a.api.get(ctx, kimiSessionPath(sessionID, "/snapshot"), &snapshot); err != nil {
		slog.Warn("kimi resync snapshot read failed", "agent_id", a.AgentID(), "error", err)
		if !status.Busy {
			// An idle session runs no main turn, so a turn the stream saw start and
			// never saw end must not latch the agent busy.
			a.reconcileTurn(nil)
		}
		return
	}
	a.reconcileTurn(snapshot.InFlightTurn)
	a.reconcileControls(sessionID, snapshot.PendingApprovals, snapshot.PendingQuestions)
	tasks, err := a.readTasks(ctx, sessionID)
	if err != nil {
		// The roster still repairs the subagents it lists. Nothing proves that an
		// unlisted subagent ended, so none is closed for that.
		slog.Warn("kimi resync task read failed", "agent_id", a.AgentID(), "error", err)
	}
	a.reconcileSubagents(snapshot.Subagents, tasks, err == nil)
	a.reconcileTasks(tasks)
}

// reconcileTurn sets the main turn flag from the snapshot's running turn. The
// status's `busy` cannot state it: `busy` is true while any agent of the
// session runs, or a background task does.
//
// A turn that the stream saw start and that is no longer the one that runs
// ended during the gap. What it left open ends as interrupted, because its
// results were lost with the gap. The caller holds dispatchMu.
func (a *Agent) reconcileTurn(inFlight *kimiInFlightTurn) {
	a.Mu.Lock()
	main := a.runLocked(kimiMainAgentID)
	wasActive := a.turnActive
	if inFlight != nil && wasActive && main.turnID == inFlight.TurnID {
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()

	if wasActive {
		a.flushRun(main, a.sink, agent.MessageCompletionInterrupted)
		a.closeOpenTools(main, a.sink, agent.MessageCompletionInterrupted)
		a.sink.ResetSpans()
	}
	a.Mu.Lock()
	a.turnActive = inFlight != nil
	// The snapshot does not state who started the turn. A steer into a turn
	// that no tracked prompt started waits in the server's queue, so the turn
	// takes none, and the input queue holds a message until the turn ends.
	a.turnSteerable = false
	main.turnActive = inFlight != nil
	if inFlight != nil {
		main.turnID = inFlight.TurnID
		a.TurnToolUses = 0
	}
	a.Mu.Unlock()
	if wasActive || inFlight != nil {
		a.PublishTurnActive()
	}
}
