package codewhale

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codewhale's thread goal.
//
// Routes: PUT /v1/threads/{id}/goal {objective} sets it and starts a kickoff
// turn at once; DELETE clears it. The runtime drives the continuation passes
// itself until the model marks the goal complete or blocked, or a limit stops
// it. It reports every change as an event:
//
//	thread_goal_updated {kind, goal: ThreadGoal}
//	thread_goal_cleared {kind, thread_id}
//	ThreadGoal {thread_id, goal_id, objective, status, token_budget?,
//	            tokens_used, time_used_seconds, continuation_count,
//	            created_at, updated_at, ...}
//
// created_at and updated_at are Unix SECONDS.
//
// The runtime has no pause or resume route: `/goal pause` is a command of its
// own terminal client. So the agent offers Set and Clear, and states Paused only
// when the runtime reports it.

// codewhaleGoal is the runtime's ThreadGoal.
type codewhaleGoal struct {
	ThreadID          string `json:"thread_id"`
	GoalID            string `json:"goal_id"`
	Objective         string `json:"objective"`
	Status            string `json:"status"`
	TokenBudget       *int64 `json:"token_budget"`
	TokensUsed        *int64 `json:"tokens_used"`
	TimeUsedSeconds   *int64 `json:"time_used_seconds"`
	ContinuationCount *int32 `json:"continuation_count"`
	CreatedAt         int64  `json:"created_at"`
}

// codewhaleGoalStatus maps the runtime's six status words onto the neutral
// ones. An unknown word reads as blocked: a status this build cannot read is
// one it must not offer controls for.
func codewhaleGoalStatus(status string) agent.GoalStatus {
	switch status {
	case goalStatusActive:
		return agent.GoalStatusActive
	case goalStatusPaused:
		return agent.GoalStatusPaused
	case goalStatusComplete:
		return agent.GoalStatusDone
	case goalStatusBlocked, goalStatusUsageLimited, goalStatusBudgetLimited:
		return agent.GoalStatusBlocked
	default:
		return agent.GoalStatusBlocked
	}
}

// handleGoalUpdated reports the thread's goal to the sink.
func (a *Agent) handleGoalUpdated(env codewhaleEnvelope) {
	var payload struct {
		Goal *codewhaleGoal `json:"goal"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Goal == nil {
		slog.Warn("codewhale goal update unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	goal := payload.Goal
	if goal.ThreadID != "" && goal.ThreadID != a.currentThreadID() {
		return
	}
	a.sink.UpsertGoal(agent.GoalUpdate{
		NativeID:        goal.GoalID,
		Objective:       goal.Objective,
		Status:          codewhaleGoalStatus(goal.Status),
		StatusDetail:    goal.Status,
		CreatedAt:       codewhaleGoalTime(goal.CreatedAt),
		TokensUsed:      goal.TokensUsed,
		TokenBudget:     goal.TokenBudget,
		TimeUsedSeconds: goal.TimeUsedSeconds,
		Iterations:      goal.ContinuationCount,
	})
}

// handleGoalCleared reports that the thread has no goal.
func (a *Agent) handleGoalCleared(env codewhaleEnvelope) {
	var payload struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return
	}
	if payload.ThreadID != "" && payload.ThreadID != a.currentThreadID() {
		return
	}
	a.sink.ClearGoal(false)
}

// codewhaleGoalTime converts Unix seconds. Zero means the field was absent.
func codewhaleGoalTime(unixSeconds int64) time.Time {
	if unixSeconds <= 0 {
		return time.Time{}
	}
	return time.Unix(unixSeconds, 0).UTC()
}

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions: the runtime has a set route and a clear route, and
// neither a pause nor a resume route.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
}

// PerformGoalAction runs one action through the goal routes. The runtime
// reports the change as an event, and that event -- not this reply -- updates
// the stored goal, so the state has one writer.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	threadID := a.currentThreadID()
	if threadID == "" {
		return agent.GoalOutcome{}, fmt.Errorf("the Codewhale agent has no thread")
	}
	switch action {
	case agent.GoalActionSet:
		if err := a.putGoal(threadID, objective); err != nil {
			return agent.GoalOutcome{}, err
		}
	case agent.GoalActionClear:
		if err := a.deleteGoal(threadID); err != nil {
			// A thread with no goal has nothing to clear, and that is the state the
			// reader asked for.
			if !providerkit.IsHTTPStatus(err, httpStatusNotFound) {
				return agent.GoalOutcome{}, err
			}
			a.sink.ClearGoal(false)
		}
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	return agent.GoalOutcome{}, nil
}
