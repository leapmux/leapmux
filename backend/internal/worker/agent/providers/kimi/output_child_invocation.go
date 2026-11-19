package kimi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// kimiInvocationIdentity is a tool item within a native conversation turn.
type kimiInvocationIdentity struct {
	sessionID, agentID, turnID, toolCallID string
}

type kimiInvocationPosition struct {
	turn, step *int64
}

type kimiInvocationProjection struct {
	task          *kimiTask
	status        bgtask.Status
	actualProfile string
}

type kimiInvocationTarget struct {
	status        bgtask.Status
	actualProfile string
	nativeTask    *kimiDiscoveredTask
	// retained identifies the last projection accepted by the output queue.
	retained kimiInvocationProjection
}

type kimiChildInvocation struct {
	identity     kimiInvocationIdentity
	position     kimiInvocationPosition
	name         string
	label        string
	targets      map[string]*kimiInvocationTarget
	linkedAgents map[string]struct{}
	corroborated bool
	restored     bool
	present      bool
	eligible     bool
	finished     bool
	resume       map[string]struct{}
	taskTargets  map[kimiTaskAddress]*kimiDiscoveredTask
	parentOutput *kimiTaskOutputAnchor
}

type kimiDiscoveredTask struct {
	address       kimiTaskAddress
	kind, childID string
	invocation    *kimiChildInvocation
	complete      bool
}

type kimiInvocationObservation struct {
	identity                     kimiInvocationIdentity
	position                     kimiInvocationPosition
	name                         string
	input, output                json.RawMessage
	taskID                       string
	refs                         []kimiConversationAgentRef
	completion                   agent.MessageCompletion
	eligible, restored, finished bool
	parentOutput                 *kimiTaskOutputAnchor
}

func (a *Agent) observeInvocation(run *kimiRun, observation kimiInvocationObservation) *kimiChildInvocation {
	if !kimiToolSpawns(observation.name) && observation.name != contracts.KimiToolBash {
		return nil
	}
	id := observation.identity
	if id.sessionID == "" || id.agentID == "" || id.turnID == "" || id.toolCallID == "" {
		return nil
	}
	var input struct {
		Description string            `json:"description"`
		Resume      string            `json:"resume"`
		ResumeIDs   map[string]string `json:"resume_agent_ids"`
	}
	if observation.name != contracts.KimiToolBash && json.Unmarshal(observation.input, &input) != nil {
		return nil
	}
	refs := make(map[string]struct{}, len(observation.refs))
	referencedTasks := make(map[kimiTaskAddress]string)
	for _, ref := range observation.refs {
		if kimiCheckID("child agent", ref.AgentID) != nil || ref.AgentID == kimiMainAgentID || ref.AgentID == run.agentID {
			return nil
		}
		if _, duplicate := refs[ref.AgentID]; duplicate {
			return nil
		}
		if observation.name == contracts.KimiToolAgent && ref.Role != "child" || observation.name == contracts.KimiToolAgentSwarm && ref.Role != "member" {
			return nil
		}
		a.Mu.Lock()
		childRun := a.runs[ref.AgentID]
		conflictingParent := childRun != nil && childRun.childParentAgentID != "" && childRun.childParentAgentID != run.agentID
		a.Mu.Unlock()
		if conflictingParent {
			return nil
		}
		refs[ref.AgentID] = struct{}{}
		if ref.TaskID != "" {
			if kimiCheckID("task", ref.TaskID) != nil {
				return nil
			}
			address := kimiTaskAddress{ownerAgentID: run.agentID, taskID: ref.TaskID}
			if _, duplicate := referencedTasks[address]; duplicate {
				return nil
			}
			referencedTasks[address] = ref.AgentID
		}
	}
	if observation.name == contracts.KimiToolAgent && input.Resume != "" {
		if _, matches := refs[input.Resume]; !matches {
			return nil
		}
		refs = map[string]struct{}{input.Resume: {}}
		for address, childID := range referencedTasks {
			if childID != input.Resume {
				delete(referencedTasks, address)
			}
		}
	}
	taskKind := "subagent"
	if observation.name == contracts.KimiToolBash {
		taskKind = "shell"
		if observation.taskID != "" {
			if kimiCheckID("task", observation.taskID) != nil {
				return nil
			}
			referencedTasks[kimiTaskAddress{ownerAgentID: run.agentID, taskID: observation.taskID}] = ""
		}
	}
	var output string
	hasOutput := json.Unmarshal(observation.output, &output) == nil
	var agentResult kimiAgentResultEvidence
	var hasAgentResult bool
	if observation.name == contracts.KimiToolAgent && hasOutput {
		agentResult, hasAgentResult = parseKimiAgentResult(output, observation.completion)
		if hasAgentResult {
			if _, matches := refs[agentResult.childID]; !matches {
				return nil
			}
		}
	}
	if run.childInvocations == nil {
		run.childInvocations = make(map[kimiInvocationIdentity]*kimiChildInvocation)
	}
	invocation := run.childInvocations[id]
	previousTasksByChild := make(map[string]*kimiDiscoveredTask)
	if invocation != nil {
		for _, previous := range invocation.taskTargets {
			previousTasksByChild[previous.childID] = previous
		}
		for address, childID := range referencedTasks {
			previous := invocation.taskTargets[address]
			if previous != nil && (previous.kind != taskKind || previous.childID != childID) {
				return nil
			}
			if previous := previousTasksByChild[childID]; previous != nil && previous.address != address {
				return nil
			}
		}
	}
	if invocation == nil {
		invocation = &kimiChildInvocation{identity: id, name: observation.name, targets: make(map[string]*kimiInvocationTarget)}
		run.childInvocations[id] = invocation

	} else if invocation.name != observation.name {
		return nil
	}
	invocation.label = input.Description
	if kimiToolSpawns(observation.name) && invocation.parentOutput == nil {
		invocation.parentOutput = observation.parentOutput
	}
	if observation.position.turn != nil {
		invocation.position.turn = observation.position.turn
	}
	if observation.position.step != nil {
		invocation.position.step = observation.position.step
	}
	invocation.present = true
	invocation.eligible = invocation.eligible || observation.eligible
	invocation.restored = observation.restored
	invocation.resume = make(map[string]struct{})
	if input.Resume != "" {
		invocation.resume[input.Resume] = struct{}{}
	}
	for childID := range input.ResumeIDs {
		invocation.resume[childID] = struct{}{}
	}
	if observation.name == contracts.KimiToolAgent && input.Resume != "" {
		invocation.corroborated = true
		for childID := range invocation.targets {
			if childID != input.Resume {
				delete(invocation.targets, childID)
			}
		}
	}
	for childID := range invocation.targets {
		if _, present := refs[childID]; !present {
			delete(invocation.targets, childID)
		}
	}
	for childID := range refs {
		if invocation.targets[childID] == nil {
			invocation.targets[childID] = &kimiInvocationTarget{nativeTask: previousTasksByChild[childID]}
		}
	}
	for address, childID := range referencedTasks {
		if invocation.taskTargets == nil {
			invocation.taskTargets = make(map[kimiTaskAddress]*kimiDiscoveredTask)
		}
		if invocation.taskTargets[address] == nil {
			invocation.taskTargets[address] = &kimiDiscoveredTask{address: address, kind: taskKind, childID: childID, invocation: invocation}
		}
		if target := invocation.targets[childID]; target != nil {
			target.nativeTask = invocation.taskTargets[address]
		}
	}
	invocation.finished = invocation.finished || observation.finished
	defer invocation.finishUnmatchedTargets()
	if !hasOutput {
		return invocation
	}
	if observation.name == contracts.KimiToolAgent {
		if hasAgentResult {
			evidence := agentResult
			invocation.corroborated = true
			invocation.targets[evidence.childID].actualProfile = evidence.actualProfile
			for childID := range invocation.targets {
				if childID != evidence.childID {
					delete(invocation.targets, childID)
				}
			}
			if evidence.status != bgtask.StatusUnspecified {
				invocation.targets[evidence.childID].status = evidence.status
			}
		}
	} else if observation.name == contracts.KimiToolAgentSwarm && observation.completion == agent.MessageCompletionComplete {
		evidence := parseKimiSwarmResult(observation.input, output, refs)
		if evidence.finished {
			invocation.corroborated = evidence.outcomes != nil
			for childID, target := range invocation.targets {
				status, known := evidence.outcomes[childID]
				if !known {
					if target.status == bgtask.StatusUnspecified {
						target.status = bgtask.StatusEndedWithUnknownOutcome
					}
					continue
				}
				target.status = status
			}
		}
	}
	return invocation
}

func (a *Agent) linkInvocationChildren(run *kimiRun, invocation *kimiChildInvocation, spawn kimiSpawn) {
	if invocation == nil || !kimiToolSpawns(invocation.name) || !a.invocationTargetsTrusted(run, invocation) {
		return
	}
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	if invocation.linkedAgents == nil {
		invocation.linkedAgents = make(map[string]struct{})
	}
	for childID := range invocation.targets {
		if _, known := invocation.linkedAgents[childID]; known {
			continue
		}
		if child, linked := a.children.get(childID); linked {
			if a.attachChildSpawnSpan(sink, child, kimiChildSpawnSpan(spawn, childID)) {
				if err := a.updateChildRegistryMetadata(sink, childID, bgtask.Upsert{Kind: bgtask.KindSubagent}); agent.OutputRetained(err) {
					invocation.linkedAgents[childID] = struct{}{}
				} else {
					slog.Warn("update Kimi child spawn metadata", "agent_id", a.AgentID(), "child", childID, "error", err)
				}
			}
			continue
		}
		a.linkSubagent(kimiChildAdoption{SubagentID: childID, Description: spawn.label}, run.agentID, spawn)
		if _, linked := a.children.get(childID); linked {
			invocation.linkedAgents[childID] = struct{}{}
		}
	}
}

func (a *Agent) invocationTargetsTrusted(run *kimiRun, invocation *kimiChildInvocation) bool {
	if invocation.corroborated || !invocation.restored {
		return true
	}
	referenced := len(invocation.targets) > 0
	for childID, target := range invocation.targets {
		reference := target.nativeTask
		if reference == nil || reference.childID != childID || reference.address.ownerAgentID != run.agentID ||
			reference.invocation != invocation || invocation.taskTargets[reference.address] != reference {
			referenced = false
			break
		}
	}
	if referenced {
		return true
	}
	for _, other := range run.childInvocations {
		if other != invocation && other.present && other.identity.toolCallID == invocation.identity.toolCallID {
			for childID := range invocation.targets {
				if _, explicit := invocation.resume[childID]; !explicit {
					return false
				}
			}
		}
	}
	return true
}

// findTaskInvocation resolves only a validated task-ID relation. A bare model
// call ID cannot distinguish reused calls in unseen turns or frames.
func (a *Agent) findTaskInvocation(task *kimiTask, owner *kimiRun) *kimiChildInvocation {
	if task.boundInvocation != nil {
		return task.boundInvocation
	}
	var exact *kimiChildInvocation
	for _, invocation := range owner.childInvocations {
		target := invocation.taskTargets[kimiTaskAddress{ownerAgentID: owner.agentID, taskID: task.native.TaskID}]
		if target == nil || target.address.taskID != task.native.TaskID || target.address.ownerAgentID != owner.agentID || target.childID != task.native.ChildAgentID {
			continue
		}
		if exact != nil && exact != invocation {
			return nil
		}
		exact = invocation
	}
	return exact
}

func kimiInvocationOutcomeKey(invocation *kimiChildInvocation, childID string) string {
	return kimiInvocationGroupKey(invocation.identity) + "/child/" + childID
}

// Parent invocation outcomes describe parent operations, independently of the
// child's current native turn. Several operations may share that transcript.
func (a *Agent) projectChildInvocation(run *kimiRun, invocation *kimiChildInvocation) {
	if invocation == nil || !invocation.eligible || !a.invocationTargetsTrusted(run, invocation) || a.invocationRecovery != nil {
		return
	}
	sink := a.runSink(run)
	if sink == nil {
		return
	}
	for childID, target := range invocation.targets {
		child, linked := a.children.get(childID)
		if !linked {
			continue
		}
		rowKey := kimiInvocationOutcomeKey(invocation, childID)
		var bound *kimiTask
		if descriptor := target.nativeTask; descriptor != nil && descriptor.childID == childID {
			a.Mu.Lock()
			candidate := a.tasks[descriptor.address]
			if candidate != nil && candidate.boundInvocation == invocation && candidate.native.ChildAgentID == childID {
				bound = candidate
			}
			a.Mu.Unlock()
		}
		status := target.status
		if status == bgtask.StatusUnspecified {
			status = bgtask.StatusRunning
		}
		desired := kimiInvocationProjection{task: bound, status: status, actualProfile: target.actualProfile}
		if target.retained == desired {
			continue
		}
		var writeErr error
		if target.actualProfile != "" && target.retained.actualProfile != target.actualProfile {
			rowKey, profile := child.rowKey, target.actualProfile
			writeErr = a.queueRegistryWrite(sink, "publish Kimi actual child profile", func(writer agent.BackgroundTaskServices) error {
				return writer.UpdateBackgroundTaskDescription(rowKey, profile)
			})
			if !agent.OutputRetained(writeErr) {
				continue
			}
		}
		// A newly verified child result can refine an earlier Unknown. Bound task
		// aliases retain that evidence before their rows are combined.
		if bound == nil || target.status.IsFinished() && target.status != bgtask.StatusEndedWithUnknownOutcome {
			title := invocation.label
			if title == "" {
				title = "Subagent task"
			}
			update := bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, ChildAgentID: child.childAgentID, Status: status}
			if invocation.name == contracts.KimiToolAgentSwarm {
				update.Kind = bgtask.KindWorkflow
				update.GroupKey = kimiInvocationGroupKey(invocation.identity)
				update.GroupLabel = invocation.label
			}
			writeErr = a.queueRegistryUpsert(sink, update)
		}
		if !agent.OutputRetained(writeErr) {
			continue
		}
		if bound != nil {
			writeErr = a.queueNativeTaskAlias(sink, rowKey, bound.rowKey, kimiAgentTaskRegistryStatus(bound.native))
		}
		if agent.OutputRetained(writeErr) {
			target.retained = desired
		}
	}
	for _, target := range invocation.taskTargets {
		a.scheduleDiscoveredNativeTask(run, target)
	}
}

// kimiInvocationGroupKey identifies a parent tool item in its owning turn.
// A later native turn can use the same call ID.
func kimiInvocationGroupKey(identity kimiInvocationIdentity) string {
	value := fmt.Sprintf("%q/%q/%q/%q", identity.sessionID, identity.agentID, identity.turnID, identity.toolCallID)
	digest := sha256.Sum256([]byte(value))
	return identity.sessionID + "/invocation-group/" + hex.EncodeToString(digest[:])
}

// A finished parent operation supplies no child outcome by itself. A validated
// native task reference retains its independent coordinator outcome.
func (invocation *kimiChildInvocation) finishUnmatchedTargets() {
	if !invocation.finished {
		return
	}
	for childID, target := range invocation.targets {
		if target.status != bgtask.StatusUnspecified {
			continue
		}
		if continuation := target.nativeTask; continuation != nil && continuation.kind == "subagent" && continuation.childID == childID {
			continue
		}
		target.status = bgtask.StatusEndedWithUnknownOutcome
	}
}

func (a *Agent) finishChildInvocationTurn(run *kimiRun, turnID string) {
	for _, invocation := range run.childInvocations {
		if invocation.identity.turnID != turnID {
			continue
		}
		invocation.finished = true
		invocation.finishUnmatchedTargets()
		a.projectChildInvocation(run, invocation)
	}
}
