package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Qwen runs a subagent inside the parent's process and session, as the tool
// call of its `agent` tool:
//
//   - A FOREGROUND subagent streams its text, its thinking and its tool calls
//     in the parent session. Qwen tags each of those updates with
//     `_meta.parentToolCallId`, the id of the spawn, and the base routes them
//     to the child transcript (Hooks.ChildUpdateRoute). The spawn's own result
//     ends the child.
//   - A BACKGROUND subagent streams nothing. Its spawn completes at once, and
//     the child writes its transcript to a file of its own, which this package
//     reads as it grows (subagent_transcript.go). A background-task
//     notification ends the child.
//
// A top-level subagent runs in the background unless the model asks for the
// foreground, so the spawn's result, not its arguments, states which kind ran.

// qwenAgentSpawnTitle is the title of a subagent whose spawn states no
// description yet. Qwen's first frame of a spawn carries no arguments.
const qwenAgentSpawnTitle = "Subagent"

// qwenExecutionBackground is the execution mode of a background subagent, as
// its spawn result states it. The other mode, `foreground`, ends the child with
// the spawn.
const qwenExecutionBackground = "background"

// qwenTaskExecution is the result type of a subagent spawn.
const qwenTaskExecution = "task_execution"

// qwenNativeSessionID validates the actual parent UUID that a new child shares.
var qwenNativeSessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// qwenBackgroundOutputFile reads the transcript path that a background spawn's
// result states ("output_file: /…/agent-<id>.jsonl (for review …)"). The path
// can hold a space, as a home directory often does, so it runs to the first
// `.jsonl` that a space or the end of the line follows.
var qwenBackgroundOutputFile = regexp.MustCompile(`(?m)^output_file:[ \t]*(.+?\.jsonl)(?:[ \t]|$)`)

// qwenBackgroundTaskIDPattern reads the native task id from a background spawn's
// launch text ("task_id: <id>").
var qwenBackgroundTaskIDPattern = regexp.MustCompile(`(?m)^task_id:[ \t]*(\S+)`)

// qwenBackgroundShellID reads the id of a shell command that runs in the
// background, as a shell result states it on start or on promotion.
var qwenBackgroundShellID = regexp.MustCompile(`(?m)(?:^Background shell started\.\s*\nid:\s*|promoted to background as\s+)(bg_[0-9A-Za-z]+)`)

// qwenShellRowPrefix keys the registry row of one background shell command.
const qwenShellRowPrefix = "shell:"

// qwenToolSendMessage is Qwen's Go-only tool name for waking or messaging an
// existing background subagent by task_id.
const qwenToolSendMessage = "send_message"

// transcriptResumeState stores the last transcript path and byte offset of a
// stopped background subagent tail so send_message can resume from that offset.
type transcriptResumeState struct {
	path   string
	offset int64
}

// childState links Qwen's subagents and background commands to LeapMux's
// registry rows. Guarded by Agent.stateMu.
type childState struct {
	// spawns holds each subagent spawn that did not end, by tool-call id.
	spawns map[string]bool
	// tails holds the transcript reader of each background subagent, by
	// tool-call id.
	tails map[string]*transcriptTail
	// workflows holds the task-list poller of each workflow run, by the tool
	// call that started it.
	workflows map[string]*workflowPoller
	// stopped holds, by tool-call id, each subagent that the Interrupt of its
	// tab stopped, until Qwen reports the end of its row. Qwen's task cancel
	// aborts the subagent's model request, and Qwen then reports the subagent
	// as failed, so the row reads its final status through this set
	// (closingStatus).
	stopped map[string]bool
	// backgroundTasks maps Qwen's native task_id to the spawning tool-call id.
	backgroundTasks map[string]string
	// transcriptStates records the transcript path and byte offset saved when a
	// background subagent finishes, by spawning tool-call id.
	transcriptStates map[string]transcriptResumeState
	// earlyShellCompletions buffers background_task_completed verdicts for
	// shells that settle before run_shell_command completes, by row key.
	earlyShellCompletions map[string]bgtask.Status
	// openBgAgents, openBgShells, and openBgWorkflows track open background
	// tasks for queue-overflow reconciliation. openBgAgents maps each open
	// background agent's tool-call id to its active run sequence so a stale
	// reconciliation reply cannot close a newly revived job.
	openBgAgents    map[string]uint64
	agentRunSeq     map[string]uint64
	closedBgAgents  map[string]bool
	openBgShells    map[string]bool
	openBgWorkflows map[string]bool
}

// toolInputs remembers the tool name and the arguments of each running tool
// call. Qwen states them on one frame of a call -- the permission request or a
// progress update -- and never on its closing frame, which is where LeapMux
// learns what the call left running. Guarded by Agent.stateMu.
type toolInputs struct {
	byCall map[string]toolInput
}

// toolInput is what one tool call stated about itself.
type toolInput struct {
	name     string
	rawInput json.RawMessage
	// A native task-list snapshot owns workflow metadata once observed.
	workflowMetadataObserved bool
}

// remember records what one frame states about a tool call. An empty field
// leaves the known value.
func (t *toolInputs) remember(toolCallID, name string, rawInput json.RawMessage) {
	if toolCallID == "" {
		return
	}
	if t.byCall == nil {
		t.byCall = make(map[string]toolInput)
	}
	known := t.byCall[toolCallID]
	if name != "" {
		known.name = name
	}
	if len(rawInput) > 0 && string(rawInput) != "{}" && string(rawInput) != "null" {
		known.rawInput = rawInput
	}
	t.byCall[toolCallID] = known
}

// qwenToolName reads the real tool name that Qwen states in a tool call's
// `_meta`. The ACP title and kind are presentation, and a bridged tool changes
// its name between frames.
func qwenToolName(meta json.RawMessage) string {
	var fields map[string]json.RawMessage
	if len(meta) == 0 || json.Unmarshal(meta, &fields) != nil {
		return ""
	}
	var name string
	_ = json.Unmarshal(fields[contracts.QwenMetaToolName], &name)
	return name
}

// childUpdateRoute reads the spawn that a subagent's update belongs to.
func childUpdateRoute(_ string, metadata map[string]json.RawMessage) string {
	var parent string
	if json.Unmarshal(metadata[contracts.QwenMetaParentToolCallId], &parent) != nil {
		return ""
	}
	return parent
}

// qwenSpawnInput is the part of a spawn's arguments that LeapMux reads.
type qwenSpawnInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// spawnObservation states what one frame tells about a spawn: the row, and the
// prompt that opens the child transcript. Every frame of a spawn identifies the
// same row and child, so a later frame that states the arguments completes it.
func (a *Agent) spawnObservation(toolCallID string, rawInput json.RawMessage, spawns bool) *acp.SubagentObservation {
	var input qwenSpawnInput
	_ = json.Unmarshal(rawInput, &input)
	title := strings.TrimSpace(input.Description)
	if title == "" {
		title = qwenAgentSpawnTitle
	}
	owner := a.CurrentSessionID()
	rowKey := qwenAgentRowKey(owner, toolCallID)
	sessionID := owner
	if !qwenNativeSessionID.MatchString(sessionID) {
		sessionID = ""
	}
	return &acp.SubagentObservation{
		RowKey:              rowKey,
		ChildAgentKey:       rowKey,
		ChildAgentSessionID: sessionID,
		Title:               title,
		Prompt:              input.Prompt,
		Status:              bgtask.StatusRunning,
		Spawns:              spawns,
	}
}

// subagentFromToolCall claims a spawn and a workflow run at their first frame,
// and records every call's tool name for its later frames.
func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	name := qwenToolName(tc.Meta)
	final := acp.StatusIsFinal(tc.Status)
	var rawInput json.RawMessage
	a.stateMu.Lock()
	if final {
		known := a.tools.byCall[tc.ToolCallID]
		if name == "" {
			name = known.name
		}
		rawInput = tc.RawInput
		if len(rawInput) == 0 || string(rawInput) == "{}" || string(rawInput) == "null" {
			rawInput = known.rawInput
		}
		delete(a.tools.byCall, tc.ToolCallID)
		delete(a.children.spawns, tc.ToolCallID)
	} else {
		a.tools.remember(tc.ToolCallID, name, tc.RawInput)
		rawInput = tc.RawInput
		if name == contracts.QwenToolAgent {
			if a.children.spawns == nil {
				a.children.spawns = make(map[string]bool)
			}
			a.children.spawns[tc.ToolCallID] = true
		}
	}
	a.stateMu.Unlock()
	switch name {
	case contracts.QwenToolAgent:
		if final {
			return nil
		}
		return a.spawnObservation(tc.ToolCallID, rawInput, true)
	case contracts.QwenToolWorkflow:
		if final {
			a.stopWorkflowPoller(tc.ToolCallID, acp.FinalStatus(tc.Status))
			return nil
		}
		a.startWorkflowPoller(tc.ToolCallID, rawInput)
		return workflowObservation(a.CurrentSessionID(), tc.ToolCallID, rawInput)
	case qwenToolSendMessage:
		// Wait for send_message to complete before reviving the child or
		// restarting its transcript tail: permission denial or a failed resume
		// emits a non-completed final update with no background_task_completed.
		if tc.Status == "completed" {
			if obs := a.sendMessageObservation(rawInput); obs != nil {
				a.ApplySubagentObservation(obs)
			}
		}
		return nil
	default:
		return nil
	}
}

// subagentFromToolCallUpdate follows a spawn, a workflow run and a shell
// command through their later frames.
func (a *Agent) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	a.stateMu.Lock()
	a.tools.remember(tcu.ToolCallID, qwenToolName(tcu.Meta), tcu.RawInput)
	known := a.tools.byCall[tcu.ToolCallID]
	final := acp.StatusIsFinal(tcu.Status)
	if final {
		delete(a.tools.byCall, tcu.ToolCallID)
	}
	a.stateMu.Unlock()
	switch known.name {
	case contracts.QwenToolAgent:
		return a.spawnUpdate(tcu, known.rawInput, final)
	case contracts.QwenToolWorkflow:
		if !final {
			if known.workflowMetadataObserved || len(tcu.RawInput) == 0 {
				return nil
			}
			// The arguments arrive on a progress frame: the run's row and its
			// group take the workflow's name from them, as a spawn's row takes
			// its title.
			return workflowObservation(a.CurrentSessionID(), tcu.ToolCallID, tcu.RawInput)
		}
		var input qwenWorkflowInput
		_ = json.Unmarshal(known.rawInput, &input)
		if tcu.Status == "completed" && input.RunInBackground {
			a.stateMu.Lock()
			if a.children.openBgWorkflows == nil {
				a.children.openBgWorkflows = make(map[string]bool)
			}
			a.children.openBgWorkflows[tcu.ToolCallID] = true
			a.stateMu.Unlock()
			if !known.workflowMetadataObserved && len(tcu.RawInput) > 0 {
				return workflowObservation(a.CurrentSessionID(), tcu.ToolCallID, known.rawInput)
			}
			return nil
		}
		a.stopWorkflowPoller(tcu.ToolCallID, acp.FinalStatus(tcu.Status))
		return &acp.SubagentObservation{RegistryOnly: true, RowKey: qwenWorkflowRowKey(a.CurrentSessionID(), tcu.ToolCallID), Status: acp.FinalStatus(tcu.Status), CloseRow: true, Mode: acp.ModeCloseOnly}
	case contracts.QwenToolRunShellCommand:
		if final && tcu.Status == "completed" {
			return a.observeBackgroundShell(tcu, known.rawInput)
		}
		return nil
	case qwenToolSendMessage:
		if !final || tcu.Status != "completed" {
			return nil
		}
		return a.sendMessageObservation(known.rawInput)
	default:
		return nil
	}
}

// qwenTaskExecutionOutput is the part of a spawn's result that LeapMux reads.
type qwenTaskExecutionOutput struct {
	Type          string `json:"type"`
	ExecutionMode string `json:"executionMode"`
	Status        string `json:"status"`
	Result        string `json:"result"`
}

// qwenSubagentStatus maps the status that a foreground spawn's result states.
func qwenSubagentStatus(status string, fallback bgtask.Status) bgtask.Status {
	switch status {
	case "completed":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	case "cancelled":
		return bgtask.StatusStopped
	default:
		return fallback
	}
}

// spawnUpdate reads one later frame of a spawn.
func (a *Agent) spawnUpdate(tcu acp.ToolCallUpdateEnvelope, rawInput json.RawMessage, final bool) *acp.SubagentObservation {
	if !final {
		if len(tcu.RawInput) == 0 {
			return nil
		}
		// The arguments arrive on a progress frame: the row takes its title and
		// the child its prompt.
		return a.spawnObservation(tcu.ToolCallID, rawInput, false)
	}
	a.stateMu.Lock()
	delete(a.children.spawns, tcu.ToolCallID)
	a.stateMu.Unlock()

	var output qwenTaskExecutionOutput
	_ = json.Unmarshal(tcu.RawOutput, &output)
	if tcu.Status == "completed" && output.Type == qwenTaskExecution && output.ExecutionMode == qwenExecutionBackground {
		resultText := acp.ToolCallText(tcu.Content)
		taskID := qwenBackgroundTaskID(resultText, tcu.RawOutput)
		rowKey := qwenAgentRowKey(a.CurrentSessionID(), tcu.ToolCallID)
		_, currentStatus, found, lookupErr := a.Sink().LookupBackgroundTask(rowKey)
		alreadyClosed := lookupErr == nil && found && currentStatus.IsFinished()

		a.stateMu.Lock()
		if taskID != "" {
			if a.children.backgroundTasks == nil {
				a.children.backgroundTasks = make(map[string]string)
			}
			a.children.backgroundTasks[taskID] = tcu.ToolCallID
		}
		if a.children.closedBgAgents[tcu.ToolCallID] {
			alreadyClosed = true
		}
		if !alreadyClosed {
			a.openBackgroundAgentLocked(tcu.ToolCallID)
		}
		a.stateMu.Unlock()
		if alreadyClosed {
			return nil
		}
		a.startBackgroundTranscript(tcu.ToolCallID, resultText)
		observation := a.spawnObservation(tcu.ToolCallID, rawInput, false)
		observation.Activity = "Running in the background"
		return observation
	}
	status := acp.FinalStatus(tcu.Status)
	if output.Type == qwenTaskExecution {
		status = qwenSubagentStatus(output.Status, status)
	}
	status = a.closingStatus(tcu.ToolCallID, status)
	report := output.Result
	if report == "" && tcu.Status == "completed" {
		report = acp.ToolCallText(tcu.Content)
	}
	return &acp.SubagentObservation{
		RowKey:   qwenAgentRowKey(a.CurrentSessionID(), tcu.ToolCallID),
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
		ReportID: tcu.ToolCallID,
		Report:   agent.SubagentReport{Text: report},
	}
}

// startBackgroundTranscript starts the reader of a background child's
// transcript, at the path that the spawn's result states. A result that states
// no usable path leaves the child's tab with its prompt alone.
func (a *Agent) startBackgroundTranscript(toolCallID, resultText string) {
	match := qwenBackgroundOutputFile.FindStringSubmatch(resultText)
	if match == nil {
		slog.Warn("qwen background subagent states no transcript", "agent_id", a.AgentID(), "tool_call_id", toolCallID)
		return
	}
	path, ok := backgroundTranscriptPath(match[1])
	if !ok {
		slog.Warn("qwen background subagent transcript path refused", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "path", match[1])
		return
	}
	tail := newTranscriptTail(a, qwenAgentRowKey(a.CurrentSessionID(), toolCallID), path)
	a.stateMu.Lock()
	if a.children.tails == nil {
		a.children.tails = make(map[string]*transcriptTail)
	}
	if _, running := a.children.tails[toolCallID]; running {
		a.stateMu.Unlock()
		return
	}
	a.children.tails[toolCallID] = tail
	clock := a.clock
	a.stateMu.Unlock()
	tail.start(clock)
}

// qwenBackgroundTask is the `_meta.backgroundTask` of a background-task
// notification.
type qwenBackgroundTask struct {
	TaskID       string `json:"taskId"`
	Status       string `json:"status"`
	Kind         string `json:"kind"`
	ToolUseID    string `json:"toolUseId"`
	Description  string `json:"description"`
	CommandLabel string `json:"commandLabel"`
}

// Qwen's kinds of background work. A queue notice states that Qwen's own
// notification queue overflowed; it concerns no task.
const (
	qwenBackgroundAgent    = "agent"
	qwenBackgroundShell    = "shell"
	qwenBackgroundMonitor  = "monitor"
	qwenBackgroundWorkflow = "workflow"
	qwenBackgroundQueue    = "queue"
)

// rowKey returns the key of the registry row that a notice concerns: the spawn
// of a background subagent, a background workflow run, or the id of a
// background command. Every other kind has no row, and returns "".
func (t qwenBackgroundTask) rowKey(sessionID string) string {
	switch {
	case t.Kind == qwenBackgroundAgent && t.ToolUseID != "":
		return qwenAgentRowKey(sessionID, t.ToolUseID)
	case t.Kind == qwenBackgroundShell && t.TaskID != "":
		return qwenShellRowPrefix + t.TaskID
	case t.Kind == qwenBackgroundWorkflow && t.ToolUseID != "":
		return qwenWorkflowRowKey(sessionID, t.ToolUseID)
	default:
		return ""
	}
}

// finishBackgroundTask ends the registry row of a background subagent,
// workflow, or command that Qwen reported as over. It reports whether such a
// row exists: only then does the row state the end, so that the notice needs no
// line of its own. A registry that cannot be read counts as no row, so the
// notice is shown rather than lost.
func (a *Agent) finishBackgroundTask(task qwenBackgroundTask) bool {
	if task.Kind == qwenBackgroundAgent && task.TaskID != "" && task.ToolUseID != "" {
		a.stateMu.Lock()
		if a.children.backgroundTasks == nil {
			a.children.backgroundTasks = make(map[string]string)
		}
		a.children.backgroundTasks[task.TaskID] = task.ToolUseID
		a.stateMu.Unlock()
	}
	rowKey := task.rowKey(a.CurrentSessionID())
	if rowKey == "" {
		return false
	}
	status := qwenSubagentStatus(task.Status, bgtask.StatusSucceeded)
	if _, _, found, err := a.Sink().LookupBackgroundTask(rowKey); err != nil || !found {
		if err != nil {
			slog.Warn("qwen background task row lookup failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
			return false
		}
		// Buffer early shell completions that arrive before run_shell_command
		// emits its final launch update, while still returning false so an
		// untracked shell notice reaches the transcript.
		if task.Kind == qwenBackgroundShell && task.TaskID != "" {
			a.stateMu.Lock()
			if a.children.earlyShellCompletions == nil {
				a.children.earlyShellCompletions = make(map[string]bgtask.Status)
			}
			a.children.earlyShellCompletions[rowKey] = status
			a.stateMu.Unlock()
		}
		return false
	}
	if task.Kind == qwenBackgroundAgent && task.ToolUseID != "" {
		a.stateMu.Lock()
		if a.children.closedBgAgents == nil {
			a.children.closedBgAgents = make(map[string]bool)
		}
		a.children.closedBgAgents[task.ToolUseID] = true
		a.stateMu.Unlock()
	}
	status = a.cleanupBackgroundTask(task.Kind, task.TaskID, task.ToolUseID, rowKey, status)
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   rowKey,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	})
	return true
}

func (a *Agent) openBackgroundAgentLocked(toolCallID string) {
	if a.children.agentRunSeq == nil {
		a.children.agentRunSeq = make(map[string]uint64)
	}
	if a.children.openBgAgents == nil {
		a.children.openBgAgents = make(map[string]uint64)
	}
	delete(a.children.closedBgAgents, toolCallID)
	a.children.agentRunSeq[toolCallID]++
	a.children.openBgAgents[toolCallID] = a.children.agentRunSeq[toolCallID]
}

// cleanupBackgroundTask tears down provider-side tracking for one settled
// background agent, workflow, or shell and returns its final registry status.
func (a *Agent) cleanupBackgroundTask(kind, taskID, toolCallID, rowKey string, status bgtask.Status) bgtask.Status {
	switch kind {
	case qwenBackgroundAgent:
		a.stateMu.Lock()
		delete(a.children.openBgAgents, toolCallID)
		a.stateMu.Unlock()
		if toolCallID != "" {
			// The child's transcript holds its last records by now: Qwen writes each
			// record before it reports the child as over. Read them before the row
			// closes, so the child's tab ends with the child's last words.
			a.stopBackgroundTranscript(toolCallID, true)
			status = a.closingStatus(toolCallID, status)
		}
		a.FinishChildTurn(rowKey)
	case qwenBackgroundWorkflow:
		a.stateMu.Lock()
		delete(a.children.openBgWorkflows, toolCallID)
		a.stateMu.Unlock()
		if toolCallID != "" {
			a.stopWorkflowPoller(toolCallID, status)
		}
	case qwenBackgroundShell:
		a.stateMu.Lock()
		delete(a.children.openBgShells, taskID)
		a.stateMu.Unlock()
	}
	return status
}

// closingStatus takes the stop recorded for the native tool call and returns
// the status that its registry row closes with.
//
// A failure after a stop that Qwen carried out is the stop. Qwen's task cancel
// aborts the subagent's model request, and Qwen 0.24 reports the aborted
// subagent as failed. A subagent that completed before the stop reached it
// keeps its completion.
// closingStatus reports the registry status a subagent's row lands on, given
// what Qwen said and whether this process stopped it.
//
// A stop this process asked for (InterruptChild) is a USER interrupt, not a
// plain stop: the row closes as StatusInterrupted and the divider reads
// "Subagent interrupted". A subagent that completed before the stop reached it
// keeps its completion. A Qwen abort reports the subagent as failed, so a stop
// upgrades that too; the record goes before the cancel (see stopChild) because
// Qwen can report the abort before it answers the cancel.
func (a *Agent) closingStatus(toolCallID string, status bgtask.Status) bgtask.Status {
	a.stateMu.Lock()
	stopped := a.children.stopped[toolCallID]
	delete(a.children.stopped, toolCallID)
	a.stateMu.Unlock()
	if !stopped {
		return status
	}
	if status == bgtask.StatusSucceeded {
		return status
	}
	// A failed or a plain-stopped verdict after OUR stop is the interrupt we
	// asked for. The frontend draws "Subagent interrupted" for StatusInterrupted
	// and "Subagent stopped" for StatusStopped.
	return bgtask.StatusInterrupted
}

// recordStop records or forgets the stop of the subagent that the spawn
// toolCallID started.
func (a *Agent) recordStop(toolCallID string, stopped bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !stopped {
		delete(a.children.stopped, toolCallID)
		return
	}
	if a.children.stopped == nil {
		a.children.stopped = make(map[string]bool)
	}
	a.children.stopped[toolCallID] = true
}

// Qwen's methods for the background work of one session. Each takes the id of
// the session, and answers for that session alone.
const (
	// qwenTaskListMethod lists the subagents, the background commands and the
	// monitors of a session. It states the tool call that spawned each
	// subagent, foreground or background.
	qwenTaskListMethod = "qwen/status/session/tasks"
	// qwenTaskCancelMethod stops one task of that list, by its id and kind.
	qwenTaskCancelMethod = "qwen/control/session/task/cancel"
)

// qwenTask is the part of one entry of Qwen's task list that LeapMux reads.
type qwenTask struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	ToolUseID string `json:"toolUseId"`
	Status    string `json:"status"`
}

// runs reports whether the task still runs. Qwen can pause a subagent, and its
// cancel abandons a paused one.
func (t qwenTask) runs() bool {
	return t.Status == "running" || t.Status == "paused"
}

// cancellable reports whether Qwen's task cancel stops a task of this kind.
// Qwen's task cancel accepts only agent, shell, and monitor tasks.
func (t qwenTask) cancellable() bool {
	switch t.Kind {
	case qwenBackgroundAgent, qwenBackgroundShell, qwenBackgroundMonitor:
		return t.ID != ""
	default:
		return false
	}
}

// qwenSessionParams are the params of a task request for sessionID.
func qwenSessionParams(sessionID string) json.RawMessage {
	params, _ := json.Marshal(map[string]string{"sessionId": sessionID})
	return params
}

// qwenTaskCancelParams are the params that stop one task of sessionID.
func qwenTaskCancelParams(sessionID string, task qwenTask) json.RawMessage {
	params, _ := json.Marshal(map[string]string{"sessionId": sessionID, "taskId": task.ID, "taskKind": task.Kind})
	return params
}

// decodeQwenTasks reads the tasks of a task-list answer.
func decodeQwenTasks(response json.RawMessage) ([]qwenTask, error) {
	var list struct {
		Tasks []qwenTask `json:"tasks"`
	}
	if err := json.Unmarshal(response, &list); err != nil {
		return nil, fmt.Errorf("read the Qwen task list: %w", err)
	}
	return list.Tasks, nil
}

var _ agent.ChildInterrupter = (*Agent)(nil)

// InterruptChild stops the subagent identified by its session-qualified
// registry key. The native task list still identifies its spawn by raw tool ID.
//
// Qwen stops a subagent, in the foreground or the background, through the task
// cancel of its session, by Qwen's own task id. No frame of a foreground spawn
// states that id, so this reads it from Qwen's task list, by the tool call of
// the spawn. A subagent that no longer runs needs no stop.
func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	if childKey == "" {
		return agent.ErrChildRouteNotReady
	}
	return a.WithSessionID(func(sessionID string) error {
		response, err := a.SendRequest(qwenTaskListMethod, qwenSessionParams(sessionID), a.APITimeout())
		if err != nil {
			return fmt.Errorf("list the Qwen tasks: %w", err)
		}
		tasks, err := decodeQwenTasks(response)
		if err != nil {
			return err
		}
		listed := false
		for _, task := range tasks {
			if task.Kind != qwenBackgroundAgent || qwenAgentRowKey(sessionID, task.ToolUseID) != childKey {
				continue
			}
			listed = true
			if task.runs() {
				return a.stopChild(sessionID, task.ToolUseID, task)
			}
		}
		if !listed {
			return fmt.Errorf("the Qwen task list holds no subagent for registry key %s", childKey)
		}
		return nil
	})
}

// stopChild cancels the native task of a spawning tool call and records the
// stop for the row's close (closingStatus).
//
// The record comes BEFORE the cancel, because Qwen can report the aborted
// subagent before it answers the cancel. A cancel that failed, or that found
// the subagent over, stopped nothing, so the record goes again and Qwen's own
// verdict stands. A row that closed between the two keeps the stop: the user
// asked for it at the moment the subagent ended.
func (a *Agent) stopChild(sessionID, toolCallID string, task qwenTask) error {
	a.recordStop(toolCallID, true)
	cancelled, err := a.cancelTask(sessionID, task)
	if err != nil || !cancelled {
		a.recordStop(toolCallID, false)
	}
	return err
}

// cancelTask stops one task of sessionID, waits for Qwen's answer, and reports
// whether the cancel stopped the task. A task that ended before the cancel
// arrived needs no stop, so a refusal of Qwen's is no failure.
func (a *Agent) cancelTask(sessionID string, task qwenTask) (bool, error) {
	response, err := a.SendRequest(qwenTaskCancelMethod, qwenTaskCancelParams(sessionID, task), a.APITimeout())
	if err != nil {
		return false, fmt.Errorf("stop the Qwen subagent: %w", err)
	}
	var result struct {
		Cancelled bool   `json:"cancelled"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		slog.Warn("qwen task cancel answer unreadable", "agent_id", a.AgentID(), "task_id", task.ID, "error", err)
		return false, nil
	}
	if !result.Cancelled {
		slog.Debug("qwen task was not running", "agent_id", a.AgentID(), "task_id", task.ID, "reason", result.Reason)
	}
	return result.Cancelled, nil
}

// retireSession stops the background work that the session a context clear
// replaced left running (Hooks.RetireSession). Qwen's session/cancel ends the
// turn with its goal and notification rounds, and leaves the background
// subagents, commands and monitors running, and Qwen offers no session/close.
// So this lists the tasks of that session and cancels each one that runs.
//
// Every request goes out detached, because the clear must not wait on the
// agent. A failure is logged: the session is already out of LeapMux's view.
func (a *Agent) retireSession(sessionID string) {
	err := a.SendDetachedRequest(qwenTaskListMethod, qwenSessionParams(sessionID), providerkit.DetachedRequestOptions{}, func(response json.RawMessage, err error) {
		if err == nil {
			var tasks []qwenTask
			if tasks, err = decodeQwenTasks(response); err == nil {
				a.cancelTasksDetached(sessionID, tasks)
				return
			}
		}
		slog.Warn("qwen list the tasks of a retired session", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	})
	if err != nil {
		slog.Warn("qwen list the tasks of a retired session", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}

// cancelTasksDetached stops each task of sessionID that runs, and waits for no
// answer.
func (a *Agent) cancelTasksDetached(sessionID string, tasks []qwenTask) {
	for _, task := range tasks {
		if !task.runs() || !task.cancellable() {
			continue
		}
		logFailure := func(_ json.RawMessage, err error) {
			if err != nil {
				slog.Warn("qwen cancel a task of a retired session", "agent_id", a.AgentID(), "session_id", sessionID, "task_id", task.ID, "error", err)
			}
		}
		if err := a.SendDetachedRequest(qwenTaskCancelMethod, qwenTaskCancelParams(sessionID, task), providerkit.DetachedRequestOptions{}, logFailure); err != nil {
			logFailure(nil, err)
		}
	}
}

// stopBackgroundTranscript stops the reader of one background child and saves
// its file path and byte offset for send_message resumption. drain reads what
// the file holds before the reader stops.
func (a *Agent) stopBackgroundTranscript(toolCallID string, drain bool) {
	a.stateMu.Lock()
	tail := a.children.tails[toolCallID]
	delete(a.children.tails, toolCallID)
	a.stateMu.Unlock()
	if tail != nil {
		tail.stop(drain)
		offset := tail.currentOffset()
		a.stateMu.Lock()
		if a.children.transcriptStates == nil {
			a.children.transcriptStates = make(map[string]transcriptResumeState)
		}
		a.children.transcriptStates[toolCallID] = transcriptResumeState{
			path:   tail.path,
			offset: offset,
		}
		a.stateMu.Unlock()
	}
}

// restartBackgroundTranscript restarts a stopped background subagent tail from
// its saved byte offset when send_message wakes the subagent.
func (a *Agent) restartBackgroundTranscript(toolCallID, rowKey string) {
	a.stateMu.Lock()
	if _, running := a.children.tails[toolCallID]; running {
		a.stateMu.Unlock()
		return
	}
	resume, ok := a.children.transcriptStates[toolCallID]
	if !ok || resume.path == "" {
		a.stateMu.Unlock()
		return
	}
	if a.children.tails == nil {
		a.children.tails = make(map[string]*transcriptTail)
	}
	tail := newTranscriptTailFrom(a, rowKey, resume.path, resume.offset)
	a.children.tails[toolCallID] = tail
	clock := a.clock
	a.stateMu.Unlock()
	tail.start(clock)
}

// qwenBackgroundTaskID reads the native task_id from a background spawn's text
// or rawOutput JSON.
func qwenBackgroundTaskID(resultText string, rawOutput json.RawMessage) string {
	if match := qwenBackgroundTaskIDPattern.FindStringSubmatch(resultText); match != nil {
		return strings.TrimSpace(match[1])
	}
	var fields struct {
		TaskID  string `json:"taskId"`
		TaskID2 string `json:"task_id"`
	}
	if json.Unmarshal(rawOutput, &fields) == nil {
		if id := strings.TrimSpace(fields.TaskID); id != "" {
			return id
		}
		if id := strings.TrimSpace(fields.TaskID2); id != "" {
			return id
		}
	}
	return ""
}

// qwenSendMessageInput is the part of send_message arguments that LeapMux reads.
type qwenSendMessageInput struct {
	TaskID  string `json:"task_id"`
	TaskID2 string `json:"taskId"`
}

func (i qwenSendMessageInput) taskID() string {
	if id := strings.TrimSpace(i.TaskID); id != "" {
		return id
	}
	return strings.TrimSpace(i.TaskID2)
}

// sendMessageObservation revives the target background subagent row and
// restarts its transcript reader from the saved offset.
func (a *Agent) sendMessageObservation(rawInput json.RawMessage) *acp.SubagentObservation {
	var input qwenSendMessageInput
	if json.Unmarshal(rawInput, &input) != nil {
		return nil
	}
	taskID := input.taskID()
	if taskID == "" {
		return nil
	}
	a.stateMu.Lock()
	toolCallID := a.children.backgroundTasks[taskID]
	a.stateMu.Unlock()
	if toolCallID == "" {
		return nil
	}
	rowKey := qwenAgentRowKey(a.CurrentSessionID(), toolCallID)
	if rowKey == "" {
		return nil
	}
	a.restartBackgroundTranscript(toolCallID, rowKey)
	a.stateMu.Lock()
	a.openBackgroundAgentLocked(toolCallID)
	a.stateMu.Unlock()
	return &acp.SubagentObservation{
		RowKey:    rowKey,
		Status:    bgtask.StatusRunning,
		Activity:  "Running in the background",
		ReviveRow: true,
	}
}

// reconcileBackgroundTasks queries Qwen's task list off the reader goroutine
// after a notification queue overflow and closes finished or dropped tasks.
func (a *Agent) reconcileBackgroundTasks() {
	sessionID := a.CurrentSessionID()
	if sessionID == "" {
		return
	}
	a.stateMu.Lock()
	trackedAgents := maps.Clone(a.children.openBgAgents)
	trackedShells := maps.Clone(a.children.openBgShells)
	trackedWorkflows := maps.Clone(a.children.openBgWorkflows)
	a.stateMu.Unlock()

	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "includeWorkflows": true})
	if err != nil {
		return
	}
	err = a.SendDetachedRequest(qwenTaskListMethod, params, providerkit.DetachedRequestOptions{}, func(response json.RawMessage, reqErr error) {
		if reqErr != nil {
			slog.Warn("qwen reconcile background tasks after queue overflow", "agent_id", a.AgentID(), "session_id", sessionID, "error", reqErr)
			return
		}
		var list struct {
			Tasks *[]qwenTask `json:"tasks"`
		}
		if err := json.Unmarshal(response, &list); err != nil {
			slog.Warn("qwen decode task list for queue overflow reconciliation", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
			return
		}
		if list.Tasks == nil || !a.IsCurrentSession(sessionID) {
			return
		}
		a.reconcileTaskList(sessionID, *list.Tasks, trackedAgents, trackedShells, trackedWorkflows)
	})
	if err != nil {
		slog.Warn("qwen reconcile background tasks after queue overflow", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}

func (a *Agent) reconcileTaskList(sessionID string, tasks []qwenTask, trackedAgents map[string]uint64, trackedShells, trackedWorkflows map[string]bool) {
	runningRows := make(map[string]bool)
	closedRows := make(map[string]bool)

	for _, task := range tasks {
		toolUseID := task.ToolUseID
		if task.Kind == qwenBackgroundAgent && toolUseID == "" && task.ID != "" {
			a.stateMu.Lock()
			toolUseID = a.children.backgroundTasks[task.ID]
			a.stateMu.Unlock()
		}
		rowKey := qwenBackgroundTask{Kind: task.Kind, TaskID: task.ID, ToolUseID: toolUseID}.rowKey(sessionID)
		if rowKey == "" {
			continue
		}
		if task.runs() {
			runningRows[rowKey] = true
			continue
		}
		status := qwenSubagentStatus(task.Status, bgtask.StatusEndedWithUnknownOutcome)
		a.closeReconciledTask(sessionID, task.Kind, task.ID, toolUseID, rowKey, status, trackedAgents[toolUseID])
		closedRows[rowKey] = true
	}

	reconcileMissing := func(kind, taskID, toolCallID, rowKey string, expectedRunSeq uint64) {
		if rowKey == "" || runningRows[rowKey] || closedRows[rowKey] {
			return
		}
		a.closeReconciledTask(sessionID, kind, taskID, toolCallID, rowKey, bgtask.StatusEndedWithUnknownOutcome, expectedRunSeq)
	}
	for toolCallID, runSeq := range trackedAgents {
		reconcileMissing(qwenBackgroundAgent, "", toolCallID, qwenAgentRowKey(sessionID, toolCallID), runSeq)
	}
	for shellID := range trackedShells {
		reconcileMissing(qwenBackgroundShell, shellID, "", qwenShellRowPrefix+shellID, 0)
	}
	for toolCallID := range trackedWorkflows {
		reconcileMissing(qwenBackgroundWorkflow, "", toolCallID, qwenWorkflowRowKey(sessionID, toolCallID), 0)
	}
}

func (a *Agent) closeReconciledTask(sessionID, kind, taskID, toolCallID, rowKey string, status bgtask.Status, expectedRunSeq uint64) {
	if !a.IsCurrentSession(sessionID) {
		return
	}
	_, currentStatus, found, err := a.Sink().LookupBackgroundTask(rowKey)
	if err != nil || !found || currentStatus.IsFinished() {
		return
	}
	if kind == qwenBackgroundAgent {
		a.stateMu.Lock()
		currentSeq, stillOpen := a.children.openBgAgents[toolCallID]
		if !stillOpen || currentSeq != expectedRunSeq {
			a.stateMu.Unlock()
			return
		}
		delete(a.children.openBgAgents, toolCallID)
		a.stateMu.Unlock()
	}
	status = a.cleanupBackgroundTask(kind, taskID, toolCallID, rowKey, status)
	a.ApplySubagentObservationForSessionOffReader(sessionID, &acp.SubagentObservation{
		RowKey:   rowKey,
		Status:   status,
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	})
}

// stopAllBackgroundTranscripts stops every reader, when the session goes.
func (a *Agent) stopAllBackgroundTranscripts() {
	a.stopAllWorkflowPollers()
	a.stopAllBackgroundTails()
}

func (a *Agent) stopAllBackgroundTails() {
	a.stateMu.Lock()
	tails := a.children.tails
	a.children.tails = nil
	a.stateMu.Unlock()
	for _, tail := range tails {
		tail.stop(false)
	}
}

// qwenShellInput is the part of a shell command's arguments that LeapMux reads.
type qwenShellInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// backgroundShellObservation opens the row of a shell command that Qwen moved
// to the background. The shell result states the command's id, which the
// background-task notification later repeats.
func backgroundShellObservation(tcu acp.ToolCallUpdateEnvelope, rawInput json.RawMessage) *acp.SubagentObservation {
	match := qwenBackgroundShellID.FindStringSubmatch(acp.ToolCallText(tcu.Content))
	if match == nil {
		return nil
	}
	var input qwenShellInput
	_ = json.Unmarshal(rawInput, &input)
	title, isCommand := strings.TrimSpace(input.Description), false
	if title == "" {
		title, isCommand = input.Command, true
	}
	if strings.TrimSpace(title) == "" {
		title, isCommand = "Background shell", false
	}
	return &acp.SubagentObservation{
		RowKey:         qwenShellRowPrefix + match[1],
		Kind:           bgtask.KindShell,
		Title:          title,
		TitleIsCommand: isCommand,
		Status:         bgtask.StatusRunning,
	}
}

// observeBackgroundShell opens a background shell row, applying any buffered
// early completion that arrived before run_shell_command finished.
func (a *Agent) observeBackgroundShell(tcu acp.ToolCallUpdateEnvelope, rawInput json.RawMessage) *acp.SubagentObservation {
	obs := backgroundShellObservation(tcu, rawInput)
	if obs == nil {
		return nil
	}
	shellID := strings.TrimPrefix(obs.RowKey, qwenShellRowPrefix)
	a.stateMu.Lock()
	earlyStatus, early := a.children.earlyShellCompletions[obs.RowKey]
	if early {
		delete(a.children.earlyShellCompletions, obs.RowKey)
	} else {
		if a.children.openBgShells == nil {
			a.children.openBgShells = make(map[string]bool)
		}
		a.children.openBgShells[shellID] = true
	}
	a.stateMu.Unlock()
	if early {
		obs.Status = earlyStatus
		obs.CloseRow = true
	}
	return obs
}

// qwenWorkflowInput is the part of a workflow's arguments that LeapMux reads.
type qwenWorkflowInput struct {
	Name            string `json:"name"`
	ScriptPath      string `json:"scriptPath"`
	RunInBackground bool   `json:"run_in_background"`
}

// workflowObservation opens the row of a workflow run. The run is one tool
// call, and its agents report to it alone, so the row is a group of one.
func workflowObservation(sessionID, toolCallID string, rawInput json.RawMessage) *acp.SubagentObservation {
	var input qwenWorkflowInput
	_ = json.Unmarshal(rawInput, &input)
	title := "Workflow"
	for _, candidate := range []string{input.Name, input.ScriptPath} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			title = candidate
			break
		}
	}
	rowKey := qwenWorkflowRowKey(sessionID, toolCallID)
	return &acp.SubagentObservation{
		RegistryOnly: true,
		RowKey:       rowKey,
		Kind:         bgtask.KindWorkflow,
		Title:        title,
		Status:       bgtask.StatusRunning,
		GroupKey:     rowKey,
		GroupLabel:   title,
	}
}

// observeControlRequest reads each permission request that the base
// publishes, right before the publication (Hooks.ControlRequestObserver). It
// records the arguments that the request states, because Qwen's progress
// frames can omit them and the closing frame always does. It stores the plan
// of a plan approval.
func (a *Agent) observeControlRequest(_ string, line *providerkit.ParsedLine) {
	var params struct {
		ToolCall struct {
			ToolCallID string          `json:"toolCallId"`
			RawInput   json.RawMessage `json:"rawInput"`
			Meta       json.RawMessage `json:"_meta"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(line.Params, &params) != nil {
		return
	}
	name := qwenToolName(params.ToolCall.Meta)
	if name == contracts.QwenToolExitPlanMode {
		a.storePlan(params.ToolCall.RawInput)
	}
	a.stateMu.Lock()
	a.tools.remember(params.ToolCall.ToolCallID, name, params.ToolCall.RawInput)
	a.stateMu.Unlock()
}
