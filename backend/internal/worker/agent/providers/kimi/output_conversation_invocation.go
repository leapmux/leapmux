package kimi

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A complete native document establishes which invocation items still exist.
// An item with a released body still has an entry in the document.
func (a *Agent) withdrawAbsentConversationInvocations(run *kimiRun, document *kimiConversationState) {
	for identity, invocation := range run.childInvocations {
		if identity.sessionID != document.sessionID || identity.agentID != document.agentID {
			continue
		}
		turn := document.turns[identity.turnID]
		if turn == nil || turn.items[identity.toolCallID] == nil {
			a.withdrawConversationInvocation(run, invocation)
		}
	}
}

func (a *Agent) withdrawConversationInvocation(run *kimiRun, invocation *kimiChildInvocation) {
	if invocation == nil || !invocation.present {
		return
	}
	invocation.present = false
	invocation.finished = true
	invocation.finishUnmatchedTargets()
	a.projectChildInvocation(run, invocation)
}

func (a *Agent) observeConversationInvocations(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, frame kimiConversationFrame, items []kimiConversationItem) {
	for _, item := range items {
		if _, retained := publication.requests[item.ItemID]; !retained {
			continue
		}
		completion := agent.MessageCompletion("")
		if item.Status == contracts.KimiConversationStatusEnded {
			completion = kimiConversationCompletion(item.Outcome)
		}
		output := item.Output
		if item.Error != nil {
			output, _ = json.Marshal(*item.Error)
		}
		anchor, err := publication.scope.CaptureTranscript(agent.MessageContent{}, agent.SpanInfo{})
		if err != nil {
			slog.Warn("capture the Kimi invocation output", "error", err)
			continue
		}
		a.Mu.Lock()
		attachedAt := a.attachedAt
		a.Mu.Unlock()
		eligible := turn.header.Status == contracts.KimiConversationStatusRunning
		if ended, err := time.Parse(time.RFC3339Nano, turn.header.EndedAt); err == nil && !ended.Before(attachedAt) {
			eligible = true
		}
		invocation := a.observeInvocation(run, kimiInvocationObservation{
			identity: kimiInvocationIdentity{sessionID: frame.SessionID, agentID: run.agentID, turnID: turn.header.TurnID, toolCallID: item.ItemID},
			position: kimiInvocationPosition{turn: turn.header.Ordinal, step: item.StepIndex}, name: *item.Name,
			input: item.Input, output: output, taskID: item.TaskID, refs: item.AgentRefs, completion: completion,
			eligible: eligible, restored: frame.Type == "turns", finished: item.Status == contracts.KimiConversationStatusEnded || turn.header.Status == contracts.KimiConversationStatusEnded,
			parentOutput: &kimiTaskOutputAnchor{captured: anchor},
		})
		if invocation == nil {
			continue
		}
		request := kimiConversationRequestItem(item)
		record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: frame.SessionID, AgentID: run.agentID, TurnID: turn.header.TurnID, Role: contracts.KimiConversationRecordRoleRequest}
		spanID := (kimiConversationRecordView{record: record, item: &request}).key()
		spawn := kimiSpawnFromArgs(spanID, *item.Name, item.Input)
		a.linkInvocationChildren(run, invocation, spawn)
		a.projectChildInvocation(run, invocation)
	}
}
