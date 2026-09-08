package agent

// Goose changes its session goal through a user-message slash command. ACP
// reports the command at runtime but does not report the resulting goal.
const gooseGoalCommand = "/goal"

// gooseGoalAdvertisedCommand is the name Goose lists in its ACP command set.
// It equals the emitted command here, and Copilot's does not, so both files
// name the two separately.
const gooseGoalAdvertisedCommand = "goal"

// gooseGoalRoute is Goose's user-message goal vocabulary.
//
// steerCarriesCommand is false: Goose steers through a separate advertised ACP
// method, and LeapMux did not verify that the method reaches the same command
// parser. A wrong claim there would state a goal that Goose does not hold.
var gooseGoalRoute = goalTextRoute{
	provider:  "goose",
	command:   gooseGoalCommand,
	clearArgs: []string{"off", "clear", "none"},
}

var _ GoalTextCommander = (*GooseCLIAgent)(nil)

func (a *GooseCLIAgent) SupportedGoalActions() []GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear}
}

func (a *GooseCLIAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	return gooseGoalRoute.perform(action, objective)
}

func (a *GooseCLIAgent) ObserveGoalCommand(delivery GoalCommandDelivery, text string) {
	if !a.hasGoalCommand() {
		return
	}
	gooseGoalRoute.observe(a.sink, delivery, text)
}

func (a *GooseCLIAgent) hasGoalCommand() bool {
	return a.hasAvailableCommand(gooseGoalAdvertisedCommand)
}
