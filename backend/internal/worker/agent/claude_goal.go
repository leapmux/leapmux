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

// claudeGoalClearArgument is the word that clears. Claude accepts clear, stop,
// off, reset, none and cancel; one of them is enough, and `clear` is the one
// its own help text names.
const claudeGoalClearArgument = "clear"

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
		a.sink.ClearGoal()
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
	a.hasGoalCommand = has
	a.mu.Unlock()
}

// --- GoalController ---

// SupportedGoalActions: set and clear, and only when the CLI actually has the
// command.
//
// Claude Code has no pause and no resume -- the feature does not exist in the
// CLI, so there is nothing to call. The two it does support cost a TURN,
// because the only write is sending the command as user input; that is why they
// go through SendInput rather than a side-band request, and why the transcript
// shows the message that caused the change.
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

func (a *ClaudeCodeAgent) SetGoal(objective string) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return fmt.Errorf("claude %s: an objective is required", claudeGoalCommand)
	}
	// A newline would end the command and send the rest as a second line, so a
	// multi-line objective has to be folded. Claude reads the whole remainder of
	// the line as the condition.
	objective = strings.Join(strings.Fields(objective), " ")
	return a.SendInput(claudeGoalCommand+" "+objective, nil)
}

func (a *ClaudeCodeAgent) ClearGoal() error {
	return a.SendInput(claudeGoalCommand+" "+claudeGoalClearArgument, nil)
}

// PauseGoal and ResumeGoal exist to satisfy GoalController and always refuse.
// SupportedGoalActions does not list them, and Manager.UpdateGoal checks that
// list before dispatching, so these are unreachable through the RPC -- they are
// the compile-time proof that the capability list and the implementations
// describe the same provider.
func (a *ClaudeCodeAgent) PauseGoal() error  { return ErrGoalControlUnsupported }
func (a *ClaudeCodeAgent) ResumeGoal() error { return ErrGoalControlUnsupported }
