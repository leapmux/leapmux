package muse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

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
