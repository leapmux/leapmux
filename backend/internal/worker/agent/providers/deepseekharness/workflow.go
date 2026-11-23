package deepseekharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type nativeWorkflow struct {
	Name      string
	Children  map[int]string
	RowClosed bool
}

type pendingWorkflowChild struct {
	RunID      string
	GroupKey   string
	GroupLabel string
	Label      string
	Sequence   int
}

func (a *Agent) workflowEvent(stream *sessionStream, kind string, raw []byte) error {
	var data struct {
		RunID      string `json:"runId"`
		Name       string `json:"name"`
		ChildID    string `json:"childId"`
		Sequence   int    `json:"seq"`
		Label      string `json:"label"`
		Outcome    string `json:"outcome"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.RunID == "" {
		return fmt.Errorf("DeepSeek Harness workflow event has no identity")
	}
	key := "deepseek-workflow:" + data.RunID
	sink := a.streamSink(stream)
	switch kind {
	case contracts.DeepseekHarnessEventWorkflowStart:
		a.Mu.Lock()
		a.workflows[data.RunID] = nativeWorkflow{Name: data.Name, Children: map[int]string{}}
		a.Mu.Unlock()
		return sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindWorkflow, GroupKey: key, GroupLabel: data.Name, Title: data.Name, Status: bgtask.StatusRunning})
	case contracts.DeepseekHarnessEventWorkflowAgentStart:
		a.Mu.Lock()
		run, exists := a.workflows[data.RunID]
		child := a.children[data.ChildID]
		if exists && data.Sequence > 0 && data.ChildID != "" {
			run.Children[data.Sequence] = data.ChildID
			a.workflows[data.RunID] = run
		}
		if exists && child == nil && data.Sequence > 0 && data.ChildID != "" {
			if a.pendingWorkflowChildren == nil {
				a.pendingWorkflowChildren = map[string]pendingWorkflowChild{}
			}
			a.pendingWorkflowChildren[data.ChildID] = pendingWorkflowChild{
				RunID:      data.RunID,
				GroupKey:   key,
				GroupLabel: run.Name,
				Label:      data.Label,
				Sequence:   data.Sequence,
			}
			a.Mu.Unlock()
			return nil
		}
		a.Mu.Unlock()
		if !exists || data.Sequence <= 0 || data.ChildID == "" {
			return fmt.Errorf("DeepSeek Harness workflow work has no owned run or child")
		}
		return sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: data.ChildID, Kind: bgtask.KindSubagent, ChildAgentID: child.agentID, GroupKey: key, GroupLabel: run.Name, Title: data.Label, Status: bgtask.StatusRunning})
	case contracts.DeepseekHarnessEventWorkflowAgentEnd:
		a.Mu.Lock()
		run, exists := a.workflows[data.RunID]
		childID := run.Children[data.Sequence]
		if exists && childID != "" {
			delete(run.Children, data.Sequence)
			delete(a.pendingWorkflowChildren, childID)
			if child := a.children[childID]; child != nil {
				child.active = false
			}
		}
		a.Mu.Unlock()
		if !exists || childID == "" {
			return fmt.Errorf("DeepSeek Harness workflow completion has no owned work")
		}
		status := bgtask.StatusSucceeded
		switch data.Outcome {
		case "completed":
		case "cancelled":
			status = bgtask.StatusInterrupted
		case "failed":
			status = bgtask.StatusFailed
		default:
			return fmt.Errorf("DeepSeek Harness workflow outcome is invalid")
		}
		return sink.CloseBackgroundTask(childID, status)
	case contracts.DeepseekHarnessEventWorkflowEnd:
		type remainingWorkflowChild struct {
			seq     int
			childID string
		}
		a.Mu.Lock()
		run, exists := a.workflows[data.RunID]
		rowClosed := run.RowClosed
		var remainingChildren []remainingWorkflowChild
		if exists {
			openSeqs := make([]int, 0, len(run.Children))
			for seq := range run.Children {
				openSeqs = append(openSeqs, seq)
			}
			slices.Sort(openSeqs)
			remainingChildren = make([]remainingWorkflowChild, 0, len(openSeqs))
			for _, seq := range openSeqs {
				if childID := run.Children[seq]; childID != "" {
					remainingChildren = append(remainingChildren, remainingWorkflowChild{seq: seq, childID: childID})
				}
			}
		}
		a.Mu.Unlock()
		if !exists {
			return fmt.Errorf("DeepSeek Harness workflow completion has no owned run")
		}
		status := bgtask.StatusSucceeded
		if data.StopReason != "completed" {
			status = bgtask.StatusFailed
			if data.StopReason == "cancelled" {
				status = bgtask.StatusInterrupted
			}
		}
		var closeErr error
		closedChildren := make([]remainingWorkflowChild, 0, len(remainingChildren))
		for _, entry := range remainingChildren {
			if err := sink.CloseBackgroundTask(entry.childID, status); err != nil {
				closeErr = errors.Join(closeErr, err)
				continue
			}
			closedChildren = append(closedChildren, entry)
		}
		workflowClosed := rowClosed
		if !rowClosed {
			if err := sink.CloseBackgroundTask(key, status); err != nil {
				closeErr = errors.Join(closeErr, err)
			} else {
				workflowClosed = true
			}
		}
		a.Mu.Lock()
		if current, ok := a.workflows[data.RunID]; ok {
			for _, entry := range closedChildren {
				delete(current.Children, entry.seq)
				delete(a.pendingWorkflowChildren, entry.childID)
				if child := a.children[entry.childID]; child != nil {
					child.active = false
				}
			}
			current.RowClosed = workflowClosed
			if closeErr == nil {
				delete(a.workflows, data.RunID)
			} else {
				a.workflows[data.RunID] = current
			}
		}
		a.Mu.Unlock()
		return closeErr
	}
	return nil
}

// closeOpenWorkflows drains all open workflow runs and closes their background task rows.
func (a *Agent) closeOpenWorkflows(status bgtask.Status) {
	a.Mu.Lock()
	runIDs := make([]string, 0, len(a.workflows))
	for runID := range a.workflows {
		runIDs = append(runIDs, runID)
	}
	clear(a.workflows)
	a.Mu.Unlock()
	if len(runIDs) == 0 || a.sink == nil {
		return
	}
	slices.Sort(runIDs)
	for _, runID := range runIDs {
		providerkit.LogRegistryRefusal("deepseekharness", "close workflow", a.sink.CloseBackgroundTask("deepseek-workflow:"+runID, status))
	}
}
