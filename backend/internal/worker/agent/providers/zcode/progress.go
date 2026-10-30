package zcode

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// zcodeProgressPublication restricts automatic model resets to the scopes of
// the record's owner. Captured writes never clear scopes admitted afterward.
type zcodeProgressPublication struct {
	agent.TranscriptPublication
	scopes []string
}

func (p zcodeProgressPublication) ReportProgress(update agent.ProgressUpdate) {
	if update.Operation != agent.ProgressModelReset || update.ScopeID != "" {
		p.TranscriptPublication.ReportProgress(update)
		return
	}
	for _, scope := range p.scopes {
		p.TranscriptPublication.ReportProgress(agent.ResetModelScopeProgress(scope))
	}
}

type zcodeProgressSink struct {
	agent.ProviderServices
	scopes []string
	source agent.MessageContent
}

func (s zcodeProgressSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	if content.Publication != nil {
		return content.Clone()
	}
	if s.source.Publication != nil {
		content = content.Clone()
		content.AgentSessionID, content.Publication = s.source.AgentSessionID, s.source.Publication
	} else {
		content = s.ProviderServices.CaptureMessage(content, span)
	}
	if content.Publication != nil {
		content.Publication = zcodeProgressPublication{TranscriptPublication: content.Publication, scopes: s.scopes}
	}
	return content
}

func (s zcodeProgressSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return s.ProviderServices.PersistMessage(source, s.CaptureMessage(content, span), span)
}
func (s zcodeProgressSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	return s.ProviderServices.PersistNotification(source, s.CaptureMessage(content, agent.SpanInfo{}))
}
func (s zcodeProgressSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	return s.ProviderServices.PersistTurnEnd(s.CaptureMessage(content, span), span)
}

func (s zcodeProgressSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	return s.ProviderServices.PersistWorkerNotification(s.CaptureMessage(content, agent.SpanInfo{}))
}
func (s zcodeProgressSink) ReportProgress(update agent.ProgressUpdate) {
	if s.source.Publication != nil {
		zcodeProgressPublication{TranscriptPublication: s.source.Publication, scopes: s.scopes}.ReportProgress(update)
		return
	}
	s.ProviderServices.ReportProgress(update)
}

func (a *Agent) zcodeProgressSinkForTurn(owner *zcodeTurnState) zcodeProgressSink {
	a.Mu.Lock()
	scopes := make([]string, 0, len(owner.contentByScope))
	for scope := range owner.contentByScope {
		scopes = append(scopes, scope)
	}
	source := agent.MessageContent{}
	if owner.pendingNative != nil {
		source = owner.pendingNative.source
	}
	a.Mu.Unlock()
	return zcodeProgressSink{ProviderServices: a.sink, scopes: scopes, source: source}
}
