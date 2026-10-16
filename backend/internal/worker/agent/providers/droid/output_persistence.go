package droid

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type droidOutputKind uint8

const (
	droidOutputMessage droidOutputKind = iota
	droidOutputNotification
	droidOutputTurnEnd
)

// droidPendingOutput retains a record across a failed write.
// The record keeps its original completion and spans. Its publication owner also stays unchanged.
type droidPendingOutput struct {
	record agent.CapturedTranscript
	kind   droidOutputKind
}

func (output droidPendingOutput) persist() error {
	switch output.kind {
	case droidOutputNotification:
		_, err := output.record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		return err
	case droidOutputTurnEnd:
		return output.record.PersistTurnEnd()
	default:
		return output.record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	}
}

// enqueueOutput preserves native order without making lifecycle completion depend on storage availability.
// Event dispatch holds dispatchMu throughout each queue operation.
func (a *Agent) enqueueOutput(target droidOutputTarget, content agent.MessageContent, span agent.SpanInfo, kind droidOutputKind) {
	target.state.pending = append(target.state.pending, droidPendingOutput{
		record: agent.CaptureTranscript(target.sink, content, span), kind: kind,
	})
	a.flushPendingOutput(target)
}

func (a *Agent) flushPendingOutput(target droidOutputTarget) {
	for len(target.state.pending) > 0 {
		if err := target.state.pending[0].persist(); err != nil {
			slog.Error("droid: persist transcript failed", "agent_id", a.AgentID(), "child_session_id", target.childSessionID, "error", err)
			return
		}
		target.state.pending[0] = droidPendingOutput{}
		target.state.pending = target.state.pending[1:]
	}
	target.state.pending = nil
}

// releaseEndedOutput removes a finished child's state only after its last retained write succeeds.
func (a *Agent) releaseEndedOutput(target droidOutputTarget) {
	if target.childSessionID != "" && target.state.ended && len(target.state.pending) == 0 {
		delete(a.outputStates, target.childSessionID)
	}
}

// finishOutput makes one final persistence attempt after every native output reader stops.
// Captured records keep their original completion even when process exit supplies a different one.
func (a *Agent) finishOutput(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	for sessionID, state := range a.outputStates {
		target, known := a.outputTargetFor(sessionID)
		if !known {
			target = droidOutputTarget{state: state, childSessionID: sessionID}
		} else if !a.IsDiscardingOutput() {
			a.persistRemainingGeneration(target, completion)
		}
		a.flushPendingOutput(target)
		a.releaseEndedOutput(target)
	}
}
