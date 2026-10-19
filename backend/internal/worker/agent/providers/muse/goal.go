package muse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
)

// deferGoalAction queues a goal write after the caller releases dispatchMu.
func (a *Agent) deferGoalAction(apply func()) {
	a.deferredGoalActions = append(a.deferredGoalActions, apply)
}

// takeDeferredGoalActions takes the queued goal writes under dispatchMu.
// An action that enters dispatch again appends to a separate queue.
func (a *Agent) takeDeferredGoalActions() []func() {
	a.dispatchMu.Lock()
	deferred := a.deferredGoalActions
	a.deferredGoalActions = nil
	a.dispatchMu.Unlock()
	return deferred
}

// parseMuseGoalEvent reads the goal field of session/goalChanged.
// An absent or invalid goal preserves the current goal. An explicit null clears it.
// A valid goal requires a readable objective and a nonblank status.
// An unknown status keeps its native word as the detail.
func parseMuseGoalEvent(raw json.RawMessage) (update agent.GoalUpdate, clear, valid bool) {
	switch providerkit.RawKindOf(raw) {
	case providerkit.RawAbsent:
		return agent.GoalUpdate{}, false, false
	case providerkit.RawNull:
		return agent.GoalUpdate{}, true, true
	case providerkit.RawValue:
		// Decode the goal object below.
	}
	var goal struct {
		Objective string `json:"objective"`
		Status    string `json:"status"`
	}
	if json.Unmarshal(raw, &goal) != nil {
		return agent.GoalUpdate{}, false, false
	}
	if strings.TrimSpace(validate.StripUnreadable(goal.Objective, contracts.GoalObjectiveByteLimit)) == "" ||
		strings.TrimSpace(goal.Status) == "" {
		return agent.GoalUpdate{}, false, false
	}
	status := agent.GoalStatusUnknown
	detail := ""
	switch goal.Status {
	case museGoalActive:
		status = agent.GoalStatusActive
	case museGoalPaused:
		status = agent.GoalStatusPaused
	case museGoalComplete:
		status = agent.GoalStatusDone
	case museGoalBlocked:
		status = agent.GoalStatusBlocked
	case museGoalUsageLimited:
		status, detail = agent.GoalStatusBlocked, "usage limit reached"
	case museGoalBudgetLimited:
		status, detail = agent.GoalStatusBlocked, "budget limit reached"
	default:
		detail = goal.Status
	}
	return agent.GoalUpdate{Objective: goal.Objective, Status: status, StatusDetail: detail}, false, true
}

func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.stateMu.Lock()
	id := a.sessionID
	a.stateMu.Unlock()
	params := map[string]any{"sessionId": id}
	method := ""
	switch action {
	case agent.GoalActionSet:
		objective = strings.TrimSpace(objective)
		if objective == "" {
			return agent.GoalOutcome{}, fmt.Errorf("the Muse goal objective is empty")
		}
		method = methodGoalSet
		params["objective"] = objective
	case agent.GoalActionClear:
		method = methodGoalClear
	case agent.GoalActionPause:
		method = methodGoalPause
	case agent.GoalActionResume:
		method = methodGoalResume
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	_, err := a.command(method, params, a.APITimeout(), func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var response turnResult
		if json.Unmarshal(raw, &response) == nil && response.TurnID != "" {
			a.startTurn(id, response.TurnID)
		}
	})
	return agent.GoalOutcome{}, err
}
