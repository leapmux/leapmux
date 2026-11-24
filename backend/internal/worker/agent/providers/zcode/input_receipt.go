package zcode

import (
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// zcodeSeenInputTurns rejects duplicate starts and conflicting input or turn
// IDs. Both sets record the same admitted start under the provider mutex.
type zcodeSeenInputTurns struct {
	inputs map[string]bool
	turns  map[string]bool
}

func (seen *zcodeSeenInputTurns) record(inputID, turnID string) {
	if seen.inputs == nil {
		seen.inputs = make(map[string]bool)
		seen.turns = make(map[string]bool)
	}
	seen.inputs[inputID] = true
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
	if err := a.sink.BindNativeInputTurn(receipt, turnID); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Error("zcode record input turn binding", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireObservedInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if receipt.SessionID == "" || receipt.InputID == "" {
		return
	}
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Error("zcode record input retirement", "agent_id", a.AgentID(), "error", err)
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
func (a *Agent) zcodeReplyOutcome(owner *zcodeTurnState, sink zcodeProgressSink, completion agent.MessageCompletion) leapmuxv1.AgentInputTurnOutcome {
	unproved := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	if sink.nativeTurnID == "" {
		a.retireObservedInput(owner.nativeReceipt(), unproved)
	}
	if sink.nativeTurnID == "" || sink.nativeInputID != "" {
		a.retireZCodeTurnSteers(owner, unproved)
	}
	if sink.nativeTurnID == "" {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
	return zcodeInputTurnOutcome(completion)
}
