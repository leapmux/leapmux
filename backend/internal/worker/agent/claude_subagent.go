package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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

// ToolNameClaudeSendMessage is the tool the main agent uses to message another
// agent. When it addresses a subagent of this session that already FINISHED, the
// CLI restarts that subagent, and this is the only signal that says so before
// the restart happens.
//
// It is deliberately absent from claudeToolSpawnsSubagent: the call starts no
// new transcript, so it owns an ordinary span that its own tool_result closes.
const ToolNameClaudeSendMessage = "SendMessage"

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
// shell, which is NOT the same as a BACKGROUNDED shell; local_agent is a Task
// subagent; local_workflow is a Workflow run. Only local_bash is NOT a spawn, so
// the code tests for that one and treats every other value -- including one this
// list does not name -- as a spawn that owns no span and gets a child
// transcript.
//
// A foreground shell reports local_bash too, once the command runs for 2
// seconds. claudeHandleTaskEvent gives the mechanism and says why the registry
// keeps the row anyway.
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
//     fires for Task subagents (local_agent), shells (local_bash, foreground
//     ones included), and Workflow runs (local_workflow).
//   - task_progress {task_id, description, last_tool_name?, usage?, workflow_progress?}
//   - task_notification {task_id, tool_use_id, status, summary, output_file, usage?}
//     is final and fires for foreground Tasks too.
//   - task_updated {task_id, patch:{status,end_time,is_backgrounded?}} repeats
//     the notification's status; consumed as a no-op refresh.
//   - background_tasks_changed is Claude's own live background-task list. It is
//     the one event that separates a backgrounded shell from a foreground one,
//     and it is consumed silently all the same -- see the case below.
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
		// Consumed as a no-op: the task_* bookends above drive the registry, and
		// neither event changes a row.
		//
		// background_tasks_changed is not redundant, and the difference decides
		// which shells the sidebar lists. The CLI registers a FOREGROUND shell as
		// a local_bash task once the command runs for 2 seconds, so the user can
		// background it with ctrl-b, and that registration emits task_started
		// (2.1.220: the Bash progress loop calls the registrar at 2000 ms with
		// isBackgrounded false). A shell row therefore appears for any command
		// slower than 2 seconds, although nothing backgrounded it. These two
		// events carry the only signal that separates the two cases:
		// background_tasks_changed lists the live tasks whose isBackgrounded is
		// true, and task_updated reports the foreground -> background flip in
		// patch.is_backgrounded.
		//
		// The registry lists every local_bash task on purpose. The CLI's own task
		// dialog hides a foreground one, so this is a divergence from the CLI, not
		// parity with it. Filtering on background_tasks_changed would trade the
		// divergence for a worse failure: the CLI's task-event queue holds 1000
		// events and drops a NON-bookend event first when it fills, so one lost
		// level event would hide a real background shell for its whole run.
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
	// Resolve the registry BEFORE anything is written, because "does a row for
	// this task already exist" is what separates a first start from a
	// re-registration, and two decisions below turn on it.
	//
	// The CLI emits task_started again for the same task_id in two cases: it
	// revived a finished subagent, or a resumed session is re-announcing the
	// tasks it once ran. Both re-register, and in both the event's tool_use_id
	// identifies the tool call that runs NOW, not the original spawn.
	known := lookupClaudeKnownTask(a.sink, ev.TaskID)

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
	//
	// The prompt fallback is for a FIRST start only. On a re-registration the
	// prompt is the message the parent just sent, and a non-blank title
	// overwrites the row's own (PreservingBlanksFrom keeps only a BLANK one), so
	// the Background-tasks entry would rename itself to a chat message while the
	// child agents row -- which EnsureChildAgent never rewrites -- kept the real
	// title. A blank title here is the correct value for a row that already has
	// one.
	title := ev.Description
	if title == "" && !known.exists && !known.unreadable {
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
	// shell is a plain Bash span whose own tool_result closes it -- at once for a
	// backgrounded one, at the end of the command for a foreground one -- so it
	// keeps its rail; every other task type starts a subagent and owns no span.
	// The same startedKind that classifies the registry row above decides it
	// here, so a task type nobody matched by name is still handled.
	//
	// A spawn that claudeToolSpawnsSubagent already matched never opened a span,
	// so this is a no-op for it. A Workflow run is why the call exists: the CLI
	// keeps that tool behind the WORKFLOW_SCRIPTS feature flag, so its wire name
	// is not a stable thing to match, and this event is the first authoritative
	// word that the call is a workflow.
	//
	// Not for a re-registration. Its tool_use_id is the tool call that RESTARTED
	// the task -- the parent's SendMessage -- and that call is still running in
	// the parent transcript. Closing its span here would free the rail mid-flight,
	// so its own tool_result would draw a connector_end with no vertical line
	// above it to meet.
	//
	// The recorded span TYPE decides it, not "does the registry hold this task".
	// The registry is durable and the span tracker is not, so a row that outlived
	// a worker restart, or one a reordered task_notification opened first, would
	// suppress the close for a genuine spawn and strand its rail open for the rest
	// of the transcript. processAssistantBlocks records a type for every tool_use,
	// CloseSpan preserves it (only the turn-end Reset clears it), and a revive's
	// task_started lands inside the sending turn -- so this reads SendMessage
	// exactly when the event re-registers a task and the spawn's own name
	// otherwise.
	// An unreadable registry suppresses the close too: it cannot prove this is a
	// spawn, and freeing a rail that is still in use is the worse of the two
	// errors -- a rail given back too late costs one column, one given back too
	// early leaves a tool_result drawing a connector end with nothing above it.
	if !known.unreadable && a.sink.GetSpanType(ev.ToolUseID) != ToolNameClaudeSendMessage &&
		startedKind != bgtask.KindShell && ev.ToolUseID != "" {
		// CloseSpan, although the subagent keeps running: the call frees the
		// column and nothing downstream distinguishes "ended" from "given back".
		// The recorded span type survives it, so the tool_result still persists
		// the real tool name.
		a.sink.CloseSpan(ev.ToolUseID)
	}

	// Pre-create the child transcript for a Task subagent (local_agent).
	// local_bash (shell) and local_workflow have no transcript; their envelopes
	// are never forwarded.
	if ev.TaskType != claudeTaskTypeBash && ev.TaskType != claudeTaskTypeWorkflow {
		// The registry's own linkage FIRST, and EnsureChildAgent only when the row
		// carries none. On a re-registration ev.ToolUseID is the SendMessage call,
		// not the spawn span, so handing it to EnsureChildAgent walks past the
		// row-key fast path (which misses whenever the row lost its linkage),
		// fails GetChildAgentBySpawnSpan, and CREATES a second transcript keyed by
		// that id -- then re-links the row to the orphan. Reading known.childID
		// makes the two resolutions one, so they cannot disagree.
		childID, err := known.childID, error(nil)
		//
		// Never from a SendMessage id. EnsureChildAgent takes a SPAWN SPAN, and on
		// a re-registration whose row was cap-evicted the row-key path misses and
		// the spawn-span lookup misses too -- so it would CREATE a second child
		// keyed by a non-spawn id and re-point the durable row at that orphan,
		// leaving the subagent's real transcript unreachable.
		if childID == "" && ev.ToolUseID != "" && a.sink.GetSpanType(ev.ToolUseID) != ToolNameClaudeSendMessage {
			childID, err = a.sink.EnsureChildAgent(ev.ToolUseID, ev.TaskID, title)
		}
		if childID != "" {
			a.rememberTaskChild(ev.TaskID, childID)
		}
		switch {
		case err != nil:
			slog.Warn("claude task_started ensure child failed", "task_id", ev.TaskID, "error", err)
		case childID == "":
			// No transcript to write into yet. When the parent addressed this task,
			// hold the delivered text: the subagent's first forwarded envelope
			// resolves its real transcript through the spawn span, and the flush
			// there puts the message above the reply it asked for. Dropping it
			// instead would lose the one thing the user cannot reconstruct.
			if a.takeClaudeRevive(ev.TaskID) && !a.claudeWakeRestartedTask(ev) {
				a.parkClaudeChildMessage(ev.TaskID, ev.Prompt)
			}
		default:
			handled, err := a.reviveClaudeSubagent(ev, childID, known)
			switch {
			case err != nil:
				// It WAS a revive; only the registry write failed. The delivered
				// message is already in the transcript and the arm is back, so the
				// first-start path below must not run -- PersistChildPrompt says
				// nothing on a transcript that already has messages anyway.
				slog.Warn("claude revive background task failed", "task_id", ev.TaskID, "error", err)
			case handled:
				// The prompt was the message the parent just sent, appended to the
				// running transcript rather than prepended as the opening instruction.
			default:
				if err := a.sink.PersistChildPrompt(childID, ev.Prompt); err != nil {
					// task_started is the only Claude event that carries the spawn
					// prompt, and it lands before any forwarded envelope, so the child
					// transcript opens on the instruction rather than on the reply.
					// Losing it costs the first message, not the transcript.
					slog.Warn("claude task_started persist prompt failed", "task_id", ev.TaskID, "error", err)
				}
			}
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

// claudeKnownTask is what the registry already knows about a task id, read once
// at the top of handleClaudeTaskStarted.
//
// `exists` is the load-bearing field: a task_started for a task the registry
// ALREADY holds is a re-registration, not a first start, and in a
// re-registration the event's tool_use_id identifies the tool call that runs now
// rather than the original spawn. Grouped into one value because childID and
// status are only ever meaningful together with it, and because two adjacent
// string parameters (this childID and the resolved one) are easy to hand over
// the wrong way round.
type claudeKnownTask struct {
	childID string
	status  bgtask.Status
	exists  bool
	// unreadable is set when the registry could not be READ, which is neither
	// "holds this task" nor "does not". Every decision below that treats a miss
	// as "this is a first start" is wrong when the truth is unknown, so they test
	// this first.
	unreadable bool
}

func lookupClaudeKnownTask(sink OutputSink, taskID string) claudeKnownTask {
	childID, status, exists, err := sink.LookupBackgroundTask(taskID)
	if err != nil {
		slog.Warn("claude task_started: registry lookup failed", "task_id", taskID, "error", err)
		return claudeKnownTask{unreadable: true}
	}
	return claudeKnownTask{childID: childID, status: status, exists: exists}
}

// reviveClaudeSubagent handles a task_started that restarted a FINISHED subagent
// because the parent sent it a message. It reopens the registry row and appends
// the delivered message to the child transcript. Reports whether it took the
// event, so the caller skips the first-start prompt path.
//
// Three conditions must all hold, and each excludes a different impostor:
//
//   - the registry already holds this task -- a first start is not a revive;
//   - the row is in a FINISHED status -- a duplicate task_started for a running
//     task changes nothing;
//   - a SendMessage this turn addressed this task id -- which is what a resumed
//     session's hydration burst never carries, and it re-announces every task it
//     once ran with all of them finished.
//
// A SendMessage is not the CLI's only restart. Captured against 2.1.233: it also
// re-registers a FINISHED subagent when one of that subagent's own backgrounded
// shells completes, emitting a task_started with NO tool_use_id whose prompt is
// a <task-notification> block, and the subagent then runs again. No arm exists
// for it, so its row stays finished while it works. Recognizing it needs a
// discriminator that a resumed session's burst cannot forge, and matching on the
// prompt text is not one -- see the report attached to this change.
//
// The message text comes from the event's prompt rather than from the
// SendMessage input. That is what the subagent actually received, wrapping and
// all, and it arrives on the one event that also proves the restart happened, so
// a send the CLI refused records nothing.
//
// Reports `handled` -- whether this event WAS a revive -- separately from the
// error, because the two answer different questions. The caller skips the
// first-start prompt path on handled, and a failed registry write does not make
// the event any less a revive: falling back there would hand the delivered
// message to PersistChildPrompt, which says nothing once the transcript has
// messages, and the arm is spent by then.
func (a *ClaudeCodeAgent) reviveClaudeSubagent(ev *claudeTaskEnvelope, childID string, known claudeKnownTask) (handled bool, err error) {
	if childID == "" || !known.exists || !known.status.IsFinished() {
		return false, nil
	}
	// Two restarts, two proofs. A SendMessage the parent made this turn, or the
	// CLI waking this subagent because one of its own backgrounded shells
	// finished. Only the first delivers text: a wake's prompt is a
	// <task-notification> block addressed to the model, which is harness
	// plumbing rather than anything the user asked for, so it reopens the row and
	// writes nothing.
	delivered := a.takeClaudeRevive(ev.TaskID)
	if !delivered && !a.claudeWakeRestartedTask(ev) {
		return false, nil
	}
	// The message BEFORE the registry write, because the two are independent and
	// the text is the half the user cannot recover. A row that stays finished is
	// visibly wrong and the next notification corrects it; a message that was
	// never written is gone, and nothing retries it.
	if delivered {
		if err := a.sink.PersistChildUserMessage(childID, ev.Prompt); err != nil {
			slog.Warn("claude revive persist message failed", "child", childID, "error", err)
		}
	}
	if err := a.sink.ReviveBackgroundTask(ev.TaskID); err != nil {
		// Put a consumed DELIVERY arm back. The row is still finished, so a later
		// task_started for this task in the same turn is still a revive and
		// deserves the retry that a consumed arm would deny it. Re-armed at ROOT
		// scope ("") because this runs on the root stream and the root's turn end
		// is the later of the two boundaries -- the retry window is never cut
		// short. A wake consumed nothing, so it has nothing to give back.
		if delivered {
			a.armClaudeRevive(ev.TaskID, "")
		}
		return true, err
	}
	return true, nil
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
	// Remember it BEFORE the writes: a wake for this task's owner can follow
	// immediately, and it proves itself by naming an id this process finalized.
	a.rememberFinishedTask(ev.TaskID)
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
	// Inside the run loop the CLI forwards a message only when it holds a tool_use
	// or a tool_result block, so every other forwarded user envelope is a
	// tool_result; and a Claude subagent cannot be steered (only Codex implements
	// ChildSteerer), so no TYPED user message reaches a child transcript either.
	//
	// A SendMessage the parent addresses to a subagent does not reach a child
	// transcript this way either, and a live recipient is not an exception.
	// Captured against 2.1.233: the CLI CONCLUDES the recipient's current run
	// before it delivers. A subagent that was mid-Bash when the parent addressed
	// it emitted task_notification=completed FIRST, then the parent's SendMessage
	// tool_use, then a task_started carrying the message as its prompt. The
	// delivered text rides on exactly two envelopes -- the parent's own tool_use
	// input, and task_started.prompt -- and never on a forwarded one. So a
	// recipient is always FINISHED by delivery time, the revive path is the whole
	// mechanism, and there is no live-delivery case for this guard to drop.
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
	if taskID == "" {
		taskID = a.taskIDForChild(childID)
	}
	a.rememberTaskChild(taskID, childID)
	// A delivered message that had no transcript when it arrived. This is the
	// first point where the subagent's real one is known, and it runs BEFORE the
	// envelope persists so the message sits above the reply it asked for.
	if text := a.takeClaudeChildMessage(taskID); text != "" {
		if err := a.sink.PersistChildUserMessage(childID, text); err != nil {
			slog.Warn("claude flush held child message failed", "child", childID, "error", err)
		}
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
		if taskID == "" {
			// The tool_use index answers "" for a REVIVED run whose result the CLI
			// forwarded under the ORIGINAL spawn span: the first completion dropped
			// that span, and the revive re-registered the task under the SendMessage
			// call. The child transcript is the same across both runs, so it still
			// identifies the row. Without this the reopened row stays Running for
			// good whenever no task_notification follows.
			taskID = a.taskIDForChild(childID)
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
		// This subagent's turn ended, so drop the arms IT set. Its own result is
		// the boundary for them; the root's is not, because this transcript
		// outlives the root turn that spawned it.
		a.clearClaudeRevives(spawnSpanID)
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
		// A subagent can message another agent, so its SendMessage calls arm a
		// revive exactly as the parent's do. The arms live on the agent, not on a
		// sink, because the task_started that fires them arrives on the root
		// stream whichever transcript sent the message. They carry this
		// transcript's spawn span, so the root's turn end cannot drop them.
		a.claudeArmRevivesFromBlocks(env, spawnSpanID)
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

// The wake block the CLI hands a subagent when one of that subagent's own
// backgrounded shells finishes. It arrives as a task_started prompt.
const (
	claudeWakeOpenTag  = "<task-notification>"
	claudeWakeCloseTag = "</task-notification>"
)

// claudeWakeTaskID reports the task id a <task-notification> wake block names.
//
// Two conditions, and neither is decoration. The block must OPEN the first line
// or CLOSE the last one, so a spawn prompt that merely discusses the tag is not
// mistaken for one. And the caller must confirm the id against the tasks THIS
// process finished: a resumed session's hydration burst replays prompts from a
// previous process, so the shell it names is one this process never saw. The
// pair is the same class of proof the SendMessage arm supplies -- positive,
// per-process evidence that the restart really happened -- rather than a guess
// from the event alone.
func claudeWakeTaskID(prompt string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(prompt), "\n")
	if len(lines) < 2 {
		return "", false
	}
	if strings.TrimSpace(lines[0]) != claudeWakeOpenTag &&
		strings.TrimSpace(lines[len(lines)-1]) != claudeWakeCloseTag {
		return "", false
	}
	for _, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "<task-id>")
		if !ok {
			continue
		}
		if id, ok := strings.CutSuffix(rest, "</task-id>"); ok && id != "" {
			return id, true
		}
	}
	return "", false
}

// rememberFinishedTask records a task this process gave a final status, so a
// later wake block that names it is provably about THIS session.
func (a *ClaudeCodeAgent) rememberFinishedTask(taskID string) {
	if taskID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finishedTasks == nil {
		a.finishedTasks = make(map[string]struct{})
	}
	a.finishedTasks[taskID] = struct{}{}
}

// claudeWakeRestartedTask reports whether this task_started is the CLI waking a
// finished subagent because one of its own backgrounded shells completed.
func (a *ClaudeCodeAgent) claudeWakeRestartedTask(ev *claudeTaskEnvelope) bool {
	shellTaskID, ok := claudeWakeTaskID(ev.Prompt)
	if !ok {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, seen := a.finishedTasks[shellTaskID]
	return seen
}

// parkClaudeChildMessage holds a delivered message until a transcript resolves.
// A no-op for a blank text, so a revive that carried none parks nothing.
func (a *ClaudeCodeAgent) parkClaudeChildMessage(taskID, text string) {
	if taskID == "" || strings.TrimSpace(text) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingChildMessage == nil {
		a.pendingChildMessage = make(map[string]string)
	}
	a.pendingChildMessage[taskID] = text
}

// takeClaudeChildMessage removes and returns a held message, so one delivery is
// written once. Returns "" when nothing is held for this task.
func (a *ClaudeCodeAgent) takeClaudeChildMessage(taskID string) string {
	if taskID == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	text, ok := a.pendingChildMessage[taskID]
	if !ok {
		return ""
	}
	delete(a.pendingChildMessage, taskID)
	return text
}

// rememberTaskChild records the task_id <-> child transcript pair, so a
// forwarded envelope can identify its registry row from the child alone.
func (a *ClaudeCodeAgent) rememberTaskChild(taskID, childID string) {
	if taskID == "" || childID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.taskChild == nil {
		a.taskChild = make(map[string]string)
	}
	if a.childTask == nil {
		a.childTask = make(map[string]string)
	}
	a.taskChild[taskID] = childID
	a.childTask[childID] = taskID
}

// taskIDForChild resolves the registry row_key for a child transcript id.
// Returns "" when no task_started linked one.
func (a *ClaudeCodeAgent) taskIDForChild(childID string) string {
	if childID == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.childTask[childID]
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
	// taskChild/childTask deliberately SURVIVE. They describe the transcript, not
	// the run, and a transcript is permanent: a revived run's forwarded envelopes
	// still have to name this row, and the spawn span the tool_use index carried
	// is gone by then. Bounded by the number of subagents this agent spawned,
	// which is the same bound as the child sinks the sink already holds.
	//
	// A held message does NOT survive: by a completion it either flushed on the
	// run's first forwarded envelope or has no transcript to reach.
	delete(a.pendingChildMessage, taskID)
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

// claudeSendMessageInput is the part of a SendMessage tool_use input this code
// reads. `to` is the recipient, and for a subagent of this session it is the
// Claude task_id -- the CLI keys its task registry by agent id, which is the
// same string LeapMux stores as the registry row_key. Every other recipient form
// (a display name, another session, a uds:/bridge:/did: address) identifies no row
// of ours and resolves to nothing, which is the correct outcome for it.
type claudeSendMessageInput struct {
	To string `json:"to"`
}

// claudeArmRevivesFromBlocks records the task ids an assistant envelope's
// SendMessage calls addressed, so a following task_started for one of them is
// recognized as a revive.
//
// The tool call is the evidence, and the event alone cannot be. A resumed
// session re-emits task_started for EVERY subagent it once ran, to rebuild the
// CLI's own task registry, and those tasks are all in a final status here. So
// "task_started for a finished row" describes the hydration burst exactly as
// well as it describes a revive, and acting on it would resurrect every old
// subagent with no close ever arriving. A SendMessage that gives that task id is
// what separates the two, because the hydration burst carries none.
//
// Arming is deliberately unconditional on whether `to` resolves. The registry
// lookup happens when the arm FIRES, so a recipient that identifies no row simply
// never matches a task_started, and the arm expires at the turn end.
//
// Shared by the parent transcript and the child one: a subagent can message
// another agent, and its tool_use blocks arrive through routeSubagentMessage.
// `armedBy` separates the two -- "" for the root, the spawn span id for a
// subagent -- so each transcript's turn end drops only the arms it set.
func (a *ClaudeCodeAgent) claudeArmRevivesFromBlocks(env *messageEnvelope, armedBy string) {
	for _, block := range env.ContentBlocks() {
		if block.Type != "tool_use" || block.Name != ToolNameClaudeSendMessage {
			continue
		}
		var input claudeSendMessageInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			slog.Warn("claude SendMessage input unmarshal failed", "agent_id", a.agentID, "error", err)
			continue
		}
		a.armClaudeRevive(input.To, armedBy)
	}
}

// armClaudeRevive records one recipient id as revivable until the turn end of
// the transcript that sent the message. armedBy is "" for the root and the
// spawn span id for a subagent.
func (a *ClaudeCodeAgent) armClaudeRevive(to, armedBy string) {
	if to == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingRevive == nil {
		a.pendingRevive = make(map[string]string)
	}
	a.pendingRevive[to] = armedBy
}

// takeClaudeRevive reports whether an in-flight SendMessage addressed taskID,
// and consumes the arm so one send cannot revive the same row twice.
func (a *ClaudeCodeAgent) takeClaudeRevive(taskID string) bool {
	if taskID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.pendingRevive[taskID]; !ok {
		return false
	}
	delete(a.pendingRevive, taskID)
	return true
}

// clearClaudeRevives drops the arms that ONE transcript set, at that
// transcript's turn end. A SendMessage to a LIVE subagent, to a recipient
// outside this session, or one the CLI refused emits no task_started, so
// without this the arms would accumulate for the agent's life.
//
// Scoped, not global: a subagent outlives the root turn that spawned it, so the
// root's result is not its turn boundary. Clearing everything there dropped a
// live subagent's arm before its task_started could fire it, which left the
// recipient's row finished and its transcript looking dead -- the exact bug the
// revive exists to fix.
func (a *ClaudeCodeAgent) clearClaudeRevives(armedBy string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	maps.DeleteFunc(a.pendingRevive, func(_, scope string) bool { return scope == armedBy })
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
