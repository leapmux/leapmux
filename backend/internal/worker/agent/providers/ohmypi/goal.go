package ohmypi

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// omp's goals are DISPLAY-ONLY over RPC.
//
// omp keeps a session goal, and reports every change to it with a goal_updated
// frame. But goal mode starts only from omp's own terminal (`/goal`,
// `/guided-goal`): no RPC command reaches the goal runtime, and the `/goal` text
// sent as a prompt reaches the model as a plain message. So the worker reports the
// goal the session has -- a resumed session keeps an active goal, and omp's `goal`
// tool changes it -- and offers no action on it.

// SupportedGoalActions reports that the running agent can do nothing to a goal.
// The goal card still shows a goal omp reports; it offers no control for it.
func (a *Agent) SupportedGoalActions() []agent.GoalAction { return nil }

// ompGoal is omp's goal, from a goal_updated frame.
type ompGoal struct {
	ID              string `json:"id"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      *int64 `json:"tokensUsed"`
	TimeUsedSeconds *int64 `json:"timeUsedSeconds"`
	CreatedAt       int64  `json:"createdAt"`
}

// handleGoalUpdated reports omp's goal to the sink: its current state, or its
// removal.
//
// A report that arrives while the session opens RESTATES a goal the resumed
// session already had, so it is a snapshot: the transcript does not announce it as
// a change that happened now.
func (a *Agent) handleGoalUpdated(raw []byte) {
	var frame struct {
		Goal *ompGoal `json:"goal"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		slog.Warn("omp goal_updated decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	snapshot := a.startupSnapshot.Load()
	goal := frame.Goal
	if goal == nil || goal.Status == goalStatusDropped || goal.Objective == "" {
		a.sink.ClearGoal(snapshot)
		return
	}
	a.sink.UpsertGoal(agent.GoalUpdate{
		NativeID:        goal.ID,
		Objective:       goal.Objective,
		Status:          goalStatus(goal.Status),
		StatusDetail:    goal.Status,
		CreatedAt:       goalTime(goal.CreatedAt),
		TokensUsed:      goal.TokensUsed,
		TokenBudget:     goal.TokenBudget,
		TimeUsedSeconds: goal.TimeUsedSeconds,
		Snapshot:        snapshot,
	})
}

// goalStatus maps omp's goal status onto the neutral one. A goal that ran out of
// its token budget is blocked; an unknown word reads as blocked too, because a
// state this build cannot read is one it cannot act on.
func goalStatus(status string) agent.GoalStatus {
	switch status {
	case goalStatusActive:
		return agent.GoalStatusActive
	case goalStatusPaused:
		return agent.GoalStatusPaused
	case goalStatusComplete:
		return agent.GoalStatusDone
	case goalStatusBudgetLimited:
		return agent.GoalStatusBlocked
	default:
		return agent.GoalStatusBlocked
	}
}

// goalTime converts omp's creation time, in Unix MILLISECONDS, to a time.Time. Zero
// means the field was absent, and the sink keeps the identity it had.
func goalTime(unixMillis int64) time.Time {
	if unixMillis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(unixMillis).UTC()
}
