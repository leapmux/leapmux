package pi

import (
	"bytes"
	"encoding/json"
	"errors"
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
	// Set and Resume can start a model turn before their command replies arrive.
	// An early empty snapshot must keep reading while either reply remains open.
	// The session key prevents an old command from refreshing a replacement session.
	pendingActivations map[string]int
	// Only the refresh goroutine reads or changes the native session index and goal-file cache.
	reader piGoalSessionReader
	files  piGoalFileReader
	// These values control the retry delay and maximum retry count.
	// Zero selects the production default.
	// Tests can supply smaller values.
	retryDelay time.Duration
	retryLimit int
}

const (
	// Pi can write the transcript or focused goal after the first read.
	// These defaults allow about five seconds for either source.
	piGoalRetryDelay = 250 * time.Millisecond
	piGoalRetryLimit = 20
	// Native get_entries has no page limit. Refuse large in-memory histories before requesting it.
	// The existing stdout scanner still enforces the response byte limit.
	piGoalMemoryMessageLimit = 32
)

// retryPolicy states how long a refresh waits for a pending transcript or goal.
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
		return agent.GoalOutcome{}, errors.New("the Pi agent is stopped")
	}
	if command == "" || !available {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	message := "/" + command
	if action == agent.GoalActionSet {
		if strings.TrimSpace(objective) == "" {
			return agent.GoalOutcome{}, errors.New("the Pi goal needs an objective")
		}
		message += " " + objective
	}
	activation := action == agent.GoalActionSet || action == agent.GoalActionResume
	activationSession := ""
	if activation {
		a.Mu.Lock()
		activationSession = a.sessionID
		if a.goal.pendingActivations == nil {
			a.goal.pendingActivations = make(map[string]int)
		}
		a.goal.pendingActivations[activationSession]++
		a.Mu.Unlock()
	}
	// Clear waits for a confirmation dialog without a deadline.
	// The stdin write confirms delivery.
	// Wait for the reply on a separate goroutine.
	// The output reader must remain free to route the answer.
	// The caller must not hold an RPC open while the user decides.
	if err := a.sendPiCommandDetached(CommandPrompt, map[string]any{"message": message}, func(err error) {
		if activation {
			a.finishPiGoalActivation(activationSession)
		}
		if err != nil && !a.IsStopped() {
			slog.Error("pi goal command failed", "agent_id", a.AgentID(), "command", command, "error", err)
			a.sink.PersistLeapMuxNotification(map[string]any{
				contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
				contracts.NotificationFieldError: err.Error(),
			})
		}
		// Pi applies the command before replying.
		// The reply confirms that the next read can observe the new state.
		a.schedulePiGoalRefresh(false)
	}); err != nil {
		if activation {
			a.finishPiGoalActivation(activationSession)
		}
		return agent.GoalOutcome{}, err
	}
	a.schedulePiGoalRefresh(false)
	return agent.GoalOutcome{}, nil
}

func (a *Agent) finishPiGoalActivation(sessionID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if remaining := a.goal.pendingActivations[sessionID]; remaining > 1 {
		a.goal.pendingActivations[sessionID] = remaining - 1
	} else {
		delete(a.goal.pendingActivations, sessionID)
	}
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
	// Retain a pending snapshot intent until publication.
	// A snapshot suppresses the transcript notification.
	// Losing the intent would announce Goal set for a goal from an earlier process.
	// A concurrent real change also uses the pending snapshot intent.
	// It updates the panel without adding a note.
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
		// Check shutdown before every pass.
		// A stop during the retry wait must prevent another command.
		if a.goal.stopping || a.StoppedLocked() || a.Context().Err() != nil {
			a.goal.running = false
			a.Mu.Unlock()
			return
		}
		sessionID, path, directory := a.sessionID, a.sessionFile, a.workingDir
		revision, snapshot := a.goal.revision, a.goal.snapshot
		pendingActivation := a.goal.pendingActivations[sessionID] > 0
		delay, limit := a.goal.retryPolicy()
		a.goal.pending = false
		a.Mu.Unlock()
		record, err := a.readPiGoalSnapshot(path, directory, sessionID)
		published, retry := false, false
		switch {
		case err == nil && record == nil && pendingActivation && waits < limit:
			// The command can write the focused goal before its reply.
			// Keep the refresh intent until the goal appears or the retry limit ends.
			waits++
			retry = true
		case err == nil:
			published = a.publishPiGoal(record, snapshot, &revision)
			waits = 0
		case errors.Is(err, os.ErrNotExist) && waits < limit:
			// Pi did not write the session file yet.
			// No new hint follows a recovery pass.
			// Keep the intent and read again so the panel can leave the previous state.
			waits++
			retry = true
		case a.Context().Err() == nil && !a.IsStopped():
			slog.Warn("recover Pi goal state", "agent_id", a.AgentID(), "error", err)
		}
		a.Mu.Lock()
		// Clear the snapshot intent only after publication.
		// A failed read or stale revision keeps the intent for the next pass.
		// An unchanged revision proves that no hint arrived during the read.
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
		// Wait outside the lock.
		// A concurrent hint must reach the scheduling fields.
		// The next pass must observe a stop that occurs during the wait.
		if retry {
			a.waitForPiGoalSource(delay)
		}
	}
}

// waitForPiGoalSource waits between reads until the transcript or focused goal appears.
// A canceled context ends the wait early.
func (a *Agent) waitForPiGoalSource(delay time.Duration) {
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
		// Custom goal messages can start a turn before Pi writes its first session file.
		if memoryErr := a.validatePiGoalMemorySource(path, sessionID); memoryErr != nil {
			return nil, errors.Join(err, memoryErr)
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
		return nil, errors.New("the Pi session response has no branch data")
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
		return nil, errors.New("the Pi session branch is incomplete")
	}
	if goalID == "" {
		return nil, nil
	}
	record, err := a.goal.files.read(a.Context(), directory, goalID)
	if err == nil && record == nil {
		err = errors.New("the focused Pi goal file is unavailable")
	}
	return record, err
}

func (a *Agent) validatePiGoalMemorySource(path, sessionID string) error {
	if err := a.Context().Err(); err != nil {
		return err
	}
	raw, err := a.sendPiCommand(CommandGetState, nil, a.APITimeout())
	if err != nil {
		return err
	}
	var state struct {
		SessionID    string `json:"sessionId"`
		MessageCount *int   `json:"messageCount"`
	}
	if json.Unmarshal(raw, &state) != nil || state.SessionID != sessionID || state.MessageCount == nil ||
		*state.MessageCount < 0 || *state.MessageCount > piGoalMemoryMessageLimit {
		return errors.New("the Pi in-memory session identity or message count is unsafe")
	}
	if *state.MessageCount == 0 {
		return nil
	}
	if err := a.Context().Err(); err != nil {
		return err
	}
	raw, err = a.sendPiCommand(CommandGetSessionStats, nil, a.APITimeout())
	if err != nil {
		return err
	}
	var stats struct {
		SessionID         string `json:"sessionId"`
		SessionFile       string `json:"sessionFile"`
		UserMessages      *int   `json:"userMessages"`
		AssistantMessages *int   `json:"assistantMessages"`
		ToolResults       *int   `json:"toolResults"`
		TotalMessages     *int   `json:"totalMessages"`
	}
	if json.Unmarshal(raw, &stats) != nil || stats.SessionID != sessionID || stats.SessionFile != path ||
		stats.UserMessages == nil || *stats.UserMessages != 0 || stats.AssistantMessages == nil || *stats.AssistantMessages != 0 ||
		stats.ToolResults == nil || *stats.ToolResults != 0 || stats.TotalMessages == nil ||
		*stats.TotalMessages < 0 || *stats.TotalMessages > piGoalMemoryMessageLimit {
		return errors.New("the Pi in-memory session contains ordinary history or invalid statistics")
	}
	return nil
}

func (a *Agent) hasPiGoalCommandsLocked() bool {
	for _, command := range piGoalCommands {
		if a.extensionCommands[command] {
			return true
		}
	}
	return false
}
