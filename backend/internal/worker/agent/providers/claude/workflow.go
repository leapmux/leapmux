package claude

import (
	"strconv"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Workflow agent rows.
//
// A Workflow run reaches the stream as one `local_workflow` task, and Claude
// Code reports each agent of that run in `task_progress.workflow_progress`
// (2.1.310 probes in .tmp/claude-wf/). Every frame carries a FULL snapshot:
// each agent entry repeated with its current state, beside one
// `workflow_phase` entry per phase. An agent's `index` is stable across the
// snapshots, so LeapMux draws one row per agent under the run's group and the
// Background tasks sidebar shows the run's work under one heading.
//
// A workflow agent owns no transcript: the CLI forwards none of its envelopes,
// so its row is registry-only, like the run's own row.

// The states of a workflow agent. `start` and `progress` are working states.
// `done` and `error` are final states.
const (
	claudeWorkflowAgentStateStart    = "start"
	claudeWorkflowAgentStateProgress = "progress"
	claudeWorkflowAgentStateDone     = "done"
	claudeWorkflowAgentStateError    = "error"
)

// claudeWorkflowProgressEntry is one entry of a task_progress snapshot. The
// CLI's own schema (a zod union on `type`) is the authority: a `workflow_agent`
// holds an agent's state, and a `workflow_phase` holds a phase title alone.
type claudeWorkflowProgressEntry struct {
	Type            string `json:"type"`
	Index           int    `json:"index"`
	Label           string `json:"label"`
	State           string `json:"state"`
	PhaseTitle      string `json:"phaseTitle"`
	AgentID         string `json:"agentId"`
	Error           string `json:"error"`
	LastToolName    string `json:"lastToolName"`
	LastToolSummary string `json:"lastToolSummary"`
	ResultPreview   string `json:"resultPreview"`
	PromptPreview   string `json:"promptPreview"`
}

// claudeWorkflowRun is one tracked run: the group its rows share, and the
// newest row of each of its agents in first-seen order.
type claudeWorkflowRun struct {
	groupKey   string
	groupLabel string
	agents     map[int]bgtask.Upsert
	order      []int
}

// claudeWorkflowRuns tracks the workflow runs of the current process. A run
// enters through task_started, which is the only frame that states the
// workflow name, and leaves through task_notification.
type claudeWorkflowRuns struct {
	mu   sync.Mutex
	runs map[string]*claudeWorkflowRun
}

// started records the group of a workflow run's rows. A repeated task_started
// for a run this process already tracks keeps the recorded group: the frames
// that follow route by task ID alone.
func (w *claudeWorkflowRuns) started(taskID, groupKey, groupLabel string) {
	if taskID == "" || groupKey == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.runs == nil {
		w.runs = make(map[string]*claudeWorkflowRun)
	}
	if _, known := w.runs[taskID]; known {
		return
	}
	w.runs[taskID] = &claudeWorkflowRun{
		groupKey:   groupKey,
		groupLabel: groupLabel,
		agents:     make(map[int]bgtask.Upsert),
	}
}

// progress applies one snapshot of a run's agents and returns the rows that
// changed. A run no task_started recorded has no group its rows could join, so
// its agents draw nothing. Phase entries carry no agent and draw nothing.
func (w *claudeWorkflowRuns) progress(taskID string, entries []claudeWorkflowProgressEntry) []bgtask.Upsert {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[taskID]
	if run == nil {
		return nil
	}
	var upserts []bgtask.Upsert
	for _, entry := range entries {
		if entry.Type != "workflow_agent" {
			continue
		}
		row := claudeWorkflowAgentUpsert(taskID, run, entry)
		previous, known := run.agents[entry.Index]
		if !known {
			run.order = append(run.order, entry.Index)
		}
		run.agents[entry.Index] = row
		if known && previous == row {
			// An unchanged agent writes nothing: a snapshot repeats every agent.
			continue
		}
		upserts = append(upserts, row)
	}
	return upserts
}

// finished ends a run: each agent row that a snapshot did not settle takes the
// run's own final status, and the run is forgotten. A settled row keeps the
// outcome its snapshot stated.
func (w *claudeWorkflowRuns) finished(taskID string, status bgtask.Status) []bgtask.Upsert {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[taskID]
	if run == nil {
		return nil
	}
	delete(w.runs, taskID)
	var upserts []bgtask.Upsert
	for _, index := range run.order {
		agent := run.agents[index]
		if agent.Status.IsFinished() {
			continue
		}
		agent.Status = status
		upserts = append(upserts, agent)
	}
	return upserts
}

// claudeWorkflowAgentUpsert builds the row of one agent: its label over its
// live activity, in the group of the run's own row. The agent's stable index
// keys the row, because two agents of one run can share a label.
func claudeWorkflowAgentUpsert(taskID string, run *claudeWorkflowRun, entry claudeWorkflowProgressEntry) bgtask.Upsert {
	title := strings.TrimSpace(entry.Label)
	description := claudeWorkflowAgentDescription(entry)
	if title == "" {
		// An unlabelled agent: its prompt states the work.
		title = bgtask.CleanTitleRunes(bgtask.FirstLine(strings.TrimSpace(entry.PromptPreview)), 120)
	}
	return bgtask.Upsert{
		RowKey:      taskID + "/wf/" + strconv.Itoa(entry.Index),
		Kind:        bgtask.KindSubagent,
		GroupKey:    run.groupKey,
		GroupLabel:  run.groupLabel,
		Title:       title,
		Description: description,
		Status:      claudeWorkflowAgentStatus(entry.State),
	}
}

// claudeWorkflowAgentDescription selects what an agent's row shows beneath its
// label: its newest tool while it works, its answer once done, its error once
// failed, and otherwise the head of its prompt.
func claudeWorkflowAgentDescription(entry claudeWorkflowProgressEntry) string {
	switch entry.State {
	case claudeWorkflowAgentStateDone:
		if preview := strings.TrimSpace(entry.ResultPreview); preview != "" {
			return preview
		}
	case claudeWorkflowAgentStateError:
		if message := strings.TrimSpace(entry.Error); message != "" {
			return message
		}
	}
	for _, candidate := range []string{entry.LastToolSummary, entry.LastToolName, entry.PromptPreview} {
		if text := strings.TrimSpace(candidate); text != "" {
			return bgtask.FirstLine(text)
		}
	}
	return ""
}

// claudeWorkflowAgentStatus maps an agent's state onto a row status. An
// unfamiliar state word stays a working row: the CLI's next snapshot states a
// state this mapping knows.
func claudeWorkflowAgentStatus(state string) bgtask.Status {
	switch state {
	case claudeWorkflowAgentStateDone:
		return bgtask.StatusSucceeded
	case claudeWorkflowAgentStateError:
		return bgtask.StatusFailed
	default:
		return bgtask.StatusRunning
	}
}
