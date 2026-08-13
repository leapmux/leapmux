package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The Claude tool names that spawn a subagent. `Agent` is the current name and
// `Task` is the legacy wire name for the SAME tool, which the CLI still emits
// for permission rules, hooks, and resumed sessions
// (src/tools/AgentTool/constants.ts: AGENT_TOOL_NAME / LEGACY_AGENT_TOOL_NAME).
// The to-do tools TaskCreate/TaskUpdate/TaskGet/TaskList/TaskOutput/TaskStop
// are separate names and are NOT spawns.
const (
	ToolNameClaudeAgent = "Agent"
	ToolNameClaudeTask  = "Task"
)

// claudeToolSpawnsSubagent reports whether a Claude tool_use block starts a
// subagent. A spawn owns no span: the subagent's own output lands in its child
// transcript, so a rail held open for the whole run only pushes every
// concurrent tool one column further right.
//
// The Workflow tool is also a spawn but is NOT matched here. It sits behind the
// CLI's WORKFLOW_SCRIPTS feature flag, so its wire name is not a stable thing to
// match; handleClaudeTaskStarted gives its span back off the authoritative
// task_started event instead.
func claudeToolSpawnsSubagent(toolName string) bool {
	return toolName == ToolNameClaudeAgent || toolName == ToolNameClaudeTask
}

// The task_type values a Claude task_started event carries. local_bash is a
// backgrounded shell; local_agent is a Task subagent; local_workflow is a
// Workflow run. Only local_bash is NOT a spawn, so the code tests for that one
// and treats every other value -- including one this list does not name -- as a
// spawn that owns no span and gets a child transcript.
const (
	claudeTaskTypeBash     = "local_bash"
	claudeTaskTypeWorkflow = "local_workflow"
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
//     is final and fires for foreground Tasks too.
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
	if a.taskKind == nil {
		a.taskKind = make(map[string]bgtask.Kind)
	}
	startedKind := bgtask.KindSubagent
	if ev.TaskType == claudeTaskTypeBash {
		startedKind = bgtask.KindShell
	}
	a.taskKind[ev.TaskID] = startedKind
	// A final result that arrived before this (reordered) task_started left
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

	kind := startedKind
	groupKey, groupLabel := a.workflowGroup(ev)

	// Some task_started events omit description but carry the spawn prompt;
	// fall back to the prompt's first line so the row never has an empty title.
	//
	// For a local_bash task the description is what the row shows, and it is
	// left out of Description here: copying the title into it rendered the row's
	// secondary line as a verbatim echo of its own title. task_notification
	// later fills it with the shell's output_file, which the title does not say.
	//
	// TitleIsCommand stays FALSE for it. BashTool sends `description || command`
	// (src/tools/BashTool/BashTool.tsx), and task_started forwards only that one
	// resolved string (src/utils/task/framework.ts) -- so the title is the
	// model's prose whenever it wrote any, the raw command when it did not, and
	// nothing on the wire says which. Prose set in the monospace face reads
	// worse than a command set in the normal one, so the ambiguous case takes
	// the normal one.
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
		Status:        bgtask.StatusRunning,
	}
	if err := a.sink.UpsertBackgroundTask(upsert); err != nil {
		slog.Warn("claude task_started upsert failed", "task_id", ev.TaskID, "error", err)
	}

	// task_started is the authority on which Claude tool calls own a span. A
	// backgrounded shell is a plain Bash span whose tool_result returns at once,
	// so it keeps its rail; every other task type starts a subagent and owns no
	// span. The same startedKind that classifies the registry row above decides
	// it here, so a task type nobody matched by name is still handled.
	//
	// A spawn that claudeToolSpawnsSubagent already matched never opened a span,
	// so this is a no-op for it. A Workflow run is why the call exists: the CLI
	// keeps that tool behind the WORKFLOW_SCRIPTS feature flag, so its wire name
	// is not a stable thing to match, and this event is the first authoritative
	// word that the call is a workflow.
	if startedKind != bgtask.KindShell && ev.ToolUseID != "" {
		// CloseSpan, although the subagent keeps running: the call frees the
		// column and nothing downstream distinguishes "ended" from "given back".
		// The recorded span type survives it, so the tool_result still persists
		// the real tool name.
		a.sink.CloseSpan(ev.ToolUseID)
	}

	// Pre-create the child transcript for a Task subagent (local_agent), keyed
	// by its spawning tool_use id. local_bash (shell) and local_workflow have no
	// transcript; their envelopes are never forwarded.
	if ev.TaskType != claudeTaskTypeBash && ev.TaskType != claudeTaskTypeWorkflow && ev.ToolUseID != "" {
		childID, err := a.sink.EnsureChildAgent(ev.ToolUseID, ev.TaskID, title)
		if err != nil {
			slog.Warn("claude task_started ensure child failed", "task_id", ev.TaskID, "error", err)
		} else if err := a.sink.PersistChildPrompt(childID, ev.Prompt); err != nil {
			// task_started is the only Claude event that carries the spawn
			// prompt, and it lands before any forwarded envelope, so the child
			// transcript opens on the instruction rather than on the reply.
			// Losing it costs the first message, not the transcript.
			slog.Warn("claude task_started persist prompt failed", "task_id", ev.TaskID, "error", err)
		}
	}

	// Apply a pending close from a result that arrived before this
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
	// An unrecognized status must NOT give a final status to the row. The map returns the
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
	// Carry the output_file in the description for later inspection.
	//
	// The kind comes from the INDEX, not from a guess here. task_notification
	// carries no task_type, and it fires for Task subagents as well as shells --
	// so hardcoding KindShell rewrote every subagent's row into a shell one,
	// which cost it its clickable transcript. Omitting the kind entirely is also
	// wrong: KindUnspecified would file a re-created row (one this upsert
	// resurrects after an eviction) in the SUBAGENT cap pool whatever it is.
	if ev.OutputFile != "" {
		_ = a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:      ev.TaskID,
			Kind:        a.kindForTask(ev.TaskID),
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

// hasToolResultBlock reports whether a forwarded user envelope carries the
// child's own tool_result, which is what every genuine one carries.
func hasToolResultBlock(env *messageEnvelope) bool {
	for _, block := range env.ContentBlocks() {
		if block.Type == "tool_result" && block.ToolUseID != "" {
			return true
		}
	}
	return false
}

// routeSubagentMessage persists a forwarded subagent envelope into the child's
// OWN transcript. Every forwarded envelope -- assistant text/thinking, the
// child's own tool_use, its tool_result, and the child's result -- carries the
// SAME parent_tool_use_id (the parent's Task tool_use). The child resolves via
// the task<->tool_use index recorded at task_started; if the index lacks the
// mapping (a reorder), EnsureChildAgent still creates the child keyed by the
// spawn span. The parent span tracker is never touched for these messages.
func (a *ClaudeCodeAgent) routeSubagentMessage(content []byte, msgType string, env *messageEnvelope) {
	// A forwarded USER envelope that carries no tool_result is the spawn prompt
	// coming back, and `PersistChildPrompt` already wrote it at task_started.
	//
	// A FOREGROUND Task emits it: before its run loop, the CLI yields one extra
	// progress event whose whole purpose is to hand the prompt to its own UI, and
	// that event carries the prompt as a user message. A backgrounded Task takes
	// the async path and emits nothing of the kind, which is why only a
	// foreground subagent opened its transcript on two identical prompts.
	//
	// Nothing else can arrive this way. Inside the run loop the CLI forwards a
	// message only when it holds a tool_use or a tool_result block, so every
	// other forwarded user envelope is a tool_result; and a Claude subagent
	// cannot be steered (only Codex implements ChildSteerer), so no typed user
	// message reaches a child transcript either.
	if msgType == claudeMsgTypeUser && !hasToolResultBlock(env) {
		return
	}

	spawnSpanID := env.ParentToolUseID
	taskID := a.taskIDForToolUse(spawnSpanID)

	childID, err := a.sink.EnsureChildAgent(spawnSpanID, taskID, "")
	if err != nil {
		slog.Warn("claude route subagent: ensure child failed", "spawn_span", spawnSpanID, "error", err)
		return
	}

	// Resolve the span metadata and reserve the tool_use's color with the SAME
	// helper the parent transcript uses, but against the CHILD sink's tracker
	// and under the spawn span as the parent. The span itself opens only AFTER
	// the persist below -- see the ordering note there.
	childSink := a.sink.ChildSink(childID)
	spanInfo := claudeSpanInfoFor(childSink, msgType, env, spawnSpanID)
	spanID, spanType := spanInfo.SpanID, spanInfo.SpanType

	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	if msgType == claudeMsgTypeUser {
		source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	}

	if msgType == claudeMsgTypeResult {
		// The subagent's final result. Also drives the registry as a
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
			// task_started has not arrived yet (a reorder): remember the final
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
	// Spans open AFTER the persist, exactly as the parent transcript does (see
	// handlePersistableMessage, which persists before processAssistantBlocks).
	// The sink derives a row's span_lines from the spans open at persist time,
	// so opening first would stamp the tool_use row with an "active" line for
	// the span the row is itself announcing: the row renders one column too deep
	// and draws a rail segment with nothing above it to connect to. Opening
	// after leaves the tool_use row at the parent depth and lets its
	// tool_result -- which persists while the span is open -- close the rail.
	//
	// Every tool_use block opens, not just the first: one assistant message can
	// carry parallel tool calls, and the user envelope below closes all of them.
	// The one exception is a nested spawn -- a subagent spawning a subagent of
	// its own -- which owns no span here for the same reason it owns none in the
	// parent transcript: its output goes to a transcript of its own.
	if msgType == claudeMsgTypeAssistant {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_use" && block.ID != "" {
				childSink.SetSpanType(block.ID, block.Name)
				if !claudeToolSpawnsSubagent(block.Name) {
					childSink.OpenSpan(block.ID, spawnSpanID)
				}
			}
		}
	}
	// A user envelope may close multiple parallel tool_result spans in the
	// child transcript.
	if msgType == claudeMsgTypeUser {
		claudeCloseToolResultSpans(childSink, env)
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
// of the index. Called when a task reaches a final state via either a
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
	delete(a.taskKind, taskID)
}

// kindForTask returns what task_started said this task is, or KindUnspecified
// when the index has no entry -- a task_started this process never saw (a
// resume) or one already forgotten. The registry's blank-means-keep rule then
// preserves whatever kind the row already carries, which is the right answer
// for an existing row and the only honest one for a task nothing described.
func (a *ClaudeCodeAgent) kindForTask(taskID string) bgtask.Kind {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taskKind[taskID]
}

// recordPendingTaskEnd remembers a final status for a Task subagent whose
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
