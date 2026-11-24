package letta

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"log/slog"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// lettaChild keeps the native identity and the progress of one child row.
// handleFrame serializes all reads and writes through dispatchMu.
type lettaChild struct {
	agentID       string
	toolCallID    string
	taskID        string
	outputFile    string
	prompt        string
	title         string
	lastText      string
	prompted      bool
	registered    bool
	closed        bool
	reportWritten bool
}

type lettaNativeChild struct {
	ID                   string `json:"subagent_id"`
	Type                 string `json:"subagent_type"`
	Description          string `json:"description"`
	Prompt               string `json:"prompt"`
	Status               string `json:"status"`
	ToolCallID           string `json:"tool_call_id"`
	ParentAgentID        string `json:"parent_agent_id"`
	ParentConversationID string `json:"parent_conversation_id"`
}

// lettaBackgroundProcessRowPrefix namespaces device_status background shell rows
// so their keys never collide with native subagent IDs.
const lettaBackgroundProcessRowPrefix = "letta-bg:"

// queueRegistryWrite enqueues a background-task mutation on the root output
// queue so transient storage errors retry in FIFO order. Callbacks capture only
// immutable values and must never acquire dispatchMu.
func (a *Agent) queueRegistryWrite(label string, write func() error) error {
	if a.sink == nil {
		return nil
	}
	if queue := agent.OutputQueueFor(a.sink); queue != nil {
		queue.Append(agent.OutputWrite{Label: label, Run: write})
		return nil
	}
	return write()
}

func (a *Agent) queueRegistryUpsert(upsert bgtask.Upsert) error {
	sink := a.sink
	return a.queueRegistryWrite("upsert Letta background task", func() error {
		return sink.UpsertBackgroundTask(upsert)
	})
}

func (a *Agent) queueRegistryRevive(rowKey string) error {
	sink := a.sink
	return a.queueRegistryWrite("revive Letta background task", func() error {
		return sink.ReviveBackgroundTask(rowKey)
	})
}

func (a *Agent) queueRegistryClose(rowKey string, status bgtask.Status) error {
	sink := a.sink
	return a.queueRegistryWrite("close Letta background task", func() error {
		return sink.CloseBackgroundTask(rowKey, status)
	})
}

// flushOutputQueue drains queued registry writes outside dispatchMu.
func (a *Agent) flushOutputQueue() {
	if a.sink == nil {
		return
	}
	queue := agent.OutputQueueFor(a.sink)
	if queue == nil {
		return
	}
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("letta: flush queued registry output failed", "agent_id", a.AgentID(), "error", err)
	}
}

// onSubagentState projects native child snapshots into Worker child rows.
func (a *Agent) onSubagentState(payload json.RawMessage, scope *runtimeScope) {
	var children []lettaNativeChild
	if err := json.Unmarshal(payload, &children); err != nil {
		slog.Warn("letta: child snapshot is unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	if children == nil {
		return
	}
	counts := make(map[string]int, len(children))
	for _, child := range children {
		counts[child.ID]++
	}
	for _, child := range children {
		if child.ID == "" || counts[child.ID] != 1 {
			continue
		}
		a.observeChild(child)
	}
	if !a.ownsRuntime(scope) {
		return
	}
	var evicted []string
	for id, child := range a.children {
		if child.registered && !child.closed && counts[id] == 0 {
			evicted = append(evicted, id)
		}
	}
	sort.Strings(evicted)
	for _, id := range evicted {
		a.closeChild(id, a.children[id], bgtask.StatusEndedWithUnknownOutcome)
	}
}

func (a *Agent) observeChild(native lettaNativeChild) {
	a.Mu.Lock()
	parentAgentID, parentConversationID := a.agentID, a.conversationID
	a.Mu.Unlock()
	if native.ParentAgentID == "" || native.ParentConversationID == "" ||
		native.ParentAgentID != parentAgentID || native.ParentConversationID != parentConversationID ||
		native.Prompt == "" {
		return
	}
	status, ok := lettaChildStatus(native.Status)
	if !ok {
		return
	}
	if a.children == nil {
		a.children = make(map[string]*lettaChild)
	}
	child := a.children[native.ID]
	if child == nil {
		title := strings.TrimSpace(native.Description)
		if title == "" {
			title = strings.TrimSpace(native.Type)
		}
		if title == "" {
			title = "Subagent"
		}
		spawnSpanID := "letta-child-" + native.ID
		if native.ToolCallID != "" {
			spawnSpanID = "letta-tool-" + native.ToolCallID
		}
		agentID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask,
			SpawnSpanID:  spawnSpanID, ProviderChildKey: native.ID, Title: title})
		if err != nil {
			slog.Warn("letta: ensure child failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
			return
		}
		child = &lettaChild{agentID: agentID, toolCallID: native.ToolCallID, prompt: native.Prompt, title: title}
		a.children[native.ID] = child
	} else if !child.closed && (child.prompt != native.Prompt || child.toolCallID != native.ToolCallID) {
		slog.Warn("letta: child snapshot changed identity", "agent_id", a.AgentID(), "subagent_id", native.ID)
		return
	}
	if child.closed {
		if status.IsFinished() {
			return
		}
		if child.prompt != native.Prompt && child.prompted {
			if err := a.sink.PersistChildUserMessage(child.agentID, native.Prompt); err != nil {
				slog.Warn("letta: persist revived child prompt failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
				return
			}
		}
		if err := a.queueRegistryRevive(native.ID); err != nil {
			slog.Warn("letta: revive child failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
			return
		}
		if child.toolCallID != native.ToolCallID {
			child.toolCallID = native.ToolCallID
			child.taskID = ""
			child.outputFile = ""
		}
		child.prompt = native.Prompt
		child.closed = false
		child.reportWritten = false
		child.lastText = ""
	}
	if !child.prompted {
		if err := a.sink.PersistChildPrompt(child.agentID, child.prompt); err != nil {
			slog.Warn("letta: persist child prompt failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
			return
		}
		child.prompted = true
	}
	rowStatus := status
	if status.IsFinished() {
		rowStatus = bgtask.StatusRunning
	}
	if err := a.queueRegistryUpsert(bgtask.Upsert{
		RowKey: native.ID, Kind: bgtask.KindSubagent, ChildAgentID: child.agentID,
		ParentAgentID: a.AgentID(), Title: child.title, Status: rowStatus,
	}); err != nil {
		slog.Warn("letta: register child failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
		return
	}
	child.registered = true
	if !status.IsFinished() {
		return
	}
	a.closeChild(native.ID, child, status)
}

func (a *Agent) closeChild(childID string, child *lettaChild, status bgtask.Status) {
	completion := lettaChildCompletion(status)
	childSink := a.sink.ChildSink(child.agentID)
	a.flushGenerationFor(childID, completion)
	a.finishOpenCompaction(childSink, childID, completion)
	a.flushHeldErrors(childSink, childID, "")
	a.retryForTarget(childID).Reset()
	if err := a.queueRegistryClose(childID, status); err != nil {
		slog.Warn("letta: finish child failed", "agent_id", a.AgentID(), "subagent_id", childID, "error", err)
		return
	}
	a.finishToolOutputScope(childID, childSink)
	child.closed = true
}

func lettaChildStatus(status string) (bgtask.Status, bool) {
	switch status {
	case "pending":
		return bgtask.StatusPending, true
	case "running":
		return bgtask.StatusRunning, true
	case "completed":
		return bgtask.StatusSucceeded, true
	case "error", "failed":
		return bgtask.StatusFailed, true
	case "cancelled":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusUnspecified, false
	}
}

func lettaChildCompletion(status bgtask.Status) agent.MessageCompletion {
	switch status {
	case bgtask.StatusFailed:
		return agent.MessageCompletionError
	case bgtask.StatusStopped, bgtask.StatusInterrupted:
		return agent.MessageCompletionInterrupted
	default:
		return agent.MessageCompletionComplete
	}
}

func (a *Agent) outputTarget(subagentID string) (agent.ProviderServices, bool) {
	if subagentID == "" {
		return a.sink, true
	}
	child := a.children[subagentID]
	if child == nil || !child.registered || child.closed {
		return nil, false
	}
	return a.sink.ChildSink(child.agentID), true
}

func lettaGenerationScope(subagentID string) string {
	if subagentID == "" {
		return "main"
	}
	return "child:" + subagentID
}

func lettaToolKey(subagentID, toolCallID string) string {
	return subagentID + "\x00" + toolCallID
}

// rememberChildTaskReceipt links a background task ID to one announced child.
func (a *Agent) rememberChildTaskReceipt(delta *lettaDelta) {
	if delta.ToolCallID == "" {
		return
	}
	var receipt string
	if json.Unmarshal(delta.ToolReturn, &receipt) != nil {
		return
	}
	const prefix = "Task running in background with task ID: "
	const outputPrefix = "Output file: "
	var taskID string
	var outputFile string
	for _, line := range strings.Split(receipt, "\n") {
		if value, ok := strings.CutPrefix(line, prefix); ok && safeLettaTaskID(value) {
			taskID = value
		}
		if value, ok := strings.CutPrefix(line, outputPrefix); ok {
			outputFile = value
		}
	}
	if taskID == "" {
		return
	}
	var match *lettaChild
	for _, child := range a.children {
		if child.toolCallID != delta.ToolCallID {
			continue
		}
		if match != nil {
			return
		}
		match = child
	}
	if match == nil || (match.taskID != "" && match.taskID != taskID) ||
		(match.outputFile != "" && match.outputFile != outputFile) {
		return
	}
	match.taskID = taskID
	match.outputFile = outputFile
}

func safeLettaTaskID(taskID string) bool {
	digits, ok := strings.CutPrefix(taskID, "task_")
	if !ok || digits == "" {
		return false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

type lettaTaskNotification struct {
	TaskID     string `xml:"task-id"`
	Status     string `xml:"status"`
	Result     string `xml:"result"`
	OutputFile string `xml:"-"`
}

// observeChildTaskNotifications closes a child only when the task ID and the
// native result header both link to that child's Agent call.
func (a *Agent) observeChildTaskNotifications(content string) {
	const openTag = "<task-notification>"
	const closeTag = "</task-notification>"
	for {
		start := strings.Index(content, openTag)
		if start < 0 {
			return
		}
		content = content[start:]
		end := strings.Index(content, closeTag)
		if end < 0 {
			return
		}
		block := content[:end+len(closeTag)]
		content = content[end+len(closeTag):]
		var notice lettaTaskNotification
		if xml.Unmarshal([]byte(block), &notice) != nil || !safeLettaTaskID(notice.TaskID) {
			continue
		}
		const outputPrefix = "Full transcript available at: "
		if line, ok := strings.CutPrefix(strings.TrimLeft(content, "\r\n"), outputPrefix); ok {
			notice.OutputFile, _, _ = strings.Cut(line, "\n")
			notice.OutputFile = strings.TrimSuffix(notice.OutputFile, "\r")
		}
		a.finishChildFromNotification(notice)
	}
}

func (a *Agent) finishChildFromNotification(notice lettaTaskNotification) {
	var childID string
	var child *lettaChild
	for id, candidate := range a.children {
		if candidate.taskID != notice.TaskID || !candidate.registered {
			continue
		}
		if child != nil {
			return
		}
		childID, child = id, candidate
	}
	if child == nil {
		return
	}
	header, report, ok := strings.Cut(notice.Result, "\n\n")
	if !ok || !lettaReportIdentifiesChild(header, childID, notice.Status) {
		return
	}
	status := bgtask.StatusFailed
	if notice.Status == "completed" {
		status = bgtask.StatusSucceeded
	} else if notice.Status != "failed" {
		return
	}
	if !child.reportWritten {
		if !child.closed {
			a.flushGenerationFor(childID, lettaChildCompletion(status))
			a.finishOpenCompaction(a.sink.ChildSink(child.agentID), childID, lettaChildCompletion(status))
			a.flushHeldErrors(a.sink.ChildSink(child.agentID), childID, "")
		}
		if child.outputFile != "" && (notice.OutputFile == "" || notice.OutputFile == child.outputFile) {
			root := a.taskLogRoot
			if root == "" {
				root, _ = lettaTaskLogRoot(nil)
			}
			full, readErr := readLettaTaskReport(root, a.taskLogDirect, child.outputFile, notice.TaskID, childID, notice.Status)
			if readErr == nil {
				report = full
			} else {
				slog.Warn("letta: full child report unavailable", "agent_id", a.AgentID(), "subagent_id", childID, "error", readErr)
			}
		}
		if strings.TrimSpace(report) != "" && report != child.lastText {
			raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, report, agent.MessageCompletionComplete)
			if err != nil {
				return
			}
			if err := a.sink.ChildSink(child.agentID).PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
				slog.Warn("letta: persist child report failed", "agent_id", a.AgentID(), "subagent_id", childID, "error", err)
				return
			}
			child.lastText = report
		}
		child.reportWritten = true
	}
	if !child.closed {
		if err := a.queueRegistryClose(childID, status); err != nil {
			slog.Warn("letta: finish child from task failed", "agent_id", a.AgentID(), "subagent_id", childID, "error", err)
			return
		}
		a.finishToolOutputScope(childID, a.sink.ChildSink(child.agentID))
		child.closed = true
		a.retryForTarget(childID).Reset()
	}
}

type lettaBackgroundProcess struct {
	ProcessID   string `json:"process_id"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Command     string `json:"command"`
	Description string `json:"description"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exit_code"`
	SubagentID  string `json:"subagent_id"`
}

func (p lettaBackgroundProcess) key() string {
	if id := strings.TrimSpace(p.ProcessID); id != "" {
		return id
	}
	return strings.TrimSpace(p.ID)
}

func lettaExitStatus(completion agent.MessageCompletion) bgtask.Status {
	switch completion {
	case agent.MessageCompletionError:
		return bgtask.StatusFailed
	case agent.MessageCompletionInterrupted:
		return bgtask.StatusStopped
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

// reconcileBackgroundProcesses diffs device_status.background_processes against
// tracked shell rows under dispatchMu.
func (a *Agent) reconcileBackgroundProcesses(processes []lettaBackgroundProcess) {
	snapshots := make([]providerkit.BackgroundProcessSnapshot, 0, len(processes))
	for _, proc := range processes {
		if proc.Kind == "agent_task" && strings.TrimSpace(proc.SubagentID) != "" {
			continue
		}
		id := proc.key()
		if id == "" {
			continue
		}
		title, isCommand := providerkit.BackgroundProcessTitle(proc.Description, "", proc.Command)
		snapshots = append(snapshots, providerkit.BackgroundProcessSnapshot{
			RowKey:         lettaBackgroundProcessRowPrefix + id,
			Title:          title,
			TitleIsCommand: isCommand,
			Status:         providerkit.BackgroundProcessStatus(proc.Status, proc.ExitCode),
		})
	}
	for _, op := range providerkit.ReconcileBackgroundProcesses(&a.backgroundProcesses, a.AgentID(), snapshots) {
		if op.Upsert != nil {
			if err := a.queueRegistryUpsert(*op.Upsert); err != nil {
				slog.Warn("letta: upsert background process failed", "agent_id", a.AgentID(), "row_key", op.Upsert.RowKey, "error", err)
			}
			continue
		}
		if err := a.queueRegistryClose(op.RowKey, op.CloseStatus); err != nil {
			slog.Warn("letta: close background process failed", "agent_id", a.AgentID(), "row_key", op.RowKey, "error", err)
			a.backgroundProcesses[op.RowKey] = struct{}{}
		}
	}
}

func (a *Agent) closeBackgroundProcesses(status bgtask.Status) {
	if len(a.backgroundProcesses) == 0 {
		return
	}
	rowKeys := make([]string, 0, len(a.backgroundProcesses))
	for rowKey := range a.backgroundProcesses {
		rowKeys = append(rowKeys, rowKey)
	}
	sort.Strings(rowKeys)
	for _, rowKey := range rowKeys {
		if err := a.queueRegistryClose(rowKey, status); err != nil {
			slog.Warn("letta: finish background process failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
			continue
		}
		delete(a.backgroundProcesses, rowKey)
	}
}

func lettaReportIdentifiesChild(header, childID, status string) bool {
	want := "success"
	if status == "failed" {
		want = "error"
	}
	var foundID, foundStatus, sawID, sawStatus bool
	for _, field := range strings.Fields(header) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "subagent_id":
			if sawID {
				return false
			}
			sawID = true
			foundID = value == childID
		case "subagent_status":
			if sawStatus {
				return false
			}
			sawStatus = true
			foundStatus = value == want
		}
	}
	return foundID && foundStatus
}
