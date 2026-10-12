package muse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/validate"
)

// deferGoalAction queues a goal write to run after the dispatch lock
// releases. The caller holds dispatchMu.
func (a *Agent) deferGoalAction(apply func()) {
	a.deferredGoalActions = append(a.deferredGoalActions, apply)
}

// takeDeferredGoalActions swaps the queued goal writes out under the dispatch
// lock, so an action that re-enters dispatch appends to a fresh queue rather
// than mutating the slice it is being read from.
func (a *Agent) takeDeferredGoalActions() []func() {
	a.dispatchMu.Lock()
	deferred := a.deferredGoalActions
	a.deferredGoalActions = nil
	a.dispatchMu.Unlock()
	return deferred
}

// parseMuseGoalEvent interprets one session/goal_changed payload's goal field.
//
// An ABSENT goal and an invalid one are both invalid: the event states nothing
// LeapMux can act on, and the current goal must survive. An EXPLICIT null goal
// is the native clear. A valid goal object requires a readable objective and a
// non-blank status; a status LeapMux cannot interpret maps to
// GoalStatusUnknown with the native word kept as the display detail, because
// proving the end of an unknown state is not the same as interpreting it.
func parseMuseGoalEvent(raw json.RawMessage) (update agent.GoalUpdate, clear, valid bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return agent.GoalUpdate{}, false, false
	}
	if trimmed == "null" {
		return agent.GoalUpdate{}, true, true
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
	switch goal.Status {
	case "active":
		status = agent.GoalStatusActive
	case "paused":
		status = agent.GoalStatusPaused
	case "completed", "satisfied":
		status = agent.GoalStatusDone
	case "blocked", "failed":
		status = agent.GoalStatusBlocked
	}
	return agent.GoalUpdate{Objective: goal.Objective, Status: status, StatusDetail: goal.Status}, false, true
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
