package zcode

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// zcodeSeenInputTurns rejects duplicate starts and conflicting input or turn
// IDs. Both maps record the same admitted start under the provider mutex.
type zcodeSeenInputTurns struct {
	inputs map[string]string
	turns  map[string]bool
}

func (seen *zcodeSeenInputTurns) record(inputID, turnID string) {
	if seen.inputs == nil {
		seen.inputs = make(map[string]string)
		seen.turns = make(map[string]bool)
	}
	seen.inputs[inputID] = turnID
	seen.turns[turnID] = true
}

// nativeReceipt reads the immutable input and session carried by an owner.
func (owner *zcodeTurnState) nativeReceipt() agent.NativeInputReceipt {
	if owner == nil || owner.background {
		return agent.NativeInputReceipt{}
	}
	return agent.NativeInputReceipt{SessionID: owner.inputSessionID, InputID: owner.inputID}
}

func (a *Agent) bindObservedInputTurn(receipt agent.NativeInputReceipt, turnID string) {
	if receipt.SessionID == "" || receipt.InputID == "" || turnID == "" {
		return
	}
	if err := a.sink.BindNativeInputTurn(receipt, turnID); !agent.OutputRetained(err) {
		slog.Error("zcode record input turn binding", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireObservedInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if receipt.SessionID == "" || receipt.InputID == "" {
		return
	}
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("zcode record input retirement", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireUnprovedInputs(receipts []agent.NativeInputReceipt) {
	for _, receipt := range receipts {
		a.retireObservedInput(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retireZCodeTurnSteers(owner *zcodeTurnState, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.Mu.Lock()
	receipts := owner.steerReceipts
	owner.steerReceipts = nil
	a.Mu.Unlock()
	for _, receipt := range receipts {
		a.retireObservedInput(receipt, outcome)
	}
}

// zcodeReplyOutcome finalizes the inputs for which the turn still proves its
// output order. Recovery can prove the lead from its native start while losing
// the position of a steer within the turn.
func (a *Agent) zcodeReplyOutcome(owner *zcodeTurnState, scope zcodeTurnReplyScope, completion agent.MessageCompletion) leapmuxv1.AgentInputTurnOutcome {
	unproved := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	if scope.nativeTurnID == "" {
		a.retireObservedInput(owner.nativeReceipt(), unproved)
	}
	if scope.nativeTurnID == "" || scope.nativeInputID != "" {
		a.retireZCodeTurnSteers(owner, unproved)
	}
	if scope.nativeTurnID == "" {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
	return zcodeInputTurnOutcome(completion)
}
