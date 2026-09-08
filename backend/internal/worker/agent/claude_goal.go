package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
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
// It arrives because StartClaudeCode launches with --output-format stream-json
// --verbose, and the headless writer emits every drained frame unfiltered under
// those two flags.
//
// A null `value` means the goal is gone -- met, impossible, or cleared by the
// user. Claude states no status enum on this frame, so a present value is an
// active goal and a null one is no goal.
//
// The envelope type itself is claudeMsgTypeActiveGoal, declared with its
// siblings in claude_output.go.

// claudeSystemSubtypeInit is the `subtype` of the `system` frame Claude Code
// emits once at startup. It carries `slash_commands`, the list of commands THIS
// build actually has.
const claudeSystemSubtypeInit = "init"

// claudeGoalCommand is the slash command that sets or clears the goal.
//
// It is the ONLY write Claude Code offers. The control protocol has no goal
// method -- its request allowlist covers set_model, set_permission_mode,
// interrupt and eleven others, and none of them touch the goal -- so a client
// changes the goal exactly the way a user does, by sending the text.
const claudeGoalCommand = "/goal"

// claudeGoalClearArguments lists each complete argument that clears a goal.
// LeapMux emits the first value and observes the complete list.
var claudeGoalClearArguments = []string{"clear", "stop", "off", "reset", "none", "cancel"}

var _ GoalTextCommander = (*ClaudeCodeAgent)(nil)

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
func (a *ClaudeCodeAgent) handleActiveGoal(content []byte) {
	var frame claudeActiveGoalFrame
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("claude active_goal parse", "agent_id", a.agentID, "error", err)
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
	a.sink.UpsertGoal(GoalUpdate{
		Objective: value.Condition,
		// A present value is a goal still being pursued. Claude clears the goal
		// the moment the per-turn check passes, so "active" is the only state
		// this frame can describe.
		Status: GoalStatusActive,
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
// It reads the `system` init frame's `slash_commands`, which is the CLI's own
// statement of what it can do. Without it the capability would be a guess:
// /goal shipped in 2.1.139, and against an older build the panel would offer a
// button whose only effect is sending the literal text "/goal ..." to the
// model as a prompt.
//
// A frame that carries no list at all leaves the answer alone rather than
// clearing it, so a future shape change degrades to "unknown" instead of
// silently disabling a working feature.
func (a *ClaudeCodeAgent) observeSlashCommands(content []byte) {
	var frame struct {
		Subtype       string   `json:"subtype"`
		SlashCommands []string `json:"slash_commands"`
	}
	if err := json.Unmarshal(content, &frame); err != nil || frame.Subtype != claudeSystemSubtypeInit {
		return
	}
	if len(frame.SlashCommands) == 0 {
		return
	}
	// The list carries bare names, without the leading slash.
	has := slices.Contains(frame.SlashCommands, strings.TrimPrefix(claudeGoalCommand, "/"))
	a.mu.Lock()
	changed := a.hasGoalCommand != has
	a.hasGoalCommand = has
	a.mu.Unlock()
	if !changed {
		return
	}
	// Re-publish, because the capability just changed and the Manager's single
	// publish at registration already ran with the old answer. Nothing orders
	// this stdout frame against the control response the startup handshake
	// waits for, so the frame can arrive after the agent registers. Without
	// this the browser keeps an empty action list and the goal card hides its
	// "Set a goal" button.
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
func (a *ClaudeCodeAgent) SupportedGoalActions() []GoalAction {
	a.mu.Lock()
	has := a.hasGoalCommand
	a.mu.Unlock()
	if !has {
		return nil
	}
	return []GoalAction{GoalActionSet, GoalActionClear}
}

// GoalCommandText builds the user message that changes Claude's goal.
//
// Claude does not emit an active_goal frame when it accepts the command. The
// observer fills that gap after delivery. A later active_goal frame can still
// report evaluation or removal.
func (a *ClaudeCodeAgent) GoalCommandText(action GoalAction, objective string) (string, error) {
	switch action {
	case GoalActionSet:
		objective = foldGoalObjective(objective)
		if objective == "" {
			return "", fmt.Errorf("claude %s: an objective is required", claudeGoalCommand)
		}
		return claudeGoalCommand + " " + objective, nil
	case GoalActionClear:
		return claudeGoalCommand + " " + claudeGoalClearArguments[0], nil
	default:
		return "", ErrGoalControlUnsupported
	}
}

// ObserveGoalCommand updates local state after the queue delivers a command.
func (a *ClaudeCodeAgent) ObserveGoalCommand(text string) {
	a.mu.Lock()
	has := a.hasGoalCommand
	a.mu.Unlock()
	if !has {
		return
	}
	intent, objective := parseGoalCommandText(text, claudeGoalCommand, claudeGoalClearArguments)
	switch intent {
	case goalTextSet:
		a.sink.UpsertGoal(GoalUpdate{
			Objective: objective,
			Status:    GoalStatusActive,
			CreatedAt: time.Now().UTC(),
		})
	case goalTextClear:
		a.sink.ClearGoal(false)
	case goalTextNotCommand, goalTextBareQuery:
		// These inputs do not change the goal.
	}
}
