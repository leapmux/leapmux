package agent

// Copilot registers /goal as an alias of /autopilot. Setting an objective also
// changes the ACP session mode to #autopilot. Clearing changes it to #agent.
const copilotGoalCommand = "/goal"

// copilotGoalAdvertisedCommand is the name Copilot lists in its ACP command
// set. It differs from the command LeapMux emits: Copilot advertises
// `autopilot` and accepts `/goal` as an undocumented alias, so the capability
// check reads one token and the emitted text carries the other. Deriving the
// emitted token from the advertised set would send `/autopilot <objective>`,
// which queries the state instead of setting it.
const copilotGoalAdvertisedCommand = "autopilot"

// copilotGoalRoute is Copilot's user-message goal vocabulary.
//
// steerCarriesCommand is false, and CopilotCLIAgent implements no InputSteerer
// at all, so Manager.SteerInput refuses a Copilot steer before any delivery.
var copilotGoalRoute = goalTextRoute{
	provider:  "copilot",
	command:   copilotGoalCommand,
	clearArgs: []string{"off"},
}

var _ GoalTextCommander = (*CopilotCLIAgent)(nil)

func (a *CopilotCLIAgent) SupportedGoalActions() []GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear}
}

func (a *CopilotCLIAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	return copilotGoalRoute.perform(action, objective)
}

func (a *CopilotCLIAgent) ObserveGoalCommand(delivery GoalCommandDelivery, text string) {
	if !a.hasGoalCommand() {
		return
	}
	copilotGoalRoute.observe(a.sink, delivery, text)
}

func (a *CopilotCLIAgent) hasGoalCommand() bool {
	return a.hasAvailableCommand(copilotGoalAdvertisedCommand)
}
