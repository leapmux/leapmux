package agent

import (
	"errors"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/validate"
)

// A session goal is a standing objective that the agent checks after each turn.
// The checks continue until the goal's condition holds.
//
// Several command-line interfaces (CLIs) have this feature and use different
// wire shapes. LeapMux reads structured reports from these CLIs:
//
//   - Codex.
//   - ZCode.
//   - Claude Code.
//   - GitHub Copilot.
//   - Pi.
//   - Reasonix.
//   - Codewhale.
//   - Kimi Code.
//   - MiMo Code.
//   - Qwen Code.
//   - Grok Build.
//   - Kiro, which runs its goal as a workflow and reports the workflow.
//   - Oh My Pi, which reports its goal and takes no change.
//
// These providers change their goal through a delivered message:
//   - Claude Code.
//   - Goose.
//   - Kilo.
// Cursor and OpenCode have no goal.
//
// Each CLI permits at most one goal per agent:
//
//   - Codex keys thread_goals by thread_id.
//   - ZCode keys its target by sessionID.
//   - Claude Code holds a single activeGoal.
//   - MiMo Code keys its goals by session ID.
//
// Nothing here is a list.

// GoalStatus is a defined type over leapmuxv1.AgentGoalStatus.
// The Worker and browser use the same enum ordinals as the agents.goal_status column.
// GoalStatusNone equals the protobuf UNSPECIFIED value, so zero means no goal.
//
// Five values describe provider goal states. GoalStatusNone describes an absent goal.
// Providers use different status vocabularies, so StatusDetail retains the provider's own status.
// GoalStatusDormant describes a stored goal whose provider process no longer runs.
type GoalStatus leapmuxv1.AgentGoalStatus

const (
	GoalStatusNone   = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED)
	GoalStatusActive = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE)
	GoalStatusPaused = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_PAUSED)
	// GoalStatusBlocked means that the goal cannot continue without user action:
	//
	//   - Codex's blocked.
	//   - Codex's usageLimited.
	//   - Codex's budgetLimited.
	//   - ZCode's notSatisfied and failed.
	//   - Reasonix's blocked and stopped.
	//   - MiMo Code's impossible verdict.
	//   - MiMo Code's limit on re-entry.
	//   - A failed MiMo Code judge.
	GoalStatusBlocked = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_BLOCKED)
	GoalStatusDone    = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DONE)
	// GoalStatusUnknown retains a valid native state that LeapMux cannot interpret.
	GoalStatusUnknown = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN)
	// GoalStatusDormant means the objective is stored but no live process pursues it.
	// LeapMux derives this display state from the running-agent map.
	// The agents.goal_status CHECK excludes this ordinal to prevent storage of the derived state.
	// The provider's last stored status remains available for comparison after a restart.
	GoalStatusDormant = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT)
)

// goalStatusWires maps each status to its goal_updated notification token.
// The empty token represents GoalStatusNone for both a cleared and a never-set goal.
// The contract supplies the tokens because the browser reads the same notification payload.
// Storage uses the enum ordinal directly. These tokens are not the storage format.
var goalStatusWires = map[GoalStatus]string{
	GoalStatusNone:    contracts.GoalStatusTokenNone,
	GoalStatusActive:  contracts.GoalStatusTokenActive,
	GoalStatusPaused:  contracts.GoalStatusTokenPaused,
	GoalStatusBlocked: contracts.GoalStatusTokenBlocked,
	GoalStatusDone:    contracts.GoalStatusTokenDone,
	GoalStatusUnknown: contracts.GoalStatusTokenUnknown,
	GoalStatusDormant: contracts.GoalStatusTokenDormant,
}

// GoalStatusWire returns the goal_updated notification token for s.
// An unmapped status returns an empty token, which the browser reads as a goal without controls.
// Keep every GoalStatus constant in goalStatusWires to prevent a silent omission.
func GoalStatusWire(s GoalStatus) string { return goalStatusWires[s] }

// GoalStatusFromWire converts a notification token to its status.
// The ZCode plugin uses this vocabulary to read a provider word.
// An unrecognized token returns GoalStatusNone because the browser must disable controls for that state.
func GoalStatusFromWire(wire string) GoalStatus {
	for status, token := range goalStatusWires {
		if token == wire {
			return status
		}
	}
	return GoalStatusNone
}

// GoalStatusToProto converts the status to the browser's wire enum.
// Both types use the same ordinals, so the conversion is a cast.
func GoalStatusToProto(s GoalStatus) leapmuxv1.AgentGoalStatus {
	return leapmuxv1.AgentGoalStatus(s)
}

// GoalUpdate is one provider's report of the current goal.
//
// Every counter is a pointer because absent and zero are different answers.
// Providers report different counter sets:
//   - Codex reports tokens and seconds without an iteration count.
//   - ZCode reports seconds and an iteration count without tokens.
//   - Copilot reports no counters.
//
// Flat values would show zero tokens for a provider that reports no token count.
//
// Claude Code reports tokens_at_start, which is a starting balance rather than usage.
// The Claude parser leaves TokensUsed nil because that balance cannot describe tokens used.
type GoalUpdate struct {
	// NativeID distinguishes provider goals that have the same objective text.
	NativeID     string
	Objective    string
	Status       GoalStatus
	StatusDetail string
	// CreatedAt distinguishes goals when the provider supplies no native ID.
	// Codex uses a new creation time for a restarted goal with the same objective.
	// Without it, that restart produces no transcript transition.
	CreatedAt time.Time

	TokensUsed      *int64
	TokenBudget     *int64
	TimeUsedSeconds *int64
	Iterations      *int32

	// Snapshot marks a historical report that updates state without a transcript notification.
	// Codex sends one on thread/resume with a null turnId.
	// A new notification would incorrectly announce an old goal at restart time.
	Snapshot bool
}

// Clean restricts provider text and removes unreadable bytes at the sink boundary.
// StripUnreadable preserves whitespace and line breaks because the objective is prose.
// CleanName would change the paragraph that the user supplies.
// One invalid byte makes proto.Marshal reject the complete AgentGoalChanged message.
// Without this repair, a provider byte could keep the goal panel empty without an explanatory log.
//
// An objective with GoalStatusNone becomes Blocked because None means no goal.
// This shared rule prevents the contradictory report from reaching storage.
// Providers interpret their native status words before this cleaner runs.
// Unknown remains distinct from a missing status.
//
// A status without an objective becomes no goal and drops its counters.
// An empty objective can come from these inputs:
//   - ZCode reports one while its status remains present.
//   - Reasonix reports one while its state machine starts.
//   - StripUnreadable removes an objective that contains only control characters.
//
// Without this rule, the card could show live controls for an unreadable goal that the user did not set.
func (u GoalUpdate) Clean() GoalUpdate {
	// The contract supplies the limits because the browser applies the same objective limit.
	// The objective reaches a protobuf string and a database column.
	// The detail appears inline on the card and uses its own byte limit.
	u.Objective = validate.StripUnreadable(u.Objective, contracts.GoalObjectiveByteLimit)
	u.StatusDetail = validate.StripUnreadable(u.StatusDetail, contracts.GoalStatusDetailByteLimit)
	if u.Objective != "" && u.Status == GoalStatusNone {
		u.Status = GoalStatusBlocked
	}
	if u.Objective == "" {
		u.NativeID = ""
		u.Status = GoalStatusNone
		u.StatusDetail = ""
		u.TokensUsed = nil
		u.TokenBudget = nil
		u.TimeUsedSeconds = nil
		u.Iterations = nil
	}
	return u
}

// GoalAction is one operation a client can ask for on the goal.
type GoalAction int

const (
	GoalActionSet GoalAction = iota
	GoalActionClear
	GoalActionPause
	GoalActionResume
)

// GoalActionFromProto converts a wire action to its domain action.
// The unspecified action returns false because it identifies no operation.
func GoalActionFromProto(a leapmuxv1.AgentGoalAction) (GoalAction, bool) {
	switch a {
	case leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET:
		return GoalActionSet, true
	case leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR:
		return GoalActionClear, true
	case leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE:
		return GoalActionPause, true
	case leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_RESUME:
		return GoalActionResume, true
	default:
		return 0, false
	}
}

// GoalActionToProto projects onto the wire enum.
func GoalActionToProto(a GoalAction) leapmuxv1.AgentGoalAction {
	switch a {
	case GoalActionSet:
		return leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET
	case GoalActionClear:
		return leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR
	case GoalActionPause:
		return leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE
	case GoalActionResume:
		return leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_RESUME
	default:
		return leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_UNSPECIFIED
	}
}

// GoalCapable marks a provider that has a session goal.
// SupportedGoalActions reports what the running process can do now.
type GoalCapable interface {
	SupportedGoalActions() []GoalAction
}

// GoalOutcome states the input that the caller must enqueue after a goal action.
// A provider that completes the action through a separate command returns the zero value.
// Kimi Code's Set returns its objective because its native goal command starts no turn.
// A provider that uses a user message returns that command in QueuedInput.
// That message changes the goal only after the durable input queue delivers it.
type GoalOutcome struct {
	// QueuedInput is an explicit provider command that the caller must enqueue.
	// Empty means the action already took effect. Never use an ordinary prompt
	// to resume a paused goal.
	QueuedInput string
}

// GoalWriter supplies every provider route for a goal change through one interface.
// A caller needs no provider-specific route selection.
//
// Not every provider that reports a goal can change it:
//
//   - Codex and ZCode have a real side-band command (thread/goal/set,
//     session/goal). A set starts a turn for both providers. ZCode pause also
//     stops the current turn. They perform all four actions at once.
//   - GitHub Copilot invokes its native autopilot command as a side-band RPC.
//     Its Clear has no command: it disposes the session and opens it again.
//   - Codewhale writes the goal through its REST route. Set starts a turn.
//     The runtime has no Pause and no Resume.
//   - Kimi Code writes the goal through its session profile. Creating a goal
//     starts no turn, so a set also returns the objective as QueuedInput.
//   - Claude Code returns its user-message command as QueuedInput.
//     GoalTextCommander builds and observes the text after queued delivery.
//   - Goose uses the same user-message route through GoalTextCommander.
//   - Kilo uses the same user-message route through GoalTextCommander.
//   - Qwen Code uses qwen/control/session/goal/control.
//     The command takes effect at once, including during a goal round.
//     Its goal report states the command's result.
//   - Grok Build sends its user-message command to its native prompt queue immediately.
//     Its goal loop occupies one turn and reads a queued prompt before the next round.
//     Its goal report states the command's result, so QueuedInput stays empty.
//   - Pi writes the command of its goal extension to the process directly.
//   - Reasonix sets its mode and submits the objective under one session lock.
//     Normal mode clears its goal.
//     The Agent Client Protocol (ACP) provides no Pause or Resume operation.
//   - MiMo Code has a goal command on its HTTP command route. A set starts a
//     turn whose prompt is the objective. MiMo has no Pause and no Resume.
//   - Kiro returns its goal-setting user-message command as QueuedInput.
//     The goal runs as a workflow. These commands require the reported workflow run ID:
//   - Clear.
//   - Pause.
//   - Resume.
//   - Oh My Pi reports its goal and implements no GoalWriter: no RPC command
//     reaches its goal runtime.
//
// The browser reads SupportedGoalActions to disable controls that the running provider cannot perform.
// The method belongs to the same interface as the operation to keep both decisions together.
// AgentInfo.accepts_messages uses the same type assertion on the running agent.
type GoalWriter interface {
	GoalCapable

	// PerformGoalAction uses a verified provider command or RPC for the action.
	// The provider decides whether this starts a turn and requires queued delivery.
	// A natural-language prompt cannot substitute for a Goal resume operation.
	PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error)
}

// GoalCommandDelivery identifies the channel that delivered the goal command.
// A provider can parse commands on one channel only, so its observer needs the exact delivery channel.
type GoalCommandDelivery int

const (
	// GoalDeliverySend is the provider's ordinary user-message channel.
	GoalDeliverySend GoalCommandDelivery = iota
	// GoalDeliverySteer interrupts the active turn with more text.
	GoalDeliverySteer
)

// GoalTextCommander owns a provider's user-message command syntax.
//
// A provider that uses a user message implements both GoalTextCommander and GoalWriter.
// PerformGoalAction returns the command text that this interface builds.
// The Manager uses this interface only to report delivery of that text.
type GoalTextCommander interface {
	GoalWriter
	// ObserveGoalCommand updates local goal state after a delivery. The
	// provider decides which channels reach its command parser.
	ObserveGoalCommand(delivery GoalCommandDelivery, text string)
}

// ErrGoalObjectiveIsCommand means the provider interprets the objective as a control argument.
// The service maps it to InvalidArgument.
var ErrGoalObjectiveIsCommand = errors.New("this objective is a reserved command argument")

// ErrGoalControlUnsupported means that the provider has no goal route or does
// not support the requested action. The service maps it to FailedPrecondition.
var ErrGoalControlUnsupported = errors.New("agent provider does not support this session-goal action")
