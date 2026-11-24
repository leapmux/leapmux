package commandcode

import (
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type commandCodeNativeReply struct {
	wireTurnID string
	receipt    agent.NativeInputReceipt
	started    bool
	unproved   bool
	outcome    leapmuxv1.AgentInputTurnOutcome
	items      []agent.NativeReplyItem
	keys       map[string]bool
	steers     []agent.NativeReplyBoundary
}

const commandCodeReplyInputOrdinal int64 = 1

func (a *Agent) commandCodeDurableTurnID(wireTurnID string) string {
	return a.replyProcessID + ":" + wireTurnID
}

func (a *Agent) beginCommandCodeReplyStart() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replyStartPending = true
}

// finishCommandCodeReplyStart identifies the captured turn. The native
// output can start and finish before the response confirms its ID.
func (a *Agent) finishCommandCodeReplyStart(wireTurnID string, receipt agent.NativeInputReceipt) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if receipt.InputID != "" {
		reply := a.commandCodeReplyForTurn(wireTurnID)
		reply.receipt = receipt
		if a.replyStreamClosed && reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		a.publishCommandCodeReply(reply)
	}
	a.replyStartPending = false
	for id, reply := range a.nativeReplies {
		if reply.receipt.InputID == "" {
			if len(reply.steers) == 0 {
				delete(a.nativeReplies, id)
			} else {
				a.publishCommandCodeReply(reply)
			}
		}
	}
}

func (a *Agent) commandCodeReplyForTurn(turnID string) *commandCodeNativeReply {
	if turnID == "" {
		return nil
	}
	if reply := a.nativeReplies[turnID]; reply != nil {
		return reply
	}
	if !a.replyStartPending {
		return nil
	}
	return a.registerCommandCodeReply(turnID)
}

func (a *Agent) registerCommandCodeReply(turnID string) *commandCodeNativeReply {
	if a.nativeReplies == nil {
		a.nativeReplies = make(map[string]*commandCodeNativeReply)
	}
	reply := &commandCodeNativeReply{
		wireTurnID: turnID,
		receipt:    agent.NativeInputReceipt{TurnID: a.commandCodeDurableTurnID(turnID)},
	}
	a.nativeReplies[turnID] = reply
	return reply
}

// Native event rows lack a turn ID. Only the start notification opens a
// bracket for their ownership. The response alone proves admission.
func (a *Agent) observeCommandCodeReplyStart(turnID string) {
	if turnID == "" {
		return
	}
	if a.replyOutputTurnID != "" && a.replyOutputTurnID != turnID {
		if previous := a.nativeReplies[a.replyOutputTurnID]; previous != nil {
			previous.unproved = true
		}
		a.flushPendingRunError()
		if err := a.persistCommandCodeGeneration(agent.MessageCompletionInterrupted, true); err != nil {
			slog.Warn("retain interrupted Command Code output before a new turn", "agent_id", a.AgentID(), "error", err)
		}
		// A scope that the queue refused must never move to the new turn.
		providerkit.DiscardGenerationScopes(&a.generation, a.sink, a.generation.ScopeIDs())
	}
	a.replyOutputTurnID = turnID
	if reply := a.commandCodeReplyForTurn(turnID); reply != nil {
		reply.started = true
	}
}

func (reply *commandCodeNativeReply) recordItem(key string, err error) {
	if !agent.OutputRetained(err) {
		reply.unproved = true
		return
	}
	if reply.keys == nil {
		reply.keys = make(map[string]bool)
	}
	reply.keys[key] = true
	reply.items = append(reply.items, agent.NativeReplyItem{
		Ordinal:    commandCodeReplyInputOrdinal + int64(len(reply.items)) + 1,
		MessageKey: key,
	})
}

func (a *Agent) recordCommandCodeSteerBoundary(wireTurnID string, receipt agent.NativeInputReceipt) bool {
	if receipt.SessionID == "" || wireTurnID == "" || wireTurnID != a.replyOutputTurnID {
		return false
	}
	reply := a.nativeReplies[wireTurnID]
	if reply == nil {
		reply = a.registerCommandCodeReply(wireTurnID)
		reply.started = true
	}
	if reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
		reply.receipt.TurnID != receipt.TurnID ||
		reply.receipt.SessionID != "" && reply.receipt.SessionID != receipt.SessionID {
		return false
	}
	reply.receipt.SessionID = receipt.SessionID
	reply.steers = append(reply.steers, agent.NativeReplyBoundary{
		InputID: receipt.InputID, AfterOrdinal: commandCodeReplyInputOrdinal + int64(len(reply.items)),
	})
	return true
}

func (a *Agent) persistCommandCodeReplyText(raw []byte, seq uint64) {
	turnID := a.replyOutputTurnID
	reply := a.nativeReplies[turnID]
	if reply == nil {
		a.persist(raw, agent.SpanInfo{})
		return
	}
	content := a.messageContent(raw)
	content.NativeTurnID = reply.receipt.TurnID
	if seq != 0 {
		content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:event:%d", content.NativeTurnID, seq)
	} else {
		content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:row:%d", content.NativeTurnID, len(reply.items))
	}
	if reply.keys[content.IdempotencyKey] {
		return
	}
	a.flushPendingRunError()
	err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	if err != nil {
		slog.Warn("persist a Command Code reply", "agent_id", a.AgentID(), "error", err)
	}
	reply.recordItem(content.IdempotencyKey, err)
}

// Persist generation in its original order. A textual scope from the open
// native turn belongs to that reply when a steer or completion flushes it.
func (a *Agent) persistCommandCodeGeneration(completion agent.MessageCompletion, completeProgress bool) error {
	turnID := a.replyOutputTurnID
	reply := a.nativeReplies[turnID]
	discarding := a.IsDiscardingOutput()
	if reply == nil || discarding {
		if completeProgress {
			return providerkit.FlushGeneration(&a.generation, a.sink, completion, discarding)
		}
		return a.generation.PersistAllTo(completion, a.sink, agent.MessageContent{Completion: completion}, agent.SpanInfo{})
	}
	var result error
	for _, scopeID := range a.generation.ScopeIDs() {
		kind, _, open := a.generation.ScopeSnapshot(scopeID)
		if !open {
			continue
		}
		content := a.messageContent(nil)
		content.Completion = completion
		if kind == agent.AssembledMessageKindText {
			content.NativeTurnID = reply.receipt.TurnID
			content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:assembled:%d", content.NativeTurnID, len(reply.items))
		}
		stored, err := a.generation.PersistScopeTo(scopeID, completion, a.sink, content, agent.SpanInfo{})
		result = errors.Join(result, err)
		if completeProgress {
			a.sink.ReportProgress(agent.CompleteModelProgress(scopeID))
		}
		if kind == agent.AssembledMessageKindText {
			if stored {
				reply.recordItem(content.IdempotencyKey, err)
			} else {
				reply.unproved = true
			}
		}
	}
	return result
}

func (a *Agent) finishCommandCodeNativeReply(turnID, reason string) {
	if a.replyOutputTurnID == turnID {
		a.replyOutputTurnID = ""
	}
	reply := a.commandCodeReplyForTurn(turnID)
	if reply == nil || reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		return
	}
	switch reason {
	case "end_turn":
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "interrupted":
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "run_error":
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	a.publishCommandCodeReply(reply)
}

func (a *Agent) publishCommandCodeReply(reply *commandCodeNativeReply) {
	receipt, outcome := reply.receipt, reply.outcome
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
		receipt.InputID == "" && (a.replyStartPending || len(reply.steers) == 0) {
		return
	}
	delete(a.nativeReplies, reply.wireTurnID)
	inputs := reply.steers
	if receipt.InputID != "" {
		inputs = append([]agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: commandCodeReplyInputOrdinal}}, inputs...)
	}
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		if reply.started && !reply.unproved {
			err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
				SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome,
				Inputs: inputs,
				Items:  reply.items,
			})
			if agent.OutputRetained(err) {
				return
			}
			slog.Error("store the exact Command Code reply", "agent_id", a.AgentID(), "error", err)
		}
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	for _, input := range inputs {
		receipt.InputID = input.InputID
		a.retireCommandCodeInput(receipt, outcome)
	}
}

func (a *Agent) retireCommandCodeInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("record the Command Code reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireCommandCodeRepliesAfterExit() {
	for _, reply := range a.nativeReplies {
		if reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		a.publishCommandCodeReply(reply)
	}
}
