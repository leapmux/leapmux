package goose

import (
	"bytes"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
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

// armGooseGoal arms the end-of-goal rule for the running prompt. The notice
// carries the goal's own text after its prefix, which is the objective the
// completion report states.
func (a *Agent) armGooseGoal(objective string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.promptOutput.goalArmed = true
	a.promptOutput.goalObjective = objective
}

// finishGooseGoalPrompt decides the goal's end at PromptEnded. An ordinary
// final message completes the goal; Goose's own error message and its
// max-turns question keep it, and so do a failed prompt, a stop, and a round
// with no final message or no arming notice. The rule serves one prompt: the
// caller captures and consumes these observations when the prompt ends.
func (a *Agent) finishGooseGoalPrompt(output goosePromptOutputState, err error, stopped bool) acp.PromptEnding {
	if err != nil || stopped || !output.goalArmed || len(bytes.TrimSpace(output.lastMessageText)) == 0 {
		return acp.PromptEnding{}
	}
	if bytes.HasPrefix(output.lastMessageText, []byte(contracts.GooseGoalTextErrorPrefix)) || bytes.Equal(output.lastMessageText, []byte(contracts.GooseGoalTextMaxTurnsMessage)) {
		return acp.PromptEnding{}
	}
	a.Sink().UpsertGoal(agent.GoalUpdate{Objective: output.goalObjective, Status: agent.GoalStatusDone})
	return acp.PromptEnding{}
}
