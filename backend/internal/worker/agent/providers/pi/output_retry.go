package pi

import (
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piHeldDivider retains the last run's end until the session settles.
// A continuation replaces this divider without storing an intermediate row.
type piHeldDivider struct {
	raw                    []byte
	env                    piAgentEndEnvelope
	willRetry, interrupted bool
}

func (a *Agent) handlePiAgentSettled(raw []byte, settledAt time.Time) {
	if providerkit.StringField(raw, "type") != contracts.PiEventAgentSettled {
		return
	}
	a.settleHeldPiTurnAt(agent.MessageCompletionComplete, settledAt, true)
}

// settleHeldPiTurn closes a turn when its process or session ends before native settlement.
func (a *Agent) settleHeldPiTurn(completion agent.MessageCompletion) {
	a.settleHeldPiTurnAt(completion, a.Clock().Now(), false)
}

func (a *Agent) settleHeldPiTurnAt(completion agent.MessageCompletion, settledAt time.Time, nativeSettled bool) {
	a.Mu.Lock()
	held := a.heldDivider
	reply := a.replyRun
	currentReply := reply != nil && reply.receipt.SessionID == a.sessionHandleLocked()
	a.replyRun = nil
	var abandoned agent.NativeInputReceipt
	if !nativeSettled {
		abandoned = a.replyPending
		a.replyPending = agent.NativeInputReceipt{}
	}
	if !a.currentTurnActive && held == nil && reply == nil && abandoned.InputID == "" {
		a.Mu.Unlock()
		return
	}
	startedAt, toolUses := a.turnStartedAt, a.TurnToolUses
	a.heldDivider = nil
	a.currentTurnActive = false
	a.turnStartedAt = time.Time{}
	a.TurnToolUses = 0
	a.interruptRequests = nil
	a.Mu.Unlock()
	interrupted := completion == agent.MessageCompletionInterrupted || (held != nil && (held.interrupted || held.willRetry))
	if interrupted {
		a.persistIncompletePiTools(agent.MessageCompletionInterrupted)
	}
	if held != nil {
		content := piAgentEndContent(held.raw, a.currentPiUsageSnapshot(), piTurnDurationMs(startedAt, settledAt))
		if interrupted {
			content.Completion = agent.MessageCompletionInterrupted
		}
		a.persistPiAgentEnd(agent.WithToolUseCount(content, toolUses))
		providerkit.ScheduleOrCancelServerErrorAutoRetry(a.orderedSink(), !held.willRetry && held.env.isRetryableFailure(), held.raw)
	}
	a.finishPiReply(reply, held, nativeSettled && currentReply)
	if abandoned.InputID != "" {
		a.retirePiReply(abandoned, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	if interrupted {
		a.ResetCumulativeOutput()
		providerkit.ResetSpansAndProgress(a.sink)
	} else {
		a.sink.ReportProgress(agent.ResetModelProgress())
	}
	a.PublishTurnActive()
}
