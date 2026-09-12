package agent

import (
	"encoding/json"
	"log/slog"
)

// SupportedGoalActions reports no goal actions.
// ACP exposes no explicit operation to resume an existing Goal.
// See https://github.com/esengine/DeepSeek-Reasonix/issues/10201.
func (a *ReasonixAgent) SupportedGoalActions() []GoalAction { return nil }

var _ GoalCapable = (*ReasonixAgent)(nil)

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
// Reasonix adopts the next prompt as its objective after entering goal mode.
// Switching to normal or plan mode clears the goal.
const reasonixMethodStatusUpdate = "_reasonix.io/session/status_update"

// Reasonix's own goal status words, as they reach the WIRE.
//
// Its Go enum also has `stopped`, but normalizeGoalStatus never emits it: the
// status projection passes running, complete and blocked, and answers "none"
// for everything else. Two words come from a different place -- a per-turn
// override sets `cancelled` when the user cancels a turn and `failed` when a
// turn returns an error -- so they appear here although the enum does not
// list them.
const (
	reasonixGoalStatusNone      = "none"
	reasonixGoalStatusRunning   = "running"
	reasonixGoalStatusComplete  = "complete"
	reasonixGoalStatusBlocked   = "blocked"
	reasonixGoalStatusCancelled = "cancelled"
	reasonixGoalStatusFailed    = "failed"
)

type reasonixStatusUpdate struct {
	SessionID string `json:"sessionId"`
	Status    *struct {
		Goal *reasonixGoal `json:"goal"`
	} `json:"status"`
	// Goal is the HOISTED shape, and it belongs to a different call: the
	// `_reasonix.io/session/status` request answers with the status object
	// flat, while this notification nests it under `status`. LeapMux reads only
	// the notification, so this field is the tolerant path rather than a second
	// observed shape of the same message -- it costs one struct field and
	// removes a whole class of silent "no goal" if the two shapes ever converge.
	Goal *reasonixGoal `json:"goal"`
}

type reasonixGoal struct {
	Status    string `json:"status"`
	Objective string `json:"objective"`
	Runtime   *struct {
		TurnsUsed *int32 `json:"turnsUsed"`
		// WorkDurationMs is MILLISECONDS. GoalUpdate.TimeUsedSeconds is
		// seconds, so the report below divides. Assigning it directly would
		// print a duration 1000 times too large.
		WorkDurationMs *int64 `json:"workDurationMs"`
		TokensUsed     *int64 `json:"tokensUsed"`
		// requestsUsed has no neutral field. The card shows tokens, seconds and
		// iterations, and a request count is a fourth unit that only Reasonix
		// reports, so nothing would render it.
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
	// Blocked, cancelled and failed are all "not progressing, needs the user".
	// They are listed rather than left to the default so the vocabulary this
	// build knows is visible, and an unrecognized word still reads as blocked --
	// a state LeapMux cannot understand is one it must not offer Pause for.
	case reasonixGoalStatusBlocked, reasonixGoalStatusCancelled, reasonixGoalStatusFailed:
		return GoalStatusBlocked
	default:
		return GoalStatusBlocked
	}
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
		// Not a snapshot, for the reason the upsert below is not one: Reasonix
		// reports a change only by restating its whole status, so marking this
		// a restatement would silence every Reasonix goal removal.
		a.sink.ClearGoal(false)
		return
	}
	report := GoalUpdate{
		Objective:    goal.Objective,
		Status:       reasonixGoalStatus(goal.Status),
		StatusDetail: goal.Status,
		// NOT a snapshot, although the status stream is a full restatement sent
		// on a cadence of its own. Marking it one would silence every Reasonix
		// goal transition, because Reasonix reports a change only by restating
		// the whole status -- there is no separate change event to carry the
		// announcement. The applier's transition test is what keeps the cadence
		// out of the transcript: it compares the objective, the status and the
		// identity, and a restatement of an unchanged goal matches all three.
		//
		// The status DETAIL is deliberately outside that test, which matters
		// most here: lastReason below moves every turn.
		Snapshot: false,
	}
	if rt := goal.Runtime; rt != nil {
		report.Iterations = rt.TurnsUsed
		report.TokensUsed = rt.TokensUsed
		if rt.WorkDurationMs != nil {
			seconds := *rt.WorkDurationMs / 1000
			report.TimeUsedSeconds = &seconds
		}
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
//
// It takes b.mu only, because b.mu is what guards b.sessionID: newSessionLocked
// writes the field under b.mu and withSessionID reads it under b.mu.
//
// It must NOT take b.sessionMu. That lock is held across a whole session/new
// round trip, and only the reader goroutine can deliver the response to it.
// This function runs ON that reader goroutine, so an RLock here stops the
// reader until the round trip finishes, and the round trip cannot finish until
// the reader runs. ClearContext then hangs for the full API timeout, and every
// notification behind the blocked line waits with it.
func (b *acpBase) isCurrentACPSession(sessionID string) bool {
	b.mu.Lock()
	current := b.sessionID
	b.mu.Unlock()
	return current == "" || current == sessionID
}
