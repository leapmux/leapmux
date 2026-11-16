package agenttest

import (
	"slices"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type testChildSinkLifetime struct {
	frozen            *testTranscriptOwner
	closed            bool
	reset             agent.TranscriptPublication
	activationClaimed atomic.Bool
}

func (sink *Sink) AcquireChildSink(childID string) agent.ChildSinkLease {
	lease, prepare := sink.acquireChildSink(childID)
	prepare()
	return lease
}

func (sink *Sink) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	lease, _ := sink.acquireChildSink(childID)
	return lease
}

func (sink *Sink) acquireChildSink(childID string) (agent.ChildSinkLease, func() agent.TranscriptPublication) {
	child := sink.Child(childID)
	child.mu.Lock()
	if previous := child.childLifetime; previous != nil && !previous.closed {
		previous.frozen = child.captureMessageLocked(agent.MessageContent{}).Publication.Owner().(*testTranscriptOwner)
	}
	lifetime := &testChildSinkLifetime{}
	child.childLifetime = lifetime
	child.mu.Unlock()
	prepareLocked := func() agent.TranscriptPublication {
		if child.childLifetime != lifetime || lifetime.closed {
			return nil
		}
		if lifetime.reset == nil {
			active := child.transcriptScope != nil && child.transcriptScope.active
			child.transcriptScope = &testTranscriptScope{active: active}
			child.tracker.Reset()
			owner := child.captureMessageLocked(agent.MessageContent{}).Publication.(*testTranscriptOwner)
			owner.activationLease = lifetime
			lifetime.reset = owner
		}
		return lifetime.reset
	}
	prepare := func() agent.TranscriptPublication {
		child.mu.Lock()
		defer child.mu.Unlock()
		return prepareLocked()
	}
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
		Activate: func() {
			if lifetime.activationClaimed.Load() {
				return
			}
			reset := prepare()
			if reset != nil && lifetime.activationClaimed.CompareAndSwap(false, true) {
				reset.ReportProgress(agent.ResetProgress())
			}
		},
		Cleanup: func() bool {
			child.mu.Lock()
			defer child.mu.Unlock()
			if child.childLifetime != lifetime || lifetime.closed {
				return false
			}
			lifetime.frozen = child.captureMessageLocked(agent.MessageContent{}).Publication.Owner().(*testTranscriptOwner)
			lifetime.closed = true
			child.transcriptScope = &testTranscriptScope{}
			child.sessionFact.Store(&testSessionFact{id: child.currentSessionIDLocked()})
			return true
		},
	}, prepare
}
