package codewhale

import (
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func codewhaleInputReceipt(threadID, turnID string) agent.NativeInputReceipt {
	// Each ordinary Codewhale input creates its own native turn.
	return agent.NativeInputReceipt{SessionID: threadID, InputID: turnID, TurnID: turnID}
}

func codewhaleReplyMessageKey(turnID, itemID string) string {
	return "codewhale-reply:" + strconv.Itoa(len(turnID)) + ":" + turnID + itemID
}

func (a *Agent) beginCodewhaleReplyStart() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replies.BeginStart()
}

// finishCodewhaleReplyStart binds the turn returned by the POST. The stream can
// finish publishing it before the response arrives.
func (a *Agent) finishCodewhaleReplyStart(receipt agent.NativeInputReceipt) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replies.FinishStart(receipt.TurnID, receipt, a.sink)
}

func (a *Agent) recordCodewhaleSteerBoundary(receipt agent.NativeInputReceipt) bool {
	if receipt.SessionID == "" || a.currentThreadID() != receipt.SessionID {
		return false
	}
	return a.replies.RecordSteerBoundary(receipt.TurnID, receipt)
}

func (a *Agent) markCodewhaleReplyUnproved(turnID string) {
	a.replies.MarkUnproved(turnID)
}

func (a *Agent) finishCodewhaleNativeReply(turnID, status string) {
	var outcome leapmuxv1.AgentInputTurnOutcome
	switch status {
	case contracts.CodewhaleTurnStatusCompleted:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case contracts.CodewhaleTurnStatusFailed:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	a.replies.Finish(turnID, outcome, a.sink)
}

func (a *Agent) retireCodewhaleInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.replies.RetireInput(a.sink, receipt, outcome)
}

// retireCodewhaleRepliesAfterExit keeps already observed completions for
// responses still in flight. The caller holds dispatchMu.
func (a *Agent) retireCodewhaleRepliesAfterExit() {
	a.replies.RetireAllAfterExit(a.sink)
}
