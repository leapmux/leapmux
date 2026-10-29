package reasonix

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	reasonixMethodGoalPause  = "_reasonix.io/session/goal/pause"
	reasonixMethodGoalResume = "_reasonix.io/session/goal/resume"
)

// observeReasonixCapabilities reads the goal extensions before the session opens.
func (a *Agent) observeReasonixCapabilities(response []byte) {
	var initialize struct {
		Capabilities struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	methods := make(map[agent.GoalAction]string)
	if json.Unmarshal(response, &initialize) == nil {
		for action, method := range map[agent.GoalAction]string{
			agent.GoalActionPause: reasonixMethodGoalPause, agent.GoalActionResume: reasonixMethodGoalResume,
		} {
			var capability struct {
				SchemaVersion int `json:"schemaVersion"`
			}
			if json.Unmarshal(initialize.Capabilities.Meta[method], &capability) == nil && capability.SchemaVersion == 1 {
				methods[action] = method
			}
		}
	}
	a.Mu.Lock()
	a.goalMethods = methods
	a.Mu.Unlock()
}

// SupportedGoalActions reports the operations that the running session advertises.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	modes := a.AvailableModes()
	if !acp.HasOption(modes, contracts.ReasonixModeNormal) {
		return nil
	}
	actions := []agent.GoalAction{agent.GoalActionClear}
	if acp.HasOption(modes, contracts.ReasonixModeGoal) {
		actions = []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, action := range []agent.GoalAction{agent.GoalActionPause, agent.GoalActionResume} {
		if a.goalMethods[action] != "" {
			actions = append(actions, action)
		}
	}
	return actions
}

var _ agent.GoalWriter = (*Agent)(nil)

func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	if !slices.Contains(a.SupportedGoalActions(), action) {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	if action == agent.GoalActionPause || action == agent.GoalActionResume {
		a.Mu.Lock()
		method := a.goalMethods[action]
		pauseMethod := a.goalMethods[agent.GoalActionPause]
		a.Mu.Unlock()
		change := func(sessionID string) error {
			return a.changeReasonixGoal(sessionID, method, pauseMethod)
		}
		if action == agent.GoalActionResume {
			// The native resume retains the objective and counters but starts no turn.
			// ACP rejects an empty prompt, so start its continuation after resume succeeds.
			return agent.GoalOutcome{}, a.SendPreparedPrompt("Continue the retained goal.", nil, change)
		}
		return agent.GoalOutcome{}, a.WithSessionID(change)
	}
	if action == agent.GoalActionSet {
		if strings.TrimSpace(objective) == "" {
			return agent.GoalOutcome{}, fmt.Errorf("a goal objective must not be empty")
		}
		var goalSessionID string
		var generation uint64
		err := a.SendPreparedPrompt(objective, nil, func(sessionID string) error {
			goalSessionID = sessionID
			// Normal mode removes the previous objective before Goal mode accepts another one.
			if err := a.setReasonixGoalMode(sessionID, contracts.ReasonixModeNormal); err != nil {
				return err
			}
			if err := a.setReasonixGoalMode(sessionID, contracts.ReasonixModeGoal); err != nil {
				return err
			}
			a.goalStatusMu.Lock()
			generation = a.goalOperationGeneration
			a.goalStatusMu.Unlock()
			return nil
		})
		if err == nil {
			err = a.confirmReasonixGoal(goalSessionID, generation)
		}
		return agent.GoalOutcome{}, err
	}
	err := a.WithSessionID(func(sessionID string) error {
		return a.setReasonixGoalMode(sessionID, contracts.ReasonixModeNormal)
	})
	return agent.GoalOutcome{}, err
}

// changeReasonixGoal keeps the native objective and counters through each operation.
func (a *Agent) changeReasonixGoal(sessionID, method, pauseMethod string) error {
	if method == "" {
		return agent.ErrGoalControlUnsupported
	}
	if sessionID == "" || a.IsStopped() {
		return fmt.Errorf("the Reasonix session is unavailable")
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return err
	}
	_, err = a.SendRequest(method, params, a.APITimeout())
	if method != reasonixMethodGoalResume || pauseMethod == "" || !providerkit.HasJSONRPCErrorCode(err, -32010) {
		return err
	}
	// Reasonix's failed status describes the last turn. The retained goal still runs,
	// so its native resume refuses it. Pause that exact goal before resuming it.
	status, readErr := a.readReasonixStatus(sessionID, a.APITimeout())
	if readErr != nil {
		return errors.Join(err, fmt.Errorf("read Reasonix goal after resume refusal: %w", readErr))
	}
	if status.SessionID != sessionID || status.Mode != contracts.ReasonixModeGoal || status.Goal == nil ||
		status.Goal.Status != reasonixGoalStatusFailed || strings.TrimSpace(status.Goal.Objective) == "" {
		return err
	}
	if _, pauseErr := a.SendRequest(pauseMethod, params, a.APITimeout()); pauseErr != nil {
		return pauseErr
	}
	_, err = a.SendRequest(method, params, a.APITimeout())
	return err
}

// The caller holds the session lock. Do not use sendSessionRPC here because it takes that lock again.
func (a *Agent) setReasonixGoalMode(sessionID, mode string) error {
	if err := a.WriteSecondarySetting(mode, func(value string, acknowledged func(string)) error {
		return a.setReasonixSessionMode(sessionID, value, acknowledged)
	}); err != nil {
		return err
	}
	a.BroadcastSettingsRefresh()
	return nil
}

// acknowledgeReasonixGoalClear cancels older confirmations after a native mode change removes the goal.
func (a *Agent) acknowledgeReasonixGoalClear() {
	a.goalStatusMu.Lock()
	defer a.goalStatusMu.Unlock()
	a.goalStatusRevision++
	a.goalOperationGeneration++
	a.Sink().ClearGoal(false)
}

// Reasonix's session goal.
//
// Reasonix runs a goal state machine and streams its whole session status over
// a custom notification beside the standard ACP updates:
//
//	_reasonix.io/session/status_update
//	  {schemaVersion, sequence, sessionId, status:{state, mode, ...,
//	   goal:{status, objective?, runtime?:{turnsUsed, tokensUsed, requestsUsed,
//	                                       workDurationMs, lastReason, stopCause}}}}
//
// Reasonix adopts the next prompt as its objective after entering goal mode.
// Switching to normal or plan mode clears the goal.
const reasonixMethodSessionStatus = "_reasonix.io/session/status"

// Reasonix publishes its starting status before SetGoal stores the objective.
// Read the native status because another notification may not arrive until the model responds.
func (a *Agent) confirmReasonixGoal(sessionID string, generation uint64) error {
	window := min(a.APITimeout(), 5*time.Second)
	expires := a.Process.Clock().Now().Add(window)
	deadline := a.Process.Clock().NewTimer(window)
	defer deadline.Stop()
	ticker := a.Process.Clock().NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.goalStatusMu.Lock()
		revision := a.goalStatusRevision
		superseded := generation != a.goalOperationGeneration
		a.goalStatusMu.Unlock()
		if superseded {
			return fmt.Errorf("the Reasonix goal changed before Set completed")
		}
		var status reasonixSessionStatus
		err := a.WithSessionID(func(current string) error {
			if current != sessionID {
				return agent.ErrContextClearCancelled
			}
			remaining := a.Process.Clock().Now().Sub(expires).Abs()
			if a.Process.Clock().Now().After(expires) {
				return agent.ErrDeliveryUncertain
			}
			var err error
			status, err = a.readReasonixStatus(sessionID, remaining)
			return err
		})
		if err != nil {
			return fmt.Errorf("read Reasonix goal status: %w", err)
		}
		if status.SessionID != sessionID || status.Goal == nil || status.Goal.Status == "" {
			return fmt.Errorf("the Reasonix goal status is invalid")
		}
		a.goalStatusMu.Lock()
		if generation != a.goalOperationGeneration {
			a.goalStatusMu.Unlock()
			return fmt.Errorf("the Reasonix goal changed before Set completed")
		}
		current := revision == a.goalStatusRevision && a.IsCurrentSession(sessionID) && a.reasonixStatusModeMatches(status.Mode)
		if current && strings.TrimSpace(status.Goal.Objective) != "" && a.acceptReasonixStatusSequenceLocked(sessionID, status.Sequence) {
			// Native status redacts secrets in the objective. The operation generation,
			// session identity, and snapshot revision establish ownership instead of text equality.
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
		case <-a.Context().Done():
			return a.Context().Err()
		case <-deadline.C:
			return agent.ErrDeliveryUncertain
		case <-ticker.C:
		}
	}
}

// The caller owns the session lock. This reader neither changes goal state nor takes that lock again.
func (a *Agent) readReasonixStatus(sessionID string, timeout time.Duration) (reasonixSessionStatus, error) {
	var status reasonixSessionStatus
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return status, err
	}
	raw, err := a.SendRequest(reasonixMethodSessionStatus, params, timeout)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return status, err
	}
	return status, nil
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
func reasonixGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case reasonixGoalStatusRunning:
		return agent.GoalStatusActive
	case reasonixGoalStatusComplete:
		return agent.GoalStatusDone
	// None, blocked, cancelled and failed are all "not progressing, needs the
	// user". All four are listed rather than left to the default so the
	// vocabulary this build knows is visible, and an unrecognized word still
	// reads as blocked -- a state LeapMux cannot understand is one it must not
	// offer Pause for.
	case reasonixGoalStatusNone, reasonixGoalStatusBlocked, reasonixGoalStatusCancelled, reasonixGoalStatusFailed:
		return agent.GoalStatusBlocked
	default:
		return agent.GoalStatusBlocked
	}
}

// applyReasonixStatusGoal keeps notification ownership and goal publication in one critical section.
func (a *Agent) applyReasonixStatusGoal(sessionID string, sequence uint64, status *reasonixSessionStatus) bool {
	// The lock covers the DECISION and the goal write that follows it.
	// goalStatusRevision is a completion mark, not a statistic: confirmReasonixGoal
	// samples the counter, runs a status round trip, and stores the user's objective
	// only when the counter did not move. An apply outside the section that bumps the
	// counter makes that mark lie. This operation bumps 5 to 6, the scheduler stops it,
	// confirmReasonixGoal samples 6, round-trips, reads 6 again and reports success --
	// and then this operation resumes and ClearGoal removes the objective it just
	// confirmed.
	//
	// Usage and phase reports stay outside this lock in handleReasonixStatusUpdate.
	// Each one reaches the sink -- a fan-out to every watcher in reportReasonixUsage, an INSERT in
	// reportReasonixPhase -- and Reasonix restates its whole status on every turn
	// tick, so holding the mutex across them made each other reader of goalStatusMu
	// wait on a database write and a broadcast to every connected tab. Neither one
	// takes part in the revision protocol, which guards the goal alone.
	a.goalStatusMu.Lock()
	defer a.goalStatusMu.Unlock()
	// The status bus is not per-connection by construction, and ClearContext
	// mints a NEW sessionId. Without this check a notification still in flight
	// for the old session would be applied to the new one, and a goal the user
	// just cleared would come back.
	currentSessionID := a.CurrentSessionID()
	if sessionID != "" && sessionID != currentSessionID {
		return false
	}
	if !a.acceptReasonixStatusSequenceLocked(currentSessionID, sequence) {
		return false
	}
	var goal *reasonixGoal
	if status != nil {
		if !a.reasonixStatusModeMatches(status.Mode) {
			// A mode clear can overtake a goal snapshot without changing its sequence.
			// The snapshot can still carry the final cost and phase, so reject only its goal.
			return true
		}
		goal = status.Goal
	}
	a.goalStatusRevision++
	a.applyReasonixGoal(goal)
	return true
}

func (a *Agent) reasonixStatusModeMatches(mode string) bool {
	nativeMode := a.CurrentNativeMode()
	return mode == "" || nativeMode == "" || mode == nativeMode
}

func (a *Agent) applyReasonixGoal(goal *reasonixGoal) {
	// An absent goal, and a goal object with no status word, are both an
	// INCOMPLETE snapshot rather than a removal. Reasonix states a change by
	// restating the whole status, so a restatement that omits the word says
	// nothing about the goal the card already shows. This keeps that card, and
	// the objective check below is the one path that removes it.
	if goal == nil || goal.Status == "" {
		return
	}
	// The full snapshot omits the objective after Clear. A previous turn can
	// still supply a cancelled or failed status through Reasonix's telemetry override.
	if strings.TrimSpace(goal.Objective) == "" {
		// Not a snapshot, for the reason the upsert below is not one: Reasonix
		// reports a change only by restating its whole status, so marking this
		// a restatement would silence every Reasonix goal removal.
		a.Sink().ClearGoal(false)
		return
	}
	report := agent.GoalUpdate{
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
		if rt.StopCause == "manual" && report.Status != agent.GoalStatusActive && report.Status != agent.GoalStatusDone {
			report.Status = agent.GoalStatusPaused
		}
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
	a.Sink().UpsertGoal(report)
}
