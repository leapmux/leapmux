package agenttest

import (
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type testChildSinkLifetime struct {
	frozen *testTranscriptOwner
	closed bool
}

func (sink *Sink) AcquireChildSink(childID string) agent.ChildSinkLease {
	child := sink.Child(childID)
	child.mu.Lock()
	if previous := child.childLifetime; previous != nil && !previous.closed {
		previous.frozen = child.captureMessageLocked(agent.MessageContent{}).Publication.Owner().(*testTranscriptOwner)
	}
	lifetime := &testChildSinkLifetime{}
	child.childLifetime = lifetime
	active := child.transcriptScope != nil && child.transcriptScope.active
	child.transcriptScope = &testTranscriptScope{active: active}
	child.tracker.Reset()
	reset := child.captureMessageLocked(agent.MessageContent{}).Publication
	child.mu.Unlock()
	services := agent.NewOrderedProviderServices(child.OutputQueue(), agent.NewProviderServices(child))
	return agent.ChildSinkLease{
		Sink: services,
		CaptureMessage: func(content agent.MessageContent, _ agent.SpanInfo) agent.MessageContent {
			content = content.Clone()
			if content.Publication != nil {
				return content
			}
			child.mu.Lock()
			defer child.mu.Unlock()
			if child.childLifetime == lifetime && !lifetime.closed {
				return child.captureMessageLocked(content)
			}
			if lifetime.frozen == nil {
				return child.captureMessageLocked(content)
			}
			owner := *lifetime.frozen
			owner.spans = slices.Clone(owner.spans)
			if content.AgentSessionID == "" {
				content.AgentSessionID = owner.key.sessionID
			}
			owner.key.sessionID = content.AgentSessionID
			content.Publication = &owner
			return content
		},
		Activate: func() { reset.ReportProgress(agent.ResetProgress()) },
		Cleanup: func() bool {
			child.mu.Lock()
			defer child.mu.Unlock()
			if child.childLifetime != lifetime || lifetime.closed {
				return false
			}
			lifetime.frozen = child.captureMessageLocked(agent.MessageContent{}).Publication.Owner().(*testTranscriptOwner)
			lifetime.closed = true
			child.transcriptScope = &testTranscriptScope{}
			child.sessionFact = &testSessionFact{id: child.currentSessionIDLocked()}
			return true
		},
	}
}
