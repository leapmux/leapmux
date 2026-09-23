package goose

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Goose changes its session goal through a user-message slash command. ACP
// reports the command at runtime but does not report the resulting goal.
const gooseGoalCommand = "/goal"

// gooseGoalAdvertisedCommand is the name Goose lists in its ACP command set.
// It equals the emitted command here, and Copilot's does not, so both files
// name the two separately.
const gooseGoalAdvertisedCommand = "goal"

// gooseGoalRoute is Goose's user-message goal vocabulary.
//
// SteerCarriesCommand is false: Goose steers through a separate advertised ACP
// method, and LeapMux did not verify that the method reaches the same command
// parser. A wrong claim there would state a goal that Goose does not hold.
var gooseGoalRoute = providerkit.GoalTextRoute{
	Provider:  "goose",
	Command:   gooseGoalCommand,
	ClearArgs: []string{"off", "clear", "none"},
}

var _ agent.GoalTextCommander = (*Agent)(nil)

func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
}

func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	return gooseGoalRoute.Perform(action, objective)
}

func (a *Agent) ObserveGoalCommand(delivery agent.GoalCommandDelivery, text string) {
	if !a.hasGoalCommand() {
		return
	}
	gooseGoalRoute.Observe(a.Sink(), delivery, text)
}

func (a *Agent) hasGoalCommand() bool {
	return a.HasAvailableCommand(gooseGoalAdvertisedCommand)
}
