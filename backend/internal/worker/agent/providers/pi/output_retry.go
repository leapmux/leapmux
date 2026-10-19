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
	a.Mu.Unlock()
	if held != nil {
		content := piAgentEndContent(held.raw, a.currentPiUsageSnapshot(), piTurnDurationMs(startedAt, settledAt))
		if completion == agent.MessageCompletionInterrupted || held.interrupted || held.willRetry {
			content.Completion = agent.MessageCompletionInterrupted
		}
		a.persistPiAgentEnd(agent.WithToolUseCount(content, toolUses))
		providerkit.ScheduleOrCancelAPIErrorAutoContinue(a.orderedSink(), !held.willRetry && held.env.isRetryableFailure(), held.raw)
	}
	a.sink.ReportProgress(agent.ResetProgress())
	a.PublishTurnActive()
}
