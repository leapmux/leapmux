package agent

import (
	"errors"
	"fmt"
	"strings"
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
	// QueuedInput is user-message text the caller MUST enqueue. Empty means the
	// action already took effect.
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
//   - Reasonix implements none of this. Setting changes its mode and takes the
//     next prompt. Clearing must restore a mode that LeapMux did not track.
//
// SupportedGoalActions is what the browser reads to disable a control, so it
// lives on the same interface as the implementations and cannot drift from
// them. This mirrors AgentInfo.accepts_messages, which is decided the same way
// (a type assertion on the running agent) for the same reason.
type GoalWriter interface {
	GoalCapable

	// PerformGoalAction runs one action. The provider decides whether that
	// starts a turn, and whether the caller has anything left to do.
	PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error)
}

// GoalCommandDelivery names the channel that carried a goal command to the
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

type goalTextIntent int

const (
	goalTextNotCommand goalTextIntent = iota
	goalTextBareQuery
	goalTextClear
	goalTextSet
)

// goalTextRoute is one provider's user-message goal vocabulary.
//
// The three text-route providers differ in five values and in nothing else, so
// the algorithm lives here and each provider declares one of these. The rule
// that provider-specific logic stays in the provider still holds: the command,
// the clear words and the capability check remain in the provider's own file.
// Only the format-and-observe algorithm is shared, exactly as
// parseGoalCommandText already is.
type goalTextRoute struct {
	// provider names the provider in an error message.
	provider string
	// command is the slash command that LeapMux emits.
	command string
	// clearArgs lists each complete argument that clears the goal. LeapMux
	// emits the first one and observes them all.
	clearArgs []string
	// steerCarriesCommand is true when the provider's steer channel reaches the
	// same command parser as its send channel. Claude Code steers by sending
	// the identical user message, so a steered command changes its goal. Goose
	// steers through a separate ACP method whose command handling LeapMux did
	// not verify, and Copilot refuses steering, so both leave this false.
	steerCarriesCommand bool
}

// ErrGoalObjectiveIsCommand means that the objective is one of the words that
// clears the goal, so the provider would read a set as a clear. The service
// maps it to InvalidArgument.
var ErrGoalObjectiveIsCommand = errors.New("this objective is a word that clears the goal")

// perform builds the user message for one action. It changes nothing itself:
// a text route takes effect only once the queue delivers what it returns.
func (r goalTextRoute) perform(action GoalAction, objective string) (GoalOutcome, error) {
	text, err := r.commandText(action, objective)
	if err != nil {
		return GoalOutcome{}, err
	}
	return GoalOutcome{QueuedInput: text}, nil
}

// commandText builds the user message for one action, or refuses it.
func (r goalTextRoute) commandText(action GoalAction, objective string) (string, error) {
	switch action {
	case GoalActionSet:
		objective = foldGoalObjective(objective)
		if objective == "" {
			return "", fmt.Errorf("%s %s: an objective is required", r.provider, r.command)
		}
		// The command is positional, so a one-word objective that equals a
		// clear word reaches the provider as a clear. The provider then removes
		// the goal, and parseGoalCommandText below reads the same text the same
		// way, so nothing reports the difference. Refuse instead, and let the
		// user see why. ZCode escapes the same hazard with its `replace` alias;
		// a text route has no alias to reach for.
		if r.matchesClearArgument(objective) {
			return "", fmt.Errorf("%w: %s %s reads %q as a clear",
				ErrGoalObjectiveIsCommand, r.provider, r.command, objective)
		}
		return r.command + " " + objective, nil
	case GoalActionClear:
		return r.command + " " + r.clearArgs[0], nil
	default:
		return "", ErrGoalControlUnsupported
	}
}

// observe writes the goal that a delivered command installed.
func (r goalTextRoute) observe(sink OutputSink, delivery GoalCommandDelivery, text string) {
	if delivery == GoalDeliverySteer && !r.steerCarriesCommand {
		return
	}
	intent, objective := parseGoalCommandText(text, r.command, r.clearArgs)
	switch intent {
	case goalTextSet:
		// A fresh CreatedAt on every set, so re-setting the SAME objective
		// reads as a restart rather than as no change. Codex earns that
		// identity from its own wire; here the observer is the only source.
		sink.UpsertGoal(GoalUpdate{
			Objective: objective,
			Status:    GoalStatusActive,
			CreatedAt: time.Now().UTC(),
		})
	case goalTextClear:
		// Not a snapshot: the user just did this, so it is a real transition.
		sink.ClearGoal(false)
	case goalTextNotCommand, goalTextBareQuery:
		// These inputs do not change the goal.
	}
}

func (r goalTextRoute) matchesClearArgument(argument string) bool {
	for _, clearArg := range r.clearArgs {
		if strings.EqualFold(argument, clearArg) {
			return true
		}
	}
	return false
}

// foldGoalObjective makes a goal safe for a single-line slash command. A
// newline ends the command, and the provider sends the rest as a second line.
func foldGoalObjective(objective string) string {
	return strings.Join(strings.Fields(objective), " ")
}

// parseGoalCommandText classifies one delivered provider command. A clear
// word must match the complete argument.
//
// It reads the FIRST LINE only. A provider takes the remainder of the command
// line as the objective, so a later line of the same message belongs to no
// command. Folding the whole message would store an objective longer than the
// one the provider installed, and no text route reports the goal back to
// correct it.
func parseGoalCommandText(text, command string, clearArgs []string) (goalTextIntent, string) {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if line == command {
		return goalTextBareQuery, ""
	}
	if !strings.HasPrefix(line, command+" ") {
		return goalTextNotCommand, ""
	}
	argument := foldGoalObjective(strings.TrimPrefix(line, command+" "))
	if argument == "" {
		return goalTextBareQuery, ""
	}
	for _, clearArg := range clearArgs {
		if strings.EqualFold(argument, clearArg) {
			return goalTextClear, ""
		}
	}
	return goalTextSet, argument
}

// ErrGoalControlUnsupported means that the provider has no goal route or does
// not support the requested action. The service maps it to FailedPrecondition.
var ErrGoalControlUnsupported = errors.New("agent provider does not support this session-goal action")
