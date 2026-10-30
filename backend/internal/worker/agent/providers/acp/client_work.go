package acp

import (
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ClientWork owns one manual operation outside the native conversation. Its
// busy hold survives native turns, but a native start retires its retry verdict.
// Only the Base that admitted it can release the hold.
type ClientWork struct {
	base             *Base
	sessionID        string
	services         agent.ProviderServices
	queue            *agent.OutputQueue
	ownsRetryVerdict bool
}

// BeginClientWork admits manual work only while the input queue is idle.
// It creates no conversation output target or boundary. Mandatory native turns
// can still start and end while the manual operation remains unresolved.
func (b *Base) BeginClientWork() *ClientWork {
	if b.sink == nil {
		return nil
	}
	queue := agent.OutputQueueFor(b.sink)
	if queue == nil {
		return nil
	}
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.promptActive || b.clientWork != nil || b.sessionID == "" || b.StoppedLocked() {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return nil
	}
	work := &ClientWork{base: b, sessionID: b.sessionID, services: b.sink, queue: queue, ownsRetryVerdict: true}
	b.clientWork = work
	state, sequence := b.turnStateLocked(), b.NextTurnSeq()
	b.Mu.Unlock()
	b.turnMu.Unlock()
	if b.publishTurnActive != nil {
		b.publishTurnActive(state, sequence)
	}
	return work
}

// End releases only this operation's busy hold. Its verdict applies before
// release unless a native conversation started after admission. A duplicate
// completion or a completion from a replaced session changes nothing.
func (work *ClientWork) End(verdict TurnVerdict) bool {
	if work == nil || work.base == nil {
		return false
	}
	b := work.base
	verdict.Payload = slices.Clone(verdict.Payload)
	// Capture before validation so a replacement cannot lend its authority to
	// this completion. Queue position keeps this operation before its own idle.
	operation := work.services.CaptureAutoRetryOperation(0, func(writer agent.AutoRetryWriter) {
		providerkit.ApplyTurnFailure(writer, verdict.Failure, verdict.Payload)
	})
	b.turnMu.Lock()
	b.Mu.Lock()
	if b.clientWork != work || b.sessionID != work.sessionID {
		b.Mu.Unlock()
		b.turnMu.Unlock()
		return false
	}
	eligible := work.ownsRetryVerdict
	b.clientWork = nil
	state, sequence := b.turnStateLocked(), b.NextTurnSeq()
	var writes []agent.OutputWrite
	if eligible {
		writes = append(writes, agent.OutputWrite{Label: "apply client work retry verdict", Run: operation})
	}
	if publish := b.publishTurnActive; publish != nil {
		writes = append(writes, agent.OutputWrite{Label: "publish client work completion", Run: func() error { publish(state, sequence); return nil }})
	}
	work.queue.Append(writes...)
	b.Mu.Unlock()
	b.turnMu.Unlock()
	if err := agent.FlushPendingOutput(work.services); err != nil {
		slog.Error("retain client work completion", "agent_id", b.AgentID(), "error", err)
	}
	return true
}
