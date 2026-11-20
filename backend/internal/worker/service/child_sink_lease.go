package service

import (
	"log/slog"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
)

// childSinkLifetime is the sole cleanup authority for one acquired child sink.
// The root mutation lock protects the identity and its frozen capture.
type childSinkLifetime struct {
	frozen                *frozenChildCapture
	reset                 agent.TranscriptPublication
	activationClaimed     atomic.Bool
	retainTranscriptSpans bool
}

type frozenChildCapture struct {
	owner   *transcriptOwner
	tracker *spantrack.FrozenSpanTracker
}

func (s *agentOutputSink) freezeChildLifetimeLocked(child *agentOutputSink) {
	if child.childLifetime == nil {
		return
	}
	content := child.captureMessageUnderMutation(agent.MessageContent{}, agent.SpanInfo{})
	owner := content.Publication.Owner().(*transcriptOwner)
	child.childLifetime.frozen = &frozenChildCapture{owner: owner, tracker: child.currentSpanTracker().Freeze()}
}

// AcquireChildSink allocates a lifetime while native session adoption remains independent.
// Capture and activation retain that lifetime's scope before any callback can run.
func (s *agentOutputSink) AcquireChildSink(childAgentID string) agent.ChildSinkLease {
	lease, prepare := s.acquireChildSink(childAgentID)
	if prepare != nil {
		prepare()
	}
	return lease
}

func (s *agentOutputSink) AcquireChildStateSink(childAgentID string) agent.ChildSinkLease {
	lease, _ := s.acquireChildSink(childAgentID)
	return lease
}

func (s *agentOutputSink) acquireChildSink(childAgentID string) (agent.ChildSinkLease, func() agent.TranscriptPublication) {
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.Lock()
	s.childMu.Lock()
	child, err := s.rawChildSinkLocked(childAgentID)
	if !child.registeredCurrent() {
		s.childMu.Unlock()
		mutation.Unlock()
		return agent.ChildSinkLease{}, nil
	}
	previousLifetime := child.childLifetime
	s.freezeChildLifetimeLocked(child)
	lifetime := &childSinkLifetime{}
	activity := s.h.activityFor(childAgentID, s.rootAgentID)
	activity.mu.Lock()
	child.childLifetime = lifetime
	if previousLifetime != nil && previousLifetime.retainTranscriptSpans {
		child.rebindSpanTracker()
	}
	activity.mu.Unlock()
	s.childMu.Unlock()
	mutation.Unlock()
	prepare := func() agent.TranscriptPublication {
		mutation.Lock()
		defer mutation.Unlock()
		if child.childLifetime != lifetime || !child.registeredCurrent() {
			return nil
		}
		if lifetime.reset == nil {
			activity.mu.Lock()
			previous := s.h.scopeLocked(activity, s.rootAgentID)
			activity.scope = &activityScope{publisher: previous.publisher}
			activity.mu.Unlock()
			child.currentSpanTracker().Reset()
			reset := child.captureMessageUnderMutation(agent.MessageContent{}, agent.SpanInfo{}).Publication.(*transcriptPublication)
			owner := *reset.owner
			owner.activationLease = lifetime
			lifetime.reset = &transcriptPublication{owner: &owner}
		}
		return lifetime.reset
	}
	if err != nil {
		slog.Warn("Read the provider session for the child lifetime", "agent_id", childAgentID, "error", err)
	}
	services := agent.NewOrderedProviderServices(child.OutputQueue(), agent.NewProviderServices(child))
	return agent.ChildSinkLease{
		Sink: services,
		CaptureMessage: func(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
			if content.Publication != nil {
				return content.Clone()
			}
			mutation.RLock()
			defer mutation.RUnlock()
			if child.childLifetime == lifetime && child.registeredCurrent() {
				return child.captureMessageUnderMutation(content, span)
			}
			frozen := lifetime.frozen
			if frozen == nil {
				return child.captureMessageUnderMutation(content, span)
			}
			content = content.Clone()
			if content.AgentSessionID == "" {
				content.AgentSessionID = frozen.owner.sessionID
			}
			owner := *frozen.owner
			owner.sessionID = content.AgentSessionID
			content.Publication = &transcriptPublication{owner: owner.withSpanSnapshot(frozen.tracker, span)}
			return content
		},
		RetainTranscriptSpans: func() bool {
			mutation.Lock()
			defer mutation.Unlock()
			if child.childLifetime != lifetime || !child.registeredCurrent() {
				return false
			}
			lifetime.retainTranscriptSpans = true
			return true
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
		Cleanup: func() bool { return s.cleanupChildLifetime(childAgentID, child, lifetime) },
	}, prepare
}
