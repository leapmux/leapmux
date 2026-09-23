package agent

import (
	"errors"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/validate"
)

// The session goal: a standing objective the agent keeps working toward, which
// a check at the end of every turn re-tests until the condition holds.
//
// Several command-line interfaces (CLIs) have this feature and use different
// wire shapes. LeapMux reads structured reports from Codex, ZCode, Claude Code,
// and Reasonix. It observes delivered goal commands for Claude Code, Goose,
// and Copilot. Cursor exposes no client-write route.
//
// There is at most ONE goal per agent, because every one of those CLIs enforces
// that itself: Codex keys thread_goals by thread_id, ZCode keys its target by
// sessionID, Claude Code holds a single activeGoal. Nothing here is a list.

// GoalStatus is the neutral status: a DEFINED type over
// leapmuxv1.AgentGoalStatus, so this package, the agents.goal_status column and
// the browser share one numbering. GoalStatusNone IS the proto's UNSPECIFIED,
// so the zero value still means "no goal", which is what a caller with an empty
// struct wants.
//
// Four values describe provider goal states. GoalStatusNone describes an absent goal.
// Providers use different status vocabularies, so StatusDetail retains the provider's own status.
// GoalStatusDormant describes a stored goal whose provider process no longer runs.
type GoalStatus leapmuxv1.AgentGoalStatus

const (
	GoalStatusNone   = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED)
	GoalStatusActive = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE)
	GoalStatusPaused = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_PAUSED)
	// GoalStatusBlocked is "not progressing, needs the user": Codex's blocked,
	// usageLimited and budgetLimited; ZCode's notSatisfied and failed;
	// Reasonix's blocked and stopped.
	GoalStatusBlocked = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_BLOCKED)
	GoalStatusDone    = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DONE)
	// GoalStatusDormant means the objective is stored but no live process pursues it.
	// LeapMux derives this display state from the running-agent map and does not persist it,
	// which the agents.goal_status CHECK enforces by excluding this ordinal.
	// The provider's last stored status remains available for comparison after a restart.
	GoalStatusDormant = GoalStatus(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT)
)

// goalStatusWires maps each status onto the token the goal_updated NOTIFICATION
// PAYLOAD carries. The empty token is GoalStatusNone, so a cleared goal and a
// never-set goal read back identically.
//
// The tokens come from the contract, because the BROWSER reads the same ones:
// the worker ships goal_status inside the goal_updated notification payload and
// the transcript renderer narrows it.
//
// This is NOT the storage format. agents.goal_status holds the ordinal
// directly, so the column and this vocabulary no longer have to be kept in step
// by hand -- which is what the contract readme used to ask for, and what a
// renumber used to break silently in a third place nothing generated.
var goalStatusWires = map[GoalStatus]string{
	GoalStatusNone:    contracts.GoalStatusTokenNone,
	GoalStatusActive:  contracts.GoalStatusTokenActive,
	GoalStatusPaused:  contracts.GoalStatusTokenPaused,
	GoalStatusBlocked: contracts.GoalStatusTokenBlocked,
	GoalStatusDone:    contracts.GoalStatusTokenDone,
	GoalStatusDormant: contracts.GoalStatusTokenDormant,
}

// GoalStatusWire returns the token the goal_updated notification payload
// carries for s.
//
// An unmapped status yields "", because that is what a Go map miss gives, and
// the browser reads "" as a goal it cannot act on -- so a status missing from
// goalStatusWires reaches the transcript as a goal with no state rather than as
// a parse failure. Keep every GoalStatus constant in goalStatusWires.
func GoalStatusWire(s GoalStatus) string { return goalStatusWires[s] }

// GoalStatusFromWire is the inverse, for the one caller that has a token rather
// than a status: the ZCode plugin, which matches a provider word against this
// vocabulary. An unrecognized token reads as GoalStatusNone rather than an
// error, because a goal whose status cannot be understood must not offer
// controls that act on it.
func GoalStatusFromWire(wire string) GoalStatus {
	for status, token := range goalStatusWires {
		if token == wire {
			return status
		}
	}
	return GoalStatusNone
}

// GoalStatusToProto projects onto the wire enum the browser reads. The two
// numberings are the same one, so this is a cast; it stays a named function
// because every caller reads better for saying what it projects onto.
func GoalStatusToProto(s GoalStatus) leapmuxv1.AgentGoalStatus {
	return leapmuxv1.AgentGoalStatus(s)
}

// GoalUpdate is one provider's report of the current goal.
//
// Every progress counter is a POINTER, because absent and zero are different
// answers and no two providers report the same set: Codex sends tokens and
// seconds but no iteration count, ZCode sends seconds and an iteration but no
// tokens, Copilot sends none. A flat struct of values would render "0 tokens
// used" for a provider that never mentioned tokens.
//
// Claude Code deserves a specific warning: its frame carries `tokens_at_start`,
// which is a STARTING BALANCE, not usage. Putting it in TokensUsed would print
// a number meaning the opposite of its label, so the Claude parser leaves
// TokensUsed nil.
type GoalUpdate struct {
	// NativeID distinguishes provider goals that have the same objective text.
	NativeID     string
	Objective    string
	Status       GoalStatus
	StatusDetail string
	// CreatedAt is part of the goal's IDENTITY. Codex puts no goal id on the
	// wire, so a goal replaced with the same objective text is distinguishable
	// only by a fresh CreatedAt -- without it, "restart this objective" looks
	// like no change at all and never reaches the transcript.
	CreatedAt time.Time

	TokensUsed      *int64
	TokenBudget     *int64
	TimeUsedSeconds *int64
	Iterations      *int32

	// Snapshot marks a report that RESTATES the goal rather than announcing a
	// change: Codex pushes one unsolicited on every thread/resume, marked by a
	// null turnId. It updates state and writes NO transcript row, because
	// persisting it would announce "Goal set: X" at restart time for a goal set
	// an hour ago -- a lie about when it happened.
	Snapshot bool
}

// Clean caps and sanitizes the provider-written text. It runs at the sink
// boundary, so no caller has to remember.
//
// StripUnreadable is the right rule rather than CleanName: an objective is
// PROSE that a user or a model wrote, and it keeps its line breaks (see that
// function's doc -- whitespace survives, and only non-whitespace controls go).
// A rule that folded whitespace would reflow the paragraph the user typed.
//
// Dropping the invalid bytes is not cosmetic. ONE invalid byte makes
// proto.Marshal fail for the WHOLE AgentGoalChanged message, and that message
// is the only way the panel ever populates -- so a single bad byte from one
// provider would leave an empty panel forever with nothing in the log to
// explain it. This is the same hazard bgtask.wireString exists to prevent.
//
// It also resolves the two contradictory reports, in OPPOSITE directions,
// because a goal needs both halves and each half decides what the other means.
//
// An objective with NO status becomes Blocked. GoalStatusNone means "no goal",
// so a report that states both says a goal exists and does not. Every provider
// already resolves an unrecognized status word to Blocked for the same reason
// -- a state this build cannot read is one it must not offer Pause for -- and
// doing it here means a NEW provider that forgets the mapping inherits the rule
// instead of storing the contradiction. A stored objective with a blank status
// is also the exact mark the applier reads as "this goal outlived a worker
// restart", so a provider able to write that state by hand would silence a real
// transition.
//
// A status with NO objective becomes no goal at all, and drops the counters
// with it. Three routes reach that state: ZCode returns early only when BOTH
// halves are empty, Reasonix sends an absent `objective` as "" while its state
// machine starts, and StripUnreadable above empties an objective made only of
// control characters. The card renders a goal from the status alone, so without
// this the panel shows an empty objective line with an active status indicator and
// live Pause and Clear buttons -- a goal with no text that the user cannot read
// and did not set.
func (u GoalUpdate) Clean() GoalUpdate {
	// The caps come from the contract, because the BROWSER enforces the same
	// objective limit on its own input. The objective is written by a model or
	// by a user and reaches a proto string and a database column, so it needs
	// the cap every other provider-chosen label carries (see
	// bgtask.LabelByteLimit); the status detail is a WORD, and anything longer
	// is a provider sending prose down a field the card renders inline.
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

// GoalActionFromProto maps the wire enum. The unspecified action has no
// meaning, so it reports false rather than defaulting to one of the four.
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

// GoalOutcome is what one goal action left for the caller to do.
//
// A side-band provider performed the action, and returns the zero value. A
// text-route provider built a user message that changes nothing until the
// durable input queue delivers it, and returns it in QueuedInput.
type GoalOutcome struct {
	// QueuedInput is an explicit provider command that the caller must enqueue.
	// Empty means the action already took effect. Never use an ordinary prompt
	// to resume a paused goal.
	QueuedInput string
}

// GoalWriter is implemented by each provider that can be TOLD to change its
// goal. One interface, whichever route the provider uses, so no caller chooses
// between two and no provider can implement both and lose one silently.
//
// Not every provider that reports a goal can change it:
//
//   - Codex and ZCode have a real side-band command (thread/goal/set,
//     session/goal). A set starts a turn for both providers. ZCode pause also
//     stops the current turn. They perform all four actions at once.
//   - Claude Code, Goose, and Copilot have only a user-message command. They
//     return it as QueuedInput, and observe it after the queue delivers it.
//     GoalTextCommander is how they build and observe that text.
//   - Reasonix sets its mode and submits the objective under one session lock.
//     Normal mode clears its goal. ACP provides no Pause or Resume operation.
//
// SupportedGoalActions is what the browser reads to disable a control, so it
// lives on the same interface as the implementations and cannot drift from
// them. This mirrors AgentInfo.accepts_messages, which is decided the same way
// (a type assertion on the running agent) for the same reason.
type GoalWriter interface {
	GoalCapable

	// PerformGoalAction uses a verified provider command or RPC for the action.
	// The provider decides whether this starts a turn and requires queued delivery.
	// A natural-language prompt cannot substitute for a Goal resume operation.
	PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error)
}

// GoalCommandDelivery identifies the channel that carried a goal command to the
// provider process. A provider reads its own command parser on one channel and
// not always on the other, so the observer must know which one delivered.
type GoalCommandDelivery int

const (
	// GoalDeliverySend is the provider's ordinary user-message channel.
	GoalDeliverySend GoalCommandDelivery = iota
	// GoalDeliverySteer interrupts the active turn with more text.
	GoalDeliverySteer
)

// GoalTextCommander owns a provider's user-message command syntax.
//
// It is NOT a second write route beside GoalWriter: a text-route provider
// implements both, and its PerformGoalAction returns the text this builds. The
// Manager asserts this one only to report a DELIVERY back, which a side-band
// provider has no use for.
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
