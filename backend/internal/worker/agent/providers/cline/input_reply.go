package cline

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// inputReply follows one explicit lead input. The receipt is set before the
// command leaves. Agent.Mu protects the observations made afterwards.
type inputReply struct {
	receipt    agent.NativeInputReceipt
	started    bool
	ambiguous  bool
	agentDone  bool
	autonomous bool
	// Keep reply row identities in case the next run starts before the
	// command returns its result.
	items  []agent.NativeReplyItem
	steers []agent.NativeReplyBoundary
	// resultSeen is also set if the connection loses the reply.
	resultSeen bool
	result     inputResult
	end        *nativeRunEnd
}

func (r *inputReply) ownsOutput(sessionID string) bool {
	return r != nil && r.receipt.SessionID == sessionID && r.started && !r.ambiguous
}

type nativeRunEnd struct {
	sessionID  string
	eventName  string
	eventID    string
	trimmedRaw []byte
	reason     string
	result     inputResult
}

// inputResult identifies the AgentResult that Cline serializes in both the
// native run end and the command reply. Keep only its canonical digest and
// finish reason.
type inputResult struct {
	reason string
	digest [sha256.Size]byte
}

// trimAgentResult drops conversation history, tool calls, and static model
// metadata from an AgentResult JSON object so both persisted turn-end rows and
// canonical result digests skip unbounded replay payloads.
func trimAgentResult(raw json.RawMessage) (json.RawMessage, string, bool) {
	fields, ok := providerkit.RawObject(raw)
	if !ok {
		return nil, "", false
	}
	finishReason := providerkit.ObjectString(fields, "finishReason")
	delete(fields, "messages")
	delete(fields, "toolCalls")
	if model, ok := providerkit.RawObject(fields["model"]); ok {
		delete(model, "info")
		if encoded, err := json.Marshal(model); err == nil {
			fields["model"] = encoded
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, "", false
	}
	return encoded, finishReason, true
}

func readInputResult(raw json.RawMessage) inputResult {
	trimmed, reason, ok := trimAgentResult(raw)
	if !ok || reason == "" {
		return inputResult{}
	}
	digest, err := agent.JSONCanonicalDigest(trimmed)
	if err != nil {
		return inputResult{}
	}
	return inputResult{reason: reason, digest: digest}
}

// recordInputResult pairs the command reply with the native run end, in
// either arrival order. The reader routes replies apart from event dispatch.
// A reconnect can replay the run end, but it cannot replay the command reply.
func (a *Agent) recordInputResult(sessionID, requestID string, result inputResult) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	if detached := a.detachedReply; detached != nil && detached.receipt.SessionID == sessionID && detached.receipt.InputID == requestID {
		detached.resultSeen, detached.result = true, result
		a.Mu.Unlock()
		a.publishDetachedReply()
		return true
	}
	reply := a.turn.inputReply
	if reply == nil || reply.receipt.SessionID != sessionID || reply.receipt.InputID != requestID {
		a.Mu.Unlock()
		return false
	}
	reply.resultSeen, reply.result = true, result
	end := reply.end
	a.Mu.Unlock()
	if end == nil {
		return false
	}
	a.finishNativeRun(*end)
	return true
}

func (end nativeRunEnd) outcome(reply *inputReply) leapmuxv1.AgentInputTurnOutcome {
	if !reply.ownsOutput(end.sessionID) || !reply.resultSeen ||
		end.result.reason == "" || end.reason != end.result.reason || reply.result != end.result {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
	switch end.eventName {
	case contracts.ClineEventRunCompleted:
		switch end.reason {
		case contracts.ClineRunReasonCompleted, contracts.ClineRunReasonMaxIterations:
			return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
		case contracts.ClineRunReasonMistakeLimit:
			// Cline publishes this reason through run.completed.
			return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
	case contracts.ClineEventRunAborted:
		if end.reason == contracts.ClineRunReasonAborted {
			return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
		}
	case contracts.ClineEventRunFailed:
		if end.reason == contracts.ClineRunReasonError || end.reason == contracts.ClineRunReasonMistakeLimit {
			return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
	}
	return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
}

// publishDetachedReply pairs the previous run's delayed result with the
// transcript recorded before the queue started its successor. The caller
// holds dispatchMu.
func (a *Agent) publishDetachedReply() {
	a.Mu.Lock()
	reply := a.detachedReply
	if reply == nil || !reply.resultSeen || (reply.end == nil && reply.result.reason != "") {
		a.Mu.Unlock()
		return
	}
	a.detachedReply = nil
	a.Mu.Unlock()
	if reply.end != nil && !a.IsDiscardingOutput() {
		if outcome := reply.end.outcome(reply); outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
				SessionID: reply.receipt.SessionID,
				TurnID:    reply.receipt.TurnID,
				Outcome:   outcome,
				Items:     reply.items,
				Inputs:    reply.replyBoundaries(),
			})
			if agent.OutputRetained(err) {
				return
			}
			slog.Error("cline retain the previous input reply", "agent_id", a.AgentID(), "error", err)
		}
	}
	a.retireInputReply(reply)
}

func (a *Agent) retireDetachedReply() {
	a.Mu.Lock()
	reply := a.detachedReply
	a.detachedReply = nil
	a.Mu.Unlock()
	a.retireInputReply(reply)
}

func (a *Agent) retireInputReply(reply *inputReply) {
	if reply == nil {
		return
	}
	for _, input := range reply.replyBoundaries() {
		receipt := reply.receipt
		receipt.InputID = input.InputID
		if err := a.sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
			slog.Error("cline retire the input reply", "agent_id", a.AgentID(), "error", err)
		}
	}
}

func (r *inputReply) replyBoundaries() []agent.NativeReplyBoundary {
	return append([]agent.NativeReplyBoundary{{InputID: r.receipt.InputID, AfterOrdinal: 1}}, r.steers...)
}
