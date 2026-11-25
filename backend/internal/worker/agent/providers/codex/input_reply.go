package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// bindCodexInputReply consumes the result of this request. A turn/started
// notification can establish activity before this result arrives.
func (a *Agent) bindCodexInputReply(pending *codexPendingTurnStart, raw json.RawMessage, responseErr error) {
	receipt := pending.receipt
	if receipt.InputID == "" {
		return
	}
	turnID := codexReplyTurnID(raw)
	a.Mu.Lock()
	if a.replyStarts[receipt.InputID] != pending || pending.replySettled || pending.replyTurnID != "" {
		a.Mu.Unlock()
		return
	}
	if responseErr != nil || turnID == "" {
		delete(a.replyStarts, receipt.InputID)
		var deliveryErr error
		var activitySettled bool
		if responseErr != nil {
			deliveryErr = pending.err
			if deliveryErr == nil {
				deliveryErr = classifyCodexTurnStartRequestError("turn/start", responseErr)
			}
		} else if a.pendingStart == pending {
			// Admission was confirmed without establishing reply ownership.
			activitySettled = a.finishCodexTurnStartLocked(pending, "", nil)
		}
		a.Mu.Unlock()
		outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		if deliveryErr != nil && !errors.Is(deliveryErr, agent.ErrDeliveryUncertain) {
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		a.retireCodexNativeReply(receipt, outcome)
		a.finishCodexReply(pending, deliveryErr)
		if activitySettled {
			close(pending.done)
			a.PublishTurnActive()
		}
		return
	}
	pending.replyTurnID = turnID
	retained, ended := pending.endedBeforeAck[turnID]
	if ended {
		delete(a.replyStarts, receipt.InputID)
	}
	pending.endedBeforeAck = nil
	var settings, settled bool
	attaching := !ended && a.pendingStart == pending && a.mainTurn.id == turnID
	if attaching {
		settings, settled = a.attachCodexTurnStartLocked(pending, turnID)
	} else if ended && a.pendingStart == pending {
		settled = a.finishCodexTurnStartLocked(pending, turnID, nil)
	}
	a.Mu.Unlock()

	err := a.sink.BindNativeInputTurn(receipt, turnID)
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Codex native input binding", "error", err)
	}
	if !agent.OutputRetained(err) || ended && !retained {
		a.retireCodexNativeReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	if attaching {
		a.publishCodexTurnStart(pending, receipt.SessionID, turnID, settings, settled)
	} else if settled {
		close(pending.done)
	}
	a.PublishTurnActive()
	a.finishCodexReply(pending, nil)
}

func codexReplyTurnID(raw json.RawMessage) string {
	var result struct {
		Turn struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Turn.ID == "" || result.Turn.Items == nil {
		return ""
	}
	switch result.Turn.Status {
	case "inProgress", "completed", "interrupted", "failed":
		return result.Turn.ID
	default:
		return ""
	}
}

func (a *Agent) finishCodexReply(pending *codexPendingTurnStart, err error) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	finishCodexReplyLocked(pending, err)
}

func finishCodexReplyLocked(pending *codexPendingTurnStart, err error) {
	if !pending.replySettled {
		pending.replySettled, pending.replyErr = true, err
		close(pending.replyDone)
	}
}

// recordCodexSteerEcho retains the input row at the matching notification.
// The caller holds outputMu, which orders the echo with reply rows and turn end.
func (a *Agent) recordCodexSteerEcho(threadID, turnID, clientID string) {
	receipt := agent.NativeInputReceipt{SessionID: threadID, TurnID: turnID, InputID: clientID}
	report, matched := a.steerLedger.TakeEcho(receipt)
	if matched && a.retainCodexSteerResolution(report) {
		a.replySteers = append(a.replySteers, receipt)
	}
}

func (a *Agent) resolveCodexUnechoedSteers(threadID, turnID string, outcome agent.SteerOutcome) {
	// Each resolved steer moves ahead of the other resolved inputs. Resolve
	// newest first to preserve the original send order in the queue.
	for _, report := range slices.Backward(a.steerLedger.TakeUnechoed(threadID, turnID)) {
		report.Outcome = outcome
		report.Receipt = agent.NativeInputReceipt{}
		a.retainCodexSteerResolution(report)
	}
}

func (a *Agent) retainCodexSteerResolution(report agent.SteerResolution) bool {
	err := agent.RetainSteerResolution(a.sink, report)
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Codex steer outcome", "input_id", report.InputID, "error", err)
		return false
	}
	return true
}

// takeCodexSteerReplies releases echoed steers for the selected native turn,
// session, or process. The caller holds outputMu.
func (a *Agent) takeCodexSteerReplies(threadID, turnID string) []agent.NativeInputReceipt {
	var receipts []agent.NativeInputReceipt
	a.replySteers = slices.DeleteFunc(a.replySteers, func(receipt agent.NativeInputReceipt) bool {
		if (threadID != "" && receipt.SessionID != threadID) || (turnID != "" && receipt.TurnID != turnID) {
			return false
		}
		receipts = append(receipts, receipt)
		return true
	})
	return receipts
}

func (a *Agent) recordCodexReplyEnd(threadID, turnID string, retained bool) {
	if threadID == "" || turnID == "" {
		return
	}
	unproved := a.takeCodexSteerReplies(threadID, turnID)
	if retained {
		unproved = nil
	}
	a.Mu.Lock()
	for inputID, pending := range a.replyStarts {
		if pending.receipt.SessionID != threadID {
			continue
		}
		switch pending.replyTurnID {
		case turnID:
			delete(a.replyStarts, inputID)
			if !retained {
				unproved = append(unproved, pending.receipt)
			}
		case "":
			pending.endedBeforeAck[turnID] = retained
		}
	}
	a.Mu.Unlock()
	for _, receipt := range unproved {
		a.retireCodexNativeReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

type codexTurnControlSink struct {
	agent.ControlServices
	threadID, turnID string
}

func (s codexTurnControlSink) PublishControlRequest(request agent.ControlRequest) error {
	request.AgentSessionID, request.NativeTurnID = s.threadID, s.turnID
	return s.ControlServices.PublishControlRequest(request)
}

func (a *Agent) codexReplyControlSink(params json.RawMessage) agent.ControlServices {
	var owner struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(params, &owner) != nil || owner.ThreadID == "" || owner.TurnID == "" || !a.isMainThreadID(owner.ThreadID) {
		return a.sink
	}
	return codexTurnControlSink{ControlServices: a.sink, threadID: owner.ThreadID, turnID: owner.TurnID}
}

func (a *Agent) retireCodexInputReplies(threadID string) {
	receipts := a.takeCodexSteerReplies(threadID, "")
	a.Mu.Lock()
	for inputID, pending := range a.replyStarts {
		if threadID == "" || threadID == pending.receipt.SessionID {
			delete(a.replyStarts, inputID)
			receipts = append(receipts, pending.receipt)
			var err error
			if pending.replyTurnID == "" {
				err = fmt.Errorf("%w: Codex retired the request before confirming it", agent.ErrDeliveryUncertain)
			}
			finishCodexReplyLocked(pending, err)
		}
	}
	a.Mu.Unlock()
	for _, receipt := range receipts {
		a.retireCodexNativeReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retireCodexNativeReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("retain the Codex native input outcome", "error", err)
	}
}

func codexNativeReplyOutcome(status string) leapmuxv1.AgentInputTurnOutcome {
	switch status {
	case "completed":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "interrupted":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "failed":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}
