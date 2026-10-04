package codebuddy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

var errCodebuddyChildRoute = errors.New("the CodeBuddy workflow child route failed")

// codebuddyTaskIndex holds only the links needed while native tasks run.
// The registry and child store hold the durable rows and transcripts.
type codebuddyTaskIndex struct {
	mu               sync.Mutex
	spawnRows        map[string]string
	spawnChildren    map[string]string
	workflowNames    map[string]string
	workflowChildren map[string]map[string]codebuddyWorkflowChild
	workflowByChild  map[string]string
	pendingEnds      map[string]bgtask.Status
}

func (i *codebuddyTaskIndex) child(spawnID string) (string, string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.spawnRows[spawnID], i.spawnChildren[spawnID]
}

func (i *codebuddyTaskIndex) rememberChild(spawnID, rowKey, childID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.spawnRows == nil {
		i.spawnRows = make(map[string]string)
		i.spawnChildren = make(map[string]string)
	}
	i.spawnRows[spawnID] = rowKey
	i.spawnChildren[spawnID] = childID
}

func (i *codebuddyTaskIndex) rememberWorkflow(taskID, name string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.workflowNames == nil {
		i.workflowNames = make(map[string]string)
	}
	i.workflowNames[taskID] = name
}

func (i *codebuddyTaskIndex) workflowName(taskID, fallback string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if name := i.workflowNames[taskID]; name != "" {
		return name
	}
	if fallback != "" {
		return fallback
	}
	return "Workflow"
}

func (i *codebuddyTaskIndex) rememberWorkflowChild(taskID, rowKey, title string, status bgtask.Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.workflowChildren == nil {
		i.workflowChildren = make(map[string]map[string]codebuddyWorkflowChild)
		i.workflowByChild = make(map[string]string)
	}
	if i.workflowChildren[taskID] == nil {
		i.workflowChildren[taskID] = make(map[string]codebuddyWorkflowChild)
	}
	if previous := i.workflowChildren[taskID][rowKey]; previous.status.IsFinished() && !status.IsFinished() {
		status = previous.status
	}
	i.workflowChildren[taskID][rowKey] = codebuddyWorkflowChild{rowKey: rowKey, title: title, status: status}
	i.workflowByChild[rowKey] = taskID
}

type codebuddyWorkflowChild struct {
	rowKey string
	title  string
	status bgtask.Status
}

func (i *codebuddyTaskIndex) rememberWorkflowChildEnd(rowKey string, status bgtask.Status) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	taskID, found := i.workflowByChild[rowKey]
	if !found {
		return false
	}
	child := i.workflowChildren[taskID][rowKey]
	child.status = status
	i.workflowChildren[taskID][rowKey] = child
	return true
}

func (i *codebuddyTaskIndex) finishWorkflow(taskID string) []codebuddyWorkflowChild {
	i.mu.Lock()
	defer i.mu.Unlock()
	children := make([]codebuddyWorkflowChild, 0, len(i.workflowChildren[taskID]))
	for rowKey, child := range i.workflowChildren[taskID] {
		children = append(children, child)
		delete(i.workflowByChild, rowKey)
	}
	sort.Slice(children, func(left, right int) bool { return children[left].rowKey < children[right].rowKey })
	delete(i.workflowChildren, taskID)
	delete(i.workflowNames, taskID)
	return children
}

func (i *codebuddyTaskIndex) forgetRow(rowKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for spawnID, key := range i.spawnRows {
		if key == rowKey {
			delete(i.spawnRows, spawnID)
			delete(i.spawnChildren, spawnID)
		}
	}
}

func (i *codebuddyTaskIndex) rememberEnd(taskID string, status bgtask.Status) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pendingEnds == nil {
		i.pendingEnds = make(map[string]bgtask.Status)
	}
	i.pendingEnds[taskID] = status
}

func (i *codebuddyTaskIndex) takeEnd(taskID string) (bgtask.Status, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	status, ok := i.pendingEnds[taskID]
	delete(i.pendingEnds, taskID)
	return status, ok
}

type codebuddyTaskEvent struct {
	Subtype          string `json:"subtype"`
	TaskID           string `json:"task_id"`
	ToolUseID        string `json:"tool_use_id"`
	TaskType         string `json:"task_type"`
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`
	Status           string `json:"status"`
	Summary          string `json:"summary"`
	WorkflowName     string `json:"workflow_name"`
	LastToolName     string `json:"last_tool_name"`
	WorkflowProgress []struct {
		Type       string `json:"type"`
		AgentID    string `json:"agentId"`
		State      string `json:"state"`
		Label      string `json:"label"`
		Title      string `json:"title"`
		PhaseTitle string `json:"phaseTitle"`
	} `json:"workflow_progress"`
}

func codebuddyTaskStatus(raw string) (bgtask.Status, bool) {
	switch raw {
	case "completed", "done", "success":
		return bgtask.StatusCompleted, true
	case "failed", "error":
		return bgtask.StatusFailed, true
	case "stopped", "killed", "cancelled":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusUnspecified, false
	}
}

// handleTaskEvent reads CodeBuddy's system/task_* frames. They describe native
// background tasks, so the registry owns them and the chat gets no raw copy.
func (a *Agent) handleTaskEvent(raw []byte) bool {
	var event codebuddyTaskEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return false
	}
	switch event.Subtype {
	case "task_started":
		a.handleTaskStarted(&event)
	case "task_progress":
		a.handleTaskProgress(&event)
	case "task_notification":
		a.handleTaskNotification(&event)
	case "task_updated", "background_tasks_changed":
		// The notification closes a task. An update can precede that final event.
	default:
		return false
	}
	return true
}

func (a *Agent) handleTaskStarted(event *codebuddyTaskEvent) {
	if event.TaskID == "" {
		return
	}
	title := strings.TrimSpace(event.Description)
	var kind bgtask.Kind
	switch event.TaskType {
	case "local_workflow":
		kind = bgtask.KindWorkflow
		if name := strings.TrimSpace(event.WorkflowName); name != "" {
			title = name
		}
		if title == "" {
			title = "Workflow"
		}
		a.tasks.rememberWorkflow(event.TaskID, title)
	case "local_agent":
		kind = bgtask.KindSubagent
		if title == "" {
			title = bgtask.FirstLine(event.Prompt)
		}
	case "local_bash":
		kind = bgtask.KindShell
		if title == "" {
			title = bgtask.FirstLine(event.Prompt)
		}
	default:
		return
	}
	if kind == bgtask.KindSubagent {
		spawnID := event.ToolUseID
		if spawnID == "" {
			spawnID = event.TaskID
		}
		oldKey, knownChild := a.tasks.child(spawnID)
		if oldKey != "" && oldKey != event.TaskID {
			providerkit.LogRegistryRefusal("codebuddy", "rename child", a.sink.RenameBackgroundTask(oldKey, event.TaskID))
		}
		a.tasks.rememberChild(spawnID, event.TaskID, knownChild)
		a.ensureChildRow(spawnID, event.TaskID, title, event.Prompt, "", "", true)
	} else {
		upsert := bgtask.Upsert{
			RowKey: event.TaskID, Kind: kind, ParentAgentID: a.AgentID(),
			Title: title, Status: bgtask.StatusRunning,
		}
		if kind == bgtask.KindWorkflow {
			upsert.GroupKey = event.TaskID
			upsert.GroupLabel = title
		}
		providerkit.LogRegistryRefusal("codebuddy", "start task", a.sink.UpsertBackgroundTask(upsert))
	}
	if status, ok := a.tasks.takeEnd(event.TaskID); ok {
		a.finishTask(event.TaskID, status, "")
	}
}

func (a *Agent) handleTaskProgress(event *codebuddyTaskEvent) {
	if event.TaskID == "" {
		return
	}
	activity := ""
	for _, progress := range event.WorkflowProgress {
		if progress.Type == "workflow_phase" && strings.TrimSpace(progress.Title) != "" {
			activity = strings.TrimSpace(progress.Title)
		}
	}
	if activity == "" {
		activity = strings.TrimSpace(event.LastToolName)
	}
	if activity == "" && len(event.WorkflowProgress) == 0 {
		activity = strings.TrimSpace(event.Description)
	}
	if activity != "" {
		_, priorStatus, exists, err := a.sink.LookupBackgroundTask(event.TaskID)
		if err != nil {
			slog.Warn("codebuddy: look up task progress", "task_id", event.TaskID, "error", err)
		} else if exists && !priorStatus.IsFinished() {
			providerkit.LogRegistryRefusal("codebuddy", "task activity", a.sink.UpdateBackgroundTaskStatus(event.TaskID, bgtask.StatusRunning, activity))
		}
	}
	if len(event.WorkflowProgress) == 0 {
		return
	}
	_, parentStatus, parentExists, err := a.sink.LookupBackgroundTask(event.TaskID)
	if err != nil {
		slog.Warn("codebuddy: look up workflow before child progress", "task_id", event.TaskID, "error", err)
		return
	}
	if parentExists && parentStatus.IsFinished() {
		return
	}
	groupLabel := a.tasks.workflowName(event.TaskID, activity)
	for _, progress := range event.WorkflowProgress {
		if progress.Type != "workflow_agent" || progress.AgentID == "" {
			continue
		}
		status := bgtask.StatusRunning
		if resolved, ok := codebuddyTaskStatus(progress.State); ok {
			status = resolved
		} else if progress.State != "start" && progress.State != "running" {
			continue
		}
		title := strings.TrimSpace(progress.Label)
		if title == "" {
			title = "Workflow agent"
		}
		_, priorStatus, exists, err := a.sink.LookupBackgroundTask(progress.AgentID)
		if err != nil {
			slog.Warn("codebuddy: look up workflow progress", "row_key", progress.AgentID, "error", err)
			continue
		}
		if exists && priorStatus.IsFinished() {
			continue
		}
		providerkit.LogRegistryRefusal("codebuddy", "workflow child", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: progress.AgentID, Kind: bgtask.KindSubagent,
			ParentAgentID: a.AgentID(), GroupKey: event.TaskID, GroupLabel: groupLabel,
			Title: title, Status: bgtask.StatusRunning,
		}))
		a.tasks.rememberWorkflowChild(event.TaskID, progress.AgentID, title, status)
		if status.IsFinished() {
			providerkit.LogRegistryRefusal("codebuddy", "await workflow child transcript", a.sink.UpdateBackgroundTaskStatus(progress.AgentID, bgtask.StatusRunning, "Loading saved transcript"))
		}
	}
}

func (a *Agent) handleTaskNotification(event *codebuddyTaskEvent) {
	if event.TaskID == "" {
		return
	}
	status, ok := codebuddyTaskStatus(event.Status)
	if !ok {
		return
	}
	if a.retryPendingWorkflowArchive(event.TaskID) {
		return
	}
	if a.recordPendingWorkflowChildEnd(event.TaskID, status) {
		return
	}
	if a.tasks.rememberWorkflowChildEnd(event.TaskID, status) {
		providerkit.LogRegistryRefusal("codebuddy", "await workflow child transcript", a.sink.UpdateBackgroundTaskStatus(event.TaskID, bgtask.StatusRunning, "Loading saved transcript"))
		return
	}
	if _, priorStatus, exists, err := a.sink.LookupBackgroundTask(event.TaskID); err != nil {
		slog.Warn("codebuddy: look up task notification", "task_id", event.TaskID, "error", err)
	} else if !exists {
		a.tasks.rememberEnd(event.TaskID, status)
		return
	} else if priorStatus.IsFinished() {
		// The first native outcome owns the final status and summary. A
		// repeated notification cannot replace either one.
		return
	}
	a.finishTask(event.TaskID, status, strings.TrimSpace(event.Summary))
}

func (a *Agent) finishTask(rowKey string, status bgtask.Status, summary string) {
	groupLabel := a.tasks.workflowName(rowKey, "")
	children := a.tasks.finishWorkflow(rowKey)
	if len(children) > 0 {
		a.finishWorkflowChildren(rowKey, groupLabel, status, summary, children)
		a.tasks.forgetRow(rowKey)
		return
	}
	providerkit.LogRegistryRefusal("codebuddy", "finish task", a.sink.UpdateBackgroundTaskStatus(rowKey, status, summary))
	providerkit.LogRegistryRefusal("codebuddy", "close task", a.sink.CloseBackgroundTask(rowKey, status))
	if childID, _, exists, err := a.sink.LookupBackgroundTask(rowKey); err == nil && exists && childID != "" {
		a.sink.CleanupChildAgent(childID)
	}
	a.tasks.forgetRow(rowKey)
}

func (a *Agent) finishWorkflowChildren(runID, groupLabel string, status bgtask.Status, summary string, children []codebuddyWorkflowChild) {
	a.mu.Lock()
	parentSessionID := a.sessionID
	a.mu.Unlock()
	providerkit.LogRegistryRefusal("codebuddy", "finish workflow", a.sink.UpdateBackgroundTaskStatus(runID, status, summary))
	providerkit.LogRegistryRefusal("codebuddy", "close workflow", a.sink.CloseBackgroundTask(runID, status))
	pending := make([]codebuddyWorkflowChild, 0, len(children))
	for _, child := range children {
		err := a.replayWorkflowChild(runID, child, groupLabel, parentSessionID)
		if errors.Is(err, errCodebuddyArchiveIncomplete) {
			pending = append(pending, child)
			providerkit.LogRegistryRefusal("codebuddy", "await workflow child transcript", a.sink.UpdateBackgroundTaskStatus(child.rowKey, bgtask.StatusRunning, "Loading saved transcript"))
			continue
		}
		if err != nil {
			if errors.Is(err, errCodebuddyChildRoute) {
				child.status = bgtask.StatusFailed
			}
			a.reportWorkflowArchiveFailure(child.rowKey, err)
		}
		a.closeWorkflowChild(child, status)
	}
	if len(pending) > 0 {
		a.startWorkflowArchiveRetry(runID, groupLabel, status, parentSessionID, pending)
	}
}

func (a *Agent) closeWorkflowChild(child codebuddyWorkflowChild, parentStatus bgtask.Status) {
	status := child.status
	if !status.IsFinished() {
		status = parentStatus
	}
	providerkit.LogRegistryRefusal("codebuddy", "finish workflow child", a.sink.UpdateBackgroundTaskStatus(child.rowKey, status, ""))
	providerkit.LogRegistryRefusal("codebuddy", "close workflow child", a.sink.CloseBackgroundTask(child.rowKey, status))
	childID, _, exists, err := a.sink.LookupBackgroundTask(child.rowKey)
	if err != nil {
		slog.Warn("codebuddy: look up workflow child", "row_key", child.rowKey, "error", err)
	} else if exists && childID != "" {
		a.sink.CleanupChildAgent(childID)
	}
}

// replayWorkflowChild exposes a transcript only after the native journal
// identifies one complete child file for this workflow agent key.
func (a *Agent) replayWorkflowChild(runID string, child codebuddyWorkflowChild, groupLabel, parentSessionID string) error {
	configDir := codebuddyConfigDir(agent.StoredSessionQuery{HomeDir: a.opts.HomeDir})
	records, err := readCodebuddyWorkflowChild(configDir, a.opts.WorkingDir, parentSessionID, runID, child.rowKey)
	if err != nil {
		return err
	}
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: child.rowKey, ProviderChildKey: child.rowKey, Title: child.title})
	if err != nil {
		return fmt.Errorf("%w: ensure workflow child %s: %w", errCodebuddyChildRoute, child.rowKey, err)
	}
	linked := false
	defer func() {
		if !linked {
			a.sink.CleanupChildAgent(childID)
		}
	}()
	toolNames := make(map[string]string)
	for index, record := range records {
		if record.Type == "message" && record.Role == "user" {
			value := codebuddyArchiveUserText(record.Content)
			if index == 0 {
				err = a.sink.PersistChildPrompt(childID, value)
			} else {
				err = a.sink.PersistChildUserMessage(childID, value)
			}
		} else {
			span := agent.SpanInfo{}
			switch record.Type {
			case "function_call":
				span.SpanID = record.toolCallID()
				span.SpanType = record.Name
				toolNames[span.SpanID] = span.SpanType
			case "function_call_output", "function_call_result":
				span.SpanID = record.toolCallID()
				span.SpanType = toolNames[span.SpanID]
				span.Closing = record.Status != "in_progress"
			}
			err = a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, record.Raw, span)
		}
		if err != nil {
			return fmt.Errorf("%w: replay workflow child %s row %d: %w", errCodebuddyChildRoute, child.rowKey, index, err)
		}
	}
	_, status, exists, err := a.sink.LookupBackgroundTask(child.rowKey)
	if err != nil {
		return fmt.Errorf("%w: workflow child row %s unavailable: %w", errCodebuddyChildRoute, child.rowKey, err)
	}
	if !exists {
		return fmt.Errorf("%w: workflow child row %s is absent", errCodebuddyChildRoute, child.rowKey)
	}
	if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: child.rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), GroupKey: runID, GroupLabel: groupLabel,
		Title: child.title, Status: status,
	}); err != nil {
		return fmt.Errorf("%w: link workflow child %s: %w", errCodebuddyChildRoute, child.rowKey, err)
	}
	linked = true
	return nil
}

// ensureChildRow links one native subagent to a virtual child transcript.
func (a *Agent) ensureChildRow(spawnID, rowKey, title, prompt, groupKey, groupLabel string, open bool) string {
	if spawnID == "" {
		return ""
	}
	if rowKey == "" {
		rowKey = spawnID
	}
	knownRow, knownChild := a.tasks.child(spawnID)
	if knownRow != "" {
		rowKey = knownRow
	}
	childID := knownChild
	if childID == "" {
		var err error
		childID, err = a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnID, ProviderChildKey: rowKey, Title: title})
		if err != nil {
			slog.Warn("codebuddy: ensure child", "spawn_id", spawnID, "error", err)
			return ""
		}
	}
	if open {
		_, priorStatus, exists, err := a.sink.LookupBackgroundTask(rowKey)
		if err != nil {
			slog.Warn("codebuddy: look up child", "row_key", rowKey, "error", err)
		} else if exists && priorStatus.IsFinished() {
			providerkit.LogRegistryRefusal("codebuddy", "revive child", a.sink.ReviveBackgroundTask(rowKey))
		}
		providerkit.LogRegistryRefusal("codebuddy", "upsert child", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
			ParentAgentID: a.AgentID(), GroupKey: groupKey, GroupLabel: groupLabel,
			Title: title, Status: bgtask.StatusRunning,
		}))
	}
	if prompt != "" {
		if err := a.sink.PersistChildPrompt(childID, prompt); err != nil {
			slog.Warn("codebuddy: persist child prompt", "row_key", rowKey, "error", err)
		}
	}
	a.tasks.rememberChild(spawnID, rowKey, childID)
	return childID
}

// observeAgentToolUses opens foreground Agent rows. Those calls need no native
// task_started frame: CodeBuddy forwards child messages under their tool-use id.
func (a *Agent) observeAgentToolUses(raw []byte) {
	var frame struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Input struct {
					Description string `json:"description"`
					Prompt      string `json:"prompt"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	for _, block := range frame.Message.Content {
		if block.Type != "tool_use" || (block.Name != "Agent" && block.Name != "Task") || block.ID == "" {
			continue
		}
		title := strings.TrimSpace(block.Input.Description)
		if title == "" {
			title = bgtask.FirstLine(block.Input.Prompt)
		}
		a.ensureChildRow(block.ID, block.ID, title, block.Input.Prompt, "", "", true)
	}
}

// observeAgentToolResults closes a foreground child when its Agent tool ends.
// A background child has a separate task id and closes on task_notification.
func (a *Agent) observeAgentToolResults(raw []byte) {
	var frame struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	for _, block := range frame.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		rowKey, _ := a.tasks.child(block.ToolUseID)
		if rowKey != block.ToolUseID {
			continue
		}
		status := bgtask.StatusCompleted
		if block.IsError {
			status = bgtask.StatusFailed
		}
		a.finishTask(rowKey, status, "")
	}
}

// routeChildFrame sends a forwarded child frame to the child's transcript.
func (a *Agent) routeChildFrame(frameType string, raw []byte) bool {
	if frameType != "assistant" && frameType != "user" && frameType != "result" {
		return false
	}
	var envelope struct {
		ParentToolUseID string `json:"parent_tool_use_id"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ParentToolUseID == "" {
		return false
	}
	rowKey, _ := a.tasks.child(envelope.ParentToolUseID)
	childID := a.ensureChildRow(envelope.ParentToolUseID, rowKey, "", "", "", "", rowKey == "")
	if childID == "" {
		return true
	}
	var err error
	if frameType == "result" {
		err = a.sink.PersistChildTurnEnd(childID, agent.MessageContent{Original: raw}, agent.SpanInfo{})
	} else {
		err = a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{})
	}
	if err != nil {
		slog.Warn("codebuddy: persist child frame", "spawn_id", envelope.ParentToolUseID, "error", err)
	}
	return true
}
