package kimi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

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
	// retained identifies the last projection accepted by the output queue.
	retained kimiInvocationProjection
}

type kimiChildInvocation struct {
	identity     kimiTranscriptToolIdentity
	position     kimiInvocationPosition
	name         string
	label        string
	targets      map[string]*kimiInvocationTarget
	corroborated bool
	restored     bool
	present      bool
	eligible     bool
	finished     bool
	resume       map[string]struct{}
	taskTarget   *kimiDiscoveredTask
	parentOutput *kimiTaskOutputAnchor
}

type kimiDiscoveredTask struct {
	address                kimiTaskAddress
	kind, childID, command string
	invocation             *kimiChildInvocation
	complete               bool
	explicitBackground     bool
}

func (a *Agent) observeChildInvocation(run *kimiRun, op kimiTranscriptOperation, frame kimiTranscriptToolFrame, source kimiFrame, position kimiInvocationPosition, eligible bool) *kimiChildInvocation {
	if !kimiToolSpawns(frame.Name) && frame.Name != contracts.KimiToolBash {
		return nil
	}
	id := kimiTranscriptToolIdentity{sessionID: source.SessionID, agentID: run.agentID, turnID: op.TurnID, stepID: op.StepID, frameID: frame.FrameID, toolCallID: frame.ToolCallID}
	if id.sessionID == "" || id.turnID == "" || id.stepID == "" || id.frameID == "" || id.toolCallID == "" {
		return nil
	}
	var input struct {
		Description string            `json:"description"`
		Resume      string            `json:"resume"`
		ResumeIDs   map[string]string `json:"resume_agent_ids"`
		Command     string            `json:"command"`
		Background  bool              `json:"run_in_background"`
	}
	if json.Unmarshal(frame.Input, &input) != nil {
		return nil
	}
	refs := make(map[string]struct{}, len(frame.AgentRefs))
	for _, ref := range frame.AgentRefs {
		if kimiCheckID("child agent", ref.AgentID) != nil || ref.AgentID == kimiMainAgentID || ref.AgentID == run.agentID {
			return nil
		}
		if _, duplicate := refs[ref.AgentID]; duplicate {
			return nil
		}
		if frame.Name == contracts.KimiToolAgent && ref.Role != "child" || frame.Name == contracts.KimiToolAgentSwarm && ref.Role != "member" {
			return nil
		}
		if childRun := a.run(ref.AgentID); childRun.childParentAgentID != "" && childRun.childParentAgentID != run.agentID {
			return nil
		}
		refs[ref.AgentID] = struct{}{}
	}
	if run.childInvocations == nil {
		run.childInvocations = make(map[kimiTranscriptToolIdentity]*kimiChildInvocation)
	}
	invocation := run.childInvocations[id]
	if invocation == nil {
		invocation = &kimiChildInvocation{identity: id, name: frame.Name, targets: make(map[string]*kimiInvocationTarget)}
		run.childInvocations[id] = invocation

	} else if invocation.name != frame.Name {
		return nil
	}
	invocation.label = input.Description
	if kimiToolSpawns(frame.Name) && invocation.parentOutput == nil {
		invocation.parentOutput = a.captureTaskOutputAnchor(a.runSink(run), source.Type == contracts.KimiEventTranscriptReset || run.transcript.activeTurn != id.turnID || !run.turnActive)
	}
	if position.turn != nil {
		invocation.position.turn = position.turn
	}
	if position.step != nil {
		invocation.position.step = position.step
	}
	invocation.present = true
	invocation.eligible = invocation.eligible || eligible
	invocation.restored = source.Type == contracts.KimiEventTranscriptReset
	invocation.resume = make(map[string]struct{})
	if input.Resume != "" {
		invocation.resume[input.Resume] = struct{}{}
	}
	for childID := range input.ResumeIDs {
		invocation.resume[childID] = struct{}{}
	}
	if frame.Name == contracts.KimiToolAgent && input.Resume != "" {
		if _, matches := refs[input.Resume]; !matches {
			return nil
		}
		refs = map[string]struct{}{input.Resume: {}}
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
			invocation.targets[childID] = &kimiInvocationTarget{}
		}
	}
	invocation.finished = invocation.finished || frame.State == contracts.KimiTranscriptToolDone || frame.State == contracts.KimiTranscriptToolError || run.transcript.turn(op.TurnID).finished
	defer invocation.finishUnmatchedTargets()
	var output string
	if json.Unmarshal(frame.Output, &output) != nil {
		return invocation
	}
	if frame.Name == contracts.KimiToolAgent {
		if evidence, valid := parseKimiAgentResult(output, frame.State); valid {
			if _, matches := refs[evidence.childID]; !matches {
				return nil
			}
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
			if evidence.taskID != "" {
				invocation.retainDiscoveredTask(&kimiDiscoveredTask{address: kimiTaskAddress{ownerAgentID: run.agentID, taskID: evidence.taskID}, kind: "subagent", childID: evidence.childID, invocation: invocation})
			}
		}
	} else if frame.Name == contracts.KimiToolAgentSwarm && frame.State == contracts.KimiTranscriptToolDone {
		evidence := parseKimiSwarmResult(frame.Input, output, refs)
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
	} else if frame.Name == contracts.KimiToolBash && frame.State == contracts.KimiTranscriptToolDone {
		if taskID, valid := parseKimiBashTaskCandidate(output); valid {
			invocation.retainDiscoveredTask(&kimiDiscoveredTask{address: kimiTaskAddress{ownerAgentID: run.agentID, taskID: taskID}, kind: "shell", command: input.Command, invocation: invocation, explicitBackground: input.Background})
		}
	}
	return invocation
}

func (a *Agent) invocationTargetsTrusted(run *kimiRun, invocation *kimiChildInvocation) bool {
	if invocation.corroborated || !invocation.restored {
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
		target := invocation.taskTarget
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
		if descriptor := invocation.taskTarget; descriptor != nil && descriptor.childID == childID {
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
	if invocation.taskTarget != nil {
		a.scheduleDiscoveredNativeTask(run, invocation.taskTarget)
	}
}

// preScanChildInvocations records full identities before replaying snapshot results.
func (a *Agent) preScanChildInvocations(run *kimiRun, source kimiFrame, snapshot *kimiTranscriptSnapshot, previous kimiTranscript) {
	for _, invocation := range run.childInvocations {
		invocation.present = false
	}
	a.Mu.Lock()
	attachedAt := a.attachedAt
	a.Mu.Unlock()
	for _, raw := range snapshot.Items {
		var turn struct {
			Kind, TurnID, State, EndedAt string
			TriggerPromptID              string `json:"triggerPromptId"`
			Ordinal                      *int64
			Steps                        []json.RawMessage
		}
		if json.Unmarshal(raw, &turn) != nil || turn.Kind != contracts.KimiTranscriptNodeTurn || turn.TurnID == "" {
			continue
		}
		transcriptTurn := run.transcript.turn(turn.TurnID)
		if prior := previous.turns[turn.TurnID]; prior != nil {
			transcriptTurn.triggerPromptID = prior.triggerPromptID
		}
		transcriptTurn.ordinal = turn.Ordinal
		a.observePromptTrigger(run, transcriptTurn, source.SessionID, turn.TriggerPromptID)
		eligible := turn.State == contracts.KimiTranscriptTurnRunning || turn.State == contracts.KimiTranscriptTurnQueued
		if ended, err := time.Parse(time.RFC3339Nano, turn.EndedAt); err == nil && !ended.Before(attachedAt) {
			eligible = true
		}
		for _, stepRaw := range turn.Steps {
			var step struct {
				StepID  string
				Ordinal *int64
				Frames  []json.RawMessage
			}
			if json.Unmarshal(stepRaw, &step) != nil || step.StepID == "" {
				continue
			}
			transcriptTurn.step(step.StepID).ordinal = step.Ordinal
			for _, rawFrame := range step.Frames {
				a.observeConsumedPrompts(run, source.SessionID, turn.TurnID, step.StepID, rawFrame)
				var frame kimiTranscriptToolFrame
				if json.Unmarshal(rawFrame, &frame) != nil || frame.Kind != contracts.KimiTranscriptFrameTool {
					continue
				}
				a.observeChildInvocation(run, kimiTranscriptOperation{TurnID: turn.TurnID, StepID: step.StepID}, frame, source, kimiInvocationPosition{turn: turn.Ordinal, step: step.Ordinal}, eligible)
			}
		}
	}
}

// kimiInvocationGroupKey identifies the complete parent operation, including
// repeated model call IDs in different native turns, steps, or frames.
func kimiInvocationGroupKey(identity kimiTranscriptToolIdentity) string {
	value := fmt.Sprintf("%q/%q/%q/%q/%q/%q", identity.sessionID, identity.agentID, identity.turnID, identity.stepID, identity.frameID, identity.toolCallID)
	digest := sha256.Sum256([]byte(value))
	return identity.sessionID + "/invocation-group/" + hex.EncodeToString(digest[:])
}

// A finished parent operation supplies no child outcome by itself. A validated
// background descriptor explicitly describes continuing coordinator work.
func (invocation *kimiChildInvocation) finishUnmatchedTargets() {
	if !invocation.finished {
		return
	}
	for childID, target := range invocation.targets {
		if target.status != bgtask.StatusUnspecified {
			continue
		}
		if continuation := invocation.taskTarget; continuation != nil && continuation.kind == "subagent" && continuation.childID == childID {
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
