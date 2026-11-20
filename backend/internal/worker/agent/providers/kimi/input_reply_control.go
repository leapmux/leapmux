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
		a.retainExactControlInputs(control)
	}
}

func (a *Agent) retainExactControlInputs(control *kimiPendingControl) {
	if control.nativeInputs.Load() == nil {
		return
	}
	sink := agent.DirectProviderServices(a.sink)
	err := a.queueProviderWrite("bind the Kimi control inputs", func() error {
		if proof := control.nativeInputs.Load(); proof != nil {
			return sink.SetNativeControlInputs(*proof)
		}
		return nil
	})
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Kimi control binding", "error", err)
	}
}

func exactControlInputs(output *kimiConversationOutput, control *kimiPendingControl) []string {
	if output == nil || control.sessionID != output.document.sessionID ||
		control.proofToolCallID == "" || control.toolIdentityConflict || control.answered {
		return nil
	}
	var owningTurn *kimiConversationTurnState
	for _, turn := range output.document.turns {
		item := turn.items[control.proofToolCallID]
		if item == nil || item.identity.kind != contracts.KimiConversationItemKindToolCall {
			continue
		}
		if owningTurn != nil {
			return nil
		}
		owningTurn = turn
	}
	if owningTurn == nil || owningTurn.header.Kind != contracts.KimiConversationTurnKindAgent ||
		owningTurn.header.Status != contracts.KimiConversationStatusRunning {
		return nil
	}
	publication := output.turns[owningTurn.header.TurnID]
	if publication == nil {
		return nil
	}
	toolStep := owningTurn.items[control.proofToolCallID].identity.stepIndex
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
