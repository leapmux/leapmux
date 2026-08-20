package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"

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

	// The per-process proof that this event RE-REGISTERS a task rather than
	// starting one. Read once at the top, because four decisions below turn on it
	// and none may disagree with another.
	restart := a.restartEvidenceFor(ev)

	startedKind := bgtask.KindSubagent
	if ev.TaskType == claudeTaskTypeBash {
		startedKind = bgtask.KindShell
	}
	pendingEnd, hasPending := a.tasks.startTask(ev.TaskID, ev.ToolUseID, startedKind)

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
	//
	// Positive restart evidence suppresses the fallback, and an UNREADABLE
	// registry is not evidence. Keying on the read failure had the asymmetry
	// backwards: a read fails most often on the first registry touch of a
	// process, where a first start is the overwhelmingly common event, and the
	// row then took a blank title that nothing rewrites -- and EnsureChildAgent,
	// given the same blank, took the tab name from the pool instead of from the
	// prompt.
	//
	// The WAKE half of the evidence matters here even though `known.exists`
	// usually answers for it: a wake against a registry this process cannot read
	// leaves exists false, and the fallback then renamed the row to the wake
	// block itself -- a literal <task-notification> in the sidebar.
	title := ev.Description
	if title == "" && !known.exists && !restart.restarted() {
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
	// the task -- the parent's SendMessage -- and that call still runs in
	// the parent transcript. Closing its span here would free the rail mid-flight,
	// so its own tool_result would draw a connector_end with no vertical line
	// above it to meet.
	//
	// The in-flight SendMessage CALLS decide it, not "does the registry hold this
	// task". The registry is durable and the call index is not, so a row that
	// outlived a worker restart, or one a reordered task_notification opened
	// first, would suppress the close for a genuine spawn and strand its rail
	// open for the rest of the transcript. claudeArmRevivesFromBlocks records
	// every SendMessage tool_use id as the block is parsed, and each transcript
	// drops its own at its own turn end -- so this reads "restart call" exactly
	// when the event re-registers a task, and nothing otherwise.
	//
	// The span tracker cannot answer this. A subagent's own SendMessage records
	// its span type on that CHILD's tracker (routeSubagentMessage), while this
	// runs on the ROOT sink, which answers "" for an id it never saw -- and ""
	// is indistinguishable from a spawn here. So a sibling-to-sibling send, the
	// case the revive exists to support, read as a spawn at both guards.
	//
	// An unreadable registry suppresses the close too: it cannot prove this is a
	// spawn, and freeing a rail that is still in use is the worse of the two
	// errors -- a rail given back too late costs one column, one given back too
	// early leaves a tool_result drawing a connector end with nothing above it.
	if !known.unreadable && !restart.restarted() &&
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
		// Never from a SendMessage id. EnsureChildAgent takes a SPAWN SPAN, and a
		// re-registration's tool_use_id is the call that RESTARTED the task -- so
		// handing it over walks past the row-key fast path, fails
		// GetChildAgentBySpawnSpan, and CREATES a child keyed by a non-spawn id,
		// which a later forwarded envelope under the real spawn span then
		// duplicates.
		//
		// A linked row never reaches this test, because the registry row that
		// carries the child outlives the display cap. What reaches it is a row
		// that holds no linkage at all: an EnsureChildAgent that a DB failure
		// defeated at spawn time, or a registry this process could not read. The
		// in-flight SendMessage calls are the only positive evidence available in
		// either state, and refusing an unproven id costs a transcript the
		// subagent never had -- creating one under the wrong key costs the
		// transcript it does have.
		//
		// The restart evidence and not a span-type read: the span tracker answers for one
		// transcript, and a subagent's own SendMessage lands on that child's
		// tracker rather than the root's. See the guard above.
		if childID == "" && ev.ToolUseID != "" && !restart.restarted() {
			childID, err = a.sink.EnsureChildAgent(ev.ToolUseID, ev.TaskID, title)
		}
		if childID != "" {
			a.tasks.rememberTaskChild(ev.TaskID, childID)
		}
		switch {
		case err != nil:
			slog.Warn("claude task_started ensure child failed", "task_id", ev.TaskID, "error", err)
		case childID == "":
			// No transcript, and none this event can open. The revive arm is left
			// STANDING rather than consumed: the row is still finished, so a later
			// task_started for this task in the same turn is still a revive and
			// deserves the retry -- the same reason a failed registry write puts
			// its arm back.
			slog.Warn("claude task_started resolved no child transcript",
				"task_id", ev.TaskID, "tool_use_id", ev.ToolUseID,
				"row_exists", known.exists, "registry_unreadable", known.unreadable)
		default:
			handled, err := a.reviveClaudeSubagent(ev, childID, known, restart)
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
		logRegistryRefusal("claude", "status", a.sink.UpdateBackgroundTaskStatus(ev.TaskID, pendingEnd, ""))
		logRegistryRefusal("claude", "close", a.sink.CloseBackgroundTask(ev.TaskID, pendingEnd))
		a.tasks.forgetTaskIndex(ev.TaskID)
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
func (a *ClaudeCodeAgent) reviveClaudeSubagent(ev *claudeTaskEnvelope, childID string, known claudeKnownTask, restart claudeRestartEvidence) (handled bool, err error) {
	if childID == "" || !known.exists || !known.status.IsFinished() {
		return false, nil
	}
	// Two restarts, two proofs. A SendMessage the parent made this turn, or the
	// CLI waking this subagent because one of its own backgrounded shells
	// finished. Only the first delivers text: a wake's prompt is a
	// <task-notification> block addressed to the model, which is harness
	// plumbing rather than anything the user asked for, so it reopens the row and
	// writes nothing.
	//
	// The evidence arrives as a parameter, because the caller reads it before it
	// derives the row title from the same event. Two reads of one fact would let
	// the title and the revive disagree about what this event is. This is also
	// the ONE decision that reads a single form rather than restarted(): only a
	// SendMessage delivers text, so a wake reopens the row and writes nothing.
	delivered := a.tasks.takeClaudeRevive(ev.TaskID)
	if !delivered && !restart.wake {
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
			a.tasks.armClaudeRevive(ev.TaskID, "")
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
	// Remember it BEFORE the writes: a wake for this shell's owner can follow
	// immediately, and it proves itself with an id this process finalized. The
	// call filters to shells; forgetTaskIndex below drops the kind, so the read
	// has to happen here.
	a.tasks.rememberFinishedShellTask(ev.TaskID)
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
		logRegistryRefusal("claude", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
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
	taskID := a.tasks.taskIDForToolUse(spawnSpanID)

	childID, err := a.sink.EnsureChildAgent(spawnSpanID, taskID, "")
	if err != nil {
		slog.Warn("claude route subagent: ensure child failed", "spawn_span", spawnSpanID, "error", err)
		return
	}
	// Only an index-derived id is recorded here. Resolving the task FROM the
	// child and then writing the pair back would be a no-op write, and it would
	// leave two copies of the child-keyed fallback -- the result branch below
	// holds the one that earns its place, where a revived run's result is the
	// only reader that needs it.
	a.tasks.rememberTaskChild(taskID, childID)

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
			taskID = a.tasks.taskIDForChild(childID)
		}
		if taskID != "" {
			status := bgtask.StatusCompleted
			if env.IsError {
				status = bgtask.StatusFailed
			}
			logRegistryRefusal("claude", "status", a.sink.UpdateBackgroundTaskStatus(taskID, status, ""))
			logRegistryRefusal("claude", "close", a.sink.CloseBackgroundTask(taskID, status))
			// Drop the index entry on the fallback path too (a task that ends
			// via the result without a task_notification would otherwise leak).
			a.tasks.forgetTaskIndex(taskID)
		} else {
			// task_started has not arrived yet (a reorder): remember the final
			// status keyed by the spawn span so the late task_started can close
			// the row it opens. Without this, task_started upserts a Running row
			// that nothing ever closes (the result already passed through).
			status := bgtask.StatusCompleted
			if env.IsError {
				status = bgtask.StatusFailed
			}
			a.tasks.recordPendingTaskEnd(spawnSpanID, status)
		}
		// This subagent's turn ended, so drop the arms IT set. Its own result is
		// the boundary for them; the root's is not, because this transcript
		// outlives the root turn that spawned it.
		a.tasks.clearClaudeRevives(spawnSpanID)
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

// claudeTaskIndex is everything this agent knows about a Claude task, in one
// value with one mutex.
//
// Eight maps index the same concept, and they were eight fields on the agent
// under processBase.mu -- the process-lifecycle lock, shared with `stopped`, the
// model, and the effort. Grouping them states the shared lifetime rule in one
// place (forgetTaskIndex drops the run-scoped entries; childTask and
// finishedTasks describe things that outlive a run) rather than in five field
// comments that a reader has to collect.
//
// A zero value is usable: every write lazily builds its map, so the many sites
// that build a ClaudeCodeAgent without StartClaudeCode need no constructor.
type claudeTaskIndex struct {
	mu sync.Mutex
	// Subagent (Task/Workflow) index. Maps a Claude task_id <-> the spawning
	// tool_use id (parent_tool_use_id on forwarded envelopes). Used to route
	// forwarded subagent output into the right child transcript and to drive
	// the background-task registry. Guarded by i.mu. NOT cleared at turn end
	// (background tasks outlive turns); entries dropped on the final
	// task_notification.
	taskToolUse map[string]string // task_id -> tool_use_id
	toolUseTask map[string]string // tool_use_id -> task_id
	// childTask indexes the same link from the CHILD transcript side, so a
	// forwarded envelope resolves its registry row when the tool_use index
	// cannot. A revive re-registers the task under the SendMessage tool_use id,
	// and the first completion already dropped the spawn span, so a run-2 result
	// forwarded under the ORIGINAL spawn resolves no task and leaves the row the
	// revive reopened Running for the agent's life. Guarded by i.mu, and NOT
	// dropped at a completion: the entry describes the transcript, which outlives
	// every run of it.
	childTask map[string]string // child_agent_id -> task_id
	// finishedTasks holds the id of every backgrounded SHELL this process gave a
	// final status. A wake block identifies the shell whose completion restarted
	// a subagent, and confirming that id here is what separates a real wake from
	// a resumed session's hydration burst, which replays prompts that identify
	// tasks of a PREVIOUS process.
	//
	// Shells only. Recording every finished task let a SUBAGENT's own id satisfy
	// the proof, and a hydration burst replays that subagent's own wake prompt --
	// so the one impostor the discriminator exists to exclude walked through it.
	// Guarded by i.mu; never cleared, and limited to the shells one process ran.
	finishedTasks map[string]struct{} // shell task_id this process finalized
	// taskKind remembers what task_started said a task IS, because only
	// task_started carries task_type. A later task_notification has to upsert
	// the row again (to record a shell's output_file) and would otherwise have
	// to guess the kind -- guessing "shell" there rewrote every Task subagent's
	// row into a shell one, since notifications fire for subagents too.
	// Guarded by i.mu; dropped with the rest of the index on the closing
	// notification.
	taskKind map[string]bgtask.Kind // task_id -> kind
	// pendingTaskEnd holds a final status for a Task subagent whose result
	// message arrived BEFORE its task_started (a forward of the child's final
	// result can race past a reordered task_started). Keyed by spawn tool_use id
	// so the late task_started can close the row it just opened. Guarded by i.mu.
	pendingTaskEnd map[string]bgtask.Status // spawn tool_use_id -> final status
	// pendingRevive holds the task ids the in-flight SendMessage calls addressed.
	// A task_started for a task already in a final status is a REVIVE only when
	// the id is armed here; see claudeArmRevivesFromBlocks for why the tool call
	// is the evidence and the event alone is not. Guarded by i.mu.
	//
	// The VALUE is the SET of transcripts that armed it: "" for the root, the
	// spawn span id for a subagent. Each transcript's own turn end drops only its
	// own arms, because a subagent outlives the root turn it was spawned in --
	// clearing on the root's result alone dropped a live subagent's arm before
	// its task_started could fire it, and left that arm standing for the agent's
	// life while the root sat idle.
	//
	// A set and not one scope, because two transcripts can address one recipient
	// inside a single root turn. With one scope the second sender overwrote the
	// first, and whichever turn ended first dropped an arm the other still
	// needed.
	pendingRevive map[string]map[string]struct{} // task_id -> the spawn spans that armed it ("" == root)
	// sendMessageCalls holds the tool_use id of every in-flight SendMessage call,
	// with the transcript that made it. handleClaudeTaskStarted asks it whether
	// an event's tool_use_id is a restart call rather than the spawn span that
	// created the task, and two decisions turn on the answer: the span close, and
	// whether the id may reach EnsureChildAgent.
	//
	// The SPAN tracker cannot answer it. A subagent's own SendMessage records its
	// span type on that child's tracker, while handleClaudeTaskStarted reads the
	// ROOT sink -- which answers "" for an id it never saw, and "" is what a
	// spawn also answers there. Recording the call where the block is parsed
	// covers both transcripts with one index. Guarded by i.mu, and cleared with
	// pendingRevive at the turn end of the transcript that set it.
	sendMessageCalls map[string]string // tool_use_id -> the spawn span that made it ("" == root)
}

// taskIDForToolUse resolves the registry row_key (Claude task_id) for a
// spawning tool_use id, recorded at task_started. Returns "" when unknown

// startTask records what a task_started says a task IS, and takes any pending
// close that a reordered final result left for it.
//
// One critical section for all three maps, because they describe one event: the
// task_id <-> tool_use_id pair, the kind, and the reordered close. Neither pair
// is cleared at a turn end -- a background task outlives the turn that spawned
// it -- and forgetTaskIndex drops all three on the closing notification.
//
// The pending close is keyed by the spawn span: a final result can arrive
// BEFORE the task_started it belongs to, and taking it here lets the caller
// close the row this event is about to open, so the row cannot leak Running.
func (i *claudeTaskIndex) startTask(taskID, toolUseID string, kind bgtask.Kind) (bgtask.Status, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if toolUseID != "" {
		if i.taskToolUse == nil {
			i.taskToolUse = make(map[string]string)
		}
		if i.toolUseTask == nil {
			i.toolUseTask = make(map[string]string)
		}
		i.taskToolUse[taskID] = toolUseID
		i.toolUseTask[toolUseID] = taskID
	}
	if i.taskKind == nil {
		i.taskKind = make(map[string]bgtask.Kind)
	}
	i.taskKind[taskID] = kind
	if toolUseID == "" {
		return bgtask.StatusPending, false
	}
	status, ok := i.pendingTaskEnd[toolUseID]
	if ok {
		delete(i.pendingTaskEnd, toolUseID)
	}
	return status, ok
}

// (a reorder or a pre-task_started forwarded envelope).
func (i *claudeTaskIndex) taskIDForToolUse(toolUseID string) string {
	if toolUseID == "" {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.toolUseTask[toolUseID]
}

// The wake block the CLI hands a subagent when one of that subagent's own
// backgrounded shells finishes. It arrives as a task_started prompt.
const (
	claudeWakeOpenTag        = "<task-notification>"
	claudeWakeCloseTag       = "</task-notification>"
	claudeWakeTaskIDOpenTag  = "<task-id>"
	claudeWakeTaskIDCloseTag = "</task-id>"
)

// claudeWakeTaskID reports the task id a <task-notification> wake block
// identifies.
//
// The block must carry BOTH the open tag and the close tag, and the id must sit
// inside a <task-id> element. Neither condition is where the safety lives: the
// caller confirms the id against the tasks THIS process finished, and
// reviveClaudeSubagent acts only on a row the registry already holds in a
// finished status. That pair is the same class of proof the SendMessage arm
// supplies -- positive, per-process evidence that the restart really happened --
// and a resumed session's hydration burst cannot forge it, because it replays
// prompts from a PREVIOUS process whose shell ids this one never saw.
//
// The parse is therefore deliberately shape-tolerant. It matches the tags
// anywhere in the prompt and takes the first <task-id> element wherever it
// sits, rather than requiring the tags at the first and last line and the id
// alone on its own. This text is model-facing prose that no LeapMux code
// controls, and the two failures are not symmetric: a false NEGATIVE silently
// restores the whole bug this change exists to fix -- the row reads "finished"
// for a run that is still going, and the run ends with no closing divider --
// while a false POSITIVE cannot get past the two proofs above.
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

// rememberFinishedShellTask records a backgrounded SHELL this process gave a
// final status, so a later wake block that identifies it is provably about THIS
// session.
//
// Shells only, because that is what a wake is about: the CLI wakes a subagent
// when one of that subagent's own backgrounded shells completes. Recording
// every finished task let a SUBAGENT's own id satisfy the proof, and a resumed
// session replays that subagent's wake prompt verbatim -- so the hydration
// burst, the one impostor the whole discriminator exists to exclude, walked
// straight through it and reopened a row with nothing to close it.
//
// The kind read shares this lock rather than calling kindForTask, which takes
// the same one. A task whose task_started this process never saw has no kind and
// is not recorded, which is the conservative answer: an unknown task is not a
// shell this process ran.
func (i *claudeTaskIndex) rememberFinishedShellTask(taskID string) {
	if taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.taskKind[taskID] != bgtask.KindShell {
		return
	}
	if i.finishedTasks == nil {
		i.finishedTasks = make(map[string]struct{})
	}
	i.finishedTasks[taskID] = struct{}{}
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
	_, seen := i.finishedTasks[shellTaskID]
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
	if i.childTask == nil {
		i.childTask = make(map[string]string)
	}
	i.childTask[childID] = taskID
}

// taskIDForChild resolves the registry row_key for a child transcript id.
// Returns "" when no task_started linked one.
func (i *claudeTaskIndex) taskIDForChild(childID string) string {
	if childID == "" {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.childTask[childID]
}

// forgetTaskIndex drops the task_id <-> tool_use_id pair from both directions
// of the index. Called when a task reaches a final state via either a
// task_notification or the result-message fallback, so a task that ends
// without a notification does not leak its index entries for the agent's life.
func (i *claudeTaskIndex) forgetTaskIndex(taskID string) {
	if taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if tuid, ok := i.taskToolUse[taskID]; ok {
		delete(i.taskToolUse, taskID)
		delete(i.toolUseTask, tuid)
	}
	// childTask deliberately SURVIVES. It describes the transcript, not the run,
	// and a transcript is permanent: a revived run's forwarded envelopes still
	// have to name this row, and the spawn span the tool_use index carried is
	// gone by then. Limited to the number of subagents this agent spawned, which
	// is the same bound as the child sinks the sink already holds.
	delete(i.taskKind, taskID)
}

// kindForTask returns what task_started said this task is, or KindUnspecified
// when the index has no entry -- a task_started this process never saw (a
// resume) or one already forgotten. The registry's blank-means-keep rule then
// preserves whatever kind the row already carries, which is the right answer
// for an existing row and the only honest one for a task nothing described.
func (i *claudeTaskIndex) kindForTask(taskID string) bgtask.Kind {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.taskKind[taskID]
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
		// The CALL is recorded whatever its input says, and before the input is
		// read. Two guards in handleClaudeTaskStarted ask "is this tool_use id a
		// restart call rather than a spawn span", and that answer does not depend
		// on the recipient resolving -- an input this code cannot parse is still
		// not a spawn.
		a.tasks.rememberSendMessageCall(block.ID, armedBy)
		var input claudeSendMessageInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			slog.Warn("claude SendMessage input unmarshal failed", "agent_id", a.agentID, "error", err)
			continue
		}
		a.tasks.armClaudeRevive(input.To, armedBy)
	}
}

// rememberSendMessageCall records one in-flight SendMessage tool_use id with the
// transcript that made it, until that transcript's turn end.
func (i *claudeTaskIndex) rememberSendMessageCall(toolUseID, armedBy string) {
	if toolUseID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sendMessageCalls == nil {
		i.sendMessageCalls = make(map[string]string)
	}
	i.sendMessageCalls[toolUseID] = armedBy
}

// claudeRestartCall reports whether toolUseID is an in-flight SendMessage call,
// which is what separates a task_started that RE-REGISTERS a task from the spawn
// that created one.
//
// It is not consumed. Both guards in handleClaudeTaskStarted read it, and a
// second task_started for the same call inside the turn must get the same
// answer; the turn end of the transcript that made the call drops it.
func (i *claudeTaskIndex) claudeRestartCall(toolUseID string) bool {
	if toolUseID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.sendMessageCalls[toolUseID]
	return ok
}

// claudeRestartEvidence is the positive, per-process proof that a task_started
// RE-REGISTERS a task rather than starting one. Each restart form the CLI has
// contributes one field, and every decision that asks "is this a
// re-registration" reads restarted() -- so a new form costs one field here and
// no decision site is left behind. The two forms drifted apart before this
// type existed: three decisions read the SendMessage half alone, and a wake
// against an unreadable registry took the prompt-derived title branch and
// renamed the row to the wake block.
//
// Positive evidence, never the absence of an impostor. "task_started against a
// finished row" describes a resumed session's hydration burst, a duplicate
// task_started, and a reordered one just as well as it describes a restart, and
// honoring any of them reopens a row that nothing will close.
type claudeRestartEvidence struct {
	// sendMessage: the event's tool_use_id is an in-flight SendMessage call, so
	// it is that call and not the spawn span that created the task.
	sendMessage bool
	// wake: the prompt is a wake block that identifies a backgrounded shell this
	// process finished, which is the CLI restarting that shell's owner. Only this
	// form is read on its own, by reviveClaudeSubagent -- a wake delivers no text,
	// and a SendMessage does.
	wake bool
}

// restarted reports whether ANY form proved a re-registration.
func (e claudeRestartEvidence) restarted() bool { return e.sendMessage || e.wake }

func (a *ClaudeCodeAgent) restartEvidenceFor(ev *claudeTaskEnvelope) claudeRestartEvidence {
	return claudeRestartEvidence{
		sendMessage: a.tasks.claudeRestartCall(ev.ToolUseID),
		wake:        a.tasks.claudeWakeRestartedTask(ev),
	}
}

// armClaudeRevive records one recipient id as revivable until the turn end of
// the transcript that sent the message. armedBy is "" for the root and the
// spawn span id for a subagent.
//
// A recipient carries the SET of transcripts that addressed it, not the last
// one. Two senders can address one subagent inside a single root turn -- the
// root and a live sibling -- and a single-valued arm let the second overwrite
// the first's scope. The sibling's own result then ended its turn, dropped the
// arm under ITS scope, and the root's send found nothing to fire: the row stayed
// finished for the whole restarted run and the delivered text never reached the
// transcript, which is the failure the arm exists to prevent.
func (i *claudeTaskIndex) armClaudeRevive(to, armedBy string) {
	if to == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pendingRevive == nil {
		i.pendingRevive = make(map[string]map[string]struct{})
	}
	if i.pendingRevive[to] == nil {
		i.pendingRevive[to] = make(map[string]struct{})
	}
	i.pendingRevive[to][armedBy] = struct{}{}
}

// takeClaudeRevive reports whether an in-flight SendMessage addressed taskID,
// and consumes every arm on it so one restart cannot revive the same row twice.
//
// The whole set goes, not one scope: the arms are proof that a restart is
// expected, and the CLI restarts the recipient ONCE however many senders queued
// a message for it.
func (i *claudeTaskIndex) takeClaudeRevive(taskID string) bool {
	if taskID == "" {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.pendingRevive[taskID]; !ok {
		return false
	}
	delete(i.pendingRevive, taskID)
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
func (i *claudeTaskIndex) clearClaudeRevives(armedBy string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	// One scope leaves each recipient's set, and the recipient goes only when its
	// last sender does. A recipient two transcripts addressed stays armed until
	// both of their turns end, so the earlier one cannot cancel the later one's
	// restart.
	for to, scopes := range i.pendingRevive {
		delete(scopes, armedBy)
		if len(scopes) == 0 {
			delete(i.pendingRevive, to)
		}
	}
	// The calls go with the arms. Both describe the same in-flight SendMessage
	// tool calls and carry the same scope, so one boundary ends both -- and a
	// call left standing would keep answering "restart" for an id whose turn
	// ended, which suppresses the span close for a later genuine spawn. The
	// tool_use id is unique per call, so this side needs no set.
	maps.DeleteFunc(i.sendMessageCalls, func(_, scope string) bool { return scope == armedBy })
}

// recordPendingTaskEnd remembers a final status for a Task subagent whose
// result message arrived before its task_started (a reorder). The late
// task_started takes the entry and closes the row it opens, so the row cannot
// leak Running. Keyed by the spawn tool_use id (the parent_tool_use_id every
// forwarded envelope shares).
func (i *claudeTaskIndex) recordPendingTaskEnd(spawnToolUseID string, status bgtask.Status) {
	if spawnToolUseID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pendingTaskEnd == nil {
		i.pendingTaskEnd = make(map[string]bgtask.Status)
	}
	i.pendingTaskEnd[spawnToolUseID] = status
}
