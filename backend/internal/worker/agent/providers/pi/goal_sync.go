package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type piGoalSync struct {
	publishMu sync.Mutex
	// Agent.Mu protects the scheduling fields.
	revision uint64
	running  bool
	pending  bool
	snapshot bool
	stopping bool
	// Only the refresh goroutine reads or changes the native session index and
	// the goal-file cache.
	reader piGoalSessionReader
	files  piGoalFileReader
	// How long a pass waits for an unwritten transcript, and how many waits it
	// spends before it gives up. Zero takes the default, so neither constructor
	// states them and a test shortens the wait.
	retryDelay time.Duration
	retryLimit int
}

const (
	// Pi writes the session file after its first assistant message. A recovery pass
	// that lands before that write has no later hint to rely on, so it waits for the
	// file. The product of the two is the patience: about five seconds, after which
	// a transcript that never lands stops holding the refresh goroutine open.
	piGoalRetryDelay = 250 * time.Millisecond
	piGoalRetryLimit = 20
)

// retryPolicy states how a pass waits for a transcript Pi has not written yet.
func (g *piGoalSync) retryPolicy() (time.Duration, int) {
	delay, limit := g.retryDelay, g.retryLimit
	if delay <= 0 {
		delay = piGoalRetryDelay
	}
	if limit <= 0 {
		limit = piGoalRetryLimit
	}
	return delay, limit
}

var piGoalCommands = map[agent.GoalAction]string{
	agent.GoalActionSet: "goal-direct", agent.GoalActionClear: "goal-clear",
	agent.GoalActionPause: "goal-pause", agent.GoalActionResume: "goal-resume",
}

func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.StoppedLocked() || a.goal.stopping {
		return nil
	}
	var actions []agent.GoalAction
	for _, action := range []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume} {
		if a.extensionCommands[piGoalCommands[action]] {
			actions = append(actions, action)
		}
	}
	return actions
}

func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	a.Mu.Lock()
	command := piGoalCommands[action]
	available := a.extensionCommands[command]
	stopping := a.StoppedLocked() || a.goal.stopping
	a.Mu.Unlock()
	if stopping {
		return agent.GoalOutcome{}, fmt.Errorf("the Pi agent is stopped")
	}
	if command == "" || !available {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	message := "/" + command
	if action == agent.GoalActionSet {
		if strings.TrimSpace(objective) == "" {
			return agent.GoalOutcome{}, fmt.Errorf("the Pi goal needs an objective")
		}
		message += " " + objective
	}
	// A clear can await Pi's confirmation dialog, and that dialog has no deadline of
	// its own. The stdin write is the delivery acceptance, so the response wait runs
	// on its own goroutine: the read loop stays free to route the answer, and the
	// caller does not hold an RPC open for as long as the user takes to decide.
	if err := a.sendPiCommandDetached(CommandPrompt, map[string]any{"message": message}, func(err error) {
		if err != nil && !a.IsStopped() {
			slog.Error("pi goal command failed", "agent_id", a.AgentID(), "command", command, "error", err)
			a.sink.PersistLeapMuxNotification(map[string]any{
				contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
				contracts.NotificationFieldError: err.Error(),
			})
		}
		// Pi applies the command before it answers, so the response is the first
		// moment a read sees the new state.
		a.schedulePiGoalRefresh(false)
	}); err != nil {
		return agent.GoalOutcome{}, err
	}
	a.schedulePiGoalRefresh(false)
	return agent.GoalOutcome{}, nil
}

func (a *Agent) stopPiGoalRefresh() {
	a.goal.publishMu.Lock()
	defer a.goal.publishMu.Unlock()
	a.Mu.Lock()
	a.goal.stopping = true
	a.goal.revision++
	a.Mu.Unlock()
}

// schedulePiGoalRefresh coalesces repeated UI hints. It never waits for a reply on the output goroutine.
func (a *Agent) schedulePiGoalRefresh(snapshot bool) {
	a.Mu.Lock()
	if a.Context() == nil || a.sessionID == "" || a.sessionFile == "" || a.StoppedLocked() || a.goal.stopping || !a.hasPiGoalCommandsLocked() {
		a.Mu.Unlock()
		return
	}
	a.goal.revision++
	// A snapshot intent survives every later hint until a publication delivers it.
	// Snapshot suppresses the transcript notification, so a lost flag prints
	// "Goal set" for a goal that the user set in an earlier process. The cost is the
	// opposite case: a real change that arrives while a snapshot is still pending
	// publishes as a snapshot, so it updates the goal panel and writes no note.
	a.goal.snapshot = a.goal.snapshot || snapshot
	a.goal.pending = true
	if a.goal.running {
		a.Mu.Unlock()
		return
	}
	a.goal.running = true
	a.Mu.Unlock()
	go a.runPiGoalRefresh()
}

func (a *Agent) runPiGoalRefresh() {
	waits := 0
	for {
		a.Mu.Lock()
		// Every pass starts by asking whether it should still run. The wait below
		// makes the window real: a stop that lands during it would otherwise spend
		// one more command on a session that shuts down.
		if a.goal.stopping || a.StoppedLocked() || a.Context().Err() != nil {
			a.goal.running = false
			a.Mu.Unlock()
			return
		}
		sessionID, path, directory := a.sessionID, a.sessionFile, a.workingDir
		revision, snapshot := a.goal.revision, a.goal.snapshot
		delay, limit := a.goal.retryPolicy()
		a.goal.pending = false
		a.Mu.Unlock()
		record, err := a.readPiGoalSnapshot(path, directory, sessionID)
		published, retry := false, false
		switch {
		case err == nil:
			published = a.publishPiGoal(record, snapshot, &revision)
			waits = 0
		case errors.Is(err, os.ErrNotExist) && waits < limit:
			// Pi has not written the session file yet. NO hint follows a recovery pass,
			// so the intent this pass carried is the only one the goal has: keep it and
			// read again rather than leave the panel on an earlier process's state.
			waits++
			retry = true
		case a.Context().Err() == nil && !a.IsStopped():
			slog.Warn("recover Pi goal state", "agent_id", a.AgentID(), "error", err)
		}
		a.Mu.Lock()
		// Release the snapshot intent only after a publication really carried it. A
		// failed read, or a newer hint that the revision check refused, leaves the flag
		// set, so the next pass still suppresses the transcript notification. An
		// unchanged revision proves that no hint arrived while the read ran.
		if published && revision == a.goal.revision {
			a.goal.snapshot = false
		}
		if retry {
			a.goal.pending = true
		}
		if a.goal.stopping || a.StoppedLocked() || a.Context().Err() != nil || !a.goal.pending {
			a.goal.running = false
			a.Mu.Unlock()
			return
		}
		a.Mu.Unlock()
		// The wait runs OUTSIDE the lock, so a hint that arrives during it still
		// reaches the scheduling fields, and the check at the top of the next pass
		// sees a stop that landed while it ran.
		if retry {
			a.waitForPiTranscript(delay)
		}
	}
}

// waitForPiTranscript pauses between two reads of a session file Pi has not
// written yet. A cancelled context ends the wait early.
func (a *Agent) waitForPiTranscript(delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-a.Context().Done():
	case <-timer.C:
	}
}

func (a *Agent) readPiGoalSnapshot(path, directory, sessionID string) (*piGoalRecord, error) {
	session, err := a.goal.reader.read(a.Context(), path, directory, sessionID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Pi delays the first file write until an assistant message exists.
		// A nonempty missing transcript cannot safely use a full get_entries response.
		raw, stateErr := a.sendPiCommand(CommandGetState, nil, a.APITimeout())
		if stateErr != nil {
			return nil, stateErr
		}
		var state struct {
			SessionID    string `json:"sessionId"`
			MessageCount *int   `json:"messageCount"`
		}
		if json.Unmarshal(raw, &state) != nil || state.SessionID != sessionID || state.MessageCount == nil || *state.MessageCount != 0 {
			return nil, err
		}
	}
	params := make(map[string]any)
	if session.LastID != "" {
		params["since"] = session.LastID
	}
	raw, err := a.sendPiCommand(CommandGetEntries, params, a.APITimeout())
	if err != nil {
		// A session switch or rewrite can invalidate the native cursor.
		a.goal.reader = piGoalSessionReader{}
		return nil, err
	}
	var response struct {
		Entries *[]json.RawMessage `json:"entries"`
		LeafID  json.RawMessage    `json:"leafId"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.Entries == nil || len(response.LeafID) == 0 {
		return nil, fmt.Errorf("the Pi session response has no branch data")
	}
	var leafID string
	if !bytes.Equal(bytes.TrimSpace(response.LeafID), []byte("null")) {
		if err := json.Unmarshal(response.LeafID, &leafID); err != nil {
			return nil, err
		}
	}
	updates := piGoalSession{}
	for _, entry := range *response.Entries {
		if err := updates.add(entry); err != nil {
			return nil, err
		}
	}
	goalID, known := session.focus(leafID, updates.Entries)
	if !known {
		return nil, fmt.Errorf("the Pi session branch is incomplete")
	}
	if goalID == "" {
		return nil, nil
	}
	record, err := a.goal.files.read(a.Context(), directory, goalID)
	if err == nil && record == nil {
		err = fmt.Errorf("the focused Pi goal file is unavailable")
	}
	return record, err
}

func (a *Agent) hasPiGoalCommandsLocked() bool {
	for _, command := range piGoalCommands {
		if a.extensionCommands[command] {
			return true
		}
	}
	return false
}
