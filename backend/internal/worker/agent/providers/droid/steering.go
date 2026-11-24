package droid

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// droidSteerAttempt identifies one queue dispatch and its native inbox request.
type droidSteerAttempt struct {
	queueInputID string
	attemptID    string
	requestID    string
	sessionID    string
}

func (a *Agent) beginDroidSteer(params addUserMessageParams, requestID string, dispatch droidInputDispatch) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.pendingSteers == nil {
		a.pendingSteers = make(map[string]droidSteerAttempt)
	}
	a.pendingSteers[params.MessageID] = droidSteerAttempt{
		queueInputID: dispatch.queueInputID, attemptID: dispatch.attemptID,
		requestID: requestID, sessionID: params.SessionID,
	}
}

func (a *Agent) droidSteerResolvedAfterInputFailure(messageID string) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	_, pending := a.pendingSteers[messageID]
	delete(a.pendingSteers, messageID)
	return !pending
}

// onSteerEcho uses the committed message ID. Equal text is unrelated evidence.
// The caller holds dispatchMu.
func (a *Agent) onSteerEcho(requestID string, message droidReplyMessage, target droidOutputTarget) bool {
	steer, pending := a.pendingSteers[message.ID]
	if !pending || steer.sessionID != a.mainSessionID() || requestID != "" && requestID != steer.requestID {
		return false
	}
	delete(a.pendingSteers, message.ID)
	// A queued steer can start after the previous turn ends. Keep the queue occupied before the echo removes its item.
	a.startTurn()
	a.persistRemainingGeneration(target, agent.MessageCompletionComplete)
	a.flushPendingToolCalls(target)

	receipt := agent.NativeInputReceipt{SessionID: steer.sessionID, InputID: message.ID}
	if owner := a.droidReplyOwningMessage(message.ParentID); owner != nil {
		owner.admitSteer(message.ID)
		receipt.TurnID = owner.receipt.TurnID
	} else if a.currentReply == nil {
		// The previous tracked turn ended. A native completion must still
		// match this input before the Worker can publish its reply.
		receipt.TurnID = message.ID
		a.registerDroidReply(receipt, steer.requestID).observeLeadEcho()
	} else {
		a.currentReply.unproved = true
	}
	a.reportDroidSteer(target.sink, steer, agent.SteerOutcomeEchoed, receipt)
	if receipt.TurnID == "" {
		a.retireDroidInput(target.sink, receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	return true
}

func (a *Agent) onQueuedMessagesDiscarded(payload []byte, target droidOutputTarget) {
	if target.childSessionID == "" {
		requestID := providerkit.StringField(payload, "requestId")
		dropped := false
		for messageID, steer := range a.pendingSteers {
			if requestID == "" || requestID == steer.requestID {
				delete(a.pendingSteers, messageID)
				a.reportDroidSteer(target.sink, steer, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})
				dropped = true
			}
		}
		if dropped {
			return
		}
	}
	a.persistNotification(payload, target)
}

func (a *Agent) reportDroidSteer(sink agent.ProviderServices, steer droidSteerAttempt, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt) {
	err := agent.RetainSteerResolution(sink, agent.SteerResolution{
		InputID: steer.queueInputID, AttemptID: steer.attemptID, Outcome: outcome, Receipt: receipt,
	})
	if !agent.OutputRetained(err) {
		slog.Error("Record the Droid steer resolution", "agent_id", a.AgentID(), "input_id", steer.queueInputID, "error", err)
	}
}
