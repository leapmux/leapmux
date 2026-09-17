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

// reasonixUsage is the subset of one Reasonix usage report the meter reads.
//
// `totalTokens` is INCLUSIVE of the prompt and completion halves, so the context
// gauge reads it directly rather than adding the parts and counting twice.
type reasonixUsage struct {
	TotalTokens      int64    `json:"totalTokens"`
	PromptTokens     int64    `json:"promptTokens"`
	CompletionTokens int64    `json:"completionTokens"`
	CacheHitTokens   int64    `json:"cacheHitTokens"`
	CacheMissTokens  int64    `json:"cacheMissTokens"`
	EstimatedCost    *float64 `json:"estimatedCost"`
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
	// None, blocked, cancelled and failed are all "not progressing, needs the
	// user". All four are listed rather than left to the default so the
	// vocabulary this build knows is visible, and an unrecognized word still
	// reads as blocked -- a state LeapMux cannot understand is one it must not
	// offer Pause for.
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
	if update.SessionID != "" && !a.isCurrentACPSession(update.SessionID) {
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
	"waiting_permission": "Waiting for permission",
	"waiting_input":      "Waiting for input",
	"checking_readiness": "Checking whether the work is ready",
	"readiness_paused":   "Paused: the work is not ready",
	"review_ready":       "Ready for review",
	"paused":             "Paused",
}

// reportReasonixPhase states one phase change, once.
//
// Reasonix restates its WHOLE status on every change, so the same phase arrives
// many times per turn. Only a move to a new phase says anything.
func (a *ReasonixAgent) reportReasonixPhase(phase string) {
	if phase == "" || phase == a.lastStatusPhase {
		return
	}
	a.lastStatusPhase = phase
	label, ok := reasonixPhaseLabels[phase]
	if !ok {
		return
	}
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: label,
	})
}

// reportReasonixUsage broadcasts the session's cumulative token and cost totals.
//
// Ephemeral, like every other provider's usage report: the meter reads it live
// and the transcript holds no row for it.
func (a *ReasonixAgent) reportReasonixUsage(usage *struct {
	Cumulative *reasonixUsage `json:"cumulative"`
}) {
	if usage == nil || usage.Cumulative == nil {
		return
	}
	total := usage.Cumulative
	info := map[string]interface{}{}
	if total.TotalTokens > 0 || total.PromptTokens > 0 || total.CompletionTokens > 0 {
		info[contracts.SessionInfoKeyContextUsage] = map[string]interface{}{
			contracts.ContextUsageFieldInputTokens:              total.PromptTokens,
			contracts.ContextUsageFieldOutputTokens:             total.CompletionTokens,
			contracts.ContextUsageFieldCacheReadInputTokens:     total.CacheHitTokens,
			contracts.ContextUsageFieldCacheCreationInputTokens: total.CacheMissTokens,
			contracts.ContextUsageFieldContextTokens:            total.TotalTokens,
		}
	}
	// The CUMULATIVE cost, which is a total rather than a delta: a restatement
	// carries the same number and cannot double it.
	if total.EstimatedCost != nil {
		info[contracts.SessionInfoKeyTotalCostUsd] = *total.EstimatedCost
	}
	if len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}

func (a *ReasonixAgent) applyReasonixGoal(goal *reasonixGoal) {
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
