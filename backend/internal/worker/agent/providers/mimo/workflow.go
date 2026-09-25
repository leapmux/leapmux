package mimo

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// MiMo's dynamic workflows.
//
// The experimental `workflow` tool runs a script that spawns actors in the same
// session and moves through named phases. Each run reports workflow.started,
// workflow.phase for each phase, and workflow.finished. A run becomes one
// workflow row in the background-task registry, and the actors it spawns group
// under it; the tool's own call shows its transcript in the parent transcript.

// Workflow statuses of workflow.finished.
const (
	workflowCompleted = "completed"
	workflowFailed    = "failed"
	workflowCancelled = "cancelled"
)

// mimoWorkflow is one workflow run.
type mimoWorkflow struct {
	runID string
	name  string
	// finished is true once workflow.finished closed the run's row.
	finished bool
}

// rowKey is the run's registry row key.
func (w *mimoWorkflow) rowKey() string {
	return "mimo-workflow:" + w.runID
}

// groupKey groups the run's row with the rows of the actors it spawns.
func (w *mimoWorkflow) groupKey() string {
	return w.rowKey()
}

// label names the run in the registry.
func (w *mimoWorkflow) label() string {
	if name := strings.TrimSpace(w.name); name != "" {
		return bgtask.CleanTitleRunes(bgtask.FirstLine(name), 80)
	}
	return "MiMo workflow"
}

// mimoWorkflowEvent covers workflow.started, workflow.phase and
// workflow.finished.
type mimoWorkflowEvent struct {
	SessionID string `json:"sessionID"`
	RunID     string `json:"runID"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Status    string `json:"status"`
}

func (a *Agent) handleWorkflowEvent(event mimoEvent) {
	var payload mimoWorkflowEvent
	if err := json.Unmarshal(event.Properties, &payload); err != nil || payload.RunID == "" {
		slog.Warn("mimo workflow event unreadable", "agent_id", a.AgentID(), "type", event.Type, "error", err)
		return
	}
	a.Mu.Lock()
	if !a.ownsSessionLocked(payload.SessionID) {
		a.Mu.Unlock()
		return
	}
	workflow := a.workflows[payload.RunID]
	if workflow == nil {
		workflow = &mimoWorkflow{runID: payload.RunID}
		a.workflows[payload.RunID] = workflow
	}
	if name := strings.TrimSpace(payload.Name); name != "" {
		workflow.name = name
	}
	if workflow.finished {
		a.Mu.Unlock()
		return
	}
	rowKey, label := workflow.rowKey(), workflow.label()
	upsert := bgtask.Upsert{
		RowKey:     rowKey,
		Kind:       bgtask.KindWorkflow,
		GroupKey:   workflow.groupKey(),
		GroupLabel: label,
		Title:      label,
		Status:     bgtask.StatusRunning,
	}
	switch event.Type {
	case eventWorkflowFinished:
		workflow.finished = true
		a.Mu.Unlock()
		providerkit.LogRegistryRefusal("mimo", "upsert", a.sink.UpsertBackgroundTask(upsert))
		providerkit.LogRegistryRefusal("mimo", "close", a.sink.CloseBackgroundTask(rowKey, workflowStatus(payload.Status)))
	case eventWorkflowPhase:
		a.Mu.Unlock()
		providerkit.LogRegistryRefusal("mimo", "upsert", a.sink.UpsertBackgroundTask(upsert))
		providerkit.LogRegistryRefusal("mimo", "status", a.sink.UpdateBackgroundTaskStatus(rowKey, bgtask.StatusRunning, strings.TrimSpace(payload.Title)))
	default:
		a.Mu.Unlock()
		providerkit.LogRegistryRefusal("mimo", "upsert", a.sink.UpsertBackgroundTask(upsert))
	}
}

// workflowStatus maps a finished run's status onto the registry's.
func workflowStatus(status string) bgtask.Status {
	switch status {
	case workflowCompleted:
		return bgtask.StatusCompleted
	case workflowCancelled:
		return bgtask.StatusStopped
	case workflowFailed:
		return bgtask.StatusFailed
	default:
		// A finished run always ends, so an unknown word still closes the row, as
		// the reading that claims the least.
		slog.Debug("mimo unknown workflow status", "status", status)
		return bgtask.StatusStopped
	}
}

// soleRunningWorkflowLocked returns the run id of the one workflow that runs,
// or "" when none runs or more than one does. An actor that registers while
// exactly one run is active belongs to that run; with two, nothing on the wire
// says which. The caller holds a.Mu.
func (a *Agent) soleRunningWorkflowLocked() string {
	found := ""
	for runID, workflow := range a.workflows {
		if workflow.finished {
			continue
		}
		if found != "" {
			return ""
		}
		found = runID
	}
	return found
}
