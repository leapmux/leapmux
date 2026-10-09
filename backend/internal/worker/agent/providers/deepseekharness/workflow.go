package deepseekharness

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type nativeWorkflow struct {
	Name     string
	Children map[int]string
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
		a.Mu.Unlock()
		if !exists || child == nil || data.Sequence <= 0 {
			return fmt.Errorf("DeepSeek Harness workflow work has no owned run or child")
		}
		return sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: data.ChildID, Kind: bgtask.KindSubagent, ChildAgentID: child.agentID, GroupKey: key, GroupLabel: run.Name, Title: data.Label, Status: bgtask.StatusRunning})
	case contracts.DeepseekHarnessEventWorkflowAgentEnd:
		a.Mu.Lock()
		run, exists := a.workflows[data.RunID]
		childID := run.Children[data.Sequence]
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
		a.Mu.Lock()
		_, exists := a.workflows[data.RunID]
		delete(a.workflows, data.RunID)
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
		return sink.CloseBackgroundTask(key, status)
	}
	return nil
}
