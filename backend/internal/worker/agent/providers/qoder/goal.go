package qoder

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Qoder's session goal: a standing objective the agent keeps working toward
// until it is met, blocked or cleared.
//
// The goal lives in Qoder's own goal manager and the CLI reports every change
// as a `system` line on the stream:
//
//	{"type":"system","subtype":"goal_updated","goal":{...},"reason":str?,
//	 "uuid":str,"session_id":str}
//	{"type":"system","subtype":"goal_cleared","goal_id":str,"reason":str?,
//	 "uuid":str,"session_id":str}
//
// The report is the source of truth for the card, so LeapMux does not observe
// the commands it sends. The goal object states:
//
//	{"id":str,"objective":str,"status":str,"turns_used":int,
//	 "time_used_seconds":int,"created_at":int,"updated_at":int,
//	 "max_turns":int?,"credits_budget":int?,"credits_used":int?}
//
// The writes go through the control channel, which the stream handshake already
// speaks: `set_goal` (objective and/or status), `clear_goal` and `resume_goal`.
// There is no `pause_goal` -- a pause is a status write through `set_goal`,
// which the CLI's own status validator admits and its goal manager applies with
// the same bookkeeping as its pause path.

// Qoder's goal status words.
const (
	qoderGoalStatusActive        = "active"
	qoderGoalStatusPaused        = "paused"
	qoderGoalStatusBlocked       = "blocked"
	qoderGoalStatusUsageLimited  = "usage_limited"
	qoderGoalStatusBudgetLimited = "budget_limited"
	qoderGoalStatusComplete      = "complete"
)

// qoderGoalStatus maps Qoder's six status words onto the four neutral ones. An
// unknown word maps to blocked: a status this build cannot read is one it must
// not offer Pause for.
func qoderGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case qoderGoalStatusActive:
		return agent.GoalStatusActive
	case qoderGoalStatusPaused:
		return agent.GoalStatusPaused
	case qoderGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// qoderGoalStatusDetail keeps what the neutral status loses: that a goal ran
// out of usage or budget, and the reason for a pause or a block. An active or a
// finished goal keeps no detail: its `reason` is how the change arrived, not a
// condition the card should show.
func qoderGoalStatusDetail(status, reason string) string {
	reason = strings.TrimSpace(reason)
	switch status {
	case qoderGoalStatusUsageLimited:
		if reason == "" || reason == status {
			return "usage limited"
		}
		return "usage limited: " + reason
	case qoderGoalStatusBudgetLimited:
		if reason == "" || reason == status {
			return "budget limited"
		}
		return "budget limited: " + reason
	case qoderGoalStatusActive, qoderGoalStatusComplete:
		return ""
	default:
		return reason
	}
}

// qoderGoalFrame is the `system` line a goal change emits. Which fields mark
// the two variants is the `subtype` beside them.
type qoderGoalFrame struct {
	Subtype string `json:"subtype"`
	Reason  string `json:"reason"`
	Goal    *struct {
		ID              string `json:"id"`
		Objective       string `json:"objective"`
		Status          string `json:"status"`
		TurnsUsed       *int32 `json:"turns_used"`
		MaxTurns        *int32 `json:"max_turns"`
		TimeUsedSeconds *int64 `json:"time_used_seconds"`
		CreditsBudget   *int64 `json:"credits_budget"`
		CreditsUsed     *int64 `json:"credits_used"`
		CreatedAt       int64  `json:"created_at"`
		UpdatedAt       int64  `json:"updated_at"`
	} `json:"goal"`
	GoalID string `json:"goal_id"`
}

// handleGoalUpdated folds one goal report into the card.
func (a *Agent) handleGoalUpdated(content []byte) {
	var frame qoderGoalFrame
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("qoder goal report unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	goal := frame.Goal
	if goal == nil || goal.ID == "" {
		a.sink.ClearGoal(false)
		return
	}
	var createdAt time.Time
	if goal.CreatedAt > 0 {
		createdAt = time.UnixMilli(goal.CreatedAt).UTC()
	}
	a.sink.UpsertGoal(agent.GoalUpdate{
		NativeID:  goal.ID,
		Objective: goal.Objective,
		// The report states the status of a goal that still exists, so the only
		// state this frame can describe is the one it names.
		Status:          qoderGoalStatus(goal.Status),
		StatusDetail:    qoderGoalStatusDetail(goal.Status, frame.Reason),
		CreatedAt:       createdAt,
		TimeUsedSeconds: goal.TimeUsedSeconds,
		Iterations:      goal.TurnsUsed,
		// `max_turns`, `credits_budget` and `credits_used` stay absent: the
		// neutral card has counters for tokens and seconds and one iteration
		// count, and a turn limit or a credit balance has no field there. The
		// panel omits a row it has no number for rather than printing a credit
		// balance under a token label.
	})
}

// handleGoalCleared removes the session goal. The sink holds one goal per
// agent and the CLI has just dropped its own, so the id on the frame is not
// matched: the goal the card shows is the one that ended.
func (a *Agent) handleGoalCleared(content []byte) {
	var frame qoderGoalFrame
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("qoder goal clear unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.sink.ClearGoal(false)
}

// --- GoalWriter ---

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports the verbs the runtime negotiated. Setting,
// clearing and pausing need `goal_v1`; resuming needs `goal_resume_v1`, which
// is the capability that grants a fresh turn budget after a pause.
//
// The answer comes from the `init` frame's `capabilities`, never from a version
// table: the same CLI build negotiates differently per host.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.hasCapability(contracts.QoderCapabilityGoalV1) {
		return nil
	}
	actions := []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause}
	if a.hasCapability(contracts.QoderCapabilityGoalResumeV1) {
		actions = append(actions, agent.GoalActionResume)
	}
	return actions
}

// PerformGoalAction sends the control request that changes the goal. The
// goal report then states what Qoder did with it.
//
// Every action is a side-band control request: it starts no turn and needs no
// queued delivery, so the outcome is empty.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	body, err := qoderGoalRequestBody(action, objective)
	if err != nil {
		return agent.GoalOutcome{}, err
	}
	_, err = a.sendControlAndWait(body, goalControlTimeout)
	return agent.GoalOutcome{}, err
}

// goalControlTimeout is the deadline of one goal control request. The goal
// manager answers from local state, so a generous bound only covers a loaded
// machine.
const goalControlTimeout = 5 * time.Second

// qoderGoalRequestBody builds the control request for one action.
//
// A set states the objective. A pause is a status write, because the control
// channel has no `pause_goal`: the CLI's status validator admits `paused`, and
// its goal manager applies that write with the same bookkeeping as its own
// pause path. A resume is `resume_goal`, which only acts on a paused or blocked
// goal and grants it a fresh turn budget. A clear is `clear_goal`.
//
// The body is the `request` object alone; the sender wraps it in the control
// envelope with the type and the request id.
func qoderGoalRequestBody(action agent.GoalAction, objective string) (string, error) {
	var fields map[string]any
	switch action {
	case agent.GoalActionSet:
		objective = strings.Join(strings.Fields(objective), " ")
		if objective == "" {
			return "", fmt.Errorf("qoder set_goal: an objective is required")
		}
		fields = map[string]any{
			"subtype":   contracts.QoderControlRequestSubtypeSetGoal,
			"objective": objective,
		}
	case agent.GoalActionClear:
		fields = map[string]any{
			"subtype": contracts.QoderControlRequestSubtypeClearGoal,
		}
	case agent.GoalActionPause:
		fields = map[string]any{
			"subtype": contracts.QoderControlRequestSubtypeSetGoal,
			"status":  qoderGoalStatusPaused,
		}
	case agent.GoalActionResume:
		fields = map[string]any{
			"subtype": contracts.QoderControlRequestSubtypeResumeGoal,
		}
	default:
		return "", agent.ErrGoalControlUnsupported
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// hasCapability reports whether the running runtime negotiated name at init.
// A process that has not reported its capabilities yet answers false, so the
// panel offers no control it cannot verify.
func (a *Agent) hasCapability(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, capability := range a.capabilities {
		if capability == name {
			return true
		}
	}
	return false
}
