package kimi

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
)

// Each captured request belongs to one goal instance and status. Even a goal
// recreated with the same objective and status gets a separate identity.
type kimiGoalMetadataRequest struct {
	sessionID, objective, status string
}

type kimiRESTGoalMetadata struct {
	GoalID    string          `json:"goalId"`
	Objective string          `json:"objective"`
	Status    string          `json:"status"`
	EndReason json.RawMessage `json:"terminalReason"`
	Budget    *kimiGoalBudget `json:"budget"`
}

// The flat feed carries activity and usage. Fetch only metadata it omits,
// independently of startup, and leave later feed usage authoritative.
func (a *Agent) requestGoalMetadata(sessionID, objective, status string) {
	if a.stream == nil || a.api == nil || !a.features[kimiFeatureGoal] || kimiCheckID("session", sessionID) != nil {
		return
	}
	a.Mu.Lock()
	next := kimiGoalMetadataRequest{sessionID: sessionID, objective: objective, status: status}
	if a.sessionID != sessionID || a.StoppedLocked() || a.ProcessExitedLocked() || a.goal.metadataRequest != nil && *a.goal.metadataRequest == next {
		a.Mu.Unlock()
		return
	}
	a.goal.metadataRequest = &next
	a.Mu.Unlock()
	a.goalMetadataWork.Run("metadata", a.refreshGoalMetadata)
}

func (a *Agent) refreshGoalMetadata() {
	a.Mu.Lock()
	wanted := a.goal.metadataRequest
	a.Mu.Unlock()
	if wanted == nil {
		return
	}
	a.stream.mu.Lock()
	streamCtx := a.stream.ctx
	a.stream.mu.Unlock()
	if streamCtx == nil || streamCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(streamCtx, a.APITimeout())
	defer cancel()
	var metadata *kimiRESTGoalMetadata
	if err := a.api.get(ctx, kimiSessionPath(wanted.sessionID, "/goal"), &metadata); err != nil {
		slog.Debug("read optional Kimi goal metadata", "agent_id", a.AgentID(), "error", err)
		return
	}
	if metadata == nil || metadata.GoalID == "" || metadata.Objective != wanted.objective || metadata.Status != wanted.status ||
		metadata.Budget != nil && metadata.Budget.TokenBudget != nil && *metadata.Budget.TokenBudget < 0 {
		return
	}
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	current := a.goal.snapshot
	valid := a.goal.metadataRequest == wanted && a.sessionID == wanted.sessionID && current.Objective == wanted.objective && current.Status == wanted.status &&
		!a.StoppedLocked() && !a.ProcessExitedLocked() && ctx.Err() == nil
	a.Mu.Unlock()
	if !valid {
		return
	}
	enriched := current
	enriched.GoalID, enriched.EndReason, enriched.Budget = metadata.GoalID, metadata.EndReason, metadata.Budget
	if !reflect.DeepEqual(current, enriched) {
		a.applyGoal(&enriched, true)
	}
}
