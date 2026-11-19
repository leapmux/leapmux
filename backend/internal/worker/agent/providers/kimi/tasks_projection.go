package kimi

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func preserveNativeTaskMetadata(previous, current kimiNativeTaskState) kimiNativeTaskState {
	if current.ChildAgentID == "" {
		current.ChildAgentID = previous.ChildAgentID
	}
	if current.ParentToolCallID == "" {
		current.ParentToolCallID = previous.ParentToolCallID
	}
	if current.Description == "" {
		current.Description = previous.Description
	}
	if current.Command == "" {
		current.Command = previous.Command
	}
	if current.SwarmIndex == nil {
		current.SwarmIndex = previous.SwarmIndex
	}
	return current
}

func (a *Agent) logNativeTaskProjection(address kimiTaskAddress) {
	if err := a.publishNativeAgentTask(address); err != nil {
		slog.Warn("publish Kimi native task", "agent_id", a.AgentID(), "task_id", address.taskID, "error", err)
	}
}

// publishNativeAgentTask retains native coordinator state in its own outcome row.
// Child turn headers, rather than coordinator outcomes, control canonical activity.
// The caller holds dispatchMu; an unlinked owner keeps publication pending.
func (a *Agent) publishNativeAgentTask(address kimiTaskAddress) error {
	a.Mu.Lock()
	task := a.tasks[address]
	if task == nil || !task.projectionPending || a.invocationRecovery != nil {
		a.Mu.Unlock()
		return nil
	}
	state, sessionID := task.native, a.sessionID
	rowKey := kimiTaskRowKey(sessionID, address.ownerAgentID, address.taskID)
	task.rowKey = rowKey
	a.Mu.Unlock()
	owner := a.run(address.ownerAgentID)
	sink := a.runSink(owner)
	if sink == nil {
		return nil
	}
	child, linked := a.children.get(state.ChildAgentID)
	if state.ChildAgentID != "" && !linked {
		a.linkSubagent(kimiChildAdoption{SubagentID: state.ChildAgentID, Description: state.Description}, address.ownerAgentID, kimiSpawn{})
		child, linked = a.children.get(state.ChildAgentID)
	}
	status := kimiAgentTaskRegistryStatus(state)
	title := strings.TrimSpace(state.Description)
	if title == "" {
		title = "Subagent task"
	}
	upsert := bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, Description: state.Description, Status: status}
	if state.SwarmIndex != nil {
		upsert.Kind = bgtask.KindWorkflow
	}
	if linked {
		upsert.ChildAgentID = child.childAgentID
		task.childAgentID = child.childAgentID
	}
	// Exact native result evidence may join a full parent invocation. A bare
	// parent tool-call ID is not sufficient when invocations can reuse it.
	invocation := task.boundInvocation
	if invocation != nil && invocation.name == contracts.KimiToolAgentSwarm {
		upsert.Kind = bgtask.KindWorkflow
		upsert.GroupKey = kimiInvocationGroupKey(invocation.identity)
		upsert.GroupLabel = invocation.label
	}
	a.captureNativeTaskOutputAnchors(address, task)
	metadataErr := a.queueRegistryUpsert(sink, upsert)
	if agent.OutputRetained(metadataErr) && invocation != nil {
		metadataErr = a.queueNativeTaskAlias(sink, kimiInvocationOutcomeKey(invocation, state.ChildAgentID), rowKey, status)
	}
	diagnosticErr := a.persistNativeTaskFailure(sessionID, address, task, status)
	a.Mu.Lock()
	if state.Status == "ended" {
		task.endStatus = status
		task.childEndRetained = agent.OutputRetained(metadataErr)
	}
	task.projectionPending = state.ChildAgentID != "" && !linked || !agent.OutputRetained(metadataErr) || !agent.OutputRetained(diagnosticErr)
	a.Mu.Unlock()
	if state.Status == "ended" {
		a.scheduleNativeTaskReport(address)
	}
	return errors.Join(metadataErr, diagnosticErr)
}

// persistNativeTaskFailure uses one immutable task identity across projection retries.
func (a *Agent) persistNativeTaskFailure(sessionID string, address kimiTaskAddress, task *kimiTask, status bgtask.Status) error {
	if task.diagnosticRetained {
		return nil
	}
	if task.native.Status != "ended" || task.childAgentID == "" || task.native.Error == "" {
		return nil
	}
	if task.endStatus != bgtask.StatusUnspecified {
		status = task.endStatus
	}
	if status != bgtask.StatusFailed {
		return nil
	}
	if task.diagnosticCapture.TranscriptWriter() == nil {
		raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, task.native.Error, agent.MessageCompletionError)
		if err != nil {
			return err
		}
		sink := a.runSink(a.run(task.native.ChildAgentID))
		if sink == nil {
			return errors.New("the Kimi task failure has no captured child writer")
		}
		content := agent.MessageContent{Original: raw,
			IdempotencyKey: fmt.Sprintf("kimi-task-failure/%q/%q/%q", sessionID, address.ownerAgentID, address.taskID)}
		capture, err := captureKimiTaskOutput(task.childOutput, sink, true, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		if err != nil {
			return err
		}
		task.diagnosticCapture = capture
	}
	err := task.diagnosticCapture.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	if agent.OutputRetained(err) {
		task.diagnosticRetained = true
		task.diagnosticCapture = agent.CapturedTranscript{}
	}
	return err
}

func (a *Agent) replayNativeTasksOwnedBy(ownerAgentID string) {
	a.Mu.Lock()
	var addresses []kimiTaskAddress
	for address, task := range a.tasks {
		if address.ownerAgentID == ownerAgentID && task.projectionPending {
			addresses = append(addresses, address)
		}
	}
	a.Mu.Unlock()
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].taskID < addresses[j].taskID })
	for _, address := range addresses {
		a.logNativeTaskProjection(address)
	}
}

func kimiAgentTaskRegistryStatus(state kimiNativeTaskState) bgtask.Status {
	if state.Status == "ended" {
		status, _ := kimiTaskStatus(state.Outcome)
		return status
	}
	if state.ChildAgentID == "" {
		return bgtask.StatusPending
	}
	return bgtask.StatusRunning
}
