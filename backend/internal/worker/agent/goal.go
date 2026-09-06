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
// Several CLIs have this feature and each spells it differently. LeapMux reads
// four of them today -- Codex's thread/goal/updated, ZCode's session-snapshot
// `goal`, Claude Code's active_goal stdout frame, and Reasonix's status_update.
// This file holds the neutral shape they reduce to, so the service layer and
// the browser learn one vocabulary instead of four.
//
// Copilot, Goose and Cursor also have a session goal and LeapMux reads none of
// them: Copilot keeps one `current` autopilot objective, Goose holds a `/goal`
// and a separate `/grind` slot, and Cursor keeps one `goalState`. Each would
// need its own reader here; nothing else in this file changes for them.
//
// There is at most ONE goal per agent, because every one of those CLIs enforces
// that itself: Codex keys thread_goals by thread_id, ZCode keys its target by
// sessionID, Claude Code holds a single activeGoal. Nothing here is a list.

// GoalStatus is the neutral status. It mirrors leapmuxv1.AgentGoalStatus with a
// friendlier zero value: the zero GoalStatus means "no goal", which is what a
// caller with an empty struct wants.
//
// Five values, deliberately. Four come from the providers, and the UI branches
// three ways on them (pause iff active, resume iff paused, clear always). The
// providers' own enums do not agree -- Codex has six words, ZCode has six
// different ones -- so a neutral value per provider word would claim a
// precision the mapping cannot deliver, and the provider's own word travels
// beside this as GoalUpdate.StatusDetail.
//
// GoalStatusDormant is the fifth, and no provider reports it. LeapMux writes it
// when the process that pursued the goal is gone. See its comment below.
type GoalStatus int

const (
	GoalStatusNone GoalStatus = iota
	GoalStatusActive
	GoalStatusPaused
	// GoalStatusBlocked is "not progressing, needs the user": Codex's blocked,
	// usageLimited and budgetLimited; ZCode's notSatisfied and failed;
	// Reasonix's blocked and stopped.
	GoalStatusBlocked
	GoalStatusDone
	// GoalStatusDormant is "the objective is stored, and no live process is
	// pursuing it". LeapMux writes it, never a provider: at worker boot for
	// every goal that outlived the process, and when one agent's process exits.
	//
	// It exists because the two alternatives both lie. Keeping the last status
	// draws a live Active dot and working Pause and Clear buttons for a process
	// that no longer exists, and blanking the status projects the enum's zero
	// value, which the browser can only read as "a status this build does not
	// understand" -- so a goal that is merely waiting renders as a fault.
	GoalStatusDormant
)

// goalStatusWires maps each status onto the token stored in agents.goal_status.
// The empty token is GoalStatusNone, so a cleared goal and a never-set goal read
// back identically.
//
// The tokens come from the contract, because the BROWSER reads the same ones:
// the worker ships goal_status inside the goal_updated notification payload and
// the transcript renderer narrows it. The agents.goal_status CHECK constraint is
// a third spelling that the generator cannot emit -- keep it in step by hand.
var goalStatusWires = map[GoalStatus]string{
	GoalStatusNone:    contracts.GoalStatusTokenNone,
	GoalStatusActive:  contracts.GoalStatusTokenActive,
	GoalStatusPaused:  contracts.GoalStatusTokenPaused,
	GoalStatusBlocked: contracts.GoalStatusTokenBlocked,
	GoalStatusDone:    contracts.GoalStatusTokenDone,
	GoalStatusDormant: contracts.GoalStatusTokenDormant,
}

// GoalStatusWire returns the token persisted in agents.goal_status.
//
// An unmapped status yields "", because that is what a Go map miss gives, and
// the column's CHECK constraint accepts "". So the write SUCCEEDS and stores a
// non-empty objective beside a blank status, which every reader then takes as
// "no goal": GoalStatusFromWire answers GoalStatusNone, the projection sends
// AGENT_GOAL_STATUS_UNSPECIFIED, and the card draws the goal as one it cannot
// act on. Keep every GoalStatus constant in goalStatusWires.
func GoalStatusWire(s GoalStatus) string { return goalStatusWires[s] }

// GoalStatusFromWire is the inverse. An unrecognized token reads as
// GoalStatusNone rather than an error: the only way one reaches the column is a
// downgrade, and a goal whose status cannot be understood must not offer
// controls that act on it.
func GoalStatusFromWire(wire string) GoalStatus {
	for status, token := range goalStatusWires {
		if token == wire {
			return status
		}
	}
	return GoalStatusNone
}

// GoalStatusToProto projects onto the wire enum the browser reads.
func GoalStatusToProto(s GoalStatus) leapmuxv1.AgentGoalStatus {
	switch s {
	case GoalStatusActive:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE
	case GoalStatusPaused:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_PAUSED
	case GoalStatusBlocked:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_BLOCKED
	case GoalStatusDone:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DONE
	case GoalStatusDormant:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DORMANT
	default:
		return leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED
	}
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
// this the panel shows an empty objective line with an armed status dot and
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

// GoalController is optionally implemented by a running Agent whose provider
// can be TOLD to change the goal. Reading a goal is separate and needs nothing
// here -- a provider that only reports its goal implements none of this and the
// browser disables every control.
//
// Not every provider that reports a goal can be told to change it, and the gap
// is not laziness:
//
//   - Codex and ZCode have a real side-band command (thread/goal/set,
//     session/goal) that is acknowledged and starts no turn. They implement all
//     four actions.
//   - Claude Code has no control-protocol method at all. Its only write is
//     sending the literal text "/goal ..." as a user turn, which costs tokens
//     and shows in the transcript. It implements Set and Clear that way -- the
//     transcript row is a feature, because it shows the cause of the change --
//     and has no pause or resume to implement.
//   - Reasonix implements none. Setting means switching to its "goal" mode and
//     hijacking the next prompt, and CLEARING means switching back to whichever
//     mode the session was in before -- which LeapMux never tracked, so a clear
//     would silently drop the user out of plan mode.
//
// SupportedGoalActions is what the browser reads to disable a control, so it
// lives on the same interface as the implementations and cannot drift from
// them. This mirrors AgentInfo.accepts_messages, which is decided the same way
// (a type assertion on the running agent) for the same reason.
type GoalController interface {
	// SupportedGoalActions reports the actions THIS agent can perform, in a
	// stable order. A provider whose support depends on the CLI version it
	// launched answers from what that process reported, never from a table.
	SupportedGoalActions() []GoalAction
	// SetGoal replaces the objective. The provider decides whether that starts
	// a turn.
	SetGoal(objective string) error
	ClearGoal() error
	PauseGoal() error
	ResumeGoal() error
}

// ErrGoalControlUnsupported is returned by the Manager when a running Agent
// does not implement GoalController, or implements it without the requested
// action. The service maps it to FailedPrecondition, so a browser acting on a
// stale capability list gets a refusal rather than a silent no-op.
var ErrGoalControlUnsupported = errors.New("agent provider does not support this session-goal action")
