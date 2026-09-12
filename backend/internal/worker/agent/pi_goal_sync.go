package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

type piGoalSync struct {
	publishMu sync.Mutex
	// PiAgent.mu protects the scheduling fields.
	revision uint64
	running  bool
	pending  bool
	snapshot bool
	stopping bool
	// Only the refresh goroutine reads or changes the native session index.
	reader piGoalSessionReader
}

var piGoalCommands = map[GoalAction]string{
	GoalActionSet: "goal-direct", GoalActionClear: "goal-clear",
	GoalActionPause: "goal-pause", GoalActionResume: "goal-resume",
}

func (a *PiAgent) SupportedGoalActions() []GoalAction {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.goal.stopping {
		return nil
	}
	var actions []GoalAction
	for _, action := range []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume} {
		if a.extensionCommands[piGoalCommands[action]] {
			actions = append(actions, action)
		}
	}
	return actions
}

func (a *PiAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	a.mu.Lock()
	command := piGoalCommands[action]
	available := a.extensionCommands[command]
	stopping := a.stopped || a.goal.stopping
	a.mu.Unlock()
	if stopping {
		return GoalOutcome{}, fmt.Errorf("the Pi agent is stopped")
	}
	if command == "" || !available {
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
	message := "/" + command
	if action == GoalActionSet {
		if strings.TrimSpace(objective) == "" {
			return GoalOutcome{}, fmt.Errorf("the Pi goal needs an objective")
		}
		message += " " + objective
	}
	// A clear can await Pi's confirmation dialog. The read loop must remain free to route its answer.
	if _, err := a.sendPiCommand(PiCommandPrompt, map[string]any{"message": message}, 0); err != nil {
		return GoalOutcome{}, err
	}
	a.schedulePiGoalRefresh(false)
	return GoalOutcome{}, nil
}

func (a *PiAgent) stopPiGoalRefresh() {
	a.goal.publishMu.Lock()
	defer a.goal.publishMu.Unlock()
	a.mu.Lock()
	a.goal.stopping = true
	a.goal.revision++
	a.mu.Unlock()
}

// schedulePiGoalRefresh coalesces repeated UI hints. It never waits for a reply on the output goroutine.
func (a *PiAgent) schedulePiGoalRefresh(snapshot bool) {
	a.mu.Lock()
	if a.ctx == nil || a.sessionID == "" || a.sessionFile == "" || a.stopped || a.goal.stopping || !a.hasPiGoalCommandsLocked() {
		a.mu.Unlock()
		return
	}
	a.goal.revision++
	if !a.goal.pending {
		a.goal.snapshot = snapshot
	} else {
		a.goal.snapshot = a.goal.snapshot && snapshot
	}
	a.goal.pending = true
	if a.goal.running {
		a.mu.Unlock()
		return
	}
	a.goal.running = true
	a.mu.Unlock()
	go a.runPiGoalRefresh()
}

func (a *PiAgent) runPiGoalRefresh() {
	for {
		a.mu.Lock()
		sessionID, path, directory := a.sessionID, a.sessionFile, a.workingDir
		revision, snapshot := a.goal.revision, a.goal.snapshot
		a.goal.pending = false
		a.mu.Unlock()
		record, err := a.readPiGoalSnapshot(path, directory, sessionID)
		if err == nil {
			a.publishPiGoal(record, snapshot, &revision)
		} else if a.ctx.Err() == nil && !a.IsStopped() {
			slog.Warn("recover Pi goal state", "agent_id", a.agentID, "error", err)
		}
		a.mu.Lock()
		if a.goal.stopping || a.stopped || a.ctx.Err() != nil || !a.goal.pending {
			a.goal.running = false
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
	}
}

func (a *PiAgent) readPiGoalSnapshot(path, directory, sessionID string) (*piGoalRecord, error) {
	session, err := a.goal.reader.read(a.ctx, path, directory, sessionID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Pi delays the first file write until an assistant message exists.
		// A nonempty missing transcript cannot safely use a full get_entries response.
		raw, stateErr := a.sendPiCommand(PiCommandGetState, nil, a.APITimeout())
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
	raw, err := a.sendPiCommand(PiCommandGetEntries, params, a.APITimeout())
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
	record, err := readPiGoalFile(a.ctx, directory, goalID)
	if err == nil && record == nil {
		err = fmt.Errorf("the focused Pi goal file is unavailable")
	}
	return record, err
}

func (a *PiAgent) hasPiGoalCommandsLocked() bool {
	for _, command := range piGoalCommands {
		if a.extensionCommands[command] {
			return true
		}
	}
	return false
}
