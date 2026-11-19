package service

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type capturedTranscriptScope struct {
	writer  agent.TranscriptServices
	anchor  *transcriptOwner
	tracker SpanTracker
}

func (sink *agentOutputSink) TranscriptScopeFor(captured agent.CapturedTranscript) (agent.TranscriptScope, error) {
	writer, owner, err := sink.transcriptScopeAnchor(captured)
	if err != nil {
		return nil, err
	}
	if owner.sessionID == "" {
		return nil, errors.New("the captured transcript has an invalid native session")
	}
	return &capturedTranscriptScope{writer: writer, anchor: owner}, nil
}

func (sink *agentOutputSink) HistoricalTranscriptScopeFor(captured agent.CapturedTranscript) (agent.TranscriptScope, error) {
	writer, owner, err := sink.transcriptScopeAnchor(captured)
	if err != nil {
		return nil, err
	}
	if owner.activity == nil || owner.scope == nil {
		return nil, errors.New("the captured transcript has no activity authority")
	}
	var empty SpanTracker
	historical := owner.withSpanSnapshot(&empty, agent.SpanInfo{})
	owner.activity.mu.Lock()
	historical.scope = &activityScope{publisher: owner.scope.publisher}
	owner.activity.mu.Unlock()
	historical.thread = &transcriptThread{}
	return &capturedTranscriptScope{writer: writer, anchor: historical}, nil
}

func (sink *agentOutputSink) transcriptScopeAnchor(captured agent.CapturedTranscript) (agent.TranscriptServices, *transcriptOwner, error) {
	content, writer := captured.FrozenContent(), captured.TranscriptWriter()
	if writer == nil {
		return nil, nil, errors.New("the captured transcript has no writer")
	}
	owner, err := readCapturedOwner(content)
	if err != nil {
		return nil, nil, err
	}
	if owner == nil || owner.sink != sink {
		return nil, nil, errors.New("the captured transcript belongs to another sink")
	}
	if owner.session == nil || owner.sessionID != owner.session.id || content.AgentSessionID != owner.session.id {
		return nil, nil, errors.New("the captured transcript has an invalid native session")
	}
	return writer, owner, nil
}

func (scope *capturedTranscriptScope) CaptureTranscript(content agent.MessageContent, span agent.SpanInfo) (agent.CapturedTranscript, error) {
	if content.Publication != nil {
		return agent.CapturedTranscript{}, errors.New("the transcript scope requires uncaptured content")
	}
	if content.AgentSessionID != "" && content.AgentSessionID != scope.anchor.sessionID {
		return agent.CapturedTranscript{}, errors.New("the transcript content belongs to another native session")
	}
	content = content.Clone()
	content.AgentSessionID = scope.anchor.sessionID
	content.Publication = &transcriptPublication{owner: scope.anchor.withSpanSnapshot(&scope.tracker, span)}
	var captured agent.CapturedTranscript
	if content.WriteReceipt != nil {
		var captureErr error
		captured, captureErr = agent.CaptureTranscriptWithReceipt(scope.writer, content, span)
		if captureErr != nil {
			return agent.CapturedTranscript{}, captureErr
		}
	} else {
		captured = agent.CaptureTranscript(scope.writer, content, span)
	}
	actual, err := readCapturedOwner(captured.FrozenContent())
	if err != nil {
		return agent.CapturedTranscript{}, err
	}
	if actual == nil || !actual.sameLiveAuthority(scope.anchor) {
		return agent.CapturedTranscript{}, errors.New("the transcript decorator changed the captured authority")
	}
	return captured, nil
}

func (scope *capturedTranscriptScope) OpenSpan(id, parent string) { scope.tracker.OpenSpan(id, parent) }
func (scope *capturedTranscriptScope) CloseSpan(id string)        { scope.tracker.CloseSpan(id) }
func (scope *capturedTranscriptScope) ResetSpans()                { scope.tracker.Reset() }
func (scope *capturedTranscriptScope) SetSpanType(id, kind string) {
	scope.tracker.SetSpanType(id, kind)
}
func (scope *capturedTranscriptScope) GetSpanType(id string) string {
	return scope.tracker.GetSpanType(id)
}
func (scope *capturedTranscriptScope) ReserveSpanColor(id, parent string) int32 {
	return scope.tracker.ReserveSpanColor(id, parent)
}
