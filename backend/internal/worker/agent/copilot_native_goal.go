package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Copilot's session goal is its autopilot objective.
//
// Every operation but Clear goes through one native command,
// `session.commands.invoke` with the name `autopilot`, and the runtime confirms the
// result through `session.autopilotObjective.getState`. The verified vocabulary is
// in CP-002, CP-003 and CP-008:
//
//   - An objective sets the goal. `--` stops the runtime's own argument parsing,
//     so an objective that reads like an option stays an objective.
//   - `off` pauses the objective and leaves it stored.
//   - `on` changes the session mode only. It does NOT resume a paused objective,
//     so no operation here sends it.
//   - `--max-ai-credits <N>` resumes the stored objective under a credit limit.
//     N must be greater than zero; the runtime refuses zero and a negative value.
//   - Clear has no command. It needs the ordered session disposal in CP-008.
const (
	copilotGoalCommand = "autopilot"
	// copilotGoalLiteralPrefix precedes an objective so the runtime reads it as text.
	copilotGoalLiteralPrefix = "-- "
	// copilotGoalPauseArgument pauses the objective without removing it.
	copilotGoalPauseArgument = "off"
	// copilotGoalResumeArgument resumes the stored objective. The runtime requires a
	// positive credit limit, so Resume cannot be expressed without one.
	copilotGoalResumeArgument = "--max-ai-credits 1"
	// copilotGoalEffectPrompt is the result kind that carries the turn LeapMux must start.
	copilotGoalEffectPrompt = "agent-prompt"
)

// The runtime's own objective status words.
const (
	copilotGoalStatusActive    = "active"
	copilotGoalStatusPaused    = "paused"
	copilotGoalStatusCompleted = "completed"
)

// copilotGoalSnapshot is the objective that LeapMux last confirmed from the runtime.
type copilotGoalSnapshot struct {
	objective string
	identity  string
	status    string
}

// copilotObjectiveState is the runtime's own projection of the objective.
type copilotObjectiveState struct {
	State *struct {
		ID          int64  `json:"id"`
		Objective   string `json:"objective"`
		Status      string `json:"status"`
		TurnCount   *int32 `json:"turnCount"`
		PauseReason string `json:"pauseReason"`
	} `json:"state"`
}

// copilotSlashCommandResult is the effect that one invoked command returns.
type copilotSlashCommandResult struct {
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
}

var _ GoalWriter = (*copilotAgent)(nil)

// SupportedGoalActions reports every operation with verified native evidence.
func (a *copilotAgent) SupportedGoalActions() []GoalAction {
	return []GoalAction{GoalActionSet, GoalActionPause, GoalActionResume, GoalActionClear}
}

func (a *copilotAgent) PerformGoalAction(action GoalAction, objective string) (GoalOutcome, error) {
	// Clear disposes the session and opens it again, so it excludes every other
	// user of that session. The three command operations leave the session in
	// place and share it, exactly as input delivery does.
	if action == GoalActionClear {
		a.sessionMu.Lock()
		defer a.sessionMu.Unlock()
		if a.IsStopped() {
			return GoalOutcome{}, fmt.Errorf("the Copilot process is stopped")
		}
		return GoalOutcome{}, a.clearNativeGoal()
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return GoalOutcome{}, fmt.Errorf("the Copilot process is stopped")
	}
	switch action {
	case GoalActionSet:
		// The objective keeps its own line breaks. The runtime takes it as one
		// `input` field rather than as the tail of a command line, and CP-003
		// confirms that a two-line objective survives unchanged.
		if strings.TrimSpace(objective) == "" {
			return GoalOutcome{}, fmt.Errorf("copilot %s: an objective is required", copilotGoalCommand)
		}
		return a.invokeNativeGoalCommand(copilotGoalLiteralPrefix + objective)
	case GoalActionPause:
		return a.invokeNativeGoalCommand(copilotGoalPauseArgument)
	case GoalActionResume:
		if a.currentNativeGoal().status != copilotGoalStatusPaused {
			return GoalOutcome{}, fmt.Errorf("%w: Copilot resumes a paused objective only", ErrGoalControlUnsupported)
		}
		return a.invokeNativeGoalCommand(copilotGoalResumeArgument)
	default:
		return GoalOutcome{}, ErrGoalControlUnsupported
	}
}

// invokeNativeGoalCommand runs one autopilot command and confirms the stored objective.
//
// The runtime answers a Set and a Resume with an `agent-prompt` effect: the
// objective is already recorded, and the prompt is what starts the model turn that
// pursues it. LeapMux returns that exact prompt for the durable input queue rather
// than writing one of its own, because a prompt LeapMux invented would not be the
// operation the runtime asked for. A command that changes nothing else returns no
// prompt, and the queue then has nothing to deliver.
func (a *copilotAgent) invokeNativeGoalCommand(input string) (GoalOutcome, error) {
	raw, err := a.requestNativeSession("commands.invoke", map[string]any{"name": copilotGoalCommand, "input": input})
	if err != nil {
		return GoalOutcome{}, err
	}
	var result copilotSlashCommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return GoalOutcome{}, fmt.Errorf("decode the Copilot autopilot result: %w", err)
	}
	state, err := a.readNativeGoal()
	if err != nil {
		return GoalOutcome{}, err
	}
	a.applyNativeGoal(state, false)
	if result.Kind != copilotGoalEffectPrompt {
		return GoalOutcome{}, nil
	}
	if strings.TrimSpace(result.Prompt) == "" {
		return GoalOutcome{}, fmt.Errorf("%w: the Copilot autopilot effect carried no prompt", ErrDeliveryUncertain)
	}
	return GoalOutcome{QueuedInput: result.Prompt}, nil
}

// clearNativeGoal removes the objective through the ordered disposal in CP-008.
//
// Deleting the objective file alone does not clear the live goal, and a pending
// runtime write can restore that file afterwards. The shutdown is what stops those
// writes, so the four steps run in this exact order and each one waits for its
// response. The session then opens again under the SAME identity, which is what
// makes the removal observable while the transcript keeps the session it is
// stored under.
//
// The caller holds sessionMu for writing, so no input and no setting change can
// reach the session between the shutdown and the reopen. A failure after the close
// stops the process: the session it had is gone, and no other one took its place.
func (a *copilotAgent) clearNativeGoal() error {
	if a.currentNativeGoal().objective == "" {
		return nil
	}
	sessionID := a.currentNativeSessionID()
	if _, err := a.requestNativeSession("shutdown", nil); err != nil {
		return fmt.Errorf("shut the Copilot session down before clearing its goal: %w", err)
	}
	if _, err := a.requestNativeSession("workspaces.deleteAutopilotObjective", nil); err != nil {
		return fmt.Errorf("delete the Copilot objective: %w", err)
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return err
	}
	if _, err := a.sendRequest("sessions.close", params, a.APITimeout()); err != nil {
		return fmt.Errorf("close the Copilot session before opening it again: %w", err)
	}
	opts := a.opts
	a.stateMu.Lock()
	opts.Options = a.options.Clone()
	a.stateMu.Unlock()
	// The disposed session runs no turn, answers no pending control, owns no child
	// transcript, and its subscriptions die with it. The identity does NOT move: the
	// session opens again under its own, so the transcript keeps it.
	a.forgetNativeSessionState("")
	// Every failure below leaves the agent with no session at all, because the close
	// already disposed of the one it had. There is nothing to roll back to.
	if err := a.reopenNativeSessionAfterClear(opts, sessionID); err != nil {
		a.stopNativeConnection()
		return err
	}
	if err := a.prepareNativeSession(opts.Options); err != nil {
		a.stopNativeConnection()
		return err
	}
	// The spans and the live counters belong to the session that went away.
	a.sink.ResetSpans()
	a.sink.ReportProgress(ResetProgress())
	state, err := a.readNativeGoal()
	if err != nil {
		return err
	}
	if state.State != nil {
		return fmt.Errorf("the Copilot objective survived the clear sequence")
	}
	a.applyNativeGoal(state, false)
	return nil
}

// reopenNativeSessionAfterClear opens the disposed session under its own identity.
//
// A resume restores the conversation, so it is what runs first. The runtime refuses
// one for a session its store never recorded -- every session that ran no model turn
// is in that state, and the browser's own goal case is one -- and it answers
// "Failed to load session events: Session not found". Creating the session under the
// SAME identity is then the way back, and it loses nothing, because the store held
// nothing to restore. See CP-012.
//
// Only the runtime's own REFUSAL takes that path. A transport failure or a timeout
// leaves the outcome unknown, and a create there could replace a session whose
// events the store still holds.
func (a *copilotAgent) reopenNativeSessionAfterClear(opts Options, sessionID string) error {
	config := newCopilotSessionConfig(opts, sessionID, true)
	continueWork := false
	config.ContinuePendingWork = &continueWork
	_, err := a.sendNativeSessionConfig("session.resume", config, a.APITimeout())
	if err == nil {
		return nil
	}
	var refusal *jsonRPCResponseError
	if !errors.As(err, &refusal) {
		return fmt.Errorf("open the Copilot session again after clearing its goal: %w", err)
	}
	if _, err := a.sendNativeSessionConfig("session.create", newCopilotSessionConfig(opts, sessionID, false), a.APITimeout()); err != nil {
		return fmt.Errorf("create the Copilot session again after clearing its goal: %w", err)
	}
	return nil
}

func (a *copilotAgent) currentNativeGoal() copilotGoalSnapshot {
	a.goalMu.Lock()
	defer a.goalMu.Unlock()
	return a.goal
}

// readNativeGoal asks the runtime for the current objective.
func (a *copilotAgent) readNativeGoal() (copilotObjectiveState, error) {
	raw, err := a.requestNativeSession("autopilotObjective.getState", nil)
	if err != nil {
		return copilotObjectiveState{}, err
	}
	var state copilotObjectiveState
	if err := json.Unmarshal(raw, &state); err != nil {
		return copilotObjectiveState{}, fmt.Errorf("decode the Copilot objective: %w", err)
	}
	return state, nil
}

// applyNativeGoal publishes one confirmed objective.
//
// `snapshot` marks a report that RESTATES the objective rather than announcing a
// change. Startup passes it for a resumed session, whose stored objective is older
// than this process: without it the transcript would say "Goal set" now for an
// objective the user set before the restart.
func (a *copilotAgent) applyNativeGoal(state copilotObjectiveState, snapshot bool) {
	a.goalMu.Lock()
	defer a.goalMu.Unlock()
	if state.State == nil || strings.TrimSpace(state.State.Objective) == "" {
		if a.goal.objective == "" {
			return
		}
		a.goal = copilotGoalSnapshot{}
		a.sink.ClearGoal(snapshot)
		return
	}
	detail := state.State.Status
	if state.State.PauseReason != "" {
		detail = state.State.PauseReason
	}
	a.goal = copilotGoalSnapshot{
		objective: state.State.Objective,
		identity:  strconv.FormatInt(state.State.ID, 10),
		status:    state.State.Status,
	}
	a.sink.UpsertGoal(GoalUpdate{
		NativeID:     a.goal.identity,
		Objective:    a.goal.objective,
		Status:       copilotGoalStatus(a.goal.status),
		StatusDetail: detail,
		Iterations:   state.State.TurnCount,
		Snapshot:     snapshot,
	})
}

// copilotGoalStatus maps the runtime's own words onto the neutral four.
//
// Every other word reads as blocked, `cap_reached` above all: that is the credit
// limit Resume asked for, and it needs the user, because a Resume under the same
// limit would stop again at once.
func copilotGoalStatus(wire string) GoalStatus {
	switch wire {
	case copilotGoalStatusActive:
		return GoalStatusActive
	case copilotGoalStatusPaused:
		return GoalStatusPaused
	case copilotGoalStatusCompleted:
		return GoalStatusDone
	default:
		return GoalStatusBlocked
	}
}

// refreshNativeGoal reads and publishes the objective.
func (a *copilotAgent) refreshNativeGoal(snapshot bool) {
	state, err := a.readNativeGoal()
	if err != nil {
		slog.Debug("Read the Copilot objective", "agent_id", a.agentID, "error", err)
		return
	}
	a.applyNativeGoal(state, snapshot)
}

// refreshNativeGoalInBackground reads the objective off the reader goroutine.
//
// `session.autopilot_objective_changed` states that the objective moved and not what
// it became, so the answer needs a request. offReader states why that request cannot
// run on the goroutine that handles the event.
func (a *copilotAgent) refreshNativeGoalInBackground() {
	a.offReader(copilotReadGoal, func() { a.refreshNativeGoal(false) })
}
