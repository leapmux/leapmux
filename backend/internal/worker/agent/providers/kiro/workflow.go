package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Kiro runs a workflow -- a goal, a recipe, or a run that the model starts
// with its `run_workflow` tool -- as a tree of nodes. Each step node runs an
// agent in a session of its OWN, which streams on the same connection under
// its own session id. The parent session learns of the run from `_kiro/workflow/*`
// notifications, which carry the run's id and the parent's session id rather
// than a session id of their own.
//
// LeapMux keeps one registry row for each run, grouped with the rows of its
// steps, and links each step's session to its row, so the step's transcript
// opens in a tab of its own.

// Kiro's workflow notifications.
const (
	kiroWorkflowRunStartMethod      = "_kiro/workflow/run_start"
	kiroWorkflowNodeStartMethod     = "_kiro/workflow/node_start"
	kiroWorkflowNodeCompleteMethod  = "_kiro/workflow/node_complete"
	kiroWorkflowNodePausedMethod    = "_kiro/workflow/node_paused"
	kiroWorkflowLoopIterationMethod = "_kiro/workflow/loop_iteration"
	kiroWorkflowPausedMethod        = "_kiro/workflow/paused"
	kiroWorkflowRunCompleteMethod   = "_kiro/workflow/run_complete"
)

// Kiro's workflow requests.
const (
	kiroWorkflowPauseMethod  = "_kiro/workflow/pause"
	kiroWorkflowResumeMethod = "_kiro/workflow/resume"
	kiroWorkflowCancelMethod = "_kiro/workflow/cancel"
	kiroWorkflowListMethod   = "_kiro/workflow/list"
)

// Kiro's words for the status of a run and of a node.
const (
	kiroRunRunning   = "running"
	kiroRunPaused    = "paused"
	kiroRunCompleted = "completed"
	kiroRunFailed    = "failed"
	kiroRunAborted   = "aborted"
	kiroNodeSkipped  = "skipped"
	kiroNodeTypeStep = "step"
)

// kiroWorkflowRowPrefix keys the registry row of one workflow run.
const kiroWorkflowRowPrefix = "workflow:"

// workflowState follows the workflow runs of the main session. Guarded by
// Agent.stateMu.
type workflowState struct {
	runs map[string]*workflowRun
}

// workflowRun is what LeapMux knows about one run.
type workflowRun struct {
	name      string
	objective string
	status    string
	// steps maps the node path of a step to the session it runs in, which is
	// also the key of its registry row, while the step runs.
	steps map[string]string
}

// finished reports whether a run ended for good. A paused run can resume.
func (r *workflowRun) finished() bool {
	return r.status == kiroRunCompleted || r.status == kiroRunFailed || r.status == kiroRunAborted
}

// kiroWorkflowNotification is the part of a workflow notification that
// LeapMux reads. Each notification states a subset of it.
type kiroWorkflowNotification struct {
	WorkflowID       string            `json:"workflowId"`
	WorkflowName     string            `json:"workflowName"`
	ParentSessionID  string            `json:"parentSessionId"`
	Inputs           map[string]string `json:"inputs"`
	NodeID           string            `json:"nodeId"`
	NodePath         []string          `json:"nodePath"`
	Type             string            `json:"type"`
	AgentName        string            `json:"agentName"`
	SessionID        string            `json:"sessionId"`
	Iteration        *int              `json:"iteration"`
	Status           string            `json:"status"`
	CapturedOutput   string            `json:"capturedOutput"`
	FailureReason    string            `json:"failureReason"`
	Reason           string            `json:"reason"`
	PauseReason      string            `json:"pauseReason"`
	StopConditionMet bool              `json:"stopConditionMet"`
	FinalState       struct {
		PauseReason string           `json:"pauseReason"`
		Root        kiroWorkflowNode `json:"root"`
	} `json:"finalState"`
}

// kiroWorkflowNode is one node of the final state of a run, reduced to what
// states why the run failed. Kiro states a failure reason on the nodes of a
// run, and never on the run itself.
type kiroWorkflowNode struct {
	Status        string             `json:"status"`
	FailureReason string             `json:"failureReason"`
	Children      []kiroWorkflowNode `json:"children"`
}

// failureReason is the reason of the deepest failed node that states one, in
// the order of the tree. That node is the cause: the step whose error failed
// its round, and the round, the loop and the run above it failed with it. It is
// "" for a tree that states no reason.
func (n kiroWorkflowNode) failureReason() string {
	for _, child := range n.Children {
		if reason := child.failureReason(); reason != "" {
			return reason
		}
	}
	if n.Status != kiroRunFailed {
		return ""
	}
	return strings.TrimSpace(n.FailureReason)
}

// nodePathKey joins a node path into one map key.
func nodePathKey(path []string) string {
	return strings.Join(path, "/")
}

// handleWorkflowNotification dispatches one workflow notification of the main
// session.
func (a *Agent) handleWorkflowNotification(method string, params json.RawMessage) {
	var note kiroWorkflowNotification
	if err := json.Unmarshal(params, &note); err != nil || note.WorkflowID == "" {
		slog.Warn("kiro workflow notification unreadable", "agent_id", a.AgentID(), "method", method, "error", err)
		return
	}
	if !a.ownsWorkflow(note) {
		return
	}
	switch method {
	case kiroWorkflowRunStartMethod:
		a.handleRunStart(note)
	case kiroWorkflowNodeStartMethod:
		a.handleNodeStart(note)
	case kiroWorkflowNodeCompleteMethod:
		a.handleNodeComplete(note)
	case kiroWorkflowNodePausedMethod:
		a.handleNodePaused(note)
	case kiroWorkflowLoopIterationMethod:
		a.handleLoopIteration(note)
	case kiroWorkflowPausedMethod:
		a.handleRunPaused(note)
	case kiroWorkflowRunCompleteMethod:
		a.handleRunComplete(note)
	}
}

// ownsWorkflow reports whether a run belongs to the session that this agent
// serves. A notification that states no parent belongs to a run this agent
// already follows.
func (a *Agent) ownsWorkflow(note kiroWorkflowNotification) bool {
	if note.ParentSessionID != "" {
		return a.IsCurrentSession(note.ParentSessionID)
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	_, known := a.workflows.runs[note.WorkflowID]
	return known
}

// runForLocked returns the run of one notification, and records it on first
// sight. The caller holds stateMu.
func (a *Agent) runForLocked(note kiroWorkflowNotification) *workflowRun {
	if a.workflows.runs == nil {
		a.workflows.runs = make(map[string]*workflowRun)
	}
	run := a.workflows.runs[note.WorkflowID]
	if run == nil {
		run = &workflowRun{status: kiroRunRunning, steps: make(map[string]string)}
		a.workflows.runs[note.WorkflowID] = run
	}
	if name := strings.TrimSpace(note.WorkflowName); name != "" {
		run.name = name
	}
	if objective := strings.TrimSpace(note.Inputs["prompt"]); objective != "" {
		run.objective = objective
	}
	return run
}

// runLabel is the name that the rows of one run carry.
func runLabel(run *workflowRun) string {
	if run.name == "" {
		return "Workflow"
	}
	return run.name
}

// upsertRunRow writes the registry row of one run.
func (a *Agent) upsertRunRow(workflowID string, run *workflowRun, status bgtask.Status, activity string, closeRow bool) {
	label := runLabel(run)
	title := label
	if run.objective != "" {
		title = label + ": " + run.objective
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:     kiroWorkflowRowPrefix + workflowID,
		Kind:       bgtask.KindWorkflow,
		Title:      title,
		Activity:   activity,
		Status:     status,
		GroupKey:   workflowID,
		GroupLabel: label,
		CloseRow:   closeRow,
	})
}

// handleRunStart opens the row of a run that starts or resumes.
func (a *Agent) handleRunStart(note kiroWorkflowNotification) {
	a.stateMu.Lock()
	run := a.runForLocked(note)
	run.status = kiroRunRunning
	snapshot := *run
	a.stateMu.Unlock()
	a.upsertRunRow(note.WorkflowID, &snapshot, bgtask.StatusRunning, "", false)
	a.observeGoalRun(note.WorkflowID, snapshot)
}

// stepTitle is the title Kiro gives a step session: the run, the node and the
// round of a loop.
func stepTitle(run *workflowRun, note kiroWorkflowNotification) string {
	title := runLabel(run) + " · " + note.NodeID
	if note.Iteration != nil {
		title += fmt.Sprintf(" #%d", *note.Iteration+1)
	}
	return title
}

// handleNodeStart opens the row of a step, once its session exists. Kiro
// reports a step twice: when it starts, and again with the id of the session
// that runs it. Only the second one can route the step's transcript.
func (a *Agent) handleNodeStart(note kiroWorkflowNotification) {
	if note.Type != kiroNodeTypeStep || note.SessionID == "" {
		return
	}
	a.stateMu.Lock()
	run := a.runForLocked(note)
	run.steps[nodePathKey(note.NodePath)] = note.SessionID
	title := stepTitle(run, note)
	label := runLabel(run)
	a.stateMu.Unlock()
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:        note.SessionID,
		ChildAgentKey: note.SessionID,
		Title:         title,
		Activity:      strings.TrimSpace(note.AgentName),
		Status:        bgtask.StatusRunning,
		GroupKey:      note.WorkflowID,
		GroupLabel:    label,
	})
	a.AttachChildSession(note.SessionID, note.SessionID)
}

// kiroNodeStatus maps the status of a node that ended.
func kiroNodeStatus(status string) bgtask.Status {
	switch status {
	case kiroRunCompleted:
		return bgtask.StatusCompleted
	case kiroRunFailed:
		return bgtask.StatusFailed
	default:
		// aborted and skipped: the step stopped without completing.
		return bgtask.StatusStopped
	}
}

// handleNodeComplete closes the row of a step, with what the step reported.
func (a *Agent) handleNodeComplete(note kiroWorkflowNotification) {
	a.stateMu.Lock()
	run := a.runForLocked(note)
	key := nodePathKey(note.NodePath)
	sessionID, running := run.steps[key]
	delete(run.steps, key)
	a.stateMu.Unlock()
	if !running {
		return
	}
	report := strings.TrimSpace(note.CapturedOutput)
	if report == "" {
		report = strings.TrimSpace(note.FailureReason)
	}
	obs := &acp.SubagentObservation{
		RowKey:   sessionID,
		Status:   kiroNodeStatus(note.Status),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	}
	if report != "" {
		obs.ReportID = sessionID
		obs.Report.Text = report
	}
	a.ApplySubagentObservation(obs)
}

// handleNodePaused states on its row why a step waits.
func (a *Agent) handleNodePaused(note kiroWorkflowNotification) {
	a.stateMu.Lock()
	run := a.runForLocked(note)
	sessionID, running := run.steps[nodePathKey(note.NodePath)]
	a.stateMu.Unlock()
	if !running {
		return
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   sessionID,
		Status:   bgtask.StatusRunning,
		Activity: pausedActivity(note.Reason),
	})
}

// pausedActivity is the line a row shows while its work waits.
func pausedActivity(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return "Paused: " + reason
	}
	return "Paused"
}

// handleLoopIteration counts the rounds of a goal.
func (a *Agent) handleLoopIteration(note kiroWorkflowNotification) {
	if note.Iteration == nil {
		return
	}
	a.noteGoalRound(note.WorkflowID, *note.Iteration+1)
}

// handleRunPaused states on its row why a run waits. A paused run can resume,
// so its row stays open.
func (a *Agent) handleRunPaused(note kiroWorkflowNotification) {
	a.stateMu.Lock()
	run := a.runForLocked(note)
	run.status = kiroRunPaused
	snapshot := *run
	a.stateMu.Unlock()
	a.upsertRunRow(note.WorkflowID, &snapshot, bgtask.StatusRunning, pausedActivity(note.PauseReason), false)
	a.observeGoalPause(note.WorkflowID, note.PauseReason)
}

// kiroRunStatus maps the status of a run that ended.
func kiroRunStatus(status string) bgtask.Status {
	switch status {
	case kiroRunCompleted:
		return bgtask.StatusCompleted
	case kiroRunFailed:
		return bgtask.StatusFailed
	default:
		return bgtask.StatusStopped
	}
}

// handleRunComplete closes the row of a run that ended, and each step it left
// open. A run that ends paused keeps its row open, because it can resume.
func (a *Agent) handleRunComplete(note kiroWorkflowNotification) {
	a.stateMu.Lock()
	run := a.runForLocked(note)
	run.status = note.Status
	snapshot := *run
	var openSteps []string
	if run.finished() {
		for key, sessionID := range run.steps {
			openSteps = append(openSteps, sessionID)
			delete(run.steps, key)
		}
		delete(a.workflows.runs, note.WorkflowID)
	}
	a.stateMu.Unlock()

	if note.Status == kiroRunPaused {
		a.upsertRunRow(note.WorkflowID, &snapshot, bgtask.StatusRunning, pausedActivity(note.FinalState.PauseReason), false)
		a.observeGoalPause(note.WorkflowID, note.FinalState.PauseReason)
		return
	}
	status := kiroRunStatus(note.Status)
	for _, sessionID := range openSteps {
		a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: sessionID, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly})
	}
	failure := note.FinalState.Root.failureReason()
	a.upsertRunRow(note.WorkflowID, &snapshot, status, failure, true)
	a.observeGoalEnd(note.WorkflowID, note.Status, failure)
}

// unfinishedRunsLocked lists the runs that did not end. The caller holds
// stateMu.
func (a *Agent) unfinishedRunsLocked() []string {
	var ids []string
	for id, run := range a.workflows.runs {
		if !run.finished() {
			ids = append(ids, id)
		}
	}
	return ids
}

// cancelRunDetached cancels one run and does not wait for the answer. A
// failure is logged, because nothing waits for it.
func (a *Agent) cancelRunDetached(workflowID string) {
	params, err := json.Marshal(map[string]string{"workflowId": workflowID, "initiator": "user"})
	if err != nil {
		slog.Warn("kiro workflow cancel marshal failed", "agent_id", a.AgentID(), "workflow_id", workflowID, "error", err)
		return
	}
	if err := a.SendDetachedRequest(kiroWorkflowCancelMethod, params, func(_ json.RawMessage, err error) {
		if err != nil {
			slog.Warn("kiro workflow cancel failed", "agent_id", a.AgentID(), "workflow_id", workflowID, "error", err)
		}
	}); err != nil {
		slog.Warn("kiro workflow cancel not sent", "agent_id", a.AgentID(), "workflow_id", workflowID, "error", err)
	}
}
