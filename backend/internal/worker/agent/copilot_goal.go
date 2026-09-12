package agent

import (
	"fmt"
	"strings"
)

// Copilot accepts /goal as an alias of /autopilot. An objective starts autopilot.
// The off argument pauses the objective and changes the session mode to agent.
const copilotGoalCommand = "/goal"

// Copilot advertises autopilot in its Agent Client Protocol command list.
const copilotGoalAdvertisedCommand = "autopilot"

// The shared route handles objective formatting and observation.
// Copilot handles its mode arguments separately because neither clears a goal.
var copilotGoalRoute = goalTextRoute{
	provider: "copilot",
	command:  copilotGoalCommand,
}

var _ GoalTextCommander = (*CopilotCLIAgent)(nil)

func (a *CopilotCLIAgent) SupportedGoalActions() []GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionPause}
}

func (a *CopilotCLIAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	switch action {
	case GoalActionSet:
		argument := foldGoalObjective(objective)
		if strings.EqualFold(argument, "off") || strings.EqualFold(argument, "on") {
			return GoalOutcome{}, fmt.Errorf("%w: copilot %s reads %q as a mode change",
				ErrGoalObjectiveIsCommand, copilotGoalCommand, argument)
		}
		return copilotGoalRoute.perform(action, argument)
	case GoalActionPause:
		return GoalOutcome{QueuedInput: copilotGoalCommand + " off"}, nil
	default:
		// Copilot exposes no verified Clear or Resume operation through ACP.
		// The on argument changes the mode but leaves the objective paused.
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
}

func (a *CopilotCLIAgent) ObserveGoalCommand(delivery GoalCommandDelivery, text string) {
	if delivery != GoalDeliverySend || !a.hasGoalCommand() {
		return
	}
	route := copilotGoalRoute
	alias := "/" + copilotGoalAdvertisedCommand
	if trimmed := strings.TrimSpace(text); trimmed == alias || strings.HasPrefix(trimmed, alias+" ") {
		route.command = alias
	}
	intent, argument := parseGoalCommandText(text, route.command, nil)
	if intent == goalTextSet {
		switch {
		case strings.EqualFold(argument, "off"):
			a.sink.UpdateGoalStatus(GoalStatusActive, GoalStatusPaused)
			return
		case strings.EqualFold(argument, "on"):
			return
		}
	}
	route.observe(a.sink, delivery, text)
}

func (a *CopilotCLIAgent) hasGoalCommand() bool {
	return a.hasAvailableCommand(copilotGoalAdvertisedCommand)
}
