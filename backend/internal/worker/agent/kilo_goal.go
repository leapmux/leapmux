package agent

// Kilo changes its session goal through a user-message slash command, the same
// route Goose and Claude Code take. Its command carries all FOUR verbs, which
// no other text route does:
//
//	goal  "Keep working toward a session goal. /goal <objective> or pause, resume, clear"
//
// The command is Kilo's own addition to the OpenCode base it forks -- every line
// of it is marked `kilocode_change`, and upstream OpenCode registers no goal
// command at all. That is why OpenCode offers no goal here and Kilo does.
const kiloGoalCommand = "/goal"

// kiloGoalAdvertisedCommand is the name Kilo lists in its ACP command set. The
// list carries bare names, without the leading slash.
const kiloGoalAdvertisedCommand = "goal"

// kiloGoalRoute is Kilo's user-message goal vocabulary.
//
// steerCarriesCommand is false for the reason Goose leaves it false: Kilo steers
// through a separate advertised ACP method, and LeapMux did not verify that the
// method reaches the same command parser. A wrong claim there would state a goal
// that Kilo does not hold.
var kiloGoalRoute = goalTextRoute{
	provider:   "kilo",
	command:    kiloGoalCommand,
	clearArgs:  []string{"clear"},
	pauseArgs:  []string{"pause"},
	resumeArgs: []string{"resume"},
}

var (
	_ GoalWriter        = (*KiloAgent)(nil)
	_ GoalTextCommander = (*KiloAgent)(nil)
)

// SupportedGoalActions reports the four verbs, and only while Kilo advertises
// the command. A build without the feature lists no `goal` command, and the
// browser then draws no goal control rather than one that does nothing.
func (a *KiloAgent) SupportedGoalActions() []GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume}
}

// PerformGoalAction builds the user message that changes Kilo's goal. The queue
// delivers it; nothing changes until it does.
func (a *KiloAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	return kiloGoalRoute.perform(action, objective)
}

// ObserveGoalCommand writes the goal that a delivered command installed. Kilo
// reports no goal of its own over ACP, so this observer is the only source.
func (a *KiloAgent) ObserveGoalCommand(delivery GoalCommandDelivery, text string) {
	if !a.hasGoalCommand() {
		return
	}
	kiloGoalRoute.observe(a.sink, delivery, text)
}

func (a *KiloAgent) hasGoalCommand() bool {
	return a.hasAvailableCommand(kiloGoalAdvertisedCommand)
}
