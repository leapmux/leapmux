package agent

import (
	"encoding/json"
	"log/slog"
)

// Reasonix's session goal.
//
// Reasonix runs a goal state machine and streams its whole session status over
// a custom notification beside the standard ACP updates:
//
//	_reasonix.io/session/status_update
//	  {schemaVersion, sequence, sessionId, state, mode, ...,
//	   goal:{status, objective?, runtime?:{turnsUsed, tokensUsed, requestsUsed,
//	                                       workDurationMs, lastReason, stopCause}}}
//
// READ ONLY. Reasonix can be told to adopt a goal, but only by switching to its
// `goal` session mode and then letting the NEXT prompt become the objective --
// and clearing means switching back to whichever mode the session was in
// before. LeapMux never tracked that mode (Reasonix leaves modeChannel
// unmapped and exposes no option groups), so a clear would drop the user into
// an arbitrary mode. A goal panel that reports honestly and offers no control
// is better than one whose Clear button silently changes something else, so
// ReasonixAgent implements no GoalController at all and the browser disables
// every action.
const reasonixMethodStatusUpdate = "_reasonix.io/session/status_update"

// Reasonix's own goal status words.
const (
	reasonixGoalStatusNone     = "none"
	reasonixGoalStatusRunning  = "running"
	reasonixGoalStatusComplete = "complete"
	reasonixGoalStatusBlocked  = "blocked"
	reasonixGoalStatusStopped  = "stopped"
)

type reasonixStatusUpdate struct {
	SessionID string `json:"sessionId"`
	Status    *struct {
		Goal *reasonixGoal `json:"goal"`
	} `json:"status"`
	// The notification is also observed with the status fields hoisted to the
	// top level rather than nested under `status`, so both shapes are read and
	// whichever one arrives wins. Declaring only one silently reported no goal.
	Goal *reasonixGoal `json:"goal"`
}

type reasonixGoal struct {
	Status    string `json:"status"`
	Objective string `json:"objective"`
	Runtime   *struct {
		TurnsUsed  *int32 `json:"turnsUsed"`
		TokensUsed *int64 `json:"tokensUsed"`
		LastReason string `json:"lastReason"`
		StopCause  string `json:"stopCause"`
	} `json:"runtime"`
}

// reasonixGoalStatus maps Reasonix's words onto the neutral four.
func reasonixGoalStatus(wire string) GoalStatus {
	switch wire {
	case reasonixGoalStatusRunning:
		return GoalStatusActive
	case reasonixGoalStatusComplete:
		return GoalStatusDone
	case reasonixGoalStatusBlocked, reasonixGoalStatusStopped:
		return GoalStatusBlocked
	default:
		return GoalStatusBlocked
	}
}

// handleExtraMethod claims Reasonix's own notification namespace.
//
// It returns true for every `_reasonix.io/` method so the shared ACP reader
// stops treating them as unknown, and false for anything else.
func (a *ReasonixAgent) handleExtraMethod(line *parsedLine) bool {
	if line.Method != reasonixMethodStatusUpdate {
		return false
	}
	a.handleReasonixStatusUpdate(line.Params)
	return true
}

func (a *ReasonixAgent) handleReasonixStatusUpdate(params json.RawMessage) {
	if len(params) == 0 {
		return
	}
	var update reasonixStatusUpdate
	if err := json.Unmarshal(params, &update); err != nil {
		slog.Warn("reasonix status_update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	// The status bus is not per-connection by construction, and ClearContext
	// mints a NEW sessionId. Without this check a notification still in flight
	// for the old session would be applied to the new one, and a goal the user
	// just cleared would come back.
	if update.SessionID != "" && !a.isCurrentACPSession(update.SessionID) {
		return
	}
	goal := update.Goal
	if update.Status != nil && update.Status.Goal != nil {
		goal = update.Status.Goal
	}
	if goal == nil {
		return
	}
	if goal.Status == "" || goal.Status == reasonixGoalStatusNone {
		a.sink.ClearGoal()
		return
	}
	report := GoalUpdate{
		Objective:    goal.Objective,
		Status:       reasonixGoalStatus(goal.Status),
		StatusDetail: goal.Status,
		// The status stream is a full restatement sent on a cadence of its own,
		// not a change event, so it is never treated as an announcement. The
		// applier still writes a transcript row when the objective or the status
		// actually differs -- see the transition test there -- so a real change
		// is not lost by this.
		Snapshot: false,
	}
	if rt := goal.Runtime; rt != nil {
		report.Iterations = rt.TurnsUsed
		report.TokensUsed = rt.TokensUsed
		// The stop cause says more than the status word when there is one:
		// "goal_stuck" and "budget_spend" are both `stopped`.
		if rt.StopCause != "" {
			report.StatusDetail = rt.StopCause
		} else if rt.LastReason != "" {
			report.StatusDetail = rt.LastReason
		}
	}
	a.sink.UpsertGoal(report)
}

// isCurrentACPSession reports whether sessionID is the session this agent is
// serving right now.
func (b *acpBase) isCurrentACPSession(sessionID string) bool {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.mu.Lock()
	current := b.sessionID
	b.mu.Unlock()
	return current == "" || current == sessionID
}
