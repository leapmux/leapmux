package agenttest

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
)

type sinkTranscriptScope struct {
	writer  agent.TranscriptServices
	anchor  *testTranscriptOwner
	tracker spantrack.SpanTracker
}

func (sink *Sink) TranscriptScopeFor(captured agent.CapturedTranscript) (agent.TranscriptScope, error) {
	content, writer := captured.FrozenContent(), captured.TranscriptWriter()
	if writer == nil || content.Publication == nil {
		return nil, errors.New("the captured transcript has no publication")
	}
	owner, valid := content.Publication.Owner().(*testTranscriptOwner)
	if !valid || owner == nil || owner.sink != sink {
		return nil, errors.New("the captured transcript belongs to another sink")
	}
	if owner.key.session == nil || owner.key.sessionID == "" || content.AgentSessionID != owner.key.session.id {
		return nil, errors.New("the captured transcript has an invalid native session")
	}
	return &sinkTranscriptScope{writer: writer, anchor: owner}, nil
}

func (scope *sinkTranscriptScope) CaptureTranscript(content agent.MessageContent, span agent.SpanInfo) (agent.CapturedTranscript, error) {
	if content.Publication != nil {
		return agent.CapturedTranscript{}, errors.New("the transcript scope requires uncaptured content")
	}
	if content.AgentSessionID != "" && content.AgentSessionID != scope.anchor.key.sessionID {
		return agent.CapturedTranscript{}, errors.New("the transcript content belongs to another native session")
	}
	owner := *scope.anchor
	owner.spans = openSpanSnapshot(&scope.tracker)
	content = content.Clone()
	content.AgentSessionID = owner.key.sessionID
	content.Publication = &owner
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
	publication := captured.FrozenContent().Publication
	if publication == nil {
		return agent.CapturedTranscript{}, errors.New("the transcript decorator removed the captured authority")
	}
	actual, valid := publication.Owner().(*testTranscriptOwner)
	if !valid || actual == nil || actual.sink != owner.sink || actual.key != owner.key {
		return agent.CapturedTranscript{}, errors.New("the transcript decorator changed the captured authority")
	}
	return captured, nil
}

func (scope *sinkTranscriptScope) OpenSpan(id, parent string)   { scope.tracker.OpenSpan(id, parent) }
func (scope *sinkTranscriptScope) CloseSpan(id string)          { scope.tracker.CloseSpan(id) }
func (scope *sinkTranscriptScope) ResetSpans()                  { scope.tracker.Reset() }
func (scope *sinkTranscriptScope) SetSpanType(id, kind string)  { scope.tracker.SetSpanType(id, kind) }
func (scope *sinkTranscriptScope) GetSpanType(id string) string { return scope.tracker.GetSpanType(id) }
func (scope *sinkTranscriptScope) ReserveSpanColor(id, parent string) int32 {
	return scope.tracker.ReserveSpanColor(id, parent)
}
