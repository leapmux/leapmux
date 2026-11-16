package kimi

import (
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Anchors are immutable after publication. Actual writes freeze their own copy.
type kimiTaskOutputAnchor struct {
	captured agent.CapturedTranscript
}

func (a *Agent) captureTaskOutputAnchor(writer agent.TranscriptServices, historical bool) *kimiTaskOutputAnchor {
	if writer == nil {
		return nil
	}
	captured := agent.CaptureTranscript(writer, agent.MessageContent{}, agent.SpanInfo{})
	if historical {
		scope, err := writer.HistoricalTranscriptScopeFor(captured)
		if err != nil {
			slog.Warn("capture historical Kimi task destination", "agent_id", a.AgentID(), "error", err)
			return nil
		}
		captured, err = scope.CaptureTranscript(agent.MessageContent{}, agent.SpanInfo{})
		if err != nil {
			slog.Warn("capture historical Kimi task authority", "agent_id", a.AgentID(), "error", err)
			return nil
		}
	}
	return &kimiTaskOutputAnchor{captured: captured}
}

// captureNativeTaskOutputAnchors never borrows a newer child's live scope for an older task.
// The caller holds dispatchMu after validating the native task address.
func (a *Agent) captureNativeTaskOutputAnchors(address kimiTaskAddress, task *kimiTask) {
	invocation := task.boundInvocation
	if invocation != nil && task.parentOutput == nil {
		task.parentOutput = invocation.parentOutput
	}
	if task.childOutput != nil || task.native.ChildAgentID == "" {
		return
	}
	child, linked := a.children.get(task.native.ChildAgentID)
	if !linked {
		return
	}
	task.childAgentID = child.childAgentID
	// The native task identifies the transcript but no exact child turn. Reports
	// retain their destination without acquiring a live child's progress or spans.
	task.childOutput = a.captureTaskOutputAnchor(a.runSink(a.run(task.native.ChildAgentID)), true)
}

func (a *Agent) taskParentOutputIsCurrent(address kimiTaskAddress, task *kimiTask) bool {
	owner := a.run(address.ownerAgentID)
	invocation := task.boundInvocation
	return invocation != nil && invocation.present && owner.turnActive && owner.transcript.activeTurn == invocation.identity.turnID
}

func captureKimiTaskOutput(anchor *kimiTaskOutputAnchor, fallback agent.TranscriptServices, historical bool, source leapmuxv1.MessageSource, content agent.MessageContent) (agent.CapturedTranscript, error) {
	var original agent.CapturedTranscript
	if anchor != nil {
		original = anchor.captured
	}
	if original.TranscriptWriter() == nil {
		if fallback == nil {
			return agent.CapturedTranscript{}, errors.New("the Kimi task output has no destination")
		}
		original = agent.CaptureTranscript(fallback, agent.MessageContent{}, agent.SpanInfo{})
		historical = true
	}
	writer := original.TranscriptWriter()
	if historical {
		scope, err := writer.HistoricalTranscriptScopeFor(original)
		if err != nil {
			return agent.CapturedTranscript{}, err
		}
		return scope.CaptureTranscript(content, agent.SpanInfo{})
	}
	template := original.FrozenContent().Clone()
	template.IdempotencyKey = content.IdempotencyKey
	template.Completion = content.Completion
	captured, present, err := agent.CapturePreparedTranscript(writer, source, template, agent.SpanInfo{}, func() []byte { return content.Original })
	if err != nil {
		return agent.CapturedTranscript{}, err
	}
	if !present {
		return agent.CapturedTranscript{}, errors.New("the Kimi task output has no content")
	}
	return captured, nil
}
