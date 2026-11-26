package kimi

import (
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// refreshExactControls runs with dispatchMu held. The physical write can then
// use the immutable proof without inspecting the live native conversation.
func (a *Agent) refreshExactControls(run *kimiRun) {
	if run.agentID != kimiMainAgentID {
		return
	}
	a.Mu.Lock()
	if len(a.controls) == 0 {
		a.Mu.Unlock()
		return
	}
	var changed []*kimiPendingControl
	for requestID, control := range a.controls {
		if control.agentID != kimiMainAgentID {
			continue
		}
		inputs := exactControlInputs(run.conversation, control)
		previous := control.nativeInputs.Load()
		if previous == nil && len(inputs) == 0 || previous != nil && slices.Equal(previous.InputIDs, inputs) {
			continue
		}
		proof := &agent.NativeControlInputs{
			SessionID: control.sessionID, RequestID: requestID,
			InstanceID: control.instanceID, InputIDs: inputs,
		}
		control.nativeInputs.Store(proof)
		if control.published {
			changed = append(changed, control)
		}
	}
	a.Mu.Unlock()
	for _, control := range changed {
		a.applyExactControlInputs(control)
	}
}

// finishPublishedControlInputs records the proof that already rode
// PublishControlRequest and applies any newer update that arrived while
// PublishControlRequest was in flight.
func (a *Agent) finishPublishedControlInputs(control *kimiPendingControl, publishedProof *agent.NativeControlInputs) {
	control.inputsMu.Lock()
	if control.appliedInputs == nil {
		control.appliedInputs = publishedProof
	}
	needsUpdate := false
	if latest := control.nativeInputs.Load(); latest != nil && latest != control.appliedInputs {
		needsUpdate = true
	}
	control.inputsMu.Unlock()
	if needsUpdate {
		a.applyExactControlInputs(control)
	}
}

// applyExactControlInputs updates the published control's native input binding
// directly, matching PublishControlRequest and CancelControlRequestInstance so
// immediate withdrawals cannot overtake it and queued output writes cannot
// delay it.
func (a *Agent) applyExactControlInputs(control *kimiPendingControl) {
	control.inputsMu.Lock()
	defer control.inputsMu.Unlock()
	proof := control.nativeInputs.Load()
	if proof == nil || proof == control.appliedInputs {
		return
	}
	a.Mu.Lock()
	active := a.controls[proof.RequestID] == control && control.published && !control.answered && !a.StoppedLocked()
	a.Mu.Unlock()
	if !active {
		return
	}
	if err := agent.DirectProviderServices(a.sink).SetNativeControlInputs(*proof); err != nil {
		slog.Warn("retain the Kimi control binding", "error", err)
		return
	}
	control.appliedInputs = proof
}

func exactControlInputs(output *kimiConversationOutput, control *kimiPendingControl) []string {
	if output == nil || control.sessionID != output.document.sessionID ||
		control.toolCallID == "" || control.toolIdentityConflict || control.answered {
		return nil
	}
	owningTurn := exactControlToolTurn(output.document, control.toolCallID)
	if owningTurn == nil || owningTurn.header.Kind != contracts.KimiConversationTurnKindAgent ||
		owningTurn.header.Status != contracts.KimiConversationStatusRunning {
		return nil
	}
	publication := output.turns[owningTurn.header.TurnID]
	if publication == nil {
		return nil
	}
	toolStep := owningTurn.items[control.toolCallID].identity.stepIndex
	var inputs []string
	for _, submission := range publication.replyInputs {
		if submission.conflictingConsumption || submission.sessionID != control.sessionID ||
			submission.receiver != control.agentID || submission.turnID != owningTurn.header.TurnID {
			continue
		}
		consuming := owningTurn.items[submission.itemID]
		if consuming == nil {
			continue
		}
		inputStep := consuming.identity.stepIndex
		if submission.consumingOpener || inputStep != nil && toolStep != nil && *inputStep < *toolStep {
			inputs = append(inputs, submission.id)
		}
	}
	slices.Sort(inputs)
	return inputs
}

func exactControlToolTurn(document *kimiConversationState, toolCallID string) *kimiConversationTurnState {
	if document.toolCallTurns != nil {
		turnIDs := document.toolCallTurns[toolCallID]
		if len(turnIDs) != 1 {
			return nil
		}
		for turnID := range turnIDs {
			return document.turns[turnID]
		}
		return nil
	}
	var owningTurn *kimiConversationTurnState
	for _, turn := range document.turns {
		item := turn.items[toolCallID]
		if item == nil || item.identity.kind != contracts.KimiConversationItemKindToolCall {
			continue
		}
		if owningTurn != nil {
			return nil
		}
		owningTurn = turn
	}
	return owningTurn
}
