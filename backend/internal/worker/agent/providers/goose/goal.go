package goose

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
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

// observeGooseSessionUpdate records the last agent message of the main
// session, by messageId, for the goal-end rule and the turn-failure verdict.
// Goose states no structured goal report, so the goal's end is inferred from
// the FINAL message of a round. Goose also ends a failed model request with an
// agent message and a successful prompt response, so that message is the only
// record of the failure. This observer keeps the message's complete text,
// because the chunks arrive before the prompt response and PromptEnded runs
// before the final text persists.
//
// It claims no update: reading one must not change its transcript route.
func (a *Agent) observeGooseSessionUpdate(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	if !a.IsCurrentSession(sessionID) {
		return false
	}
	var message struct {
		SessionUpdate string `json:"sessionUpdate"`
		MessageID     string `json:"messageId"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Meta struct {
			Goose struct {
				Inference json.RawMessage `json:"inference"`
			} `json:"goose"`
		} `json:"_meta"`
	}
	if json.Unmarshal(update, &message) != nil || message.Content.Type != "text" {
		return false
	}
	if message.SessionUpdate != contracts.ACPUpdateAgentMessageChunk || message.MessageID == "" {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.lastMessageID != message.MessageID {
		a.lastMessageID = message.MessageID
		a.lastMessageText = ""
		a.lastMessageInference = false
	}
	a.lastMessageText += message.Content.Text
	if providerkit.RawPresent(message.Meta.Goose.Inference) {
		a.lastMessageInference = true
	}
	a.lastMessageUpdate = append(json.RawMessage(nil), update...)
	return false
}

// gooseLastMessage is one snapshot of the last agent message of a prompt.
type gooseLastMessage struct {
	text      string
	inference bool
	update    json.RawMessage
}

// takeGooseLastMessage returns the last agent message and clears it, under one
// lock acquisition, so the goal-end rule and the turn-failure verdict read the
// same message.
func (a *Agent) takeGooseLastMessage() gooseLastMessage {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	last := gooseLastMessage{text: a.lastMessageText, inference: a.lastMessageInference, update: a.lastMessageUpdate}
	a.lastMessageID, a.lastMessageText, a.lastMessageInference, a.lastMessageUpdate = "", "", false, nil
	return last
}

// clearGooseLastMessage drops the last agent message before a prompt starts
// (Hooks.PromptPrepared), so a message of an earlier turn or of a session
// replay never decides the next prompt's end.
func (a *Agent) clearGooseLastMessage() {
	a.takeGooseLastMessage()
}

// armGooseGoal arms the end-of-goal rule for the running prompt. The notice
// carries the goal's own text after its prefix, which is the objective the
// completion report states.
func (a *Agent) armGooseGoal(objective string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.goalArmed = true
	a.goalObjective = objective
}

// finishGooseGoalPrompt decides the goal's end at PromptEnded. lastText is the
// final agent message of the prompt. An ordinary final message completes the
// goal; one of Goose's own error messages and its max-turns question keep it,
// and so do a failed prompt, a stop, and a round with no final message or no
// arming notice. The rule serves one prompt: the arming is consumed here
// either way.
func (a *Agent) finishGooseGoalPrompt(err error, stopped bool, lastText string) {
	a.Mu.Lock()
	armed, objective := a.goalArmed, a.goalObjective
	a.goalArmed, a.goalObjective = false, ""
	a.Mu.Unlock()
	if err != nil || stopped || !armed || strings.TrimSpace(lastText) == "" {
		return
	}
	if gooseErrorText(lastText) || lastText == contracts.GooseGoalTextMaxTurnsMessage {
		return
	}
	a.Sink().UpsertGoal(agent.GoalUpdate{Objective: objective, Status: agent.GoalStatusDone})
}
