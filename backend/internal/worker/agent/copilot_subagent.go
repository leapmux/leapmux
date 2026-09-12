package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"github.com/leapmux/leapmux/generated/contracts"
)

type copilotTaskInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Mode        string `json:"mode"`
}

type copilotTaskState struct {
	input              copilotTaskInput
	rowKey             string
	base               *acpBase
	awaitingCompletion bool
}

func (a *CopilotCLIAgent) configureSubagentHooks(base *acpBase) {
	base.subagentFromToolCall = func(call acpToolCallEnvelope) *acpSubagentObservation { return a.subagentToolCall(base, call) }
	base.subagentFromToolCallUpdate = func(update acpToolCallUpdateEnvelope) *acpSubagentObservation {
		return a.subagentToolCallUpdate(base, update)
	}
}

func copilotSpawnedAgentID(record *copilotNativeTool) string {
	if record == nil {
		return ""
	}
	for _, raw := range []json.RawMessage{record.Started, record.Finished} {
		var event struct {
			AgentID string `json:"agentId"`
		}
		if json.Unmarshal(raw, &event) == nil && event.AgentID != "" {
			return event.AgentID
		}
	}
	return ""
}

func (a *CopilotCLIAgent) nativeTool(toolCallID string) *copilotNativeTool {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	record, err := readCopilotNativeTool(ctx, copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir()), toolCallID)
	if err != nil {
		slog.Debug("Read Copilot tool metadata", "tool_call_id", toolCallID, "error", err)
	}
	return record
}

// Copilot's ACP title can be a description, so the native event identifies the tool.
func (a *CopilotCLIAgent) subagentToolCall(base *acpBase, tc acpToolCallEnvelope) *acpSubagentObservation {
	a.subagentOpsMu.Lock()
	defer a.subagentOpsMu.Unlock()
	return a.observeSubagentToolCall(base, tc)
}

func (a *CopilotCLIAgent) observeSubagentToolCall(base *acpBase, tc acpToolCallEnvelope) *acpSubagentObservation {
	a.subagentMu.Lock()
	task := a.subagentTasks[tc.ToolCallID]
	a.subagentMu.Unlock()
	if task == nil {
		record := a.nativeTool(tc.ToolCallID)
		if record == nil || record.ToolName != contracts.CopilotToolTask {
			return nil
		}
		var input copilotTaskInput
		if err := json.Unmarshal(record.Arguments, &input); err != nil {
			slog.Debug("Decode Copilot subagent arguments", "tool_call_id", tc.ToolCallID, "error", err)
		}
		task = &copilotTaskState{input: input, base: base, rowKey: StringOrDefault(copilotSpawnedAgentID(record), tc.ToolCallID)}
		a.subagentMu.Lock()
		if a.subagentTasks == nil {
			a.subagentTasks = make(map[string]*copilotTaskState)
		}
		a.subagentTasks[tc.ToolCallID] = task
		a.subagentMu.Unlock()
	}
	title := StringOrDefault(task.input.Name, StringOrDefault(task.input.Description, tc.Title))
	return &acpSubagentObservation{RowKey: task.rowKey, Title: title, Status: bgtask.StatusRunning, Spawns: true}
}

func (a *CopilotCLIAgent) subagentToolCallUpdate(base *acpBase, update acpToolCallUpdateEnvelope) *acpSubagentObservation {
	a.subagentOpsMu.Lock()
	defer a.subagentOpsMu.Unlock()
	return a.observeSubagentToolCallUpdate(base, update)
}

func (a *CopilotCLIAgent) observeSubagentToolCallUpdate(base *acpBase, update acpToolCallUpdateEnvelope) *acpSubagentObservation {
	if !acpStatusIsFinal(update.Status) {
		return a.observeSubagentToolCall(base, acpToolCallEnvelope{ToolCallID: update.ToolCallID, Title: update.Title})
	}
	if a.observeSubagentToolCall(base, acpToolCallEnvelope{ToolCallID: update.ToolCallID, Title: update.Title}) == nil {
		return nil
	}
	a.subagentMu.Lock()
	task := a.subagentTasks[update.ToolCallID]
	a.subagentMu.Unlock()
	status := acpFinalStatus(update.Status)
	if update.Status == "completed" && task.input.Mode == "background" {
		record := a.nativeTool(update.ToolCallID)
		var valid bool
		if record != nil {
			status, valid = copilotTaskOutcome(record.Finished)
		}
		if !valid {
			a.watchBackgroundTask(task)
			return nil
		}
	}
	a.finishSubagentTask(update.ToolCallID, status)
	return &acpSubagentObservation{RowKey: task.rowKey, Mode: acpModeCloseOnly, CloseRow: true, Status: status}
}

func (a *CopilotCLIAgent) finishSubagentTask(toolCallID string, status bgtask.Status) {
	a.subagentMu.Lock()
	task := a.subagentTasks[toolCallID]
	if task == nil {
		a.subagentMu.Unlock()
		return
	}
	delete(a.subagentTasks, toolCallID)
	child := a.childTools[task.rowKey]
	delete(a.childTools, task.rowKey)
	a.subagentMu.Unlock()
	if child != nil {
		completion := MessageCompletionComplete
		if status == bgtask.StatusFailed {
			completion = MessageCompletionError
		}
		if status == bgtask.StatusStopped {
			completion = MessageCompletionInterrupted
		}
		child.base.finishACPTurn(child.base.drainTurn(), completion)
		child.base.sink.ResetSpans()
		child.parent.CleanupChildAgent(child.base.agentID)
	}
}
