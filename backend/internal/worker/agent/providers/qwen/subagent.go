package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// qwenBackgroundOutputFile reads the transcript path that a background spawn's
// result states ("output_file: /…/agent-<id>.jsonl (for review …)"). The path
// can hold a space, as a home directory often does, so it runs to the first
// `.jsonl` that a space or the end of the line follows.
var qwenBackgroundOutputFile = regexp.MustCompile(`(?m)^output_file:[ \t]*(.+?\.jsonl)(?:[ \t]|$)`)

// qwenBackgroundShellID reads the id of a shell command that runs in the
// background, as a shell result states it on start or on promotion.
var qwenBackgroundShellID = regexp.MustCompile(`(?m)(?:^Background shell started\.\s*\nid:\s*|promoted to background as\s+)(bg_[0-9A-Za-z]+)`)

// qwenShellRowPrefix keys the registry row of one background shell command.
const qwenShellRowPrefix = "shell:"

// childState links Qwen's subagents and background commands to LeapMux's
// registry rows. Guarded by Agent.stateMu.
type childState struct {
	// spawns holds each subagent spawn that did not end, by tool-call id.
	spawns map[string]bool
	// tails holds the transcript reader of each background subagent, by
	// tool-call id.
	tails map[string]*transcriptTail
	// stopped holds, by tool-call id, each subagent that the Interrupt of its
	// tab stopped, until Qwen reports the end of its row. Qwen's task cancel
	// aborts the subagent's model request, and Qwen then reports the subagent
	// as failed, so the row reads its final status through this set
	// (closingStatus).
	stopped map[string]bool
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
func spawnObservation(toolCallID string, rawInput json.RawMessage, spawns bool) *acp.SubagentObservation {
	var input qwenSpawnInput
	_ = json.Unmarshal(rawInput, &input)
	title := strings.TrimSpace(input.Description)
	if title == "" {
		title = qwenAgentSpawnTitle
	}
	return &acp.SubagentObservation{
		RowKey:        toolCallID,
		ChildAgentKey: toolCallID,
		Title:         title,
		Prompt:        input.Prompt,
		Status:        bgtask.StatusRunning,
		Spawns:        spawns,
	}
}

// subagentFromToolCall claims a spawn and a workflow run at their first frame,
// and records every call's tool name for its later frames.
func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	name := qwenToolName(tc.Meta)
	a.stateMu.Lock()
	a.tools.remember(tc.ToolCallID, name, tc.RawInput)
	if name == contracts.QwenToolAgent {
		if a.children.spawns == nil {
			a.children.spawns = make(map[string]bool)
		}
		a.children.spawns[tc.ToolCallID] = true
	}
	a.stateMu.Unlock()
	switch name {
	case contracts.QwenToolAgent:
		return spawnObservation(tc.ToolCallID, tc.RawInput, true)
	case contracts.QwenToolWorkflow:
		return workflowObservation(tc.ToolCallID, tc.RawInput)
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
			return nil
		}
		return &acp.SubagentObservation{RowKey: tcu.ToolCallID, Status: acp.FinalStatus(tcu.Status), CloseRow: true, Mode: acp.ModeCloseOnly}
	case contracts.QwenToolRunShellCommand:
		if final && tcu.Status == "completed" {
			return backgroundShellObservation(tcu, known.rawInput)
		}
		return nil
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
		return bgtask.StatusCompleted
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
		return spawnObservation(tcu.ToolCallID, rawInput, false)
	}
	a.stateMu.Lock()
	delete(a.children.spawns, tcu.ToolCallID)
	a.stateMu.Unlock()

	var output qwenTaskExecutionOutput
	_ = json.Unmarshal(tcu.RawOutput, &output)
	if tcu.Status == "completed" && output.Type == qwenTaskExecution && output.ExecutionMode == qwenExecutionBackground {
		a.startBackgroundTranscript(tcu.ToolCallID, acp.ToolCallText(tcu.Content))
		observation := spawnObservation(tcu.ToolCallID, rawInput, false)
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
		RowKey:   tcu.ToolCallID,
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
	tail := newTranscriptTail(a, toolCallID, path)
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
// of a background subagent, or the id of a background command. Every other
// kind has no row, and returns "".
func (t qwenBackgroundTask) rowKey() string {
	switch {
	case t.Kind == qwenBackgroundAgent && t.ToolUseID != "":
		return t.ToolUseID
	case t.Kind == qwenBackgroundShell && t.TaskID != "":
		return qwenShellRowPrefix + t.TaskID
	default:
		return ""
	}
}

// finishBackgroundTask ends the registry row of a background subagent or
// command that Qwen reported as over. It reports whether such a row exists:
// only then does the row state the end, so that the notice needs no line of
// its own. A registry that cannot be read counts as no row, so the notice is
// shown rather than lost.
func (a *Agent) finishBackgroundTask(task qwenBackgroundTask) bool {
	rowKey := task.rowKey()
	if rowKey == "" {
		return false
	}
	if _, _, found, err := a.Sink().LookupBackgroundTask(rowKey); err != nil || !found {
		if err != nil {
			slog.Warn("qwen background task row lookup failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		}
		return false
	}
	if task.Kind == qwenBackgroundAgent {
		// The child's transcript holds its last records by now: Qwen writes each
		// record before it reports the child as over. Read them before the row
		// closes, so the child's tab ends with the child's last words.
		a.stopBackgroundTranscript(task.ToolUseID, true)
		a.FinishChildTurn(task.ToolUseID)
	}
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:   rowKey,
		Status:   a.closingStatus(rowKey, qwenSubagentStatus(task.Status, bgtask.StatusCompleted)),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	})
	return true
}

// closingStatus returns the status that the row of rowKey closes with, when
// Qwen reports status for it, and takes the record of a stop for that row.
//
// A failure after a stop that Qwen carried out is the stop. Qwen's task cancel
// aborts the subagent's model request, and Qwen 0.24 reports the aborted
// subagent as failed. A subagent that completed before the stop reached it
// keeps its completion.
func (a *Agent) closingStatus(rowKey string, status bgtask.Status) bgtask.Status {
	a.stateMu.Lock()
	stopped := a.children.stopped[rowKey]
	delete(a.children.stopped, rowKey)
	a.stateMu.Unlock()
	if stopped && status == bgtask.StatusFailed {
		return bgtask.StatusStopped
	}
	return status
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
// The list holds no workflow run unless it is asked for one, and a workflow run
// in an ACP session runs in the foreground, where its turn owns it.
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

// InterruptChild stops one subagent. childKey is the registry row key: the id
// of the tool call that spawned the subagent.
//
// Qwen stops a subagent, in the foreground or the background, through the task
// cancel of its session, by Qwen's own task id. No frame of a foreground spawn
// states that id, so this reads it from Qwen's task list, by the tool call of
// the spawn. A subagent that no longer runs needs no stop.
func (a *Agent) InterruptChild(childKey string) error {
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
			if task.Kind != qwenBackgroundAgent || task.ToolUseID != childKey {
				continue
			}
			listed = true
			if task.runs() {
				return a.stopChild(sessionID, childKey, task)
			}
		}
		if !listed {
			return fmt.Errorf("the Qwen task list holds no subagent of the tool call %s", childKey)
		}
		return nil
	})
}

// stopChild cancels the task of the subagent that the spawn childKey started,
// and records the stop for the row's close (closingStatus).
//
// The record comes BEFORE the cancel, because Qwen can report the aborted
// subagent before it answers the cancel. A cancel that failed, or that found
// the subagent over, stopped nothing, so the record goes again and Qwen's own
// verdict stands. A row that closed between the two keeps the stop: the user
// asked for it at the moment the subagent ended.
func (a *Agent) stopChild(sessionID, childKey string, task qwenTask) error {
	a.recordStop(childKey, true)
	cancelled, err := a.cancelTask(sessionID, task)
	if err != nil || !cancelled {
		a.recordStop(childKey, false)
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
	err := a.SendDetachedRequest(qwenTaskListMethod, qwenSessionParams(sessionID), func(response json.RawMessage, err error) {
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
		if err := a.SendDetachedRequest(qwenTaskCancelMethod, qwenTaskCancelParams(sessionID, task), logFailure); err != nil {
			logFailure(nil, err)
		}
	}
}

// stopBackgroundTranscript stops the reader of one background child. drain
// reads what the file holds before the reader stops.
func (a *Agent) stopBackgroundTranscript(toolCallID string, drain bool) {
	a.stateMu.Lock()
	tail := a.children.tails[toolCallID]
	delete(a.children.tails, toolCallID)
	a.stateMu.Unlock()
	if tail != nil {
		tail.stop(drain)
	}
}

// stopAllBackgroundTranscripts stops every reader, when the session goes.
func (a *Agent) stopAllBackgroundTranscripts() {
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

// qwenWorkflowInput is the part of a workflow's arguments that LeapMux reads.
type qwenWorkflowInput struct {
	Name       string `json:"name"`
	ScriptPath string `json:"scriptPath"`
}

// workflowObservation opens the row of a workflow run. The run is one tool
// call, and its agents report to it alone, so the row is a group of one.
func workflowObservation(toolCallID string, rawInput json.RawMessage) *acp.SubagentObservation {
	var input qwenWorkflowInput
	_ = json.Unmarshal(rawInput, &input)
	title := "Workflow"
	for _, candidate := range []string{input.Name, input.ScriptPath} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			title = candidate
			break
		}
	}
	return &acp.SubagentObservation{
		RowKey:     toolCallID,
		Kind:       bgtask.KindWorkflow,
		Title:      title,
		Status:     bgtask.StatusRunning,
		GroupKey:   toolCallID,
		GroupLabel: title,
	}
}

// observeControlRequest reads each permission request that the base
// publishes, right before the publication (Hooks.ControlRequestObserver). It
// records the arguments that the request states, because Qwen's progress
// frames can omit them and the closing frame always does. It stores the plan
// of a plan approval.
func (a *Agent) observeControlRequest(line *providerkit.ParsedLine) {
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
