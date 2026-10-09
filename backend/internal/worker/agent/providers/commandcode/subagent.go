package commandcode

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type childState struct {
	rowKey, childID, label string
	background             bool
	identified             bool
	finishedStatus         bgtask.Status
}

var nativeAgentID = regexp.MustCompile(`(?m)^agent_id: ([A-Za-z0-9_-]+)$`)

func childRowKey(sessionID, id string) string {
	return bgtask.NormalizeRowKey(sessionID + ":child:" + id)
}

func (a *Agent) observeChild(raw []byte, event nativeEvent) {
	if event.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	state := a.children[event.ToolCallID]
	opening, known := a.tools[event.ToolCallID]
	sessionID := a.sessionID
	a.Mu.Unlock()
	if event.Type == contracts.CommandCodeEventSubagentStart {
		if state != nil || !known || opening.name != contracts.CommandCodeToolAgent {
			return
		}
		var input struct {
			Prompt      string `json:"prompt"`
			Description string `json:"description"`
		}
		if json.Unmarshal(opening.input, &input) != nil {
			return
		}
		rowKey := childRowKey(sessionID, event.ToolCallID)
		id, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: event.ToolCallID, ProviderChildKey: rowKey, Title: input.Description})
		if err != nil {
			slog.Warn("open a Command Code child transcript", "error", err)
			return
		}
		if err := a.sink.PersistChildPrompt(id, input.Prompt); err != nil {
			slog.Warn("persist the Command Code child prompt", "error", err)
		}
		state = &childState{rowKey: rowKey, childID: id, label: input.Description, background: event.Background}
		a.Mu.Lock()
		if a.children == nil {
			a.children = make(map[string]*childState)
		}
		a.children[event.ToolCallID] = state
		a.Mu.Unlock()
		if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: id, Title: input.Description, Status: bgtask.StatusRunning}); err != nil {
			slog.Warn("publish a Command Code child task", "error", err)
		}
		return
	}
	if state == nil {
		return
	}
	if event.Type == contracts.CommandCodeEventSubagentProgress {
		if err := a.sink.PersistChildMessage(state.childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
			slog.Warn("persist Command Code child activity", "error", err)
		}
		return
	}
	if event.Type == contracts.CommandCodeEventSubagentStop && state.background {
		status := childStatus(event.Status)
		a.Mu.Lock()
		state.finishedStatus = status
		identified := state.identified
		a.Mu.Unlock()
		if identified {
			a.finishChild(event.ToolCallID, state, status)
		}
	}
}

func childStatus(status string) bgtask.Status {
	switch status {
	case "interrupted", "stopped", "cancelled":
		return bgtask.StatusStopped
	case "failed", "run_error":
		return bgtask.StatusFailed
	case "running":
		return bgtask.StatusRunning
	default:
		return bgtask.StatusSucceeded
	}
}

func (a *Agent) finishChildTool(event nativeEvent, opening openTool) {
	if opening.name == contracts.CommandCodeToolAgentOutput {
		var input struct {
			AgentID string `json:"agent_id"`
			Action  string `json:"action"`
		}
		if json.Unmarshal(opening.input, &input) != nil || input.AgentID == "" {
			return
		}
		text := nativeText(event.Result)
		if strings.Contains(text, "\nstatus: running\n") {
			return
		}
		a.Mu.Lock()
		key := childRowKey(a.sessionID, input.AgentID)
		a.Mu.Unlock()
		_, status, known, err := a.sink.LookupBackgroundTask(key)
		if err != nil {
			slog.Warn("read a Command Code child task", "error", err)
			return
		}
		if !known || text == "" {
			return
		}
		write := agent.ChildSubagentReportWrite{RowKey: key, Write: agent.SubagentReportWrite{ReportID: "result:" + input.AgentID, Report: agent.SubagentReport{Text: text, Status: bgtask.StatusWire(status)}}}
		if _, err := a.sink.PersistChildSubagentReport(write); err != nil {
			slog.Warn("persist a Command Code background child report", "error", err)
		}
		return
	}
	if opening.name != contracts.CommandCodeToolAgent {
		return
	}
	a.Mu.Lock()
	state := a.children[event.ToolCallID]
	sessionID := a.sessionID
	a.Mu.Unlock()
	if state == nil {
		return
	}
	text := nativeText(event.Result)
	if state.background {
		match := nativeAgentID.FindStringSubmatch(text)
		if len(match) != 2 {
			return
		}
		key := childRowKey(sessionID, match[1])
		if err := a.sink.RenameBackgroundTask(state.rowKey, key); err != nil {
			slog.Warn("identify a Command Code background child", "error", err)
			return
		}
		a.Mu.Lock()
		state.rowKey = key
		state.identified = true
		status := state.finishedStatus
		a.Mu.Unlock()
		if status != 0 {
			a.finishChild(event.ToolCallID, state, status)
		}
		return
	}
	status := bgtask.StatusSucceeded
	if event.Type != contracts.CommandCodeEventToolCompleted || strings.HasPrefix(text, "[sub-agent stopped early:") {
		status = bgtask.StatusFailed
	}
	if strings.HasPrefix(text, "[sub-agent interrupted") {
		status = bgtask.StatusStopped
	}
	if text != "" {
		write := agent.ChildSubagentReportWrite{RowKey: state.rowKey, Write: agent.SubagentReportWrite{ReportID: "result:" + event.ToolCallID, Report: agent.SubagentReport{Label: state.label, Text: text, Status: bgtask.StatusWire(status)}}}
		if _, err := a.sink.PersistChildSubagentReport(write); err != nil {
			slog.Warn("persist a Command Code child report", "error", err)
		}
	}
	a.finishChild(event.ToolCallID, state, status)
}

func (a *Agent) finishChild(callID string, state *childState, status bgtask.Status) {
	if err := a.sink.CloseBackgroundTask(state.rowKey, status); err != nil {
		slog.Warn("close a Command Code child task", "error", err)
	}
	a.sink.CleanupChildAgent(state.childID)
	a.Mu.Lock()
	delete(a.children, callID)
	a.Mu.Unlock()
}

func (a *Agent) closeChildren() {
	a.Mu.Lock()
	children := a.children
	a.children = make(map[string]*childState)
	a.Mu.Unlock()
	for _, child := range children {
		if err := a.sink.CloseBackgroundTask(child.rowKey, bgtask.StatusStopped); err != nil {
			slog.Warn("stop a Command Code child task", "error", err)
		}
		a.sink.CleanupChildAgent(child.childID)
	}
}
