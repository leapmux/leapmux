package codebuddy

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// CodeBuddy's session goal.
//
// `/goal <condition>` registers a session-scoped Stop hook whose body is the
// condition. At the end of every turn a separate model call decides whether the
// condition holds; while it does not, the hook blocks stopping and CodeBuddy
// keeps working. `/goal clear` (and its single-token aliases) ends the goal
// early. The goal auto-clears once the condition is met.
//
// The slash command registration states the vocabulary:
//
//	{name:"goal", argumentHint:"<condition> | clear"}
//
// and the CLI's own documentation names the clear aliases: `stop`, `off`,
// `reset`, `none`, `cancel`. Each is recognized ONLY as an exact single token,
// so `/goal stop using deprecated API` sets that text as the condition.
//
// There is NO goal report on the stream-json channel. CodeBuddy stores the goal
// as session meta and reports it to its own UI and to an Agent Client Protocol
// client through `codebuddy.ai/goalStatus`; the headless stream writer converts
// only user, assistant, tool and reasoning history items into frames, so a goal
// event never reaches stdout. The observer below is therefore the only source
// the goal card has: it writes the row when the queue delivers the command.

// codebuddyGoalCommand is the slash command that sets or clears the goal.
const codebuddyGoalCommand = "/goal"

// codebuddyGoalAdvertisedName is how the command appears in the init frame's
// `slash_commands` list: bare, without the leading slash.
const codebuddyGoalAdvertisedName = "goal"

// codebuddyGoalRoute is CodeBuddy's user-message goal vocabulary.
//
// ClearArgs lists the whole set so Set refuses a one-word objective that
// CodeBuddy would read as a clear. Pause and Resume stay empty: the command
// takes a condition or a clear word and nothing else, so those two actions are
// unsupported here.
//
// SteerCarriesCommand stays false: CodeBuddy steers through its own `steer`
// control request rather than the user-message channel, and LeapMux has not
// verified that a steered line reaches the command parser. Observing one would
// write a row the CLI never acted on.
var codebuddyGoalRoute = providerkit.GoalTextRoute{
	Provider: "codebuddy",
	Command:  codebuddyGoalCommand,
	ClearArgs: []string{
		"clear", "stop", "off", "reset", "none", "cancel",
	},
}

var _ agent.GoalTextCommander = (*Agent)(nil)

// observeSlashCommands records whether THIS CodeBuddy build has /goal.
//
// It reads the `system` init frame's `slash_commands`, which is the CLI's own
// statement of what it can do. Without it the capability would be a guess, and
// the panel would offer a button whose only effect is sending the literal text
// "/goal ..." to the model as a prompt.
//
// A frame that carries no list at all leaves the answer alone rather than
// clearing it, so a future shape change degrades to "unknown" instead of
// silently disabling a working feature.
func (a *Agent) observeSlashCommands(content []byte) {
	var frame struct {
		Subtype       string   `json:"subtype"`
		SlashCommands []string `json:"slash_commands"`
	}
	if err := json.Unmarshal(content, &frame); err != nil || frame.Subtype != contracts.CodebuddySystemSubtypeInit {
		return
	}
	if len(frame.SlashCommands) == 0 {
		return
	}
	has := slices.Contains(frame.SlashCommands, codebuddyGoalAdvertisedName)
	a.mu.Lock()
	changed := a.hasGoalCommand != has || !a.goalCommandKnown
	a.hasGoalCommand = has
	a.goalCommandKnown = true
	a.mu.Unlock()
	if !changed {
		return
	}
	// Re-publish, because the capability just changed and the Manager's single
	// publish at registration already ran with the old answer. The init frame
	// can arrive after the agent registers.
	a.sink.PublishGoalCapabilities()
}

// SupportedGoalActions reports set and clear, and only when the CLI advertises
// the command.
//
// CodeBuddy's `/goal` takes a condition or a clear word. It has no pause and no
// resume verb, so those two actions are absent rather than faked. Both
// supported actions cost a turn, because the only write is a user-message
// command.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	a.mu.Lock()
	has := a.hasGoalCommand
	a.mu.Unlock()
	if !has {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
}

// PerformGoalAction builds the user message that changes CodeBuddy's goal. The
// queue delivers it; nothing changes until it does.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	return codebuddyGoalRoute.Perform(action, objective)
}

// ObserveGoalCommand updates local state after a delivery.
//
// It refuses only when the CLI positively reported a command list WITHOUT
// /goal. Before the init frame arrives the capability is UNKNOWN, and a refusal
// there would drop the write for a cold-started process that the queue reached
// before its first stdout frame. observeSlashCommands states the same rule for
// the same reason.
//
// The observer is the ONLY writer here: no later frame restates the goal (see
// the file comment), so the row this writes stands until the next command.
func (a *Agent) ObserveGoalCommand(delivery agent.GoalCommandDelivery, text string) {
	a.mu.Lock()
	known, has := a.goalCommandKnown, a.hasGoalCommand
	a.mu.Unlock()
	if known && !has {
		return
	}
	codebuddyGoalRoute.Observe(a.sink, delivery, text)
}
