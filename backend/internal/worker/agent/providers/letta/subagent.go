package letta

import (
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// onSubagentState projects native child snapshots into Worker child rows.
func (a *Agent) onSubagentState(line []byte, payload json.RawMessage) {
	var children []lettaNativeChild
	if err := json.Unmarshal(payload, &children); err != nil {
		slog.Warn("letta: child snapshot is unreadable", "agent_id", a.AgentID(), "error", err)
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
	a.persistNotification(line)
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
		agentID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnSpanID, ProviderChildKey: native.ID, Title: title})
		if err != nil {
			slog.Warn("letta: ensure child failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
			return
		}
		child = &lettaChild{agentID: agentID, toolCallID: native.ToolCallID, prompt: native.Prompt, title: title}
		a.children[native.ID] = child
	} else if child.prompt != native.Prompt || child.toolCallID != native.ToolCallID {
		slog.Warn("letta: child snapshot changed identity", "agent_id", a.AgentID(), "subagent_id", native.ID)
		return
	}
	if child.closed {
		return
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
	if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{
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
	a.flushGenerationFor(native.ID)
	if err := a.sink.CloseBackgroundTask(native.ID, status); err != nil {
		slog.Warn("letta: finish child failed", "agent_id", a.AgentID(), "subagent_id", native.ID, "error", err)
		return
	}
	a.finishToolOutputScope(native.ID, a.sink.ChildSink(child.agentID))
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
			a.flushGenerationFor(childID)
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
		if err := a.sink.CloseBackgroundTask(childID, status); err != nil {
			slog.Warn("letta: finish child from task failed", "agent_id", a.AgentID(), "subagent_id", childID, "error", err)
			return
		}
		a.finishToolOutputScope(childID, a.sink.ChildSink(child.agentID))
		child.closed = true
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
