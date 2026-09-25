package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kiro sets its session goal through the `/goal` prompt command, and runs the
// goal as a workflow named `goal`: a loop of steps that works toward the
// objective until a step reports success or the loop reaches its limit. The
// workflow's notifications state the goal, so LeapMux does not observe the
// command it sends:
//
//	/goal <objective> [--max <rounds>]
//
// The command only sets. The workflow's own requests pause, resume and cancel
// the run, and they need the id of the run, which the notifications state.
const (
	kiroGoalCommand      = "/goal"
	kiroGoalWorkflowName = "goal"
)

// kiroWorkflowInspectMethod reads the state of one run.
const kiroWorkflowInspectMethod = "_kiro/workflow/inspect"

// kiroGoalRoute builds the command that sets a goal. The command takes the
// objective as its whole argument and has no verb, so no objective equals one.
var kiroGoalRoute = providerkit.GoalTextRoute{
	Provider: "kiro",
	Command:  kiroGoalCommand,
}

// kiroGoalRoundsSuffix matches the trailing round limit that Kiro splits off an
// objective: `--max` as its own word after another word, then a final
// all-digit word. It mirrors Kiro's own parse (`parseGoalCommand`), which
// trims the text after `/goal ` and matches `/\s+--max\s+(\d+)$/` on it. The
// whitespace before `--max` is required there, so an objective that is only
// `--max 5` stays the objective, and Kiro does not read it as a limit.
var kiroGoalRoundsSuffix = regexp.MustCompile(`\s--max\s+\d+$`)

// goalState is the goal run of the main session. Guarded by Agent.stateMu.
type goalState struct {
	// runID is the workflow that runs the goal, or "" with no goal run.
	runID string
	// update is the goal that LeapMux reported last.
	update agent.GoalUpdate
	// stepError is the last error that a step of the run sent to the session
	// since the run last started. Kiro states only its own phrase for the
	// failure of a run that a step's error ended ("Step signaled error via
	// send_message."), so this states why.
	stepError string
}

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports what Kiro can do with the goal now. A goal can
// always be set. Pause, resume and clear act on a run, so they need the id of
// the run that holds the goal.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	a.stateMu.Lock()
	runID := a.goal.runID
	a.stateMu.Unlock()
	if runID == "" {
		return []agent.GoalAction{agent.GoalActionSet}
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction changes the goal. A set builds the prompt that the queue
// delivers. The three other actions are requests on the goal's run, whose
// notifications then report the change.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	switch action {
	case agent.GoalActionSet:
		// Kiro takes a trailing `--max <rounds>` off the objective as a limit.
		// The goal card has no limit field, so an objective that ends that way
		// is text the user wrote, and Kiro would store a shorter objective.
		folded := strings.Join(strings.Fields(objective), " ")
		if match := kiroGoalRoundsSuffix.FindStringIndex(folded); match != nil {
			return agent.GoalOutcome{}, fmt.Errorf("%w: Kiro %s reads the trailing %q as a round limit",
				agent.ErrGoalObjectiveIsCommand, kiroGoalCommand, folded[match[0]+1:])
		}
		return kiroGoalRoute.Perform(action, objective)
	case agent.GoalActionClear:
		return agent.GoalOutcome{}, a.clearGoalRun()
	case agent.GoalActionPause:
		return agent.GoalOutcome{}, a.pauseGoalRun()
	case agent.GoalActionResume:
		return agent.GoalOutcome{}, a.resumeGoalRun()
	default:
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
}

// goalRunID returns the run that holds the goal, or an error when no run does.
func (a *Agent) goalRunID() (string, error) {
	a.stateMu.Lock()
	runID := a.goal.runID
	a.stateMu.Unlock()
	if runID == "" {
		return "", agent.ErrGoalControlUnsupported
	}
	return runID, nil
}

// requestGoalRun sends one workflow request for the goal's run, and returns
// the reply.
func (a *Agent) requestGoalRun(method string, params map[string]any) (json.RawMessage, error) {
	runID, err := a.goalRunID()
	if err != nil {
		return nil, err
	}
	params["workflowId"] = runID
	params["initiator"] = "user"
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	reply, err := a.SendRequest(method, raw, a.APITimeout())
	if err != nil {
		return nil, providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	return reply, nil
}

// clearGoalRun cancels the run that holds the goal. Kiro reports the end of the
// run, and a run that had ended already reports nothing, so the goal clears
// here too.
func (a *Agent) clearGoalRun() error {
	runID, err := a.goalRunID()
	if err != nil {
		return err
	}
	if _, err := a.requestGoalRun(kiroWorkflowCancelMethod, map[string]any{}); err != nil {
		return err
	}
	a.forgetGoalRun(runID)
	return nil
}

// pauseGoalRun pauses the run that holds the goal.
func (a *Agent) pauseGoalRun() error {
	reply, err := a.requestGoalRun(kiroWorkflowPauseMethod, map[string]any{})
	if err != nil {
		return err
	}
	var paused struct {
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal(reply, &paused); err != nil {
		return fmt.Errorf("read the Kiro pause reply: %w", err)
	}
	if !paused.Paused {
		return fmt.Errorf("the goal run was not running, so Kiro did not pause it")
	}
	return nil
}

// resumeGoalRun resumes the run that holds the goal, under the policy preset of
// the session, so its steps ask for permission as the session's own turns do.
func (a *Agent) resumeGoalRun() error {
	params := map[string]any{}
	if presets := a.currentPolicyPreset().presets; len(presets) > 0 {
		params["policyPreset"] = presets
	}
	_, err := a.requestGoalRun(kiroWorkflowResumeMethod, params)
	return err
}

// observeGoalRun reports a goal run that starts or resumes as the active goal.
func (a *Agent) observeGoalRun(workflowID string, run workflowRun) {
	if run.name != kiroGoalWorkflowName {
		return
	}
	a.stateMu.Lock()
	newRun := a.goal.runID != workflowID
	if newRun {
		a.goal.runID = workflowID
		a.goal.update = agent.GoalUpdate{NativeID: workflowID, CreatedAt: time.Now().UTC()}
	}
	if run.objective != "" {
		a.goal.update.Objective = run.objective
	}
	a.goal.update.Status = agent.GoalStatusActive
	a.goal.update.StatusDetail = ""
	// A start begins a round of work again, so an error of an earlier round
	// states nothing about the next end.
	a.goal.stepError = ""
	update := a.goal.update
	a.stateMu.Unlock()
	a.Sink().UpsertGoal(update)
	if newRun {
		a.Sink().PublishGoalCapabilities()
	}
}

// noteGoalStepError records the error that a step of the run workflowID sent,
// when that run holds the goal.
func (a *Agent) noteGoalStepError(workflowID, message string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if workflowID == "" || a.goal.runID != workflowID {
		return
	}
	a.goal.stepError = message
}

// noteGoalRound counts the rounds that a goal run finished.
func (a *Agent) noteGoalRound(workflowID string, rounds int) {
	a.stateMu.Lock()
	if a.goal.runID != workflowID {
		a.stateMu.Unlock()
		return
	}
	count := int32(rounds)
	a.goal.update.Iterations = &count
	update := a.goal.update
	a.stateMu.Unlock()
	a.Sink().UpsertGoal(update)
}

// observeGoalPause reports the goal paused, with the reason Kiro states. A
// goal pauses at its round limit, on the reader's request, and when a step
// waits for the reader.
func (a *Agent) observeGoalPause(workflowID, reason string) {
	a.stateMu.Lock()
	if a.goal.runID != workflowID {
		a.stateMu.Unlock()
		return
	}
	a.goal.update.Status = agent.GoalStatusPaused
	a.goal.update.StatusDetail = strings.TrimSpace(reason)
	update := a.goal.update
	a.stateMu.Unlock()
	a.Sink().UpsertGoal(update)
}

// observeGoalEnd reports the end of a goal run. A run that completed achieved
// the goal. A run that failed leaves the goal blocked, with the reason. A run
// that was cancelled leaves no goal.
func (a *Agent) observeGoalEnd(workflowID, status, failure string) {
	a.stateMu.Lock()
	if a.goal.runID != workflowID {
		a.stateMu.Unlock()
		return
	}
	switch status {
	case kiroRunCompleted:
		a.goal.update.Status = agent.GoalStatusDone
		a.goal.update.StatusDetail = ""
	case kiroRunFailed:
		a.goal.update.Status = agent.GoalStatusBlocked
		a.goal.update.StatusDetail = strings.TrimSpace(failure)
		if a.goal.stepError != "" {
			a.goal.update.StatusDetail = a.goal.stepError
		}
	default:
		a.stateMu.Unlock()
		a.forgetGoalRun(workflowID)
		return
	}
	a.goal.runID = ""
	update := a.goal.update
	a.stateMu.Unlock()
	a.Sink().UpsertGoal(update)
	a.Sink().PublishGoalCapabilities()
}

// forgetGoalRun clears the goal that a run held, once, whichever report of
// its end arrives first.
func (a *Agent) forgetGoalRun(workflowID string) {
	a.stateMu.Lock()
	if a.goal.runID != workflowID {
		a.stateMu.Unlock()
		return
	}
	a.goal = goalState{}
	a.stateMu.Unlock()
	a.Sink().ClearGoal(false)
	a.Sink().PublishGoalCapabilities()
}

// kiroWorkflowRunSummary is one run of `_kiro/workflow/list`.
type kiroWorkflowRunSummary struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowName    string `json:"workflowName"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	UpdatedAt       string `json:"updatedAt"`
	ParentSessionID string `json:"parentSessionId"`
}

// recoverGoalRun finds the goal run that a resumed session left in Kiro's own
// store: a goal that still runs, or waits for the reader. Kiro keeps the run
// across a restart and can resume it by itself, and its controls need the
// run's id, which only Kiro knows. A new session has no run to find. It runs
// on a goroutine of its own, so the start does not wait for two requests.
func (a *Agent) recoverGoalRun(opts agent.Options) {
	if opts.ResumeSessionID == "" {
		return
	}
	sessionID := a.CurrentSessionID()
	if sessionID == "" {
		return
	}
	go func() {
		if err := a.recoverGoalRunOf(sessionID); err != nil {
			slog.Warn("kiro goal run recovery failed", "agent_id", a.AgentID(), "error", err)
		}
	}()
}

// recoverGoalRunOf reads the goal run of one session, and reports it.
func (a *Agent) recoverGoalRunOf(sessionID string) error {
	params, err := json.Marshal(map[string]string{"sessionId": sessionID})
	if err != nil {
		return err
	}
	reply, err := a.SendRequest(kiroWorkflowListMethod, params, a.APITimeout())
	if err != nil {
		return fmt.Errorf("list the workflow runs: %w", err)
	}
	var list struct {
		Runs []kiroWorkflowRunSummary `json:"runs"`
	}
	if err := json.Unmarshal(reply, &list); err != nil {
		return fmt.Errorf("read the workflow runs: %w", err)
	}
	run, found := latestOpenGoalRun(list.Runs, sessionID)
	if !found {
		return nil
	}
	params, err = json.Marshal(map[string]string{"workflowId": run.WorkflowID})
	if err != nil {
		return err
	}
	reply, err = a.SendRequest(kiroWorkflowInspectMethod, params, a.APITimeout())
	if err != nil {
		return fmt.Errorf("inspect the goal run: %w", err)
	}
	var inspect struct {
		State struct {
			Status      string            `json:"status"`
			Inputs      map[string]string `json:"inputs"`
			PauseReason string            `json:"pauseReason"`
		} `json:"state"`
	}
	if err := json.Unmarshal(reply, &inspect); err != nil {
		return fmt.Errorf("read the goal run: %w", err)
	}
	status := inspect.State.Status
	if status == "" {
		status = run.Status
	}
	a.restoreGoalRun(sessionID, run.WorkflowID, strings.TrimSpace(inspect.State.Inputs["prompt"]), status, inspect.State.PauseReason)
	return nil
}

// latestOpenGoalRun picks the newest goal run of a session that did not end.
func latestOpenGoalRun(runs []kiroWorkflowRunSummary, sessionID string) (kiroWorkflowRunSummary, bool) {
	open := make([]kiroWorkflowRunSummary, 0, len(runs))
	for _, run := range runs {
		name := run.WorkflowName
		if name == "" {
			name = run.Name
		}
		if name != kiroGoalWorkflowName || (run.ParentSessionID != "" && run.ParentSessionID != sessionID) {
			continue
		}
		if run.Status != kiroRunRunning && run.Status != kiroRunPaused {
			continue
		}
		open = append(open, run)
	}
	if len(open) == 0 {
		return kiroWorkflowRunSummary{}, false
	}
	// RFC 3339 timestamps of one clock order as text.
	sort.SliceStable(open, func(i, j int) bool { return open[i].UpdatedAt > open[j].UpdatedAt })
	return open[0], true
}

// restoreGoalRun reports a recovered goal run of the session sessionID. It
// RESTATES the goal rather than announcing a change, because the run began
// before this process did.
//
// A context clear can replace the session while the recovery waits on Kiro.
// The run then belongs to the replaced session, whose retirement cancels the
// runs that it knows, so the restore writes nothing. goalRestoreMu makes the
// check and the report one step against that retirement.
func (a *Agent) restoreGoalRun(sessionID, workflowID, objective, status, pauseReason string) {
	a.goalRestoreMu.Lock()
	defer a.goalRestoreMu.Unlock()
	if !a.IsCurrentSession(sessionID) {
		slog.Info("kiro dropped a recovered goal run of a replaced session", "agent_id", a.AgentID(), "session_id", sessionID, "workflow_id", workflowID)
		return
	}
	a.stateMu.Lock()
	if a.goal.runID != "" {
		// A live notification reported a run first, and it is newer.
		a.stateMu.Unlock()
		return
	}
	a.goal.runID = workflowID
	a.goal.update = agent.GoalUpdate{NativeID: workflowID, Objective: objective, Status: agent.GoalStatusActive}
	if status == kiroRunPaused {
		a.goal.update.Status = agent.GoalStatusPaused
		a.goal.update.StatusDetail = strings.TrimSpace(pauseReason)
	}
	update := a.goal.update
	update.Snapshot = true
	a.stateMu.Unlock()
	a.Sink().UpsertGoal(update)
	a.Sink().PublishGoalCapabilities()
}
