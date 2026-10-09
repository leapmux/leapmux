package claude

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// These Claude tools start a subagent:
//   - Agent, the current tool name.
//   - Task, the previous spelling still emitted for permission rules, hooks, and resumed sessions.
// See src/tools/AgentTool/constants.ts and its AGENT_TOOL_NAME and LEGACY_AGENT_TOOL_NAME constants.
// The separate to-do tools start no subagent:
//   - TaskCreate.
//   - TaskUpdate.
//   - TaskGet.
//   - TaskList.
//   - TaskOutput.
//   - TaskStop.
const (
	ToolNameAgent = "Agent"
	ToolNameTask  = "Task"
)

// ToolNameSendMessage lets the main agent send a message to another agent.
// If the recipient is an ended subagent of this session, the CLI restarts it.
// This call is the only advance signal of that restart.
//
// Do not include it in claudeToolSpawnsSubagent because it creates no new transcript.
// It owns an ordinary span that its own tool_result closes.
const ToolNameSendMessage = "SendMessage"

// ToolNameSubagentHandback identifies the one-time report delivery tool available to an auto-mode child in Claude Code 2.1.277.
// The child stream carries its call and acknowledgement.
// A foreground parent reports the outcome through tool_use_result.handback.
// A background parent supplies a peer-origin result with handback=true.
const ToolNameSubagentHandback = "SubagentHandback"

// claudeToolSpawnsSubagent reports whether a Claude tool_use starts a child.
// A spawn owns no span because its output enters a separate child transcript.
// Keeping its rail open for the whole run would move every concurrent tool one additional column right.
//
// Workflow also starts child work but is excluded from this name-based detector.
// The CLI controls it through WORKFLOW_SCRIPTS, so its wire name is not a stable detection rule.
// handleClaudeTaskStarted instead releases its span after the authoritative task_started event.
func claudeToolSpawnsSubagent(toolName string) bool {
	return toolName == ToolNameAgent || toolName == ToolNameTask
}

// Claude task_started reports these task_type values:
//   - local_bash identifies a shell, including foreground work, and keeps its tool span.
//   - local_agent identifies a Task subagent and releases its tool span.
//   - local_workflow identifies a Workflow run and releases its tool span.
// Every other task type also releases the span.
// A workflow owns no child transcript, while an agent or unknown spawn type does.
//
// A foreground shell also reports local_bash after its command runs for two seconds.
// claudeHandleTaskEvent explains why the registry still retains that row.
const (
	claudeTaskTypeBash     = "local_bash"
	claudeTaskTypeWorkflow = "local_workflow"
)

// claudeTaskEnvelope holds a parsed Claude system event with one of these subtypes:
//   - task_started.
//   - task_progress.
//   - task_notification.
//   - task_updated.
//   - background_tasks_changed.
// Every field is optional.
// For an unknown subtype, claudeHandleTaskEvent returns false and preserves the ordinary persistence path.
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

// claudeHandleTaskEvent parses a Claude system line and applies task or workflow events to the registry.
// For local_agent Task subagents, it also creates the child transcript.
// Return true for a consumed event, so the caller skips ordinary persistence.
// Return false for other system lines, which continue unchanged.
//
// Claude Code 2.1.220 probes and 2.1.277 bundle and launch checks established these shapes:
//   - task_started {task_id, tool_use_id, task_type, description, workflow_name?}
//     It reports Task subagents as local_agent, all shells as local_bash, and Workflow runs as local_workflow.
//   - task_progress {task_id, description, last_tool_name?, usage?, workflow_progress?}
//   - task_notification {task_id, tool_use_id, status, summary, output_file, usage?}
//     It is final and includes foreground Task calls.
//   - task_updated {task_id, patch:{status,end_time,is_backgrounded?}}
//     It repeats the notification status and causes no registry change.
//   - background_tasks_changed supplies the CLI's current background-task list.
//     It distinguishes foreground and background shells, but this handler consumes it without a registry change. See below.
// Workflow agents forward no transcript: the probes found zero parent_tool_use_id rows.
// Their registry-only rows group by workflow_name.
func (a *Agent) claudeHandleTaskEvent(content []byte) bool {
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
		// Consume both events without changing a row because the task start and end events drive the registry.
		//
		// background_tasks_changed distinguishes foreground and background shells rather than merely repeating other events.
		// The CLI registers a foreground local_bash task after two seconds so the user can background it through Ctrl+B.
		// Claude Code 2.1.220's Bash progress loop registers it at 2000 ms with isBackgrounded=false and emits task_started.
		// A command longer than two seconds therefore gets a shell row even when it stays in the foreground.
		// Only these two events identify that distinction:
		//   - background_tasks_changed lists active tasks with isBackgrounded=true.
		//   - task_updated reports the change through patch.is_backgrounded.
		//
		// The registry intentionally lists every local_bash task, although the CLI's own task dialog hides foreground tasks.
		// Filtering through background_tasks_changed would cause a more serious failure.
		// The CLI's task-event queue holds 1000 events and first discards an event that is neither a start nor an end.
		// Losing one background list could therefore hide a real background shell for its complete run.
		return true
	case claudeSystemSubtypeSessionStateChanged:
		// Consume this frame without timeline persistence.
		// observeTurnFromOutput already reads its native turn state, and the turn flag exposes that state to the reader.
		// The CLI emits the frame at every turn transition, so storing it adds no reader information.
		return true
	default:
		return false
	}
}

// claudePreStartRowKey identifies a child row whose forwarded envelope precedes task_started, before a task ID exists.
//
// Use a prefixed spawn span instead of the bare span for this early-row path.
// handleClaudeTaskStarted then attempts the rename unconditionally.
// An ordinary start finds no early row because its envelope follows task_started.
// A restarted run can find the early row under its original spawn span.
// See the rename below for merging that row when the target task ID already exists.
func claudePreStartRowKey(spawnSpanID string) string {
	if spawnSpanID == "" {
		return ""
	}
	return "prestart:" + spawnSpanID
}

// handleClaudeTaskStarted records the task-to-tool-use index and upserts its registry row.
// For a local_agent Task with tool_use_id, it also creates and links the child transcript early so forwarded envelopes resolve immediately.
func (a *Agent) handleClaudeTaskStarted(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// Read the registry before any write to distinguish a first start from a repeated registration.
	// Two later decisions use that result.
	//
	// The CLI repeats task_started for the same task_id when restarting an ended child or hydrating tasks from a resumed session.
	// In both cases, the event's tool_use_id identifies the current call instead of the original spawn.
	known := lookupClaudeKnownTask(a.sink, ev.TaskID)

	// Read the process-local restart evidence once before the four decisions that depend on it.
	// Every decision must use the same classification of this event.
	restart := a.restartEvidenceFor(ev)

	startedKind := bgtask.KindSubagent
	switch ev.TaskType {
	case claudeTaskTypeBash:
		startedKind = bgtask.KindShell
	case claudeTaskTypeWorkflow:
		startedKind = bgtask.KindWorkflow
	}
	// The tool_use index must retain the span that every forwarded envelope of this run carries.
	// For a first start, it equals the event's tool_use_id.
	// For a repeated registration, those IDs can differ.
	//
	// The CLI registers a restart under the parent's SendMessage call or no call for a shell wake.
	// It still runs the child under the toolUseId from the original spawn and forwards envelopes under that original ID.
	// Indexing only the new event ID made those envelopes unresolved.
	// They then opened a second registry row for the transcript already linked by the first. See routeSubagentMessage.
	//
	// A row with an existing child link also requires reading the original span.
	// That link proves an earlier registration of the same task ID.
	// Before the first task_started, routeSubagentMessage indexes an early row by the spawn span, not ev.TaskID.
	// A genuine first start therefore has known.childID="" and skips this branch.
	// This matters after a worker restart, when process-local restart evidence disappears.
	// A shell wake supplies no tool_use_id, so omitting the durable child lookup would leave every envelope unindexed and create another row.
	spawnSpanID := ev.ToolUseID
	if restart.restarted() || known.childID != "" {
		spawnSpanID = a.claudeRestartSpawnSpan(known.childID)
	}
	// Index both the original forwarding span and the event's own tool-use ID.
	// They are equal on a first start.
	// See claudeTaskIndex.runs.taskToolUse for why a restarted run needs both IDs.
	pendingEnd, hasPending := a.tasks.startTask(ev.TaskID, startedKind, spawnSpanID, ev.ToolUseID)
	if restart.restarted() {
		// A pending close belongs to the ended run, while this event starts another run.
		// The spawn span identifies that close and survives each subagent run.
		// An earlier result without its task_started can leave such a close, including after a restart with empty tool-use and childTask indexes.
		// Applying it would close the row that reviveClaudeSubagent reopens and display Succeeded throughout the new run.
		//
		// Discard it here after startTask consumes the map entry.
		// It then cannot affect another later run.
		hasPending = false
	}

	groupKey, groupLabel := a.workflowGroup(ev)

	title := claudeTaskStartedTitle(ev, known, restart)

	// A forwarded envelope before task_started creates an early row under the spawn span. See routeSubagentMessage.
	// Rename it to the actual task ID before upsert so the run retains one row and one activity count.
	// Creating another row would orphan the first and count the child twice.
	// The later fields then update the renamed row.
	if spawnSpanID != "" {
		// Use the original spawn span, not a repeated registration's new tool_use_id.
		// A restarted run can create its early row before this event under that original span.
		// Using the event's new key would miss that row and leave it beside the actual task row.
		//
		// The prefixed early-row key lets this rename run unconditionally.
		// An ordinary registration finds no early-row entry and makes no change.
		if err := a.sink.RenameBackgroundTask(claudePreStartRowKey(spawnSpanID), ev.TaskID); err != nil {
			slog.Warn("claude task_started rename failed",
				"spawn_span", spawnSpanID, "task_id", ev.TaskID, "error", err)
		}
	}

	// For local_bash, description already supplies the title.
	// Do not copy it into Description, which would repeat the same text in the secondary line.
	// task_notification later supplies output_file there for separate inspection.
	//
	// Keep TitleIsCommand=false.
	// BashTool selects description || command in src/tools/BashTool/BashTool.tsx, and src/utils/task/framework.ts forwards only that selected string.
	// A model description takes precedence over the command, and the event supplies no discriminator.
	// Use normal text for that ambiguous title rather than displaying prose in monospace.
	//
	// The registry representation follows task kind:
	//   - A shell has a registry row only.
	//   - local_workflow has a grouped registry row only.
	//   - local_agent has a registry row and the child transcript below.
	upsert := bgtask.Upsert{
		RowKey:        ev.TaskID,
		Kind:          startedKind,
		ParentAgentID: a.AgentID(),
		Title:         title,
		GroupKey:      groupKey,
		GroupLabel:    groupLabel,
		Status:        bgtask.StatusRunning,
	}
	if err := a.sink.UpsertBackgroundTask(upsert); err != nil {
		slog.Warn("claude task_started upsert failed", "task_id", ev.TaskID, "error", err)
	}

	// task_started determines which Claude calls retain a span.
	// A shell keeps its Bash rail until tool_result closes it, immediately for a background command or after a foreground command ends.
	// Every other task type releases that span.
	// Use the same startedKind as the registry classification, including a type that the name-based detector does not recognize.
	//
	// A known spawn never opens a span, so releasing it changes nothing.
	// Workflow requires this step because WORKFLOW_SCRIPTS controls its tool name.
	// task_started supplies the first authoritative workflow classification.
	//
	// Do not release the span for a repeated registration's restarting SendMessage call.
	// That call still runs in the parent transcript and must retain its rail until its own tool_result supplies connector_end.
	// Releasing it early would leave that connector without a preceding vertical line.
	//
	// The recorded SendMessage-call index determines this distinction, not the durable registry row.
	// A row can survive worker restart or a reordered task_notification before a genuine spawn arrives.
	// Treating either row as restart evidence would leave the actual spawn rail open for the remaining transcript.
	// claudeArmRestartsFromBlocks records every SendMessage tool-use ID while parsing its block.
	// The call ID remains recorded for the agent's lifetime, so a late event still identifies the original restart call.
	// Each transcript's turn end removes only its delivery intents.
	//
	// The root's span tracker cannot replace that index.
	// A subagent's SendMessage uses its child tracker through routeSubagentMessage.
	// The root returns "" for that unseen ID, which is indistinguishable from a spawn.
	// A sibling-to-sibling send would therefore fail both span-based checks.
	//
	// A failed registry read also prevents releasing the span because it supplies no proof of a spawn.
	// A late release temporarily costs one column.
	// An early release permanently leaves tool_result's end connector without a preceding rail.
	if !known.unreadable && !restart.restarted() &&
		startedKind != bgtask.KindShell && ev.ToolUseID != "" {
		// Close the span while the child continues, releasing only its display column.
		// Downstream code treats the released span like any ended span.
		// Its recorded type remains available, so tool_result still persists the actual tool name.
		a.sink.CloseSpan(ev.ToolUseID)
	}

	// Pre-create the child transcript for a Task subagent (local_agent).
	// local_bash (shell) and local_workflow have no transcript; their envelopes
	// are never forwarded.
	if ev.TaskType != claudeTaskTypeBash && ev.TaskType != claudeTaskTypeWorkflow {
		a.openClaudeTaskChild(ev, known, restart, title)
	}

	// Apply a pending close from a result that precedes this task_started.
	// The upsert opens a Running row, which must close now because that result already arrived.
	// forgetTaskIndex performs the same removal as the ordinary result path.
	if hasPending {
		providerkit.LogRegistryRefusal("claude", "status", a.sink.UpdateBackgroundTaskStatus(ev.TaskID, pendingEnd, ""))
		providerkit.LogRegistryRefusal("claude", "close", a.sink.CloseBackgroundTask(ev.TaskID, pendingEnd))
		a.tasks.forgetTaskIndex(ev.TaskID)
	}
}

// claudeTaskStartedTitle selects the registry title for task_started.
// When a first-start event omits description but supplies a prompt, use its first line instead of an empty title.
//
// Do not use that fallback for a repeated registration.
// Its prompt contains the newly delivered message, and PreservingBlanksFrom preserves only a blank incoming title.
// A nonempty fallback would rename the registry entry to that message while EnsureChildAgent retains the child's actual title.
// Return a blank title to preserve the existing row title.
//
// Require positive restart evidence before suppressing the fallback.
// A failed registry read alone proves no restart and often occurs on a process's first registry access.
// Suppressing the first-start prompt there would retain a blank registry title and make EnsureChildAgent choose a pooled tab name.
//
// Shell-wake evidence also matters when the registry read fails and known.exists remains false.
// Without that evidence, the fallback would rename the row to the literal <task-notification> wake block.
func claudeTaskStartedTitle(ev *claudeTaskEnvelope, known claudeKnownTask, restart claudeRestartEvidence) string {
	if ev.Description != "" {
		return ev.Description
	}
	if known.exists || restart.restarted() {
		return ""
	}
	return bgtask.FirstLine(ev.Prompt)
}

// openClaudeTaskChild resolves the task_started child transcript and applies the event's prompt.
// Prefer the existing registry child link and call EnsureChildAgent only when that link is absent.
// reviveClaudeSubagent appends a restarted run's delivered message; a first start supplies its opening instruction.
//
// The caller checks task type because local_bash and local_workflow own no transcript.
// The method uses open because startTask already identifies the separate index-write operation.
func (a *Agent) openClaudeTaskChild(ev *claudeTaskEnvelope, known claudeKnownTask, restart claudeRestartEvidence, title string) {
	// Prefer known.childID and call EnsureChildAgent only when the registry supplies no child link.
	// A repeated registration's ev.ToolUseID identifies SendMessage rather than the spawn span.
	// Passing it to EnsureChildAgent can miss both row linkage and GetChildAgentBySpawnSpan.
	// That would create a second transcript under the wrong ID and link the row to that orphan.
	// The existing registry child link keeps both resolution paths on the same transcript.
	childID, err := known.childID, error(nil)
	// Never create a child from a SendMessage ID.
	// EnsureChildAgent expects a spawn span, while a repeated registration's tool_use_id identifies its restarting call.
	// Using that ID can miss the row link and GetChildAgentBySpawnSpan, then create a child that later original-span envelopes duplicate.
	//
	// A linked row skips this check because its stored child link survives display eviction.
	// This path instead sees a row whose initial EnsureChildAgent failed or whose registry cannot be read.
	// Active SendMessage calls supply the only positive restart evidence in those states.
	// Refusing an unproven ID leaves an unavailable transcript unchanged; creating under the wrong ID can lose the existing transcript.
	//
	// Use restart evidence instead of a span-type lookup.
	// A nested child's SendMessage belongs to its own tracker, which the root tracker cannot identify. See the preceding guard.
	if childID == "" && ev.ToolUseID != "" && !restart.restarted() {
		childID, err = a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: ev.ToolUseID, ProviderChildKey: ev.TaskID, Title: title})
	}
	if childID != "" {
		a.tasks.rememberTaskChild(ev.TaskID, childID)
	}
	switch {
	case err != nil:
		slog.Warn("claude task_started ensure child failed", "task_id", ev.TaskID, "error", err)
	case childID == "":
		// This event can open no transcript.
		// Preserve its restart intent because the row remains final and a later task_started in the same turn can retry it.
		// A failed registry revival preserves that intent for the same reason.
		slog.Warn("claude task_started resolved no child transcript",
			"task_id", ev.TaskID, "tool_use_id", ev.ToolUseID,
			"row_exists", known.exists, "registry_unreadable", known.unreadable)
	default:
		handled, err := a.reviveClaudeSubagent(ev, childID, known, restart)
		switch {
		case err != nil:
			// The event still identifies a restart even when the registry write fails.
			// The delivered message already exists, and the restart intent remains available.
			// Skip the first-start path, which cannot add an opening prompt to a transcript that already contains messages.
			slog.Warn("claude restart background task failed", "task_id", ev.TaskID, "error", err)
		case handled:
			// The prompt was the message the parent just sent, appended to the
			// running transcript rather than prepended as the opening instruction.
		default:
			if err := a.sink.PersistChildPrompt(childID, ev.Prompt); err != nil {
				// task_started supplies this path's spawn prompt.
				// PersistChildPrompt records it as the child's opening instruction when that instruction is still absent.
				// A failure can lose the opening message but must not discard the transcript.
				slog.Warn("claude task_started persist prompt failed", "task_id", ev.TaskID, "error", err)
			}
		}
	}
}

// claudeKnownTask holds the registry result for one task ID, read once at the start of handleClaudeTaskStarted.
//
// exists distinguishes a repeated registration from a first start.
// For a repeated registration, tool_use_id identifies the current call rather than the original spawn.
// childID and status are meaningful only with that existence result, so retain them in one value.
// This also avoids passing adjacent child-ID strings in the wrong order.
type claudeKnownTask struct {
	childID string
	status  bgtask.Status
	exists  bool
	// unreadable records a failed registry read.
	// That result proves neither presence nor absence.
	// Check it before treating a missing row as evidence of a first start.
	unreadable bool
}

// claudeRestartSpawnSpan reads a repeated registration's original spawn span from its linked child transcript.
// That stored span survives worker restart when the in-memory index cannot supply it.
//
// Return "" when the row has no child transcript or the read fails.
// The caller still indexes the event's tool-use ID, preserving resolution from the parent's side.
// The original span then remains absent from that index, although every forwarded child envelope uses it.
// routeSubagentMessage resolves through the durable child link to prevent either case from creating a second registry row.
func (a *Agent) claudeRestartSpawnSpan(childID string) string {
	if childID == "" {
		return ""
	}
	span, err := a.sink.ChildSpawnSpan(childID)
	if err != nil {
		slog.Warn("claude task_started: cannot read the spawn span of the child",
			"child", childID, "error", err)
		return ""
	}
	return span
}

func lookupClaudeKnownTask(sink agent.BackgroundTaskServices, taskID string) claudeKnownTask {
	childID, status, exists, err := sink.LookupBackgroundTask(taskID)
	if err != nil {
		slog.Warn("claude task_started: registry lookup failed", "task_id", taskID, "error", err)
		return claudeKnownTask{unreadable: true}
	}
	return claudeKnownTask{childID: childID, status: status, exists: exists}
}

// reviveClaudeSubagent handles task_started for an ended child that restarts through SendMessage or a shell wake.
// Reopen its registry row and append delivered message text only for SendMessage.
// Return handled separately from the error so the caller can skip first-start prompt persistence even if revival fails.
//
// Require all these conditions:
//   - The child transcript exists.
//   - The registry already contains this task.
//   - The row has a final status.
//   - This process records a current SendMessage delivery or a verified shell wake.
// A first start or duplicate registration of a running task therefore does not revive a row.
// Stored restart evidence also distinguishes a live restart from a resumed session's hydration of previously ended tasks.
//
// Claude Code 2.1.233 also restarts an ended child after its own background shell completes.
// That task_started omits tool_use_id and carries a <task-notification> prompt.
// The completed-shell record verifies this wake; prompt text alone supplies insufficient proof.
// A wake reopens the row without copying harness notification text into the transcript.
//
// For SendMessage, use the event prompt rather than the call input because it contains the actual delivered text and wrappers.
// A send refused by the CLI supplies no confirmed restart event and therefore records no delivered message here.
// If the registry write fails, handled remains true and the delivery intent returns for a retry.
// Treating that failure as a first start would pass delivered text to PersistChildPrompt, which ignores a transcript that already contains messages.
func (a *Agent) reviveClaudeSubagent(ev *claudeTaskEnvelope, childID string, known claudeKnownTask, restart claudeRestartEvidence) (handled bool, err error) {
	if childID == "" || !known.exists || !known.status.IsFinished() {
		return false, nil
	}
	// Accept either a SendMessage delivery during this turn or a verified wake from the child's completed background shell.
	// Only SendMessage appends text.
	// A wake's <task-notification> prompt belongs to the model harness, so it only reopens the row.
	//
	// The caller supplies evidence already read before deriving the title.
	// Both decisions therefore use the same event classification.
	// This branch checks the delivery form separately because only that form supplies transcript text.
	delivered := a.tasks.takeClaudeRestart(ev.TaskID)
	if !delivered && !restart.wake {
		return false, nil
	}
	// Persist the delivered message before reviving the registry row because the writes are independent.
	// A still-final row remains visibly incorrect and a later notification can correct it.
	// A failed text write only logs its error.
	// If row revival succeeds, no delivery intent remains to retry that missing message.
	if delivered {
		if err := a.sink.PersistChildUserMessage(childID, ev.Prompt); err != nil {
			slog.Warn("claude restart persist message failed", "child", childID, "error", err)
		}
	}
	if err := a.sink.ReviveBackgroundTask(ev.TaskID); err != nil {
		// Restore a consumed delivery intent when the registry revival fails.
		// The final row can then retry on another task_started in the same turn.
		// Use root scope ("") because this handler runs on the root stream.
		// The root turn ends later than the child turn, so that scope preserves the complete retry interval.
		// A shell wake consumes no delivery intent and restores none.
		if delivered {
			a.tasks.armClaudeRestart(ev.TaskID, "")
		}
		return true, err
	}
	return true, nil
}

func (a *Agent) handleClaudeTaskProgress(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// Native probes establish this activity selection order because task_progress supplies no summary:
	//   - description.
	//   - last_tool_name.
	//   - A usage-derived string.
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

func (a *Agent) handleClaudeTaskNotification(ev *claudeTaskEnvelope) {
	if ev.TaskID == "" {
		return
	}
	// Ignore every status absent from the map instead of choosing a final outcome.
	// A missing entry yields StatusUnspecified, which is not a valid stored task status.
	// The database CHECK rejects zero, so attempting that write would fail the close rather than report a known outcome.
	status, known := claudeTaskStatusMap[ev.Status]
	if !known {
		return
	}
	// A stopped notification after this process calls InterruptChild represents a reader-requested interruption.
	// Use StatusInterrupted and "Subagent interrupted" instead of the ordinary StatusStopped and "Subagent stopped".
	// takeInterrupted consumes the mark, so the first notification or result close determines that wording.
	//
	// Every closing notification consumes the mark regardless of its status.
	// The CLI can finish the task before reading stop_task and still acknowledge the request with success.
	// Its notification then reports completed or failed because the stop did not affect the task.
	// Keeping the mark would incorrectly relabel a later restarted run's independent stop.
	stopRequested := a.tasks.takeInterrupted(ev.TaskID)
	if status == bgtask.StatusStopped && stopRequested {
		status = bgtask.StatusInterrupted
	}
	// Record the completed shell before these writes because its owner's wake can arrive immediately afterwards.
	// That wake requires proof of a shell this process actually finishes.
	// rememberFinishedShellTask filters to shells, and forgetTaskIndex later removes the kind needed for that check.
	a.tasks.rememberFinishedShellTask(ev.TaskID)
	summary := strings.TrimSpace(ev.Summary)
	if err := a.sink.UpdateBackgroundTaskStatus(ev.TaskID, status, summary); err != nil {
		slog.Warn("claude task_notification status update failed", "task_id", ev.TaskID, "error", err)
	}
	// Keep output_file in Description for later inspection.
	//
	// Read the kind from the task index because task_notification contains no task_type and reports both child tasks and shells.
	// Hardcoded KindShell would convert a child row into a shell and remove its clickable transcript.
	// Omitting the kind is also incorrect for a row recreated after eviction.
	// A newly inserted row must carry its actual kind; the database rejects KindUnspecified.
	if ev.OutputFile != "" {
		providerkit.LogRegistryRefusal("claude", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:      ev.TaskID,
			Kind:        a.tasks.kindForTask(ev.TaskID),
			Description: ev.OutputFile,
			Status:      status,
		}))
	}
	if err := a.sink.CloseBackgroundTask(ev.TaskID, status); err != nil {
		slog.Warn("claude task_notification close failed", "task_id", ev.TaskID, "error", err)
	}
	// Drop the index entry.
	a.tasks.forgetTaskIndex(ev.TaskID)
}

// workflowGroup derives the (group_key, group_label) for a workflow task from
// its workflow_name. Non-workflow tasks return ("", "").
func (a *Agent) workflowGroup(ev *claudeTaskEnvelope) (string, string) {
	if ev.WorkflowName == "" {
		return "", ""
	}
	return "workflow:" + ev.WorkflowName, ev.WorkflowName
}

var claudeTaskStatusMap = map[string]bgtask.Status{
	"completed": bgtask.StatusSucceeded,
	"failed":    bgtask.StatusFailed,
	"stopped":   bgtask.StatusStopped,
}

// InterruptChild stops a child's current turn inside its owner process.
// childKey is the registry row key and equals the CLI task_id set by handleClaudeTaskStarted.
// stop_task addresses that task registry entry:
//
//   {"type":"control_request","request_id":"...",
//    "request":{"subtype":"stop_task","task_id":"<task_id>"}}
//
// Claude offers no native child-input route, so this provider implements ChildInterrupter without ChildSteerer.
// The Manager reports ErrChildOperationUnsupported only when a provider lacks this method.
//
// The task index supplies the process-local route.
// An unknown key returns ErrChildRouteNotReady, which the worker handler can retry.
// A worker restart announces tasks again, and a late task_started renames an early row to the actual task ID.
// A native stop_task failure reaches the caller unchanged.
func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	if !a.tasks.knowsTask(childKey) {
		return fmt.Errorf("%w: unknown claude subagent task %q", agent.ErrChildRouteNotReady, childKey)
	}
	body, err := json.Marshal(map[string]string{
		"subtype": "stop_task",
		"task_id": childKey,
	})
	if err != nil {
		return err
	}
	// The CLI can close the task before it answers stop_task. Record the user
	// interrupt before sending, so that closing notification sees it.
	a.tasks.markInterrupted(childKey)
	// Use the agent context so process exit releases the wait.
	// APITimeout limits that wait, as it does for a root interrupt.
	_, err = a.sendControlAndWait(a.Context(), string(body), a.APITimeout())
	if err != nil {
		a.tasks.clearInterrupted(childKey)
		return err
	}
	return nil
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

// routeSubagentMessage persists a forwarded envelope only in the child's transcript.
// Each forwarded envelope carries the parent's Task tool-use ID in parent_tool_use_id:
//   - Assistant text or thinking.
//   - A child tool_use.
//   - A child tool_result.
//   - The child's result.
// Resolve the child through the task-to-tool-use index recorded at task_started.
// If that index misses an early envelope, EnsureChildAgent can still use the spawn span.
// Never change the parent span tracker for these messages.
func (a *Agent) routeSubagentMessage(content []byte, msgType string, env *messageEnvelope) {
	// A forwarded user envelope without tool_result repeats the spawn prompt already recorded through PersistChildPrompt at task_started.
	//
	// The synchronous foreground Task path emits that user prompt as one extra progress event before the run loop for its own UI.
	// The asynchronous background path emits no corresponding event, so only foreground children displayed the duplicate prompt.
	// Within the run loop, the CLI forwards only messages with tool_use or tool_result blocks.
	// Every other genuine forwarded user envelope therefore carries tool_result.
	// Claude implements no ChildSteerer, so no typed user input arrives through this child route either.
	//
	// SendMessage also supplies no forwarded user-text case, including for a live recipient.
	// In Claude Code 2.1.233, delivery ended the recipient's current run first.
	// A child running Bash emitted task_notification=completed before the parent's SendMessage tool_use and task_started with the delivered prompt.
	// Claude Code 2.1.281 instead queues through queuePendingMessage for a running child or resumeAgentBackground for an idle child.
	// Both versions carry the delivered text only in the parent's tool_use input and task_started.prompt.
	// The forwarding filter therefore discards no live message delivery.
	if msgType == claudeMsgTypeUser && !hasToolResultBlock(env) {
		return
	}

	spawnSpanID := env.ParentToolUseID
	taskID := a.tasks.taskIDForToolUse(spawnSpanID)

	// A forwarded child envelope can precede task_started, before a task ID exists.
	// recordPendingTaskEnd handles the corresponding result-order case.
	// EnsureChildAgent can create the child then, but a blank task ID links no registry row.
	// Without that row, the child would appear idle with no thinking indicator while its transcript receives output.
	//
	// Use the known spawn span for the early row.
	// handleClaudeTaskStarted later renames it to the actual task ID, preserving one row for the entire run.
	// A blank span still links nothing and causes no registry write until the run becomes identifiable.
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnSpanID, ProviderChildKey: taskID, Title: ""})
	if err != nil {
		slog.Warn("claude route subagent: ensure child failed", "spawn_span", spawnSpanID, "error", err)
		return
	}
	if taskID == "" {
		// Use the child transcript to resolve a run when the tool-use index cannot.
		// That transcript remains stable across all child runs and both restart tool-use IDs.
		// It supplies the run when handleClaudeTaskStarted cannot index the spawn span because row linkage is absent or unreadable.
		// Without this lookup, an envelope creates another row for the same transcript.
		// The result path resolves and closes only the actual row, leaving the additional row permanently Running.
		taskID = a.tasks.taskIDForChild(childID)
	}
	registryKey := taskID
	if registryKey == "" {
		registryKey = claudePreStartRowKey(spawnSpanID)
	}
	if taskID == "" {
		// Open the row as Running because the forwarded envelope proves active child work.
		// Without a row, activity derivation has no live child input.
		if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: registryKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
			ParentAgentID: a.AgentID(), Status: bgtask.StatusRunning,
		}); err != nil {
			slog.Warn("claude route subagent: open pre-start row failed", "spawn_span", spawnSpanID, "error", err)
		}
	}
	// Remember the resolved task-child link.
	// Repeating an existing child-derived link changes nothing, while an index-derived link still needs this write.
	// rememberTaskChild ignores a blank taskID.
	a.tasks.rememberTaskChild(taskID, childID)

	// Resolve span metadata and reserve the tool color through the same helper used by the parent transcript.
	// Use the child's tracker with the spawn span as parent.
	// Open the tool's own span only after persistence below. See the ordering comment there.
	childSink := a.sink.ChildSink(childID)
	if msgType == claudeMsgTypeAssistant {
		if toolUseID, report, ok := claudeSubagentHandback(env); ok {
			if a.tasks.rememberHandbackToolUse(spawnSpanID, taskID, toolUseID, env.TaskDescription, report) {
				providerkit.PersistSubagentReport(childSink, agent.SubagentReportWrite{
					ReportID: toolUseID,
					Report:   agent.SubagentReport{Label: env.TaskDescription, Text: report},
				})
			}
			return
		}
		if a.tasks.isHandbackEcho(spawnSpanID, env) {
			return
		}
	}
	if msgType == claudeMsgTypeUser && a.tasks.consumeHandbackToolResult(env) {
		return
	}
	spanInfo := claudeSpanInfoFor(childSink, msgType, env, spawnSpanID)
	spanID, spanType := spanInfo.SpanID, spanInfo.SpanType

	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	if msgType == claudeMsgTypeUser {
		source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	}

	if msgType == claudeMsgTypeResult {
		// A child result also closes its registry row when no task_notification arrives.
		//
		// After InterruptChild sends stop_task, mark the turn interrupted only when the result confirms that the stop affected it.
		// The divider then displays "Turn interrupted", and the row reports interrupted.
		// A child that ends before the CLI reads stop_task keeps its own outcome, as a root turn does.
		// takeInterrupted consumes the mark regardless of the frame, so this path and task_notification cannot both claim the wording.
		stopRequested := a.tasks.takeInterrupted(taskID)
		interrupted := stopRequested && env.statesAbortedTurn()
		turnEnd := agent.MessageContent{Original: content}
		if interrupted {
			turnEnd.Completion = agent.MessageCompletionInterrupted
		}
		if err := childSink.PersistTurnEnd(turnEnd, spanInfo); err != nil {
			slog.Warn("claude route subagent turn-end failed", "child", childID, "error", err)
		}
		// Read IsError once for both registry-update branches so they select the same outcome.
		//
		// The preceding lookup resolves taskID through the child for every envelope.
		// That lookup supports restarted runs whose task_started could not index the spawn span.
		// An empty taskID here means that no run is identifiable, not merely that the tool-use index misses.
		status := bgtask.StatusSucceeded
		if env.IsError {
			status = bgtask.StatusFailed
		}
		if interrupted {
			status = bgtask.StatusInterrupted
		}
		if taskID != "" {
			providerkit.LogRegistryRefusal("claude", "status", a.sink.UpdateBackgroundTaskStatus(taskID, status, ""))
			providerkit.LogRegistryRefusal("claude", "close", a.sink.CloseBackgroundTask(taskID, status))
			// Remove the task index on this fallback close also.
			// A task ending through result without task_notification must retain no index entry.
			a.tasks.forgetTaskIndex(taskID)
		} else {
			// The result precedes task_started, so retain its final status under the spawn span.
			// The later task_started then closes the row it creates.
			// Otherwise it would leave a Running row after the final result already passed.
			a.tasks.recordPendingTaskEnd(spawnSpanID, status)
		}
		// At this child's turn end, remove the restart intents that this child records.
		// Its result controls that lifetime because the child transcript can outlive the root turn that starts it.
		// The root's earlier turn end must preserve those intents.
		a.tasks.clearClaudeRestarts(spawnSpanID)
		return
	}

	if err := childSink.PersistMessage(source, agent.MessageContent{Original: content}, spanInfo); err != nil {
		slog.Warn("claude route subagent message failed", "child", childID, "error", err)
	}
	if spanType != "" {
		childSink.SetSpanType(spanID, spanType)
	}
	// Open spans after persistence, as handlePersistableMessage does before processAssistantBlocks in the parent transcript.
	// The sink derives span_lines from the spans already open at write time.
	// Opening first would add the announcing tool_use row's own active line and an extra depth column with no preceding rail.
	// Opening afterwards preserves parent depth and lets tool_result close the rail while the span remains open.
	//
	// Open every tool_use block because one assistant message can contain parallel calls and the following user envelope closes every result.
	// A nested spawn is the exception.
	// It owns no span in this child or the parent because its output belongs to a separate transcript.
	if msgType == claudeMsgTypeAssistant {
		// A child's SendMessage can prepare a restart, as the parent's SendMessage does.
		// The agent owns those intents because task_started arrives on the root stream regardless of the sending transcript.
		// Each intent retains the sending transcript's spawn span, so the root's turn end cannot remove the child's intent.
		a.claudeArmRestartsFromBlocks(env, spawnSpanID)
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

func claudeSubagentHandback(env *messageEnvelope) (toolUseID, report string, ok bool) {
	blocks := env.ContentBlocks()
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].Name != ToolNameSubagentHandback || blocks[0].ID == "" {
		return "", "", false
	}
	var input struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(blocks[0].Input, &input) != nil || strings.TrimSpace(input.Message) == "" {
		return "", "", false
	}
	return blocks[0].ID, input.Message, true
}

type claudePendingHandback struct {
	taskID            string
	handbackToolUseID string
	label             string
	report            string
}

func (a *Agent) persistClaudeHandbackResult(env *messageEnvelope) {
	pending, outcome, ok := a.tasks.takeHandbackForAgentResult(env)
	if !ok {
		return
	}
	report := agent.SubagentReport{Label: pending.label, Text: pending.report, Status: outcome}
	if outcome == "send" || outcome == "flagged" {
		providerkit.PersistSubagentReport(a.sink, agent.SubagentReportWrite{ReportID: pending.handbackToolUseID, Report: report})
	}
}

func (a *Agent) persistClaudePeerHandbackResult(env *messageEnvelope) bool {
	if env.Origin.Kind != "peer" || !env.Origin.Handback {
		return false
	}
	pending, found := a.tasks.takeHandbackForPeerResult(env.Origin.SenderTaskID)
	text := claudePeerHandbackText(env.Origin.Body)
	label := env.Origin.From
	if found {
		label = pending.label
		if text == "" {
			text = pending.report
		}
	}
	status := "send"
	if env.Origin.Flagged {
		status = "flagged"
	}
	senderTaskID := strings.TrimSpace(env.Origin.SenderTaskID)
	reportID := providerkit.SubagentReportContentID("claude-peer", senderTaskID, text)
	if eventID := strings.TrimSpace(env.UUID); eventID != "" {
		reportID = "claude-peer-event:" + eventID
	}
	if found {
		reportID = pending.handbackToolUseID
	}
	providerkit.PersistSubagentReport(a.sink, agent.SubagentReportWrite{
		ReportID: reportID,
		Report:   agent.SubagentReport{Label: label, Text: text, Status: status},
	})
	return true
}

func claudePeerHandbackText(body string) string {
	const reportMarker = "The report follows:\n"
	if index := strings.Index(body, reportMarker); index >= 0 {
		body = body[index+len(reportMarker):]
	}
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimPrefix(line, "  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// claudeTaskRunIndex owns these run-specific values:
//   - Task kind.
//   - Routing IDs.
//   - A final result that arrives before its start event.
// A restart can supply both the original spawn ID and a SendMessage ID.
// Both IDs must resolve the run, and completion must remove both.
type claudeTaskRunIndex struct {
	taskToolUse map[string]map[string]struct{}
	toolUseTask map[string]string
	taskKind    map[string]bgtask.Kind
	pendingEnd  map[string]bgtask.Status
	// interrupted records the tasks this process stopped through InterruptChild.
	// A stopped task notification after InterruptChild records an interrupted registry status.
	// The next matching close consumes the marker.
	interrupted map[string]struct{}
}

// claudeTaskTranscriptIndex outlives a run. It lets a restarted task recover
// its durable child transcript after the run-specific tool ids disappear.
type claudeTaskTranscriptIndex struct {
	childTask map[string]string
}

// claudeTaskRestartIndex retains process-lifetime restart evidence:
//   - finishedShells rejects hydration replays.
//   - pendingByTask lets each transcript remove only its own restart intent.
//   - sendMessageCalls preserves classification for a late task_started.
type claudeTaskRestartIndex struct {
	finishedShells   map[string]struct{}
	pendingByTask    map[string]map[string]struct{}
	sendMessageCalls map[string]struct{}
}

// claudeTaskHandbackIndex owns a report from the child's handback call until
// the parent result states its delivery outcome.
type claudeTaskHandbackIndex struct {
	toolUses map[string]struct{}
	pending  map[string]claudePendingHandback
	tasks    map[string]string
}

// claudeTaskIndex keeps one mutex because a task transition updates more than
// one lifecycle at once. The nested values make each lifetime explicit without
// introducing lock ordering between them. The zero value is usable.
type claudeTaskIndex struct {
	mu          sync.Mutex
	runs        claudeTaskRunIndex
	transcripts claudeTaskTranscriptIndex
	restarts    claudeTaskRestartIndex
	handbacks   claudeTaskHandbackIndex
}

func (i *claudeTaskIndex) rememberHandbackToolUse(spawnSpanID, taskID, toolUseID, label, report string) bool {
	if spawnSpanID == "" || toolUseID == "" || strings.TrimSpace(report) == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.handbacks.toolUses == nil {
		i.handbacks.toolUses = make(map[string]struct{})
	}
	i.handbacks.toolUses[toolUseID] = struct{}{}
	if i.handbacks.pending == nil {
		i.handbacks.pending = make(map[string]claudePendingHandback)
	}
	if _, duplicate := i.handbacks.pending[spawnSpanID]; !duplicate {
		i.handbacks.pending[spawnSpanID] = claudePendingHandback{
			taskID: taskID, handbackToolUseID: toolUseID, label: label, report: report,
		}
		if taskID != "" {
			if i.handbacks.tasks == nil {
				i.handbacks.tasks = make(map[string]string)
			}
			i.handbacks.tasks[taskID] = spawnSpanID
		}
		return true
	}
	return false
}

func (i *claudeTaskIndex) isHandbackEcho(spawnSpanID string, env *messageEnvelope) bool {
	blocks := env.ContentBlocks()
	if len(blocks) != 1 || blocks[0].Type != "text" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	pending, ok := i.handbacks.pending[spawnSpanID]
	return ok && strings.TrimSpace(blocks[0].Text) == strings.TrimSpace(pending.report)
}

func (i *claudeTaskIndex) consumeHandbackToolResult(env *messageEnvelope) bool {
	blocks := env.ContentBlocks()
	if len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.handbacks.toolUses[blocks[0].ToolUseID]; !ok {
		return false
	}
	delete(i.handbacks.toolUses, blocks[0].ToolUseID)
	return true
}

func (i *claudeTaskIndex) takeHandbackForAgentResult(env *messageEnvelope) (claudePendingHandback, string, bool) {
	var result struct {
		Handback string `json:"handback"`
	}
	if json.Unmarshal(env.ToolUseResult, &result) != nil || result.Handback == "" {
		return claudePendingHandback{}, "", false
	}
	blocks := env.ContentBlocks()
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, block := range blocks {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		pending, ok := i.handbacks.pending[block.ToolUseID]
		if !ok {
			continue
		}
		i.deletePendingHandbackLocked(block.ToolUseID, pending)
		return pending, result.Handback, true
	}
	return claudePendingHandback{}, "", false
}

func (i *claudeTaskIndex) takeHandbackForPeerResult(taskID string) (claudePendingHandback, bool) {
	if taskID == "" {
		return claudePendingHandback{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	spawnSpanID := i.handbacks.tasks[taskID]
	pending, ok := i.handbacks.pending[spawnSpanID]
	if !ok {
		return claudePendingHandback{}, false
	}
	i.deletePendingHandbackLocked(spawnSpanID, pending)
	return pending, true
}

func (i *claudeTaskIndex) deletePendingHandbackLocked(spawnSpanID string, pending claudePendingHandback) {
	delete(i.handbacks.pending, spawnSpanID)
	delete(i.handbacks.toolUses, pending.handbackToolUseID)
	if pending.taskID != "" && i.handbacks.tasks[pending.taskID] == spawnSpanID {
		delete(i.handbacks.tasks, pending.taskID)
	}
}

// startTask records the task kind and every tool-use ID that identifies its run, then consumes any preceding final result.
//
// Hold one critical section while updating the four related maps for that event.
// They contain task-to-tool-use links, the task kind, and the reordered close.
// Turn end preserves those links because a background task can outlive its spawning turn.
// forgetTaskIndex removes links and kind at the closing notification.
// Only a start consumes a pending close.
//
// spawnSpanID identifies the forwarding span, while eventToolUseID comes from task_started itself.
// They are equal at first start and can differ after restart.
// Skip each blank ID, so a first start indexes one ID and an event without any tool_use_id indexes none.
//
// A pending close uses the tool-use ID from its forwarded envelope.
// A final result can precede task_started, and consuming it here lets the caller immediately close the row that the start creates.
// Try every supplied ID because the early envelope can use either.
// The spawn-span close takes precedence, but still consume the other close so it cannot affect a future run of the same task.
func (i *claudeTaskIndex) startTask(taskID string, kind bgtask.Kind, spawnSpanID, eventToolUseID string) (bgtask.Status, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runs.taskKind == nil {
		i.runs.taskKind = make(map[string]bgtask.Kind)
	}
	i.runs.taskKind[taskID] = kind
	pending := bgtask.StatusPending
	found := false
	// The spawn span first, so it decides the status when both ids hold a close.
	for _, toolUseID := range [2]string{spawnSpanID, eventToolUseID} {
		if toolUseID == "" {
			continue
		}
		i.indexToolUseLocked(taskID, toolUseID)
		if status, ok := i.runs.pendingEnd[toolUseID]; ok {
			delete(i.runs.pendingEnd, toolUseID)
			if !found {
				pending, found = status, true
			}
		}
	}
	return pending, found
}

// indexToolUseLocked records both directions of one tool-use ID's taskID link.
// The caller must hold i.mu.
func (i *claudeTaskIndex) indexToolUseLocked(taskID, toolUseID string) {
	if i.runs.taskToolUse == nil {
		i.runs.taskToolUse = make(map[string]map[string]struct{})
	}
	if i.runs.toolUseTask == nil {
		i.runs.toolUseTask = make(map[string]string)
	}
	if i.runs.taskToolUse[taskID] == nil {
		i.runs.taskToolUse[taskID] = make(map[string]struct{})
	}
	i.runs.taskToolUse[taskID][toolUseID] = struct{}{}
	i.runs.toolUseTask[toolUseID] = taskID
}

// taskIDForToolUse returns the registry row_key, which equals Claude task_id, recorded for a run's tool-use ID at task_started.
// Return "" when unknown, including an early forwarded envelope or another reordered event.
func (i *claudeTaskIndex) taskIDForToolUse(toolUseID string) string {
	if toolUseID == "" {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runs.toolUseTask[toolUseID]
}

// The wake block the CLI hands a subagent when one of that subagent's own
// backgrounded shells finishes. It arrives as a task_started prompt.
const (
	claudeWakeOpenTag        = "<task-notification>"
	claudeWakeCloseTag       = "</task-notification>"
	claudeWakeTaskIDOpenTag  = "<task-id>"
	claudeWakeTaskIDCloseTag = "</task-id>"
)

// claudeWakeTaskID reads the task ID identified by a <task-notification> wake prompt.
//
// Require both notification tags and a nonempty ID between task-id tags.
// The caller also requires a shell that this process records as finished.
// reviveClaudeSubagent requires an existing registry row with a final status.
// Those process-local checks provide restart evidence beyond the prompt's text, as SendMessage delivery intent does.
// A resumed session's old prompts identify shell IDs from the preceding process rather than this process's completed-shell set.
//
// Allow the notification tags anywhere in the prompt and read the first task-id element.
// Do not require the tags on the first and last lines or the ID on a separate line.
// LeapMux does not control this model-facing prose layout.
// Rejecting a valid layout would leave an actually running child with a final registry status.
// The completed-shell and existing-final-row checks still apply to every accepted candidate.
func claudeWakeTaskID(prompt string) (string, bool) {
	if !strings.Contains(prompt, claudeWakeOpenTag) || !strings.Contains(prompt, claudeWakeCloseTag) {
		return "", false
	}
	_, rest, ok := strings.Cut(prompt, claudeWakeTaskIDOpenTag)
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, claudeWakeTaskIDCloseTag)
	if !ok {
		return "", false
	}
	id = strings.TrimSpace(id)
	return id, id != ""
}

// rememberFinishedShellTask records a shell task that this process gives a final status.
// A later wake prompt must identify one of these tasks to prove current-process shell activity.
//
// Record shells only because the CLI wakes a child after that child's background shell completes.
// Recording all ended tasks formerly let a subagent's own ID satisfy this check.
// A resumed session can replay that subagent's previous wake prompt.
// The replay then incorrectly reopened a final row with no later close.
//
// Read the kind under the existing lock instead of calling kindForTask, which acquires the same mutex.
// A task without a task_started in this process has no known kind and enters no completed-shell record.
// An unknown task supplies no proof of a shell that this process runs.
func (i *claudeTaskIndex) rememberFinishedShellTask(taskID string) {
	if taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runs.taskKind[taskID] != bgtask.KindShell {
		return
	}
	if i.restarts.finishedShells == nil {
		i.restarts.finishedShells = make(map[string]struct{})
	}
	i.restarts.finishedShells[taskID] = struct{}{}
}

// claudeWakeRestartedTask reports whether this task_started is the CLI waking a
// finished subagent because one of its own backgrounded shells completed.
func (i *claudeTaskIndex) claudeWakeRestartedTask(ev *claudeTaskEnvelope) bool {
	shellTaskID, ok := claudeWakeTaskID(ev.Prompt)
	if !ok {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	_, seen := i.restarts.finishedShells[shellTaskID]
	return seen
}

// rememberTaskChild records the child transcript -> task_id link, so a
// forwarded envelope can identify its registry row from the child alone.
func (i *claudeTaskIndex) rememberTaskChild(taskID, childID string) {
	if taskID == "" || childID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.transcripts.childTask == nil {
		i.transcripts.childTask = make(map[string]string)
	}
	i.transcripts.childTask[childID] = taskID
}

// taskIDForChild resolves the registry row_key for a child transcript id.
// Returns "" when no task_started linked one.
func (i *claudeTaskIndex) taskIDForChild(childID string) string {
	if childID == "" {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.transcripts.childTask[childID]
}

// forgetTaskIndex removes both directions of every tool-use ID linked to a task.
// Call it at task_notification or a fallback final result, so a task without a notification retains no index entries.
//
// Remove every ID, not merely the last recorded ID.
// A restarted run contains its original spawn span and the restarting call ID.
// Retaining either toolUseTask entry would resolve future envelopes to an ended task.
func (i *claudeTaskIndex) forgetTaskIndex(taskID string) {
	if taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for tuid := range i.runs.taskToolUse[taskID] {
		// Remove an ID only when it still resolves to this task.
		// A restart recovers its original span from the child row even when its event does not contain that ID.
		// Two task sets can therefore contain the same ID.
		// The later writer owns toolUseTask, and removing its entry would make every forwarded envelope of that active run unresolved.
		if i.runs.toolUseTask[tuid] == taskID {
			delete(i.runs.toolUseTask, tuid)
		}
	}
	delete(i.runs.taskToolUse, taskID)
	if spawnSpanID := i.handbacks.tasks[taskID]; spawnSpanID != "" {
		if pending, ok := i.handbacks.pending[spawnSpanID]; ok {
			i.deletePendingHandbackLocked(spawnSpanID, pending)
		}
	}
	// Preserve childTask because it identifies the durable transcript, not one run.
	// Restarted envelopes must still resolve their registry row after the run's tool-use entries disappear.
	// routeSubagentMessage reads this link on every index miss, preventing a second row for the same child.
	// The map has at most one entry per spawned subagent, matching the lifetime of the stored child transcripts.
	delete(i.runs.taskKind, taskID)
}

// knowsTask reports whether this process registers taskID through task_started without a later final notification or result removing it.
// InterruptChild requires that process-local route.
// A missing entry returns the same retryable condition in these cases:
//   - The row belongs to a previous process.
//   - The run already ends.
//   - The supplied key identifies an early pre-start row.
func (i *claudeTaskIndex) knowsTask(taskID string) bool {
	if taskID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.runs.taskKind[taskID]
	return ok
}

// markInterrupted records the reader's InterruptChild request for this task.
// The first matching close consumes the mark and uses StatusInterrupted with "Subagent interrupted" instead of the ordinary stopped outcome.
func (i *claudeTaskIndex) markInterrupted(taskID string) {
	if taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runs.interrupted == nil {
		i.runs.interrupted = make(map[string]struct{})
	}
	i.runs.interrupted[taskID] = struct{}{}
}

func (i *claudeTaskIndex) clearInterrupted(taskID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.runs.interrupted, taskID)
}

// takeInterrupted reports the task's InterruptChild request and consumes its mark so only one close uses it.
// The result and task_notification paths can both report the same stop.
// The first close uses interrupted wording, and the second uses the ordinary stopped outcome.
func (i *claudeTaskIndex) takeInterrupted(taskID string) bool {
	if taskID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.runs.interrupted[taskID]; !ok {
		return false
	}
	delete(i.runs.interrupted, taskID)
	return true
}

// kindForTask returns the kind recorded by task_started, or KindUnspecified when no entry exists.
// An entry can be absent after resume or after its removal.
// PreservingBlanksFrom retains an existing row's kind when the incoming kind is unspecified.
// An undescribed new task still supplies no invented kind.
func (i *claudeTaskIndex) kindForTask(taskID string) bgtask.Kind {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runs.taskKind[taskID]
}

// claudeSendMessageInput holds the SendMessage fields that this code reads.
// to identifies the recipient, and a child in this session uses its Claude task_id.
// The CLI's task registry uses that agent ID, which also supplies the LeapMux row_key.
// Other forms identify no row in this registry:
//   - A display name.
//   - Another session.
//   - A uds:, bridge:, or did: address.
// Those recipients therefore resolve to no child row.
type claudeSendMessageInput struct {
	To string `json:"to"`
}

// claudeArmRestartsFromBlocks records recipient task IDs from assistant SendMessage calls as expected delivery restarts.
// A later task_started can then prove that the CLI delivers to that recipient.
//
// task_started alone cannot prove a restart.
// A resumed session announces every previous subagent again, including final rows, while rebuilding its native task registry.
// Reopening all such rows would leave old subagents active with no later close.
// A recorded SendMessage supplies current delivery evidence that the hydration sequence lacks.
//
// Record delivery intent without first requiring that to resolves.
// Registry lookup occurs when the event consumes the intent.
// An external recipient matches no task_started and loses its intent at turn end.
//
// The parent and child transcripts share this method because subagent SendMessage calls arrive through routeSubagentMessage.
// armedBy identifies the sender: "" for the root or the child's spawn span.
// Each transcript's turn end therefore removes only its own delivery intents.
func (a *Agent) claudeArmRestartsFromBlocks(env *messageEnvelope, armedBy string) {
	for _, block := range env.ContentBlocks() {
		if block.Type != "tool_use" || block.Name != ToolNameSendMessage {
			continue
		}
		// Record the SendMessage call before parsing its input, regardless of the input content.
		// Two handleClaudeTaskStarted checks distinguish that call ID from a spawn span.
		// An unparseable recipient is still a SendMessage call, not a spawn.
		a.tasks.rememberSendMessageCall(block.ID)
		var input claudeSendMessageInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			slog.Warn("claude SendMessage input unmarshal failed", "agent_id", a.AgentID(), "error", err)
			continue
		}
		a.tasks.armClaudeRestart(input.To, armedBy)
	}
}

// rememberSendMessageCall records one SendMessage tool_use id for the life of the
// agent. See claudeTaskIndex.restarts.sendMessageCalls for why it outlives the turn.
func (i *claudeTaskIndex) rememberSendMessageCall(toolUseID string) {
	if toolUseID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.restarts.sendMessageCalls == nil {
		i.restarts.sendMessageCalls = make(map[string]struct{})
	}
	i.restarts.sendMessageCalls[toolUseID] = struct{}{}
}

// claudeRestartCall reports whether toolUseID identifies a recorded SendMessage call rather than the original spawn.
// That classification distinguishes a repeated task registration from its initial spawn.
//
// Do not consume or expire the call record.
// Every handleClaudeTaskStarted decision must receive the same classification for that ID, including an event arriving in a later turn.
func (i *claudeTaskIndex) claudeRestartCall(toolUseID string) bool {
	if toolUseID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.restarts.sendMessageCalls[toolUseID]
	return ok
}

// claudeRestartEvidence holds positive process-local proof that task_started repeats an existing run instead of starting a new task.
// Each supported restart form supplies one field, and shared classification uses restarted().
// A new form therefore needs one field without leaving an existing decision unchanged.
// Before this type, three decisions read only SendMessage evidence.
// A shell wake with an unreadable registry then selected its prompt as a title and renamed the row to the wake block.
//
// A final registry row alone supplies no restart proof.
// It can also describe resumed-session hydration, a duplicate event, or reordered updates.
// Reopening from that state alone can leave a row active with no later close.
type claudeRestartEvidence struct {
	// sendMessage records that the event's tool_use_id identifies a known SendMessage call rather than the original spawn.
	sendMessage bool
	// wake records that the prompt identifies a background shell completed by this process, which supplies evidence of a native owner restart.
	// reviveClaudeSubagent reads this form separately because a wake supplies no user text, while SendMessage does.
	wake bool
}

// restarted reports whether ANY form proved a re-registration.
func (e claudeRestartEvidence) restarted() bool { return e.sendMessage || e.wake }

func (a *Agent) restartEvidenceFor(ev *claudeTaskEnvelope) claudeRestartEvidence {
	return claudeRestartEvidence{
		sendMessage: a.tasks.claudeRestartCall(ev.ToolUseID),
		wake:        a.tasks.claudeWakeRestartedTask(ev),
	}
}

// armClaudeRestart records a recipient's expected restart until the sending transcript's turn ends.
// armedBy is "" for the root or the sender child's spawn span.
//
// Retain every sending transcript for one recipient, not only the latest sender.
// The root and a live sibling can both send within one root turn.
// A single sender value formerly let the sibling replace the root scope.
// The sibling's turn end then removed the intent before the root's delivery event.
// That event left the restarted row final and omitted its delivered message.
func (i *claudeTaskIndex) armClaudeRestart(to, armedBy string) {
	if to == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.restarts.pendingByTask == nil {
		i.restarts.pendingByTask = make(map[string]map[string]struct{})
	}
	if i.restarts.pendingByTask[to] == nil {
		i.restarts.pendingByTask[to] = make(map[string]struct{})
	}
	i.restarts.pendingByTask[to][armedBy] = struct{}{}
}

// takeClaudeRestart reports whether a current delivery intent exists for taskID and consumes every sender scope for it.
// One restart must not reopen the same row twice.
// The CLI restarts a recipient once even when multiple senders queue messages, so consume the complete set rather than one scope.
func (i *claudeTaskIndex) takeClaudeRestart(taskID string) bool {
	if taskID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.restarts.pendingByTask[taskID]; !ok {
		return false
	}
	delete(i.restarts.pendingByTask, taskID)
	return true
}

// clearClaudeRestarts removes one transcript's delivery intents at that transcript's turn end.
// These sends can produce no task_started:
//   - A message to a live child.
//   - A recipient outside this session.
//   - A send refused by the CLI.
// Without removal, their intents would accumulate for the agent's lifetime.
//
// Remove only the ending transcript's scope.
// A child can outlive the root turn that starts it, so the root result must preserve that child's intent.
// Clearing all scopes there formerly prevented a later restart, leaving its recipient final with an inactive-looking transcript.
func (i *claudeTaskIndex) clearClaudeRestarts(armedBy string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	// Remove only the ending transcript from each recipient's sender set.
	// Remove the recipient entry when its final sender ends.
	// When two transcripts address one child, the earlier turn end therefore cannot cancel the later sender's expected restart.
	for to, scopes := range i.restarts.pendingByTask {
		delete(scopes, armedBy)
		if len(scopes) == 0 {
			delete(i.restarts.pendingByTask, to)
		}
	}
	// Preserve recorded SendMessage call IDs when delivery intents expire.
	// A delivery intent authorizes row revival only through its sending turn.
	// A call ID records an immutable fact about a unique tool invocation and remains valid for the agent lifetime.
	// See claudeTaskIndex.restarts.sendMessageCalls.
}

// recordPendingTaskEnd retains a Task child's final status when its result precedes task_started.
// Use the spawn tool-use ID supplied in each forwarded envelope's parent_tool_use_id.
// The later task_started consumes that status and closes its new row instead of leaving it Running.
func (i *claudeTaskIndex) recordPendingTaskEnd(spawnToolUseID string, status bgtask.Status) {
	if spawnToolUseID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runs.pendingEnd == nil {
		i.runs.pendingEnd = make(map[string]bgtask.Status)
	}
	i.runs.pendingEnd[spawnToolUseID] = status
}
