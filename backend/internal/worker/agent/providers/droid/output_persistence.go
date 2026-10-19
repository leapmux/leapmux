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

// orderedSink shares one queue between transcript rows, steer reports, and turn release.
func (a *Agent) orderedSink() agent.ProviderServices {
	queue := agent.OutputQueueFor(a.sink)
	if queue == nil {
		queue = &a.outputQueue
	}
	return agent.NewOrderedProviderServices(queue, a.sink)
}

// enqueueOutput gives each immutable row to the shared queue before another native event can run.
// The target supplies its own sink and queue, including for a child transcript.
func (a *Agent) enqueueOutput(target droidOutputTarget, content agent.MessageContent, span agent.SpanInfo, kind droidOutputKind) {
	var err error
	switch kind {
	case droidOutputNotification:
		_, err = target.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
	case droidOutputTurnEnd:
		err = target.sink.PersistTurnEnd(content, span)
	default:
		err = target.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
	}
	if err != nil {
		slog.Error("droid: persist transcript failed", "agent_id", a.AgentID(), "child_session_id", target.childSessionID, "error", err)
	}
}

func (state *droidOutputState) hasPendingOutput() bool {
	return state.outputQueue != nil && state.outputQueue.HasPending()
}

func (a *Agent) flushPendingOutput(target droidOutputTarget) {
	if target.state.outputQueue == nil {
		return
	}
	if err := target.state.outputQueue.Flush(); err != nil {
		slog.Error("droid: persist pending output failed", "agent_id", a.AgentID(), "child_session_id", target.childSessionID, "error", err)
	}
}

// releaseEndedOutput retains a finished child's state until its pending writes and live notifications resolve.
func (a *Agent) releaseEndedOutput(target droidOutputTarget) {
	if target.childSessionID != "" && target.state.ended && !target.state.hasPendingOutput() && !target.state.turnErrorSeen && !target.state.compactionLive {
		delete(a.outputStates, target.childSessionID)
	}
}

// finishOutput flushes retained records after every native output reader stops.
// Each record keeps its original completion and publication owner.
func (a *Agent) finishOutput(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	for sessionID, state := range a.outputStates {
		target, known := a.outputTargetFor(sessionID)
		if !known {
			target = droidOutputTarget{state: state, childSessionID: sessionID}
		} else if !a.IsDiscardingOutput() {
			a.persistRemainingGeneration(target, completion)
			a.flushPendingToolCalls(target)
		}
		a.flushPendingOutput(target)
		a.releaseEndedOutput(target)
	}
}
