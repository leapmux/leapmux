package commandcode

import (
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) commandCodeDurableTurnID(wireTurnID string) string {
	return a.replyProcessID + ":" + wireTurnID
}

func (a *Agent) beginCommandCodeReplyStart() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replies.BeginStart()
}

// finishCommandCodeReplyStart identifies the captured turn. The native
// output can start and finish before the response confirms its ID.
func (a *Agent) finishCommandCodeReplyStart(wireTurnID string, receipt agent.NativeInputReceipt) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replies.FinishStart(wireTurnID, receipt, a.sink)
}

// Native event rows lack a turn ID. Only the start notification opens a
// bracket for their ownership. The response alone proves admission.
func (a *Agent) observeCommandCodeReplyStart(turnID string) {
	if turnID == "" {
		return
	}
	if a.replyOutputTurnID != "" && a.replyOutputTurnID != turnID {
		if previous := a.replies.Lookup(a.replyOutputTurnID); previous != nil {
			previous.MarkUnproved()
		}
		a.flushPendingRunError()
		if err := a.persistCommandCodeGeneration(agent.MessageCompletionInterrupted, true); err != nil {
			slog.Warn("retain interrupted Command Code output before a new turn", "agent_id", a.AgentID(), "error", err)
		}
		// A scope that the queue refused must never move to the new turn.
		providerkit.DiscardGenerationScopes(&a.generation, a.sink, a.generation.ScopeIDs())
	}
	a.replyOutputTurnID = turnID
	a.replies.MarkStarted(turnID)
}

func (a *Agent) recordCommandCodeSteerBoundary(wireTurnID string, receipt agent.NativeInputReceipt) bool {
	if receipt.SessionID == "" || wireTurnID == "" || wireTurnID != a.replyOutputTurnID {
		return false
	}
	if !a.replies.RecordSteerBoundary(wireTurnID, receipt) {
		return false
	}
	a.replies.MarkStarted(wireTurnID)
	return true
}

func (a *Agent) persistCommandCodeReplyText(raw []byte, seq uint64) {
	turnID := a.replyOutputTurnID
	reply := a.replies.Lookup(turnID)
	if reply == nil {
		a.persist(raw, agent.SpanInfo{})
		return
	}
	content := a.messageContent(raw)
	content.NativeTurnID = reply.TurnID()
	if seq != 0 {
		content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:event:%d", content.NativeTurnID, seq)
	} else {
		content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:row:%d", content.NativeTurnID, reply.ItemCount())
	}
	if reply.HasKey(content.IdempotencyKey) {
		return
	}
	a.flushPendingRunError()
	err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	if err != nil {
		slog.Warn("persist a Command Code reply", "agent_id", a.AgentID(), "error", err)
	}
	reply.RecordItem(content.IdempotencyKey, err)
}

// Persist generation in its original order. A textual scope from the open
// native turn belongs to that reply when a steer or completion flushes it.
func (a *Agent) persistCommandCodeGeneration(completion agent.MessageCompletion, completeProgress bool) error {
	turnID := a.replyOutputTurnID
	reply := a.replies.Lookup(turnID)
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
			content.NativeTurnID = reply.TurnID()
			content.IdempotencyKey = fmt.Sprintf("command-code-reply:%q:assembled:%d", content.NativeTurnID, reply.ItemCount())
		}
		stored, err := a.generation.PersistScopeTo(scopeID, completion, a.sink, content, agent.SpanInfo{})
		result = errors.Join(result, err)
		if completeProgress {
			a.sink.ReportProgress(agent.CompleteModelProgress(scopeID))
		}
		if kind == agent.AssembledMessageKindText {
			if stored {
				reply.RecordItem(content.IdempotencyKey, err)
			} else {
				reply.MarkUnproved()
			}
		}
	}
	return result
}

func (a *Agent) finishCommandCodeNativeReply(turnID, reason string) {
	if a.replyOutputTurnID == turnID {
		a.replyOutputTurnID = ""
	}
	var outcome leapmuxv1.AgentInputTurnOutcome
	switch reason {
	case "end_turn":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "interrupted":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "run_error":
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	a.replies.Finish(turnID, outcome, a.sink)
}

func (a *Agent) retireCommandCodeInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.replies.RetireInput(a.sink, receipt, outcome)
}

func (a *Agent) retireCommandCodeRepliesAfterExit() {
	a.replyOutputTurnID = ""
	a.replies.RetireAllAfterExit(a.sink)
}
