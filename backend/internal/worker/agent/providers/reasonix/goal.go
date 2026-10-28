package reasonix

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
		err := a.SendPreparedPrompt(objective, nil, func(sessionID string) error {
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
	raw, readErr := a.SendRequest(reasonixMethodSessionStatus, params, a.APITimeout())
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	var status reasonixStatusUpdate
	if decodeErr := json.Unmarshal(raw, &status); decodeErr != nil {
		return errors.Join(err, fmt.Errorf("read Reasonix goal after resume refusal: %w", decodeErr))
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
	if sessionID == "" || a.IsStopped() {
		return fmt.Errorf("the Reasonix session is unavailable")
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modeId": mode})
	if err != nil {
		return err
	}
	if _, err := a.SendRequest(acp.MethodSessionSetMode, params, a.APITimeout()); err != nil {
		return err
	}
	a.SetPermissionMode(mode)
	a.BroadcastSettingsRefresh()
	if mode == contracts.ReasonixModeNormal {
		a.goalStatusMu.Lock()
		a.goalStatusRevision++
		a.Sink().ClearGoal(false)
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
func (a *Agent) confirmReasonixGoal(sessionID, objective string) error {
	window := min(a.APITimeout(), 5*time.Second)
	expires := a.Process.Clock().Now().Add(window)
	deadline := a.Process.Clock().NewTimer(window)
	defer deadline.Stop()
	ticker := a.Process.Clock().NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.goalStatusMu.Lock()
		revision := a.goalStatusRevision
		a.goalStatusMu.Unlock()
		var raw json.RawMessage
		err := a.WithSessionID(func(current string) error {
			if current != sessionID {
				return agent.ErrContextClearCancelled
			}
			params, err := json.Marshal(map[string]string{"sessionId": sessionID})
			if err != nil {
				return err
			}
			remaining := a.Process.Clock().Now().Sub(expires).Abs()
			if a.Process.Clock().Now().After(expires) {
				return agent.ErrDeliveryUncertain
			}
			raw, err = a.SendRequest(reasonixMethodSessionStatus, params, remaining)
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
		current := revision == a.goalStatusRevision && a.IsCurrentSession(sessionID)
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
		case <-a.Context().Done():
			return a.Context().Err()
		case <-deadline.C:
			return agent.ErrDeliveryUncertain
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
		// Phase is the live state of the turn (`implementing`, `waiting_permission`,
		// `review_ready`, ...). It is one word, not a sentence, so the row reads it
		// through a table rather than printing the token.
		Phase string `json:"phase"`
		// Usage carries the turn's own totals and the session's cumulative ones.
		// The meter reads the CUMULATIVE half: the turn half restarts, and a gauge
		// that restarted with it would report a context that shrank.
		Usage *struct {
			Cumulative *reasonixUsage `json:"cumulative"`
		} `json:"usage"`
	} `json:"status"`
	// Goal is the HOISTED shape, and it belongs to a different call: the
	// `_reasonix.io/session/status` request answers with the status object
	// flat, while this notification nests it under `status`. LeapMux reads only
	// the notification, so this field is the tolerant path rather than a second
	// observed shape of the same message -- it costs one struct field and
	// removes a whole class of silent "no goal" if the two shapes ever converge.
	Goal *reasonixGoal `json:"goal"`
}

// reasonixUsage holds the cumulative cost. ACP usage_update owns the current context gauge.
type reasonixUsage struct {
	EstimatedCost *float64 `json:"estimatedCost"`
	Currency      string   `json:"currency"`
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

func (a *Agent) handleReasonixStatusUpdate(params json.RawMessage) {
	if len(params) == 0 {
		return
	}
	var update reasonixStatusUpdate
	if err := json.Unmarshal(params, &update); err != nil {
		slog.Warn("reasonix status_update unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	// The lock covers the DECISION and the goal write that follows it.
	// goalStatusRevision is a completion mark, not a statistic: confirmReasonixGoal
	// samples the counter, runs a status round trip, and stores the user's objective
	// only when the counter did not move. An apply outside the section that bumps the
	// counter makes that mark lie. This handler bumps 5 to 6, the scheduler stops it,
	// confirmReasonixGoal samples 6, round-trips, reads 6 again and reports success --
	// and then this handler resumes and ClearGoal removes the objective it just
	// confirmed.
	//
	// The two reports below stay OUTSIDE the lock. Each one reaches the sink -- a
	// fan-out to every watcher in reportReasonixUsage, an INSERT in
	// reportReasonixPhase -- and Reasonix restates its whole status on every turn
	// tick, so holding the mutex across them made each other reader of goalStatusMu
	// wait on a database write and a broadcast to every connected tab. Neither one
	// takes part in the revision protocol, which guards the goal alone.
	a.goalStatusMu.Lock()
	// The status bus is not per-connection by construction, and ClearContext
	// mints a NEW sessionId. Without this check a notification still in flight
	// for the old session would be applied to the new one, and a goal the user
	// just cleared would come back.
	if update.SessionID != "" && !a.IsCurrentSession(update.SessionID) {
		a.goalStatusMu.Unlock()
		return
	}
	goal := update.Goal
	if update.Status != nil && update.Status.Goal != nil {
		goal = update.Status.Goal
	}
	a.goalStatusRevision++
	status := update.Status
	a.applyReasonixGoal(goal)
	a.goalStatusMu.Unlock()

	if status != nil {
		a.reportReasonixUsage(status.Usage)
		a.reportReasonixPhase(status.Phase)
	}
}

// The phase words Reasonix reports that a reader must see, and the sentence each
// one becomes.
//
// A phase is one wire token, so a row that printed it would show
// `waiting_permission`. Only the phases that state something a reader can ACT on
// are here: the working phases (`starting`, `implementing`) say what every other
// surface in the app already says, and repeating them would put a row in the
// transcript for each step of every turn.
var reasonixPhaseLabels = map[string]string{
	"checking_readiness": "Checking whether the work is ready",
	"readiness_paused":   "Paused: the work is not ready",
	"review_ready":       "Ready for review",
	"paused":             "Paused",
}

// reportReasonixPhase states one phase change, once.
//
// Reasonix restates its WHOLE status on every change, so the same phase arrives
// many times per turn. Only a move to a new phase says anything.
func (a *Agent) reportReasonixPhase(phase string) {
	if phase == "" || phase == a.lastStatusPhase {
		return
	}
	a.lastStatusPhase = phase
	label, ok := reasonixPhaseLabels[phase]
	if !ok {
		return
	}
	a.Sink().PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: label,
	})
}

// reportReasonixUsage broadcasts a cumulative cost stated in US dollars.
//
// Ephemeral, like every other provider's usage report: the meter reads it live
// and the transcript holds no row for it.
func (a *Agent) reportReasonixUsage(usage *struct {
	Cumulative *reasonixUsage `json:"cumulative"`
}) {
	if usage == nil || usage.Cumulative == nil {
		return
	}
	total := usage.Cumulative
	info := map[string]interface{}{}
	// The CUMULATIVE cost, which is a total rather than a delta: a restatement
	// carries the same number and cannot double it.
	if total.EstimatedCost != nil && *total.EstimatedCost >= 0 && total.Currency == "USD" {
		info[contracts.SessionInfoKeyTotalCostUsd] = *total.EstimatedCost
	}
	if len(info) > 0 {
		a.Sink().BroadcastSessionInfo(info)
	}
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
