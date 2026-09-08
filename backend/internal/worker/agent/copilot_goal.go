package agent

import (
	"fmt"
	"time"
)

// Copilot registers /goal as an alias of /autopilot. Setting an objective also
// changes the ACP session mode to #autopilot. Clearing changes it to #agent.
const copilotGoalCommand = "/goal"

var copilotGoalClearArguments = []string{"off"}

var _ GoalTextCommander = (*CopilotCLIAgent)(nil)

func (a *CopilotCLIAgent) SupportedGoalActions() []GoalAction {
	if !a.hasAvailableCommand("autopilot") {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear}
}

func (a *CopilotCLIAgent) GoalCommandText(action GoalAction, objective string) (string, error) {
	switch action {
	case GoalActionSet:
		objective = foldGoalObjective(objective)
		if objective == "" {
			return "", fmt.Errorf("copilot %s: an objective is required", copilotGoalCommand)
		}
		return copilotGoalCommand + " " + objective, nil
	case GoalActionClear:
		return copilotGoalCommand + " " + copilotGoalClearArguments[0], nil
	default:
		return "", ErrGoalControlUnsupported
	}
}

func (a *CopilotCLIAgent) ObserveGoalCommand(text string) {
	if !a.hasAvailableCommand("autopilot") {
		return
	}
	intent, objective := parseGoalCommandText(text, copilotGoalCommand, copilotGoalClearArguments)
	switch intent {
	case goalTextSet:
		a.sink.UpsertGoal(GoalUpdate{Objective: objective, Status: GoalStatusActive, CreatedAt: time.Now().UTC()})
	case goalTextClear:
		a.sink.ClearGoal(false)
	case goalTextNotCommand, goalTextBareQuery:
		// These inputs do not change the goal.
	}
}
