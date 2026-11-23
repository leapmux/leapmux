package kimi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type kimiNativeTaskState struct {
	TaskID           string `json:"task_id"`
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	Outcome          string `json:"outcome"`
	Detached         bool   `json:"detached"`
	Description      string `json:"description"`
	Command          string `json:"command"`
	ChildAgentID     string `json:"child_agent_id"`
	ParentToolCallID string `json:"parent_tool_call_id"`
	SwarmIndex       *int   `json:"swarm_index"`
	Summary          string `json:"result_summary"`
	Error            string `json:"error"`
}

func (state *kimiNativeTaskState) UnmarshalJSON(data []byte) error {
	type taskState kimiNativeTaskState
	var decoded taskState
	wire := struct {
		*taskState
		Detached *bool `json:"detached"`
	}{taskState: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Detached == nil {
		return fmt.Errorf("the Kimi task state omits its required detached flag")
	}
	decoded.Detached = *wire.Detached
	validated := kimiNativeTaskState(decoded)
	if err := validated.validate(); err != nil {
		return err
	}
	*state = validated
	return nil
}

func (state kimiNativeTaskState) validate() error {
	if err := kimiCheckID("task", state.TaskID); err != nil {
		return err
	}
	switch state.Kind {
	case "shell", "subagent", "other":
	default:
		return fmt.Errorf("the Kimi task has an invalid kind %q", state.Kind)
	}
	if state.Status != "running" && state.Status != "ended" {
		return fmt.Errorf("the Kimi task has an invalid state %q", state.Status)
	}
	if state.Outcome != "" || state.Status == "ended" {
		if _, finished := kimiTaskStatus(state.Outcome); !finished {
			return fmt.Errorf("the Kimi task has an invalid outcome %q", state.Outcome)
		}
	}
	if state.ChildAgentID != "" {
		if err := kimiCheckID("child agent", state.ChildAgentID); err != nil {
			return err
		}
		if state.Kind == "subagent" && state.ChildAgentID == kimiMainAgentID {
			return fmt.Errorf("the Kimi subagent task identifies the main agent as its child")
		}
	}
	if state.SwarmIndex != nil && *state.SwarmIndex < 0 {
		return fmt.Errorf("the Kimi task has a negative swarm index")
	}
	return nil
}

func (a *Agent) handleNativeTaskFrame(frame kimiFrame) {
	var envelope struct {
		AgentID string                `json:"agent_id"`
		Tasks   []kimiNativeTaskState `json:"tasks"`
	}
	if frame.SessionID == "" || json.Unmarshal(frame.Raw, &envelope) != nil || kimiCheckID("agent", envelope.AgentID) != nil {
		return
	}
	if frame.Type == contracts.KimiEventTaskList {
		if envelope.Tasks == nil {
			return
		}
		seen := make(map[string]struct{}, len(envelope.Tasks))
		for _, state := range envelope.Tasks {
			if _, duplicate := seen[state.TaskID]; duplicate {
				return
			}
			seen[state.TaskID] = struct{}{}
		}
		for _, state := range envelope.Tasks {
			a.applyNativeTaskState(envelope.AgentID, state)
		}
		// Native lists omit ended tasks; retained projections can still need a retry.
		a.replayNativeTasksOwnedBy(envelope.AgentID)
		a.recoverMissingNativeTasks(frame.SessionID, envelope.AgentID, envelope.Tasks)
	} else {
		var state kimiNativeTaskState
		if json.Unmarshal(frame.Raw, &state) != nil {
			return
		}
		a.applyNativeTaskState(envelope.AgentID, state)
	}
}

func (a *Agent) recoverMissingNativeTasks(sessionID, ownerAgentID string, running []kimiNativeTaskState) {
	if a.stream == nil {
		return
	}
	present := make(map[string]struct{}, len(running))
	for _, state := range running {
		if kimiCheckID("task", state.TaskID) != nil {
			return
		}
		present[state.TaskID] = struct{}{}
	}
	a.Mu.Lock()
	var missing []kimiTaskAddress
	for address, task := range a.tasks {
		if address.ownerAgentID != ownerAgentID || task.ended || (task.kind != kimiTaskKindAgent && task.kind != kimiTaskKindProcess) {
			continue
		}
		if _, exists := present[address.taskID]; !exists {
			missing = append(missing, address)
		}
	}
	a.Mu.Unlock()
	sort.Slice(missing, func(i, j int) bool { return missing[i].taskID < missing[j].taskID })
	for _, address := range missing {
		key := fmt.Sprintf("%q/%q/%q", sessionID, address.ownerAgentID, address.taskID)
		a.taskRecoveryWork.Run(key, func() { a.recoverNativeTask(sessionID, address) })
	}
}

func (a *Agent) recoverNativeTask(sessionID string, address kimiTaskAddress) {
	a.Mu.Lock()
	task := a.tasks[address]
	needed := a.sessionID == sessionID && !a.StoppedLocked() && !a.ProcessExitedLocked() && task != nil && !task.ended
	a.Mu.Unlock()
	if !needed {
		return
	}
	ctx, cancel := a.requestContext()
	state, err := a.stream.readTaskState(ctx, sessionID, address)
	cancel()
	if err != nil {
		slog.Warn("recover Kimi task state", "agent_id", a.AgentID(), "task_id", address.taskID, "error", err)
		a.settleUnrecoverableNativeTask(sessionID, address)
		return
	}
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	current := a.sessionID == sessionID && !a.StoppedLocked() && !a.ProcessExitedLocked()
	a.Mu.Unlock()
	if current {
		a.applyNativeTaskState(address.ownerAgentID, state)
	}
}

func (a *Agent) settleUnrecoverableNativeTask(sessionID string, address kimiTaskAddress) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	task := a.tasks[address]
	if a.sessionID != sessionID || a.StoppedLocked() || a.ProcessExitedLocked() || task == nil || task.ended {
		a.Mu.Unlock()
		return
	}
	kind := task.kind
	rowKey := task.rowKey
	if rowKey == "" {
		rowKey = kimiTaskRowKey(sessionID, address.ownerAgentID, address.taskID)
		task.rowKey = rowKey
	}
	task.ended = true
	task.endStatus = bgtask.StatusEndedWithUnknownOutcome
	task.projectionPending = false
	a.Mu.Unlock()
	if kind != kimiTaskKindAgent && kind != kimiTaskKindProcess {
		return
	}
	writer := a.sink
	if kind == kimiTaskKindAgent {
		if sink := a.runSink(a.run(address.ownerAgentID)); sink != nil {
			writer = sink
		}
	}
	err := a.queueRegistryClose(writer, rowKey, bgtask.StatusEndedWithUnknownOutcome)
	providerkit.LogRegistryRefusal("kimi", "close", err)
	if kind == kimiTaskKindAgent {
		a.Mu.Lock()
		task.childEndRetained = agent.OutputRetained(err)
		a.Mu.Unlock()
	}
}

func (a *Agent) applyNativeTaskState(ownerAgentID string, state kimiNativeTaskState) {
	a.applyNativeTaskStateBound(ownerAgentID, state, nil)
}

func (a *Agent) applyNativeTaskStateBound(ownerAgentID string, state kimiNativeTaskState, invocation *kimiChildInvocation) {
	if kimiCheckID("agent", ownerAgentID) != nil || state.validate() != nil {
		return
	}
	status, finished := kimiTaskStatus(state.Outcome)
	if state.Status == "ended" && !finished {
		return
	}
	address := kimiTaskAddress{ownerAgentID: ownerAgentID, taskID: state.TaskID}
	a.Mu.Lock()
	existing := a.tasks[address]
	alreadyEnded := existing != nil && existing.ended
	a.Mu.Unlock()
	if invocation != nil && existing != nil && existing.boundInvocation != nil && existing.boundInvocation != invocation {
		return
	}
	if invocation == nil && existing != nil && existing.kind == kimiTaskKindAgent && a.invocationRecovery == nil {
		owner := a.run(ownerAgentID)
		candidate := a.findTaskInvocation(existing, owner)
		if candidate != nil {
			target := candidate.taskTargets[address]
			if target != nil && a.currentDiscoveredTaskOwner(target) != nil && kimiDiscoveredTaskMatches(target, preserveNativeTaskMetadata(existing.native, state)) {
				invocation = candidate
				target.complete = true
			}
		}
	}
	if alreadyEnded {
		if invocation != nil && existing.boundInvocation == nil {
			a.Mu.Lock()
			existing.boundInvocation = invocation
			existing.projectionPending = true
			a.Mu.Unlock()
		}
		if existing.projectionPending {
			a.logNativeTaskProjection(address)
		}
		return
	}
	switch state.Kind {
	case "subagent":
		if existing != nil && existing.agentID != "" && state.ChildAgentID != "" && existing.agentID != state.ChildAgentID {
			return
		}
		a.Mu.Lock()
		if existing == nil {
			existing = &kimiTask{kind: kimiTaskKindAgent, rowKey: kimiTaskRowKey(a.sessionID, ownerAgentID, state.TaskID)}
			if a.tasks == nil {
				a.tasks = make(map[kimiTaskAddress]*kimiTask)
			}
			a.tasks[address] = existing
		}
		state = preserveNativeTaskMetadata(existing.native, state)
		if invocation != nil {
			existing.boundInvocation = invocation
		}
		existing.native = state
		existing.agentID = state.ChildAgentID
		existing.ended = state.Status == "ended"
		existing.projectionPending = true
		a.Mu.Unlock()
		if invocation == nil && a.invocationRecovery == nil {
			candidate := a.findTaskInvocation(existing, a.run(ownerAgentID))
			if candidate != nil {
				target := candidate.taskTargets[address]
				if target != nil && a.currentDiscoveredTaskOwner(target) != nil && kimiDiscoveredTaskMatches(target, state) {
					a.Mu.Lock()
					existing.boundInvocation = candidate
					a.Mu.Unlock()
					target.complete = true
				}
			}
		}
		a.logNativeTaskProjection(address)
	case "shell":
		if !state.Detached {
			return
		}
		if existing == nil {
			a.startShellTask(ownerAgentID, state)
		}
		if state.Status == "ended" {
			a.endShellTask(ownerAgentID, state, status)
		}
	}
}
