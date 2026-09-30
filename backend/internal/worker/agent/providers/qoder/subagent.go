package qoder

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	qoderTaskStarted      = "task_started"
	qoderTaskNotification = "task_notification"
	qoderTaskLocalAgent   = "local_agent"
	qoderWorkflowAgent    = "workflow-subagent"
)

// A workflow key combines the native session ID with the model tool ID.
// The output loop owns active runs. Archive retries keep the captured key.
type qoderWorkflowKey struct {
	sessionID string
	toolUseID string
}

type qoderWorkflowRun struct {
	key      qoderWorkflowKey
	taskID   string
	runID    string
	label    string
	children map[string]*qoderWorkflowChild
	order    []string
}

type qoderWorkflowChild struct {
	taskID  string
	title   string
	prompt  string
	childID string
}

// Qoder reports one task on the root stream before it forwards child frames.
// Its tool-use ID also appears on each child as parent_tool_use_id.
type qoderTaskEvent struct {
	SessionID    string `json:"session_id"`
	Subtype      string `json:"subtype"`
	TaskID       string `json:"task_id"`
	ToolUseID    string `json:"tool_use_id"`
	TaskType     string `json:"task_type"`
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	Status       string `json:"status"`
	OutputFile   string `json:"output_file"`
}

// handleWorkflowLaunch reads Qoder's structured result of a Workflow tool.
// A workflow can start with zero children, so this result opens its row.
func (a *Agent) handleWorkflowLaunch(raw []byte) {
	var frame struct {
		SessionID string `json:"session_id"`
		Message   struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"message"`
		ToolUseResult struct {
			Summary string          `json:"summary"`
			Payload json.RawMessage `json:"payload"`
		} `json:"tool_use_result"`
	}
	if json.Unmarshal(raw, &frame) != nil || len(frame.ToolUseResult.Payload) == 0 {
		return
	}
	toolID := ""
	for _, block := range frame.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		if toolID != "" || block.ToolUseID == "" {
			return
		}
		toolID = block.ToolUseID
	}
	if toolID == "" {
		return
	}
	var payloadText string
	if json.Unmarshal(frame.ToolUseResult.Payload, &payloadText) != nil {
		return
	}
	var payload struct {
		Status string `json:"status"`
		TaskID string `json:"taskId"`
		RunID  string `json:"runId"`
	}
	if json.Unmarshal([]byte(payloadText), &payload) != nil || payload.Status != "async_launched" || !strings.HasPrefix(payload.TaskID, "wf-") || !strings.HasPrefix(payload.RunID, "wf_") {
		return
	}
	key := a.workflowKey(frame.SessionID, toolID)
	run := a.workflowRun(key)
	if run.taskID != "" && run.taskID != payload.TaskID {
		slog.Warn("qoder workflow launch changed task identity", "agent_id", a.AgentID(), "tool_use_id", toolID)
		return
	}
	run.taskID = payload.TaskID
	run.runID = payload.RunID
	if label := bgtask.FirstLine(frame.ToolUseResult.Summary); label != "" {
		run.label = label
	}
	a.upsertWorkflowRows(run)
}

func (a *Agent) workflowKey(sessionID, toolUseID string) qoderWorkflowKey {
	if sessionID == "" {
		a.mu.Lock()
		sessionID = a.sessionID
		a.mu.Unlock()
	}
	return qoderWorkflowKey{sessionID: sessionID, toolUseID: toolUseID}
}

func (a *Agent) workflowRun(key qoderWorkflowKey) *qoderWorkflowRun {
	if a.workflows == nil {
		a.workflows = make(map[qoderWorkflowKey]*qoderWorkflowRun)
	}
	run := a.workflows[key]
	if run == nil {
		run = &qoderWorkflowRun{key: key, label: "Workflow", children: make(map[string]*qoderWorkflowChild)}
		a.workflows[key] = run
	}
	return run
}

func qoderWorkflowRowKey(key qoderWorkflowKey) string {
	return bgtask.NormalizeRowKey("workflow:" + key.sessionID + ":" + key.toolUseID)
}

func (a *Agent) upsertWorkflowRows(run *qoderWorkflowRun) {
	rowKey := qoderWorkflowRowKey(run.key)
	providerkit.LogRegistryRefusal("qoder", "upsert workflow", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindWorkflow, ParentAgentID: a.AgentID(),
		GroupKey: rowKey, GroupLabel: run.label, Title: run.label, Status: bgtask.StatusRunning,
	}))
	for _, taskID := range run.order {
		child := run.children[taskID]
		providerkit.LogRegistryRefusal("qoder", "upsert workflow child", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: child.taskID, Kind: bgtask.KindSubagent, ChildAgentID: child.childID,
			ParentAgentID: a.AgentID(), GroupKey: rowKey, GroupLabel: run.label,
			Title: child.title, Status: bgtask.StatusRunning,
		}))
	}
}

// handleTaskEvent owns the registry row and child transcript for a native task.
func (a *Agent) handleTaskEvent(raw []byte) bool {
	var event qoderTaskEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return false
	}
	switch event.Subtype {
	case qoderTaskStarted:
		if event.TaskType != qoderTaskLocalAgent {
			return false
		}
		if event.SubagentType == qoderWorkflowAgent {
			return a.startWorkflowChild(&event)
		}
		if event.TaskID == "" || event.ToolUseID == "" {
			slog.Warn("qoder child start lacks an identity", "agent_id", a.AgentID())
			return true
		}
		return a.startChildTask(&event)
	case qoderTaskNotification:
		if event.TaskID == "" || event.ToolUseID == "" {
			return false
		}
		key := a.workflowKey(event.SessionID, event.ToolUseID)
		if run := a.workflows[key]; run != nil {
			return a.finishWorkflow(&event, run)
		}
		if a.retryPendingWorkflowArchive(key) || strings.HasPrefix(event.TaskID, "wf-") {
			return true
		}
		return a.finishChildTask(&event)
	default:
		return false
	}
}

func (a *Agent) startWorkflowChild(event *qoderTaskEvent) bool {
	if event.TaskID == "" || event.ToolUseID == "" {
		slog.Warn("qoder workflow child start lacks an identity", "agent_id", a.AgentID())
		return true
	}
	run := a.workflowRun(a.workflowKey(event.SessionID, event.ToolUseID))
	if run.children[event.TaskID] != nil {
		return true
	}
	title := bgtask.FirstLine(event.Description)
	if title == "" {
		title = bgtask.FirstLine(event.Prompt)
	}
	if title == "" {
		title = "Workflow child"
	}
	child := &qoderWorkflowChild{taskID: event.TaskID, title: title, prompt: event.Prompt}
	if childID, err := a.sink.EnsureChildAgent(event.TaskID, event.TaskID, title); err != nil {
		providerkit.LogRegistryRefusal("qoder", "open workflow child", err)
	} else {
		child.childID = childID
		if err := a.sink.PersistChildPrompt(childID, event.Prompt); err != nil {
			slog.Error("qoder persist workflow child prompt", "agent_id", a.AgentID(), "error", err)
		}
	}
	run.children[event.TaskID] = child
	run.order = append(run.order, event.TaskID)
	a.upsertWorkflowRows(run)
	return true
}

func (a *Agent) finishWorkflow(event *qoderTaskEvent, run *qoderWorkflowRun) bool {
	if run.taskID != "" && event.TaskID != run.taskID {
		slog.Warn("qoder workflow notification changed task identity", "agent_id", a.AgentID(), "tool_use_id", event.ToolUseID)
		return true
	}
	status, ok := qoderFinalChildStatus(event.Status)
	if !ok {
		slog.Warn("qoder workflow status is unknown", "agent_id", a.AgentID(), "status", event.Status)
		return true
	}
	archive, err := readQoderWorkflowArchive(a.opts, run.key.sessionID, event, run)
	if err != nil {
		slog.Warn("qoder workflow archive unavailable", "agent_id", a.AgentID(), "error", err)
		if errors.Is(err, errQoderArchiveIncomplete) {
			providerkit.LogRegistryRefusal("qoder", "close workflow", a.sink.CloseBackgroundTask(qoderWorkflowRowKey(run.key), status))
			for _, taskID := range run.order {
				providerkit.LogRegistryRefusal("qoder", "await workflow child transcript", a.sink.UpdateBackgroundTaskStatus(taskID, bgtask.StatusRunning, "Loading saved transcript"))
			}
			delete(a.workflows, run.key)
			a.startWorkflowArchiveRetry(*event, run, status)
			return true
		}
		a.closeWorkflowWithoutArchive(run, status, err)
		delete(a.workflows, run.key)
		return true
	}
	a.applyWorkflowArchive(run, status, archive)
	delete(a.workflows, run.key)
	return true
}

func (a *Agent) applyWorkflowArchive(run *qoderWorkflowRun, status bgtask.Status, archive qoderWorkflowArchive) {
	run.label = archive.name
	routeErrors := make(map[string]error)
	for _, taskID := range archive.order {
		if run.children[taskID] != nil {
			continue
		}
		saved := archive.children[taskID]
		title := bgtask.FirstLine(saved.prompt)
		child := &qoderWorkflowChild{taskID: taskID, title: title, prompt: saved.prompt}
		if childID, err := a.sink.EnsureChildAgent(taskID, taskID, title); err != nil {
			routeErrors[taskID] = fmt.Errorf("open saved workflow child: %w", err)
		} else {
			child.childID = childID
		}
		run.children[taskID] = child
		run.order = append(run.order, taskID)
	}
	a.upsertWorkflowRows(run)
	for _, taskID := range run.order {
		child := run.children[taskID]
		childStatus := status
		routeErr := routeErrors[taskID]
		if saved, found := archive.children[taskID]; found {
			childStatus = saved.status
			if child.childID == "" && routeErr == nil {
				routeErr = errors.New("the workflow child has no transcript route")
			}
			if routeErr == nil {
				if err := a.sink.PersistChildPrompt(child.childID, saved.prompt); err != nil {
					routeErr = fmt.Errorf("persist saved workflow prompt: %w", err)
				}
			}
			if routeErr == nil {
				for _, message := range saved.messages {
					if message.initialPrompt {
						continue
					}
					var err error
					if message.userText != "" {
						err = a.sink.PersistChildUserMessage(child.childID, message.userText)
					} else {
						err = a.sink.PersistChildMessage(child.childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, message.raw, agent.SpanInfo{})
					}
					if err != nil {
						routeErr = fmt.Errorf("persist workflow child history: %w", err)
						break
					}
				}
			}
		}
		if routeErr != nil {
			childStatus = bgtask.StatusFailed
			a.reportWorkflowArchiveFailure(fmt.Errorf("child %s: %w", taskID, routeErr))
		}
		providerkit.LogRegistryRefusal("qoder", "close workflow child", a.sink.CloseBackgroundTask(taskID, childStatus))
		if child.childID != "" {
			a.sink.CleanupChildAgent(child.childID)
		}
	}
	providerkit.LogRegistryRefusal("qoder", "close workflow", a.sink.CloseBackgroundTask(qoderWorkflowRowKey(run.key), status))
}

func (a *Agent) closeWorkflowWithoutArchive(run *qoderWorkflowRun, status bgtask.Status, readErr error) {
	for _, taskID := range run.order {
		child := run.children[taskID]
		providerkit.LogRegistryRefusal("qoder", "close workflow child", a.sink.CloseBackgroundTask(taskID, status))
		if child.childID != "" {
			a.sink.CleanupChildAgent(child.childID)
		}
	}
	providerkit.LogRegistryRefusal("qoder", "close workflow", a.sink.CloseBackgroundTask(qoderWorkflowRowKey(run.key), status))
	a.reportWorkflowArchiveFailure(readErr)
}

func (a *Agent) startChildTask(event *qoderTaskEvent) bool {
	title := event.Description
	if title == "" {
		title = "Subagent"
	}
	childID, err := a.sink.EnsureChildAgent(event.ToolUseID, event.ToolUseID, title)
	if err != nil {
		providerkit.LogRegistryRefusal("qoder", "open child", err)
		return true
	}
	if err := a.sink.PersistChildPrompt(childID, event.Prompt); err != nil {
		slog.Error("qoder persist child prompt", "agent_id", a.AgentID(), "error", err)
	}
	providerkit.LogRegistryRefusal("qoder", "start child", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:       event.ToolUseID,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: childID,
		Title:        title,
		Status:       bgtask.StatusRunning,
	}))
	return true
}

func qoderFinalChildStatus(status string) (bgtask.Status, bool) {
	switch status {
	case "completed":
		return bgtask.StatusCompleted, true
	case "failed":
		return bgtask.StatusFailed, true
	case "stopped", "cancelled":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusUnspecified, false
	}
}

func (a *Agent) finishChildTask(event *qoderTaskEvent) bool {
	childID, _, found, err := a.sink.LookupBackgroundTask(event.ToolUseID)
	if err != nil {
		providerkit.LogRegistryRefusal("qoder", "find finished child", err)
		return true
	}
	if !found || childID == "" {
		return false
	}
	status, ok := qoderFinalChildStatus(event.Status)
	if !ok {
		slog.Warn("qoder child status is unknown", "agent_id", a.AgentID(), "status", event.Status)
		return true
	}
	if err := a.sink.CloseBackgroundTask(event.ToolUseID, status); err != nil {
		providerkit.LogRegistryRefusal("qoder", "close child", err)
	}
	a.clearChildStream(event.ToolUseID)
	a.sink.CleanupChildAgent(childID)
	return true
}

// routeChildFrame keeps every known forwarded message out of the root tab.
func (a *Agent) routeChildFrame(raw []byte) bool {
	var frame struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil || frame.ParentToolUseID == "" {
		return false
	}
	childID, _, found, err := a.sink.LookupBackgroundTask(frame.ParentToolUseID)
	if err != nil {
		providerkit.LogRegistryRefusal("qoder", "find child frame", err)
		return true
	}
	if !found || childID == "" {
		slog.Warn("qoder frame has no known child", "agent_id", a.AgentID(), "spawn_tool_use_id", frame.ParentToolUseID)
		return true
	}
	if frame.Type == "stream_event" {
		a.handleChildStreamFrame(childID, frame.ParentToolUseID, raw)
		return true
	}
	if err := a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
		slog.Error("qoder persist child frame", "agent_id", a.AgentID(), "error", err)
	}
	return true
}
