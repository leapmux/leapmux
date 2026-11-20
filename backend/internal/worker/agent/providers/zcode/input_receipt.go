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
