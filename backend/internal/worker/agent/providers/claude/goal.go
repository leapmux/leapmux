package claude

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Claude Code's session goal.
//
// `/goal <condition>` installs a session-scoped Stop hook whose body is the
// condition. At the end of every turn a separate model call decides whether the
// condition holds; while it does not, the hook blocks stopping and Claude keeps
// working. The goal auto-clears once the condition is met.
//
// The wire frame is a first-class member of Claude's StdoutMessage union:
//
//	{"type":"active_goal",
//	 "value":{"condition":str,"iterations":int,"set_at":int,
//	          "tokens_at_start":int,"last_reason":str?} | null,
//	 "uuid":str,"session_id":str}
//
// Claude Code 2.1.302 emits active_goal only under CLAUDE_CODE_REMOTE.
// LeapMux's local launch receives no such frame, even with stream-json and verbose output.
// The command observer records delivered /goal text. The browser shows the objective without an unconfirmed status.
// handleActiveGoal remains available for a build that sends this frame.
//
// A null `value` means the goal is gone -- met, impossible, or cleared by the
// user. Claude states no status enum on this frame, so a present value is an
// active goal and a null one is no goal.
//
// The envelope type itself is claudeMsgTypeActiveGoal, declared with its
// siblings in output.go.

// claudeGoalCommand is the slash command that sets or clears the goal.
//
// It is the ONLY write Claude Code offers. The control protocol has no goal
// method -- its request allowlist covers set_model, set_permission_mode,
// interrupt and eleven others, and none of them touch the goal -- so a client
// changes the goal exactly the way a user does, by sending the text.
const claudeGoalCommand = "/goal"

// claudeGoalRoute is Claude Code's user-message goal vocabulary. Claude accepts
// six words that clear; LeapMux emits `clear`, the one that its own help text gives.
//
// SteerCarriesCommand is true because Agent.SteerInput writes the
// same user message on the same stdin channel, with priority "next". A steered
// `/goal ...` therefore reaches the same command parser.
var claudeGoalRoute = providerkit.GoalTextRoute{
	Provider:            "claude",
	Command:             claudeGoalCommand,
	ClearArgs:           []string{"clear", "stop", "off", "reset", "none", "cancel"},
	SteerCarriesCommand: true,
}

var _ agent.GoalTextCommander = (*Agent)(nil)

type claudeActiveGoalFrame struct {
	// A POINTER: null is the signal that the goal is gone, and it must be
	// distinguishable from a frame that merely omitted the field.
	Value *claudeActiveGoalValue `json:"value"`
}

type claudeActiveGoalValue struct {
	Condition  string `json:"condition"`
	Iterations *int32 `json:"iterations"`
	SetAt      int64  `json:"set_at"`
	LastReason string `json:"last_reason"`
	// TokensAtStart is deliberately NOT read. It is the token balance when the
	// goal was set -- a STARTING BALANCE, not consumption -- so reporting it as
	// the goal's token usage would show a number meaning the opposite of its
	// label, and it would grow with the context rather than with the work.
}

// handleActiveGoal reports Claude's goal frame to the sink.
func (a *Agent) handleActiveGoal(content []byte) {
	var frame claudeActiveGoalFrame
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("claude active_goal parse", "agent_id", a.AgentID(), "error", err)
		return
	}
	if frame.Value == nil {
		// Claude Code marks no restatement of its own: the CLI emits active_goal
		// only when the goal actually changes, and it emits nothing at all until
		// it receives input. So a null frame is always a real removal.
		a.sink.ClearGoal(false)
		return
	}
	value := frame.Value
	a.sink.UpsertGoal(agent.GoalUpdate{
		Objective: value.Condition,
		// A present value is a goal still being pursued. Claude clears the goal
		// the moment the per-turn check passes, so "active" is the only state
		// this frame can describe.
		Status: agent.GoalStatusActive,
		// The evaluator's reason for the last "not yet" is the most useful thing
		// Claude knows about the goal, and it is what its own overlay panel
		// shows under "Last check".
		StatusDetail: value.LastReason,
		CreatedAt:    claudeGoalTime(value.SetAt),
		Iterations:   value.Iterations,
		// No token or time counters: see TokensAtStart above. Claude reports no
		// elapsed time on this frame either, so both stay absent rather than
		// being invented from set_at -- the panel omits a row it has no number
		// for.
	})
}

// claudeGoalTime converts Claude's set_at, which is Unix MILLISECONDS
// (Date.now()). Zero means absent.
func claudeGoalTime(unixMillis int64) time.Time {
	if unixMillis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(unixMillis).UTC()
}

// observeSlashCommands records whether THIS Claude build has /goal.
//
// It reads slash_commands from the system init frame at the start of each turn.
// Claude sends no init before the first user message. The list states which commands this build accepts.
// Without the list, a goal button could send literal /goal text to a build that does not support the command.
//
// A frame that carries no list at all leaves the answer alone rather than
// clearing it, so a future shape change degrades to "unknown" instead of
// silently disabling a working feature.
func (a *Agent) observeSlashCommands(content []byte) {
	var frame struct {
		Subtype       string   `json:"subtype"`
		SlashCommands []string `json:"slash_commands"`
	}
	if err := json.Unmarshal(content, &frame); err != nil || frame.Subtype != contracts.ClaudeHiddenSystemSubtypeInit {
		return
	}
	if len(frame.SlashCommands) == 0 {
		return
	}
	a.applySlashCommandNames(frame.SlashCommands)
}

// observeChangedCommands replaces the slash-command list from a
// `commands_changed` frame.
//
// The frame's own contract tells a client to REPLACE its cached command list
// with this payload. A present list updates /goal availability even when it drops the command.
//
// Each element is a command descriptor from the command line interface.
// Its shape is {name, description, argumentHint, aliases?, builtin?, plugin?}.
// Only `name` matters here.
// It carries the bare command name without the leading slash, exactly like `slash_commands` in the init frame.
//
// A frame without a list leaves the current answer unchanged.
// An unknown replacement shape must not silently disable a working feature.
func (a *Agent) observeChangedCommands(content []byte) {
	var frame struct {
		Subtype  string          `json:"subtype"`
		Commands json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(content, &frame); err != nil || frame.Subtype != contracts.ClaudeHiddenSystemSubtypeCommandsChanged {
		return
	}
	// An absent or null commands field keeps the current list.
	// An array replaces the list, including an empty array.
	if providerkit.RawKindOf(frame.Commands) != providerkit.RawValue {
		return
	}
	var commands []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(frame.Commands, &commands); err != nil {
		slog.Warn("invalid claude commands_changed JSON", "agent_id", a.AgentID(), "error", err)
		return
	}
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	a.applySlashCommandNames(names)
}

// applySlashCommandNames records whether THIS Claude build has /goal.
//
// The list carries bare names without the leading slash.
// A change publishes the goal capability again because registration precedes the first turn's init frame.
// Without that publish, the browser keeps an empty action list and hides the Set a goal button.
func (a *Agent) applySlashCommandNames(names []string) {
	has := slices.Contains(names, strings.TrimPrefix(claudeGoalCommand, "/"))
	a.Mu.Lock()
	changed := a.hasGoalCommand != has || !a.goalCommandKnown
	a.hasGoalCommand = has
	a.goalCommandKnown = true
	a.Mu.Unlock()
	if !changed {
		return
	}
	a.sink.PublishGoalCapabilities()
}

// --- GoalTextCommander ---

// SupportedGoalActions: set and clear, and only when the CLI actually has the
// command.
//
// Claude Code has no pause and no resume. The two supported actions cost a
// turn, because the only write is a user-message command.
//
// The answer comes from the running process's own `slash_commands` (see
// observeSlashCommands), never from a version table: /goal shipped in 2.1.139,
// and a table would offer a control that does nothing against an older build.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	a.Mu.Lock()
	has := a.hasGoalCommand
	a.Mu.Unlock()
	if !has {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}
}

// PerformGoalAction builds the user message that changes Claude's goal. The
// queue delivers it; nothing changes until it does.
//
// The local Claude launch emits no active_goal frame, so the command observer writes the objective.
// LeapMux cannot confirm later evaluations or automatic removal. The browser therefore shows no live goal status.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	return claudeGoalRoute.Perform(action, objective)
}

// ObserveGoalCommand updates local state after a delivery.
//
// It refuses only when the CLI positively reported a command list WITHOUT
// /goal. Before the init frame arrives the capability is UNKNOWN, and a refusal
// there would drop the write for a cold-started process that the queue reached
// before its first stdout frame. observeSlashCommands states the same rule for
// the same reason.
func (a *Agent) ObserveGoalCommand(delivery agent.GoalCommandDelivery, text string) {
	a.Mu.Lock()
	known, has := a.goalCommandKnown, a.hasGoalCommand
	a.Mu.Unlock()
	if known && !has {
		return
	}
	claudeGoalRoute.Observe(a.sink, delivery, text)
}
