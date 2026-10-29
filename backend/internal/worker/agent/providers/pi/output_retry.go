package pi

import (
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
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
	a.settleHeldPiTurnAt(agent.MessageCompletionComplete, settledAt)
}

// settleHeldPiTurn closes a turn when its process or session ends before native settlement.
func (a *Agent) settleHeldPiTurn(completion agent.MessageCompletion) {
	a.settleHeldPiTurnAt(completion, a.Clock().Now())
}

func (a *Agent) settleHeldPiTurnAt(completion agent.MessageCompletion, settledAt time.Time) {
	a.Mu.Lock()
	held := a.heldDivider
	if !a.currentTurnActive && held == nil {
		a.Mu.Unlock()
		return
	}
	startedAt, toolUses := a.turnStartedAt, a.TurnToolUses
	a.heldDivider = nil
	a.currentTurnActive = false
	a.turnStartedAt = time.Time{}
	a.TurnToolUses = 0
	a.interruptRequests = nil
	// A turn that ends while the agent stops is not retried.
	stopping := a.StoppedLocked() || a.IntentionalStopRequested()
	a.Mu.Unlock()
	if held != nil {
		content := piAgentEndContent(held.raw, a.currentPiUsageSnapshot(), piTurnDurationMs(startedAt, settledAt))
		if completion == agent.MessageCompletionInterrupted || held.interrupted || held.willRetry {
			content.Completion = agent.MessageCompletionInterrupted
		}
		a.persistPiAgentEnd(agent.WithToolUseCount(content, toolUses))
		if held.willRetry {
			// Pi retries this failure itself, so LeapMux schedules no
			// continuation of its own, and a pending one is cancelled.
			a.orderedSink().CancelServerErrorRetry(piRetriesFailedRequestReason)
		} else {
			failure := piTurnFailure(held.env, held.interrupted || stopping, held.env.failedAt(a.Clock().Now()))
			providerkit.ApplyTurnFailure(a.orderedSink(), failure, held.raw)
		}
	}
	a.sink.ReportProgress(agent.ResetProgress())
	a.PublishTurnActive()
}

// piRetriesFailedRequestReason is the reason that a settled turn gives when
// Pi announced its own retry and the turn cancels the pending server-error
// retry.
const piRetriesFailedRequestReason = "Pi retries the failed request itself"
