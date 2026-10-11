package providerkit

import (
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// goalTextIntent classifies one user message against a provider's goal command.
type goalTextIntent int

const (
	goalTextNotCommand goalTextIntent = iota
	goalTextBareQuery
	goalTextClear
	goalTextSet
	goalTextPause
	goalTextResume
)

// GoalTextRoute is one provider's user-message goal vocabulary.
//
// Providers share objective formatting and clear-command observation through this type.
// Each provider supplies its command vocabulary and capability check.
// Providers handle additional control arguments in their own implementations.
type GoalTextRoute struct {
	// Provider identifies the provider in an error message.
	Provider string
	// Command is the slash command that LeapMux emits.
	Command string
	// ClearArgs lists each complete argument that clears the goal. LeapMux
	// emits the first one and observes them all.
	ClearArgs []string
	// PauseArgs and ResumeArgs list the arguments for the other two verbs, in
	// the same form as ClearArgs. Empty means the provider's command has no such
	// verb, and the action is unsupported: Goose's `/goal` takes an objective or
	// a clear word, while Kilo's takes all four.
	PauseArgs  []string
	ResumeArgs []string
	// SetVerb is the word that states an objective when the provider's command
	// reads the first word of its argument as a verb (Qwen Code's `/goal set`).
	// A route with one emits it before every objective, so no objective can
	// reach the provider as a verb, and Set refuses none. Empty means the
	// command takes the objective as its whole argument.
	SetVerb string
	// QueryArgs lists each complete argument that asks for the goal rather than
	// changing it, in the same form as ClearArgs. The bare command is always a
	// query. LeapMux never emits a query, but an objective that equals one of
	// these words would reach the provider as the query, so Set refuses it.
	QueryArgs []string
	// SteerCarriesCommand is true when the provider's steer channel reaches the
	// same command parser as its send channel. Claude Code steers by sending
	// the identical user message, so a steered command changes its goal. Goose
	// steers through a separate ACP method whose command handling LeapMux did
	// not verify, and Copilot steers through its own immediate-send route whose
	// command handling LeapMux did not verify either, so both leave this false.
	SteerCarriesCommand bool
}

// Perform builds the user message for one action. It changes nothing itself:
// a text route takes effect only once the queue delivers what it returns.
func (r GoalTextRoute) Perform(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	text, err := r.CommandText(action, objective)
	if err != nil {
		return agent.GoalOutcome{}, err
	}
	return agent.GoalOutcome{QueuedInput: text}, nil
}

// CommandText builds the user message for one action, or refuses it.
func (r GoalTextRoute) CommandText(action agent.GoalAction, objective string) (string, error) {
	switch action {
	case agent.GoalActionSet:
		objective = foldGoalObjective(objective)
		if objective == "" {
			return "", fmt.Errorf("%s %s: an objective is required", r.Provider, r.Command)
		}
		if r.SetVerb != "" {
			return r.Command + " " + r.SetVerb + " " + objective, nil
		}
		// The command is positional, so a one-word objective that equals a
		// clear word reaches the provider as a clear. The provider then removes
		// the goal, and the parser below reads the same text the same
		// way, so nothing reports the difference. Refuse instead, and let the
		// user see why. ZCode escapes the same hazard with its `replace` alias;
		// a text route has no alias to reach for.
		if verb := r.reservedArgument(objective); verb != "" {
			return "", fmt.Errorf("%w: %s %s reads %q as a %s",
				agent.ErrGoalObjectiveIsCommand, r.Provider, r.Command, objective, verb)
		}
		return r.Command + " " + objective, nil
	case agent.GoalActionClear:
		return r.verbText(r.ClearArgs)
	case agent.GoalActionPause:
		return r.verbText(r.PauseArgs)
	case agent.GoalActionResume:
		return r.verbText(r.ResumeArgs)
	default:
		return "", agent.ErrGoalControlUnsupported
	}
}

// Observe writes the goal that a delivered command installed.
func (r GoalTextRoute) Observe(sink agent.GoalServices, delivery agent.GoalCommandDelivery, text string) {
	if delivery == agent.GoalDeliverySteer && !r.SteerCarriesCommand {
		return
	}
	intent, objective := r.parse(text)
	switch intent {
	case goalTextSet:
		// A fresh CreatedAt on every set, so re-setting the SAME objective
		// reads as a restart rather than as no change. Codex earns that
		// identity from its own wire; here the observer is the only source.
		sink.UpsertGoal(agent.GoalUpdate{
			Objective: objective,
			Status:    agent.GoalStatusActive,
			CreatedAt: time.Now().UTC(),
		})
	case goalTextClear:
		// Not a snapshot: the user just did this, so it is a real transition.
		sink.ClearGoal(false)
	case goalTextPause:
		// The objective and the identity survive a pause, so this changes the
		// status alone. It moves an ACTIVE goal only: a paused or finished goal
		// has nothing to pause, and a compare-and-set says so without reading
		// the goal back first.
		sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusPaused)
	case goalTextResume:
		sink.UpdateGoalStatus(agent.GoalStatusPaused, agent.GoalStatusActive)
	case goalTextNotCommand, goalTextBareQuery:
		// These inputs do not change the goal.
	}
}

// verbText emits the first argument of a verb the provider's command accepts.
func (r GoalTextRoute) verbText(args []string) (string, error) {
	if len(args) == 0 {
		return "", agent.ErrGoalControlUnsupported
	}
	return r.Command + " " + args[0], nil
}

// reservedArgument names the verb an objective would be read as, or "" when the
// objective is safe to send.
//
// The command is positional, so a one-word objective that equals a verb reaches the
// provider as that verb. The provider then acts on it, and the observer below reads the
// same text the same way, so nothing reports the difference.
func (r GoalTextRoute) reservedArgument(objective string) string {
	for verb, args := range map[string][]string{"clear": r.ClearArgs, "pause": r.PauseArgs, "resume": r.ResumeArgs, "query": r.QueryArgs} {
		for _, arg := range args {
			if strings.EqualFold(objective, arg) {
				return verb
			}
		}
	}
	return ""
}

// foldGoalObjective makes a goal safe for a single-line slash command. A
// newline ends the command, and the provider sends the rest as a second line.
func foldGoalObjective(objective string) string {
	return strings.Join(strings.Fields(objective), " ")
}

// parse classifies one delivered provider command. A verb argument
// must match the complete argument.
//
// It reads the FIRST LINE only. A provider takes the remainder of the command
// line as the objective, so a later line of the same message belongs to no
// command. Folding the whole message would store an objective longer than the
// one the provider installed, and no text route reports the goal back to
// correct it.
func (r GoalTextRoute) parse(text string) (goalTextIntent, string) {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if line == r.Command {
		return goalTextBareQuery, ""
	}
	if !strings.HasPrefix(line, r.Command+" ") {
		return goalTextNotCommand, ""
	}
	argument := foldGoalObjective(strings.TrimPrefix(line, r.Command+" "))
	if argument == "" {
		return goalTextBareQuery, ""
	}
	if verb, objective, _ := strings.Cut(argument, " "); r.SetVerb != "" && strings.EqualFold(verb, r.SetVerb) {
		// The provider reads the first word as a verb, so the set verb with no
		// objective after it installs nothing.
		if objective == "" {
			return goalTextBareQuery, ""
		}
		return goalTextSet, objective
	}
	switch r.reservedArgument(argument) {
	case "clear":
		return goalTextClear, ""
	case "pause":
		return goalTextPause, ""
	case "resume":
		return goalTextResume, ""
	case "query":
		return goalTextBareQuery, ""
	}
	return goalTextSet, argument
}
