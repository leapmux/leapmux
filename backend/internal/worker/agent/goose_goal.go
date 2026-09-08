package agent

import (
	"fmt"
	"time"
)

// Goose changes its session goal through a user-message slash command. ACP
// reports the command at runtime but does not report the resulting goal.
const gooseGoalCommand = "/goal"

var gooseGoalClearArguments = []string{"off", "clear", "none"}

var _ GoalTextCommander = (*GooseCLIAgent)(nil)

func (a *GooseCLIAgent) SupportedGoalActions() []GoalAction {
	if !a.hasAvailableCommand("goal") {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear}
}

func (a *GooseCLIAgent) GoalCommandText(action GoalAction, objective string) (string, error) {
	switch action {
	case GoalActionSet:
		objective = foldGoalObjective(objective)
		if objective == "" {
			return "", fmt.Errorf("goose %s: an objective is required", gooseGoalCommand)
		}
		return gooseGoalCommand + " " + objective, nil
	case GoalActionClear:
		return gooseGoalCommand + " " + gooseGoalClearArguments[0], nil
	default:
		return "", ErrGoalControlUnsupported
	}
}

func (a *GooseCLIAgent) ObserveGoalCommand(text string) {
	if !a.hasAvailableCommand("goal") {
		return
	}
	intent, objective := parseGoalCommandText(text, gooseGoalCommand, gooseGoalClearArguments)
	switch intent {
	case goalTextSet:
		a.sink.UpsertGoal(GoalUpdate{Objective: objective, Status: GoalStatusActive, CreatedAt: time.Now().UTC()})
	case goalTextClear:
		a.sink.ClearGoal(false)
	case goalTextNotCommand, goalTextBareQuery:
		// These inputs do not change the goal.
	}
}
