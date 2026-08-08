package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// claudeTaskEnvelope is the parsed shape of a Claude system event of subtype
// task_started / task_progress / task_notification / task_updated /
// background_tasks_changed. Every field is optional; unknown subtypes are
// ignored (claudeHandleTaskEvent returns false -> normal persist path).
type claudeTaskEnvelope struct {
	Subtype      string           `json:"subtype"`
	TaskID       string           `json:"task_id"`
	ToolUseID    string           `json:"tool_use_id"`
	TaskType     string           `json:"task_type"` // local_agent | local_bash | local_workflow
	Description  string           `json:"description"`
	Prompt       string           `json:"prompt"` // task_started carries the spawn prompt
	Status       string           `json:"status"` // task_notification: completed|failed|stopped
	Summary      string           `json:"summary"`
	OutputFile   string           `json:"output_file"`
	WorkflowName string           `json:"workflow_name"`
	LastToolName string           `json:"last_tool_name"`
	Usage        *claudeTaskUsage `json:"usage"`
}

type claudeTaskUsage struct {
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int64 `json:"tool_uses"`
	DurationMs  int64 `json:"duration_ms"`
}

// claudeHandleTaskEvent parses a Claude `system` line and, when it is a
// task/workflow event, drives the background-task registry (and, for local_agent
// Task subagents, pre-creates the child transcript). Returns true when the
// event was consumed (so the caller skips the normal persist path); false for
// any non-task system line so it falls through unchanged.
//
// Findings (Claude 2.1.220, --forward-subagent-text):
//   - task_started {task_id, tool_use_id, task_type, description, workflow_name?}
//     fires for Task subagents (local_agent), bg shells (local_bash), and
//     Workflow runs (local_workflow).
//   - task_progress {task_id, description, last_tool_name?, usage?, workflow_progress?}
//   - task_notification {task_id, tool_use_id, status, summary, output_file, usage?}
//     is terminal and fires for foreground Tasks too.
//   - task_updated {task_id, patch:{status,end_time}} is redundant with the
//     notification; consumed as a no-op refresh.
//   - background_tasks_changed is Claude's own bg-shell list push; redundant
//     with task_* events here, so consumed silently.
//   - Workflow agents do NOT forward their transcripts (0 parent_tool_use_id
//     rows); they are registry-only, grouped by workflow_name.
func (a *ClaudeCodeAgent) claudeHandleTaskEvent(content []byte) bool {
	var ev claudeTaskEnvelope
	if err := json.Unmarshal(content, &ev); err != nil {
		return false
	}
	switch ev.Subtype {
	case "task_started":
		a.handleClaudeTaskStarted(&ev)
		return true
	case "task_progress":
		a.handleClaudeTaskProgress(&ev)
		return true
	case "task_notification":
		a.handleClaudeTaskNotification(&ev)
		return true
	case "task_updated", "background_tasks_changed":
		// Consumed: redundant with the task_* events above. Our registry is
		// authoritative, so these carry no extra information.
		return true
	case "session_state_changed":
		// Consumed silently: carries session bookkeeping we don't surface.
		return true
	default:
		return false
	}
}

// handleClaudeTaskStarted records the task<->tool_use index and upserts the
// registry row. For a local_agent Task with a tool_use_id it also pre-creates
// the child transcript so the registry links early and forwarded envelopes
// resolve immediately.
func (a *ClaudeCodeAgent) handleClaudeTaskStarted(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// Record the index (task_id <-> tool_use_id). Guarded by a.mu; never
	// cleared at turn end (background tasks outlive turns).
	a.mu.Lock()
	if a.taskToolUse == nil {
		a.taskToolUse = make(map[string]string)
	}
	if a.toolUseTask == nil {
		a.toolUseTask = make(map[string]string)
	}
	if ev.ToolUseID != "" {
		a.taskToolUse[ev.TaskID] = ev.ToolUseID
		a.toolUseTask[ev.ToolUseID] = ev.TaskID
	}
	// A terminal result that arrived before this (reordered) task_started left
	// a pending close keyed by the spawn span. Take it now and close the row
	// this upsert is about to open, so the row cannot leak Running.
	var pendingEnd bgtask.Status
	var hasPending bool
	if ev.ToolUseID != "" && a.pendingTaskEnd != nil {
		if s, ok := a.pendingTaskEnd[ev.ToolUseID]; ok {
			pendingEnd, hasPending = s, true
			delete(a.pendingTaskEnd, ev.ToolUseID)
		}
	}
	a.mu.Unlock()

	kind := bgtask.KindSubagent
	if ev.TaskType == "local_bash" {
		kind = bgtask.KindShell
	}
	groupKey, groupLabel := a.workflowGroup(ev)

	// Some task_started events omit description but carry the spawn prompt;
	// fall back to the prompt's first line so the row never has an empty title.
	title := ev.Description
	if title == "" {
		title = bgtask.FirstLine(ev.Prompt)
	}

	// Registry upsert (kind shell -> registry only; local_workflow -> registry
	// only, grouped; local_agent -> registry + child transcript below).
	upsert := bgtask.Upsert{
		RowKey:        ev.TaskID,
		Kind:          kind,
		ParentAgentID: a.agentID,
		Title:         title,
		GroupKey:      groupKey,
		GroupLabel:    groupLabel,
		Description:   title,
		Status:        bgtask.StatusRunning,
	}
	if err := a.sink.UpsertBackgroundTask(upsert); err != nil {
		slog.Warn("claude task_started upsert failed", "task_id", ev.TaskID, "error", err)
	}

	// Pre-create the child transcript for a Task subagent (local_agent) that
	// owns a tool_use span. local_bash (shell) and local_workflow have no
	// transcript; their envelopes are never forwarded.
	if ev.TaskType != "local_bash" && ev.TaskType != "local_workflow" && ev.ToolUseID != "" {
		if _, err := a.sink.EnsureChildAgent(ev.ToolUseID, ev.TaskID, title); err != nil {
			slog.Warn("claude task_started ensure child failed", "task_id", ev.TaskID, "error", err)
		}
	}

	// Apply a pending terminal close from a result that arrived before this
	// (reordered) task_started. The upsert above opened a Running row; close it
	// now so it cannot leak. forgetTaskIndex mirrors the normal result path.
	if hasPending {
		_ = a.sink.UpdateBackgroundTaskStatus(ev.TaskID, pendingEnd, "")
		_ = a.sink.CloseBackgroundTask(ev.TaskID, pendingEnd)
		a.forgetTaskIndex(ev.TaskID)
	}
}

func (a *ClaudeCodeAgent) handleClaudeTaskProgress(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// Probe-verified order (task_progress has no summary): description, then
	// last_tool_name, then a usage-derived string.
	activity := ev.Description
	if activity == "" {
		activity = ev.Summary
	}
	if activity == "" {
		activity = ev.LastToolName
	}
	if activity == "" && ev.Usage != nil {
		activity = fmt.Sprintf("%d tool uses - %d tokens", ev.Usage.ToolUses, ev.Usage.TotalTokens)
	}
	if err := a.sink.UpdateBackgroundTaskStatus(ev.TaskID, bgtask.StatusRunning, activity); err != nil {
		slog.Warn("claude task_progress update failed", "task_id", ev.TaskID, "error", err)
	}
}

func (a *ClaudeCodeAgent) handleClaudeTaskNotification(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// An unrecognized status must NOT terminalize the row. The map returns the
	// zero value StatusPending on a miss; closing with it writes a Running row
	// to status='pending' with ended_at set (active+ended) and pins the parent's
	// thinking indicator. Ignore statuses the map does not know.
	status, known := claudeTaskStatusMap[ev.Status]
	if !known {
		return
	}
	summary := strings.TrimSpace(ev.Summary)
	if err := a.sink.UpdateBackgroundTaskStatus(ev.TaskID, status, summary); err != nil {
		slog.Warn("claude task_notification status update failed", "task_id", ev.TaskID, "error", err)
	}
	// Carry the shell's output_file in the description for later inspection.
	if ev.OutputFile != "" {
		_ = a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:      ev.TaskID,
			Description: ev.OutputFile,
			Status:      status,
		})
	}
	if err := a.sink.CloseBackgroundTask(ev.TaskID, status); err != nil {
		slog.Warn("claude task_notification close failed", "task_id", ev.TaskID, "error", err)
	}
	// Drop the index entry.
	a.forgetTaskIndex(ev.TaskID)
}

// workflowGroup derives the (group_key, group_label) for a workflow task from
// its workflow_name. Non-workflow tasks return ("", "").
func (a *ClaudeCodeAgent) workflowGroup(ev *claudeTaskEnvelope) (string, string) {
	if ev.WorkflowName == "" {
		return "", ""
	}
	return "workflow:" + ev.WorkflowName, ev.WorkflowName
}

var claudeTaskStatusMap = map[string]bgtask.Status{
	"completed": bgtask.StatusCompleted,
	"failed":    bgtask.StatusFailed,
	"stopped":   bgtask.StatusStopped,
}

// routeSubagentMessage persists a forwarded subagent envelope into the child's
// OWN transcript. Every forwarded envelope -- assistant text/thinking, the
// child's own tool_use, its tool_result, and the child's result -- carries the
// SAME parent_tool_use_id (the parent's Task tool_use). The child resolves via
// the task<->tool_use index recorded at task_started; if the index lacks the
// mapping (a reorder), EnsureChildAgent still creates the child keyed by the
// spawn span. The parent span tracker is never touched for these messages.
func (a *ClaudeCodeAgent) routeSubagentMessage(content []byte, msgType string, env *messageEnvelope) {
	spawnSpanID := env.ParentToolUseID
	taskID := a.taskIDForToolUse(spawnSpanID)

	childID, err := a.sink.EnsureChildAgent(spawnSpanID, taskID, "")
	if err != nil {
		slog.Warn("claude route subagent: ensure child failed", "spawn_span", spawnSpanID, "error", err)
		return
	}

	// Resolve the child span id / type with the same per-block logic the main
	// path uses, but against the CHILD sink's span tracker.
	var spanID, spanType string
	closing := false
	if msgType == claudeMsgTypeAssistant {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_use" && block.ID != "" {
				spanID, spanType = block.ID, block.Name
				break
			}
		}
	} else if msgType == claudeMsgTypeUser {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				spanID = block.ToolUseID
				closing = true
				break
			}
		}
	}

	// Open the child span and look up the tool name for a tool_result.
	childSink := a.sink.ChildSink(childID)
	if spanType == "" && spanID != "" {
		spanType = childSink.GetSpanType(spanID)
	}
	var spanColor int32
	if msgType == claudeMsgTypeAssistant && spanID != "" {
		spanColor = childSink.ReserveSpanColor(spanID, spawnSpanID)
		// Open the child span so a later tool_result (carrying the same
		// parent_tool_use_id) can close it inside the child transcript.
		childSink.OpenSpan(spanID, spawnSpanID)
	}

	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	if msgType == claudeMsgTypeUser {
		source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	}

	// Match the main path's markType derivation for user tool_results so a
	// self-displaying control tool (AskUserQuestion/ExitPlanMode) forwarded into
	// the child transcript gets its CONTROL_RESPONSE scroll-rail mark.
	var markType leapmuxv1.MarkType
	if msgType == claudeMsgTypeUser {
		markType = claudeUserEnvelopeBlocksMarkType(env.ContentBlocks(), childSink.GetSpanType)
	}
	spanInfo := SpanInfo{
		ParentSpanID: spawnSpanID,
		SpanID:       spanID,
		SpanType:     spanType,
		SpanColor:    spanColor,
		Closing:      closing,
		MarkType:     markType,
	}

	if msgType == claudeMsgTypeResult {
		// The subagent's terminal result. Also drives the registry as a
		// fallback when no task_notification arrived.
		if err := childSink.PersistTurnEnd(content, spanInfo); err != nil {
			slog.Warn("claude route subagent turn-end failed", "child", childID, "error", err)
		}
		if taskID != "" {
			status := bgtask.StatusCompleted
			if env.IsError {
				status = bgtask.StatusFailed
			}
			_ = a.sink.UpdateBackgroundTaskStatus(taskID, status, "")
			_ = a.sink.CloseBackgroundTask(taskID, status)
			// Drop the index entry on the fallback path too (a task that ends
			// via the result without a task_notification would otherwise leak).
			a.forgetTaskIndex(taskID)
		} else {
			// task_started has not arrived yet (a reorder): remember the terminal
			// status keyed by the spawn span so the late task_started can close
			// the row it opens. Without this, task_started upserts a Running row
			// that nothing ever closes (the result already passed through).
			status := bgtask.StatusCompleted
			if env.IsError {
				status = bgtask.StatusFailed
			}
			a.recordPendingTaskEnd(spawnSpanID, status)
		}
		return
	}

	if err := childSink.PersistMessage(source, content, spanInfo); err != nil {
		slog.Warn("claude route subagent message failed", "child", childID, "error", err)
	}
	if spanType != "" {
		childSink.SetSpanType(spanID, spanType)
	}
	// A user envelope may close multiple parallel tool_result spans in the
	// child transcript.
	if msgType == claudeMsgTypeUser {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				childSink.CloseSpan(block.ToolUseID)
			}
		}
	}
}

// taskIDForToolUse resolves the registry row_key (Claude task_id) for a
// spawning tool_use id, recorded at task_started. Returns "" when unknown
// (a reorder or a pre-task_started forwarded envelope).
func (a *ClaudeCodeAgent) taskIDForToolUse(toolUseID string) string {
	if toolUseID == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.toolUseTask[toolUseID]
}

// forgetTaskIndex drops the task_id <-> tool_use_id pair from both directions
// of the index. Called when a task reaches a terminal state via either a
// task_notification or the result-message fallback, so a task that ends
// without a notification does not leak its index entries for the agent's life.
func (a *ClaudeCodeAgent) forgetTaskIndex(taskID string) {
	if taskID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if tuid, ok := a.taskToolUse[taskID]; ok {
		delete(a.taskToolUse, taskID)
		delete(a.toolUseTask, tuid)
	}
}

// recordPendingTaskEnd remembers a terminal status for a Task subagent whose
// result message arrived before its task_started (a reorder). The late
// task_started takes the entry and closes the row it opens, so the row cannot
// leak Running. Keyed by the spawn tool_use id (the parent_tool_use_id every
// forwarded envelope shares).
func (a *ClaudeCodeAgent) recordPendingTaskEnd(spawnToolUseID string, status bgtask.Status) {
	if spawnToolUseID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingTaskEnd == nil {
		a.pendingTaskEnd = make(map[string]bgtask.Status)
	}
	a.pendingTaskEnd[spawnToolUseID] = status
}
