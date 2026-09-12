package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"github.com/leapmux/leapmux/generated/contracts"
)

type copilotChildTools struct {
	base            *acpBase
	parent          ProviderServices
	spawnToolCallID string
}

func (a *CopilotCLIAgent) routeChildToolMessage(update json.RawMessage) bool {
	var header struct {
		ToolCallID string                     `json:"toolCallId"`
		Meta       map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(update, &header) != nil || header.ToolCallID == "" {
		return false
	}
	var metadata struct {
		AgentID string `json:"agentId"`
	}
	if json.Unmarshal(header.Meta["github.com/copilot"], &metadata) != nil || metadata.AgentID == "" {
		return false
	}
	a.subagentOpsMu.Lock()
	defer a.subagentOpsMu.Unlock()
	a.subagentMu.Lock()
	knownChild := a.childTools[metadata.AgentID]
	a.subagentMu.Unlock()
	// The provider's agent ID remains sufficient after its native parent link resolves.
	if knownChild != nil {
		knownChild.base.handleACPUpdate(update, nil)
		return true
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	path := copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir())
	record, err := readCopilotNativeTool(ctx, path, header.ToolCallID)
	if err != nil || record == nil || record.AgentID != metadata.AgentID {
		if err != nil {
			slog.Debug("Read Copilot child tool identity", "tool_call_id", header.ToolCallID, "error", err)
		}
		return false
	}
	child, err := a.childToolsFor(ctx, path, metadata.AgentID, record.ParentToolCallID, make(map[string]bool))
	if err != nil {
		slog.Debug("Resolve Copilot child transcript", "tool_call_id", header.ToolCallID, "error", err)
		return false
	}
	child.base.handleACPUpdate(update, nil)
	return true
}

// Native parent task IDs identify the hierarchy even when several children run at once.
func (a *CopilotCLIAgent) childToolsFor(ctx context.Context, path, nativeAgentID, parentToolCallID string, visited map[string]bool) (*copilotChildTools, error) {
	if nativeAgentID == "" || parentToolCallID == "" || visited[parentToolCallID] {
		return nil, fmt.Errorf("the Copilot child has an invalid parent task")
	}
	visited[parentToolCallID] = true
	a.subagentMu.Lock()
	existing := a.childTools[nativeAgentID]
	a.subagentMu.Unlock()
	if existing != nil {
		return existing, nil
	}
	parentTask, err := readCopilotNativeTool(ctx, path, parentToolCallID)
	if err != nil {
		return nil, err
	}
	if parentTask == nil || parentTask.ToolName != contracts.CopilotToolTask || parentTask.AgentID == nativeAgentID {
		return nil, fmt.Errorf("the Copilot parent task is unavailable")
	}
	parent := a.sink
	parentAgentID := a.agentID
	if parentTask.AgentID != "" {
		owner, err := a.childToolsFor(ctx, path, parentTask.AgentID, parentTask.ParentToolCallID, visited)
		if err != nil {
			return nil, err
		}
		parent = owner.base.sink
		parentAgentID = owner.base.agentID
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(parentTask.Arguments, &input); err != nil {
		return nil, fmt.Errorf("decode Copilot parent task: %w", err)
	}
	workerAgentID, err := parent.EnsureChildAgent(parentToolCallID, nativeAgentID, StringOrDefault(input.Name, input.Description))
	if err != nil {
		return nil, err
	}
	if err := parent.RenameBackgroundTask(parentToolCallID, nativeAgentID); err != nil {
		return nil, err
	}
	if err := parent.PersistChildPrompt(workerAgentID, input.Prompt); err != nil {
		return nil, err
	}
	if err := parent.UpsertBackgroundTask(bgtask.Upsert{RowKey: nativeAgentID, Kind: bgtask.KindSubagent, ChildAgentID: workerAgentID, ParentAgentID: parentAgentID, Title: StringOrDefault(input.Name, input.Description), Status: bgtask.StatusRunning}); err != nil {
		return nil, err
	}
	base := &acpBase{sink: parent.ChildSink(workerAgentID), workingDir: a.currentWorkingDir()}
	base.agentID = workerAgentID
	base.providerName = "copilot"
	base.ctx = a.ctx
	// The route holds the operation lock while this child processes its message.
	base.subagentFromToolCall = func(call acpToolCallEnvelope) *acpSubagentObservation { return a.observeSubagentToolCall(base, call) }
	base.subagentFromToolCallUpdate = func(update acpToolCallUpdateEnvelope) *acpSubagentObservation {
		return a.observeSubagentToolCallUpdate(base, update)
	}
	child := &copilotChildTools{base: base, parent: parent, spawnToolCallID: parentToolCallID}
	a.subagentMu.Lock()
	defer a.subagentMu.Unlock()
	if task := a.subagentTasks[parentToolCallID]; task != nil {
		task.rowKey = nativeAgentID
	}
	if existing = a.childTools[nativeAgentID]; existing != nil {
		return existing, nil
	}
	if a.childTools == nil {
		a.childTools = make(map[string]*copilotChildTools)
	}
	a.childTools[nativeAgentID] = child
	return child, nil
}

func (a *CopilotCLIAgent) clearChildTools() {
	a.subagentOpsMu.Lock()
	defer a.subagentOpsMu.Unlock()
	a.subagentMu.Lock()
	children := a.childTools
	tasks := a.subagentTasks
	a.childTools = nil
	a.subagentTasks = nil
	a.subagentMu.Unlock()
	for _, task := range tasks {
		if err := task.base.sink.CloseBackgroundTask(task.rowKey, bgtask.StatusStopped); err != nil {
			slog.Warn("Close Copilot subagent", "row_key", task.rowKey, "error", err)
		}
	}
	for _, child := range children {
		child.base.finishACPTurn(child.base.drainTurn(), MessageCompletionInterrupted)
		child.base.sink.ResetSpans()
		child.parent.CleanupChildAgent(child.base.agentID)
	}
}
