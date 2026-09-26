package junie

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Junie's session goal over ACP.
//
// The jar's `_session/goal` extension is a request/response method. Its
// GoalRequest carries {sessionId, action}, and the handler in
// `JunieAgentSupport.goalControl` accepts ONE action: `clear`. Every other
// action word is rejected with GOAL_ACTION_NOT_SUPPORTED, so this writer
// supports Clear alone and reports the rest unsupported. A goal is SET through
// Junie's own goal-setup elicitation, not through this method.
//
// The goal state travels the other way as a `session_info_update` whose
// `_meta.goal` holds the snapshot (see GoalSnapshot in the jar):
//
//	_meta.goal = {objective, status, createdAt, updatedAt, timeUsedSeconds,
//	              controlMethod}
//
// A cleared goal is `_meta.goal: null`. The status words are `active`,
// `limited` and `complete` (GoalExtensionKt's GOAL_STATUS_*).

// The `_session/goal` request method and its one action word. From the jar's
// GoalExtensionKt: GOAL_METHOD_NAME and GOAL_ACTION_CLEAR.
const (
	junieGoalMethod      = "_session/goal"
	junieGoalActionClear = "clear"
)

// Junie's goal status words. From the jar's GoalExtensionKt.
const (
	junieGoalStatusActive   = "active"
	junieGoalStatusLimited  = "limited"
	junieGoalStatusComplete = "complete"
)

// junieGoalRequest is the `_session/goal` request body (the jar's GoalRequest).
type junieGoalRequest struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`
}

// junieGoalSnapshot is the goal state `_meta.goal` carries (the jar's
// GoalSnapshot). The timestamps are epoch milliseconds.
type junieGoalSnapshot struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds"`
	ControlMethod   string `json:"controlMethod"`
}

// junieGoalFingerprint is what makes two goal reports the same goal in the same
// state. A repeat of the same fingerprint is a RESTATEMENT and writes no
// transcript row; a change is a transition and does.
type junieGoalFingerprint struct {
	objective string
	status    string
	createdAt int64
}

func fingerprintOf(s junieGoalSnapshot) junieGoalFingerprint {
	return junieGoalFingerprint{objective: s.Objective, status: s.Status, createdAt: s.CreatedAt}
}

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports Clear alone. The jar's goal handler rejects any
// other action with GOAL_ACTION_NOT_SUPPORTED.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionClear}
}

// PerformGoalAction runs the one action the extension has. Set, Pause and
// Resume are not on the GoalRequest, so they report unsupported rather than
// pretend through a prompt.
func (a *Agent) PerformGoalAction(action agent.GoalAction, _ string) (agent.GoalOutcome, error) {
	if action != agent.GoalActionClear {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	err := a.WithSessionID(func(sessionID string) error {
		if sessionID == "" || a.IsStopped() {
			return fmt.Errorf("the Junie session is unavailable")
		}
		params, err := json.Marshal(junieGoalRequest{SessionID: sessionID, Action: junieGoalActionClear})
		if err != nil {
			return fmt.Errorf("marshal the Junie goal request: %w", err)
		}
		_, err = a.SendRequest(junieGoalMethod, params, a.APITimeout())
		return err
	})
	return agent.GoalOutcome{}, err
}

// junieGoalStatus maps Junie's status word onto the neutral status. An
// unrecognized word maps to Blocked, so a state this build cannot read never
// offers Pause. `limited` is Junie's budget/turn limit: the same meaning as
// Codex's usageLimited, which goal.go already maps to Blocked.
func junieGoalStatus(word string) agent.GoalStatus {
	switch word {
	case junieGoalStatusActive:
		return agent.GoalStatusActive
	case junieGoalStatusLimited:
		return agent.GoalStatusBlocked
	case junieGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// handleGoalMeta is the SessionMetadataHandler for Junie's goal reports. It
// reads `_meta.goal` from a `session_info_update` and folds it into the goal
// sink. It returns true for an update that carried a goal key, so the base
// neither dispatches nor persists the update: the fold replaces the verbatim
// row that the unknown-update default would write.
func (a *Agent) handleGoalMeta(updateType string, metadata map[string]json.RawMessage, _ json.RawMessage) bool {
	if updateType != contracts.ACPUpdateSessionInfoUpdate {
		return false
	}
	raw, ok := metadata["goal"]
	if !ok {
		// A session_info_update with no goal key is the runtime's own title or
		// modified time. The base reads nothing from it; leave it alone.
		return false
	}
	a.foldGoalMeta(raw)
	return true
}

// foldGoalMeta applies one `_meta.goal` value: a snapshot object upserts the
// goal, and a null removes it.
func (a *Agent) foldGoalMeta(raw json.RawMessage) {
	if isJSONNull(raw) {
		a.clearGoalReport()
		return
	}
	var snap junieGoalSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		slog.Warn("junie: unreadable goal snapshot", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.upsertGoalReport(snap)
}

// isJSONNull reports a JSON null literal.
func isJSONNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// upsertGoalReport folds one goal snapshot into the sink.
//
// The Snapshot flag separates a TRANSITION from a RESTATEMENT. The first report
// of a goal whose createdAt predates this process restates a stored goal, and
// an identical repeat restates the current one; either writes no "Goal set"
// row. A new goal or a status change is a transition and writes one.
func (a *Agent) upsertGoalReport(snap junieGoalSnapshot) {
	a.goalMu.Lock()
	prior := a.goalLast
	startedAt := a.goalStartedAt
	fp := fingerprintOf(snap)
	// The comparison is at MILLIsecond precision because createdAt is an epoch
	// millisecond. A nanosecond-precision start time would read a goal created
	// in the same millisecond as predating the process.
	restatement := (prior != nil && *prior == fp) ||
		(prior == nil && snap.CreatedAt < startedAt.UnixMilli())
	a.goalLast = &fp
	a.goalMu.Unlock()

	timeUsed := snap.TimeUsedSeconds
	createdAt := time.UnixMilli(snap.CreatedAt)
	a.Sink().UpsertGoal(agent.GoalUpdate{
		Objective:       snap.Objective,
		Status:          junieGoalStatus(snap.Status),
		StatusDetail:    snap.Status,
		CreatedAt:       createdAt,
		TimeUsedSeconds: &timeUsed,
		Snapshot:        restatement,
	})
}

// clearGoalReport folds a `_meta.goal: null` into the sink. A clear with no
// goal before it restates the absence and writes no "Goal cleared" row; a
// clear that follows a goal announces the removal.
func (a *Agent) clearGoalReport() {
	a.goalMu.Lock()
	hadGoal := a.goalLast != nil
	a.goalLast = nil
	a.goalMu.Unlock()
	a.Sink().ClearGoal(!hadGoal)
}
