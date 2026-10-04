package qwen

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen changes its session goal through the `/goal` prompt command, and states
// the whole goal on the `_meta.goalState` of a message after every change:
//
//	/goal set <objective> | pause | resume | clear
//
// The report is the source of truth for the card, so LeapMux does not observe
// the command it sends.
const (
	qwenGoalCommand           = "/goal"
	qwenGoalAdvertisedCommand = "goal"
)

// qwenGoalRoute is Qwen's user-message goal vocabulary.
//
// Qwen reads the FIRST word of the argument as a verb: `set` and `edit` take
// the rest as the objective, and `pause`, `resume` and a clear word act on
// their own. So a bare objective that starts with one of those words would not
// reach Qwen as that objective. The route always states `set`, which makes
// every objective safe, and needs no word reserved for it.
var qwenGoalRoute = providerkit.GoalTextRoute{
	Provider:   "qwen",
	Command:    qwenGoalCommand,
	SetVerb:    "set",
	ClearArgs:  []string{"clear", "stop", "off", "reset", "none", "cancel"},
	PauseArgs:  []string{"pause"},
	ResumeArgs: []string{"resume"},
}

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports the four verbs, and only while Qwen advertises
// the command.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.HasAvailableCommand(qwenGoalAdvertisedCommand) {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction builds the prompt that changes Qwen's goal. The queue
// delivers it, and the goal state then reports what Qwen did with it.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	if !a.HasAvailableCommand(qwenGoalAdvertisedCommand) {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	return qwenGoalRoute.Perform(action, objective)
}

// qwenGoalSnapshot is Qwen's goal state (`GoalSnapshotV2`).
type qwenGoalSnapshot struct {
	Goal *struct {
		GoalID       string `json:"goalId"`
		Objective    string `json:"objective"`
		Status       string `json:"status"`
		TurnCount    *int32 `json:"turnCount"`
		ActiveTimeMs *int64 `json:"activeTimeMs"`
		TokensUsed   *int64 `json:"tokensUsed"`
		TokenBudget  *int64 `json:"tokenBudget"`
		CreatedAt    int64  `json:"createdAt"`
		LastReason   string `json:"lastReason"`
	} `json:"goal"`
	Activity string `json:"activity"`
}

// Qwen's goal status words.
const (
	qwenGoalStatusActive       = "active"
	qwenGoalStatusPaused       = "paused"
	qwenGoalStatusBlocked      = "blocked"
	qwenGoalStatusUsageLimited = "usage_limited"
	qwenGoalStatusComplete     = "complete"
)

// qwenGoalStatus maps Qwen's five status words onto the four neutral ones. An
// unknown word maps to blocked: a status this build cannot read is one it must
// not offer Pause for.
func qwenGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case qwenGoalStatusActive:
		return agent.GoalStatusActive
	case qwenGoalStatusPaused:
		return agent.GoalStatusPaused
	case qwenGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// qwenGoalStatusDetail keeps what the neutral status loses: that a goal ran out
// of usage, that a check runs, and the reason for a pause or a block.
func qwenGoalStatusDetail(status, activity, reason string) string {
	var parts []string
	if status == qwenGoalStatusUsageLimited {
		parts = append(parts, "usage limited")
	}
	if status == qwenGoalStatusActive && activity == "verifying" {
		parts = append(parts, "verifying")
	}
	if status != qwenGoalStatusActive && status != qwenGoalStatusComplete {
		if reason = strings.TrimSpace(reason); reason != "" {
			parts = append(parts, reason)
		}
	}
	return strings.Join(parts, ": ")
}

// handleGoalState folds one goal state into the card. A state with no goal
// states that no goal exists.
func (a *Agent) handleGoalState(raw json.RawMessage) {
	var snapshot qwenGoalSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		slog.Warn("qwen goal state unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	goal := snapshot.Goal
	if goal == nil || goal.GoalID == "" {
		a.Sink().ClearGoal(false)
		return
	}
	var seconds *int64
	if goal.ActiveTimeMs != nil {
		value := *goal.ActiveTimeMs / 1000
		seconds = &value
	}
	var createdAt time.Time
	if goal.CreatedAt > 0 {
		createdAt = time.UnixMilli(goal.CreatedAt).UTC()
	}
	a.Sink().UpsertGoal(agent.GoalUpdate{
		NativeID:        goal.GoalID,
		Objective:       goal.Objective,
		Status:          qwenGoalStatus(goal.Status),
		StatusDetail:    qwenGoalStatusDetail(goal.Status, snapshot.Activity, goal.LastReason),
		CreatedAt:       createdAt,
		TokensUsed:      goal.TokensUsed,
		TokenBudget:     goal.TokenBudget,
		TimeUsedSeconds: seconds,
		Iterations:      goal.TurnCount,
	})
}
