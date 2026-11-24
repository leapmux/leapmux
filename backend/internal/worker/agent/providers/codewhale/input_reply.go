package codewhale

import (
	"log/slog"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// codewhalePendingReply holds only output from one native turn.
// The HTTP response identifies which of the observed turns accepted the input.
type codewhalePendingReply struct {
	receipt  agent.NativeInputReceipt
	outcome  leapmuxv1.AgentInputTurnOutcome
	unproved bool
	keys     map[string]bool
	items    []agent.NativeReplyItem
	steers   []agent.NativeReplyBoundary
}

const codewhaleReplyInputOrdinal int64 = 1

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
	a.replyStarts++
}

// finishCodewhaleReplyStart binds the turn returned by the POST. The stream can
// finish publishing it before the response arrives.
func (a *Agent) finishCodewhaleReplyStart(receipt agent.NativeInputReceipt) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if receipt.InputID != "" {
		reply := a.codewhaleReplyForTurn(receipt.TurnID)
		reply.receipt = receipt
		if a.replyStreamClosed && reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		a.publishCodewhaleReply(reply)
	}
	a.replyStarts--
	if a.replyStarts == 0 {
		for turnID, reply := range a.replyTurns {
			if reply.receipt.InputID == "" {
				if len(reply.steers) == 0 {
					delete(a.replyTurns, turnID)
				} else {
					a.publishCodewhaleReply(reply)
				}
			}
		}
	}
}

// codewhaleReplyForTurn also observes turns that arrive before the response,
// while a start request can still identify them. The caller holds dispatchMu.
func (a *Agent) codewhaleReplyForTurn(turnID string) *codewhalePendingReply {
	if turnID == "" {
		return nil
	}
	if existing := a.replyTurns[turnID]; existing != nil {
		return existing
	}
	if a.replyStarts == 0 {
		return nil
	}
	return a.registerCodewhaleReply(turnID)
}

func (a *Agent) registerCodewhaleReply(turnID string) *codewhalePendingReply {
	if a.replyTurns == nil {
		a.replyTurns = make(map[string]*codewhalePendingReply)
	}
	reply := &codewhalePendingReply{receipt: agent.NativeInputReceipt{TurnID: turnID}}
	a.replyTurns[turnID] = reply
	return reply
}

func (reply *codewhalePendingReply) recordItem(key string, err error) {
	if !agent.OutputRetained(err) {
		reply.unproved = true
		return
	}
	if reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED || reply.keys[key] {
		return
	}
	if reply.keys == nil {
		reply.keys = make(map[string]bool)
	}
	reply.keys[key] = true
	reply.items = append(reply.items, agent.NativeReplyItem{
		Ordinal:    codewhaleReplyInputOrdinal + int64(len(reply.items)) + 1,
		MessageKey: key,
	})
}

func (a *Agent) recordCodewhaleSteerBoundary(receipt agent.NativeInputReceipt) bool {
	if receipt.SessionID == "" || a.currentThreadID() != receipt.SessionID || receipt.TurnID == "" || receipt.InputID == "" {
		return false
	}
	reply := a.replyTurns[receipt.TurnID]
	if reply == nil {
		reply = a.registerCodewhaleReply(receipt.TurnID)
	}
	if reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
		reply.receipt.SessionID != "" && reply.receipt.SessionID != receipt.SessionID {
		return false
	}
	reply.receipt.SessionID = receipt.SessionID
	reply.steers = append(reply.steers, agent.NativeReplyBoundary{
		InputID: receipt.InputID, AfterOrdinal: codewhaleReplyInputOrdinal + int64(len(reply.items)),
	})
	return true
}

func (a *Agent) markCodewhaleReplyUnproved(turnID string) {
	if reply := a.codewhaleReplyForTurn(turnID); reply != nil {
		reply.unproved = true
	}
}

func (a *Agent) finishCodewhaleNativeReply(turnID, status string) {
	reply := a.codewhaleReplyForTurn(turnID)
	if reply == nil || reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		return
	}
	switch status {
	case contracts.CodewhaleTurnStatusCompleted:
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case contracts.CodewhaleTurnStatusInterrupted, contracts.CodewhaleTurnStatusCanceled:
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case contracts.CodewhaleTurnStatusFailed:
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	a.publishCodewhaleReply(reply)
}

func (a *Agent) publishCodewhaleReply(reply *codewhalePendingReply) {
	receipt, outcome := reply.receipt, reply.outcome
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
		receipt.InputID == "" && (a.replyStarts > 0 || len(reply.steers) == 0) {
		return
	}
	delete(a.replyTurns, receipt.TurnID)
	inputs := reply.steers
	if receipt.InputID != "" {
		inputs = append([]agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: codewhaleReplyInputOrdinal}}, inputs...)
	}
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		if !reply.unproved {
			err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
				SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome,
				Inputs: inputs,
				Items:  reply.items,
			})
			if agent.OutputRetained(err) {
				return
			}
			slog.Error("Store the exact Codewhale reply", "agent_id", a.AgentID(), "error", err)
		}
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	for _, input := range inputs {
		receipt.InputID = input.InputID
		a.retireCodewhaleInput(receipt, outcome)
	}
}

func (a *Agent) retireCodewhaleInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("Record the Codewhale reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

// retireCodewhaleRepliesAfterExit keeps already observed completions for
// responses still in flight. The caller holds dispatchMu.
func (a *Agent) retireCodewhaleRepliesAfterExit() {
	a.replyStreamClosed = true
	for _, reply := range a.replyTurns {
		if reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		a.publishCodewhaleReply(reply)
	}
}
