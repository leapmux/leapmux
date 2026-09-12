package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

// SupportedGoalActions reports each operation that the advertised modes support.
// ACP exposes no explicit operation to pause or resume an existing goal.
// See https://github.com/esengine/DeepSeek-Reasonix/issues/10201.
func (a *ReasonixAgent) SupportedGoalActions() []GoalAction {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !hasACPOption(a.availableModes, contracts.ReasonixModeNormal) {
		return nil
	}
	if hasACPOption(a.availableModes, contracts.ReasonixModeGoal) {
		return []GoalAction{GoalActionSet, GoalActionClear}
	}
	return []GoalAction{GoalActionClear}
}

var _ GoalWriter = (*ReasonixAgent)(nil)

func (a *ReasonixAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	if !slices.Contains(a.SupportedGoalActions(), action) {
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
	if action == GoalActionSet {
		if strings.TrimSpace(objective) == "" {
			return GoalOutcome{}, fmt.Errorf("a goal objective must not be empty")
		}
		var goalSessionID string
		err := a.sendPreparedPrompt(objective, nil, func(sessionID string) error {
			goalSessionID = sessionID
			// Normal mode removes the previous objective before Goal mode accepts another one.
			if err := a.setReasonixGoalMode(sessionID, contracts.ReasonixModeNormal); err != nil {
				return err
			}
			return a.setReasonixGoalMode(sessionID, contracts.ReasonixModeGoal)
		})
		if err == nil {
			err = a.confirmReasonixGoal(goalSessionID, objective)
		}
		return GoalOutcome{}, err
	}
	err := a.withSessionID(func(sessionID string) error {
		return a.setReasonixGoalMode(sessionID, contracts.ReasonixModeNormal)
	})
	return GoalOutcome{}, err
}

// The caller holds the session lock. Do not use sendSessionRPC here because it takes that lock again.
func (a *ReasonixAgent) setReasonixGoalMode(sessionID, mode string) error {
	if sessionID == "" || a.IsStopped() {
		return fmt.Errorf("the Reasonix session is unavailable")
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modeId": mode})
	if err != nil {
		return err
	}
	if _, err := a.sendRequest(acpMethodSessionSetMode, params, a.APITimeout()); err != nil {
		return err
	}
	a.mu.Lock()
	a.permissionMode = mode
	a.mu.Unlock()
	a.broadcastSettingsRefresh()
	if mode == contracts.ReasonixModeNormal {
		a.goalStatusMu.Lock()
		a.goalStatusRevision++
		a.sink.ClearGoal(false)
		a.goalStatusMu.Unlock()
	}
	return nil
}

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

const reasonixMethodSessionStatus = "_reasonix.io/session/status"

// Reasonix publishes its starting status before SetGoal stores the objective.
// Read the native status because another notification may not arrive until the model responds.
func (a *ReasonixAgent) confirmReasonixGoal(sessionID, objective string) error {
	expires := time.Now().Add(min(a.APITimeout(), 5*time.Second))
	deadline := time.NewTimer(time.Until(expires))
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.goalStatusMu.Lock()
		revision := a.goalStatusRevision
		a.goalStatusMu.Unlock()
		var raw json.RawMessage
		err := a.withSessionID(func(current string) error {
			if current != sessionID {
				return ErrContextClearCancelled
			}
			params, err := json.Marshal(map[string]string{"sessionId": sessionID})
			if err != nil {
				return err
			}
			remaining := time.Until(expires)
			if remaining <= 0 {
				return ErrDeliveryUncertain
			}
			raw, err = a.sendRequest(reasonixMethodSessionStatus, params, remaining)
			return err
		})
		if err != nil {
			return err
		}
		var status reasonixStatusUpdate
		if err := json.Unmarshal(raw, &status); err != nil {
			return fmt.Errorf("read Reasonix goal status: %w", err)
		}
		if status.SessionID != sessionID || status.Goal == nil || status.Goal.Status == "" {
			return fmt.Errorf("the Reasonix goal status is invalid")
		}
		a.goalStatusMu.Lock()
		current := revision == a.goalStatusRevision && a.isCurrentACPSession(sessionID)
		if current && status.Goal.Objective != "" {
			if status.Goal.Objective != objective {
				a.goalStatusMu.Unlock()
				return fmt.Errorf("the Reasonix goal objective differs from the request")
			}
			a.applyReasonixGoal(status.Goal)
			a.goalStatusRevision++
			a.goalStatusMu.Unlock()
			return nil
		}
		a.goalStatusMu.Unlock()
		if current && status.Mode != contracts.ReasonixModeGoal {
			return fmt.Errorf("the Reasonix goal changed before Set completed")
		}
		select {
		case <-a.ctx.Done():
			return a.ctx.Err()
		case <-deadline.C:
			return ErrDeliveryUncertain
		case <-ticker.C:
		}
	}
}

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
	Mode      string `json:"mode"`
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
	case reasonixGoalStatusNone, reasonixGoalStatusBlocked, reasonixGoalStatusCancelled, reasonixGoalStatusFailed:
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
	a.goalStatusMu.Lock()
	defer a.goalStatusMu.Unlock()
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
	a.goalStatusRevision++
	a.applyReasonixGoal(goal)
}

func (a *ReasonixAgent) applyReasonixGoal(goal *reasonixGoal) {
	if goal == nil || goal.Status == "" {
		return
	}
	// The full snapshot omits the objective after Clear. A previous turn can
	// still supply a cancelled or failed status through Reasonix's telemetry override.
	if strings.TrimSpace(goal.Objective) == "" {
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
