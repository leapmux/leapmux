package service

import (
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/spantrack"
)

// childSinkLifetime is the sole cleanup authority for one acquired child sink.
// The root mutation lock protects the identity and its frozen capture.
type childSinkLifetime struct {
	frozen *frozenChildCapture
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
	child.childLifetime.frozen = &frozenChildCapture{owner: owner, tracker: child.tracker.Freeze()}
}

// AcquireChildSink allocates a lifetime while native session adoption remains independent.
// Capture and activation retain that lifetime's scope before any callback can run.
func (s *agentOutputSink) AcquireChildSink(childAgentID string) agent.ChildSinkLease {
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.Lock()
	s.childMu.Lock()
	child, err := s.rawChildSinkLocked(childAgentID)
	if !child.registeredCurrent() {
		s.childMu.Unlock()
		mutation.Unlock()
		return agent.ChildSinkLease{}
	}
	s.freezeChildLifetimeLocked(child)
	lifetime := &childSinkLifetime{}
	child.childLifetime = lifetime
	activity := s.h.activityFor(childAgentID, s.rootAgentID)
	activity.mu.Lock()
	previous := s.h.scopeLocked(activity, s.rootAgentID)
	activity.scope = &activityScope{publisher: previous.publisher}
	activity.mu.Unlock()
	child.tracker.Reset()
	reset := child.captureMessageUnderMutation(agent.MessageContent{}, agent.SpanInfo{}).Publication
	s.childMu.Unlock()
	mutation.Unlock()
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
		Activate: func() { reset.ReportProgress(agent.ResetProgress()) },
		Cleanup:  func() bool { return s.cleanupChildLifetime(childAgentID, child, lifetime) },
	}
}
