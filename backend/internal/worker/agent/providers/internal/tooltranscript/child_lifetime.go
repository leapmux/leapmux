package tooltranscript

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

var errTranscriptRetired = errors.New("the child transcript no longer accepts new output")

// unobservedChildSource protects child lifetime ordering without native enrichment.
type unobservedChildSource struct {
	SourceDefaults
	providerName string
}

func (source unobservedChildSource) ProviderName() string                   { return source.providerName }
func (unobservedChildSource) AuthorityIsCurrent(agent.TranscriptOwner) bool { return false }
func (unobservedChildSource) ToolCallID([]byte) string                      { return "" }
func (unobservedChildSource) Locate(string) Location                        { return Location{} }
func (unobservedChildSource) ReadSupplements(context.Context, Location, map[string]agent.MessageContent, bool) (map[string][]byte, error) {
	return nil, nil
}
func (unobservedChildSource) CaptureSupplement(Location, agent.MessageContent, agent.SpanInfo) SupplementReader {
	return nil
}

// ReserveTranscriptOutput serializes fresh row admission with retirement.
func (s *Transcript) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	if queue != s.queue {
		return nil, errors.New("the transcript reservation uses another output queue")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retirement != nil {
		return nil, errTranscriptRetired
	}
	return queue.Append(writes...), nil
}

func (s *Transcript) acceptsExecutingWrite(content agent.MessageContent) bool {
	s.mu.Lock()
	retirement := s.retirement
	s.mu.Unlock()
	return retirement == nil || agent.TranscriptWritePrecedes(content, retirement.ticket.Load())
}

func (s *Transcript) captureLifetimeMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	if s.childLease.CaptureMessage != nil {
		return s.childLease.CaptureMessage(content, span)
	}
	return s.ProviderServices.CaptureMessage(content, span)
}

func (s *Transcript) activateChildLease() {
	s.activateOnce.Do(func() {
		if s.childLease.Activate != nil {
			s.childLease.Activate()
		}
	})
}

func (s *Transcript) finishChildAdmission() {
	s.mu.Lock()
	s.childAdmissions--
	retirement := s.retirement
	s.mu.Unlock()
	if retirement != nil {
		go retirement.wake()
	}
}

func (s *Transcript) childWrapper(childID string) *Transcript {
	s.mu.Lock()
	if s.cleanupCommitted || s.retiredComplete {
		s.mu.Unlock()
		return nil
	}
	if child := s.children[childID]; child != nil {
		s.mu.Unlock()
		return child
	}
	s.childAdmissions++
	s.mu.Unlock()
	defer s.finishChildAdmission()
	// Acquiring a lease invokes no callbacks. Serialize it with cache publication
	// so a concurrent cache miss cannot supersede an unpublished lease.
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	s.mu.Lock()
	child := s.children[childID]
	s.mu.Unlock()
	if child != nil {
		return child
	}
	lease := s.ProviderServices.AcquireChildSink(childID)
	if lease.Sink == nil {
		return nil
	}
	source := s.source.NewChild(childID, lease.Sink)
	if source == nil {
		source = unobservedChildSource{providerName: s.source.ProviderName()}
	}
	child = New(s.ctx, lease.Sink, source)
	child.childAgentID = childID
	child.childLease = lease
	s.mu.Lock()
	if s.children == nil {
		s.children = make(map[string]*Transcript)
	}
	s.children[childID] = child
	s.mu.Unlock()
	return child
}

func (s *Transcript) ChildSink(childID string) agent.ProviderServices {
	child := s.childWrapper(childID)
	if child == nil {
		return nil
	}
	child.activateChildLease()
	return child
}

func (s *Transcript) AcquireChildSink(childID string) agent.ChildSinkLease {
	child := s.childWrapper(childID)
	if child == nil {
		return agent.ChildSinkLease{}
	}
	return agent.ChildSinkLease{Sink: child, CaptureMessage: child.CaptureMessage, Activate: child.activateChildLease,
		Cleanup: func() bool { return s.cleanupChildWrapper(childID, child) }}
}

func (s *Transcript) cleanupChildWrapper(childID string, expected *Transcript) bool {
	s.sourceMu.Lock()
	s.mu.Lock()
	child := s.children[childID]
	if expected != nil && child != expected {
		if _, exists := s.retired[expected]; exists {
			child = expected
		} else {
			s.mu.Unlock()
			s.sourceMu.Unlock()
			return false
		}
	}
	if child == nil {
		known := false
		for retired := range s.retired {
			if retired.childAgentID == childID {
				known = true
				break
			}
		}
		s.mu.Unlock()
		s.sourceMu.Unlock()
		if !known {
			s.ProviderServices.CleanupChildAgent(childID)
		}
		return known
	}
	if s.children[childID] == child {
		delete(s.children, childID)
	}
	s.mu.Unlock()
	child.setRetireCleanup(func() {
		if child.childLease.Cleanup != nil {
			child.childLease.Cleanup()
		}
	})
	// Freeze capture before another acquisition can advance the child's lifetime.
	child.enqueueRetirement()
	s.sourceMu.Unlock()
	s.retainRetired([]*Transcript{child})
	child.startRetirement()
	return true
}

func (s *Transcript) CleanupChildAgent(childID string) { s.cleanupChildWrapper(childID, nil) }

func (s *Transcript) sourceOwnerCurrent(owner agent.TranscriptOwner) bool {
	s.mu.Lock()
	retirement := s.retirement
	s.mu.Unlock()
	if retirement != nil {
		return owner != nil && owner.IsSessionCurrent()
	}
	return s.source.AuthorityIsCurrent(owner)
}

func (s *Transcript) sourceContentCurrent(content agent.MessageContent) bool {
	if content.Publication == nil {
		return false
	}
	s.mu.Lock()
	retirement := s.retirement
	s.mu.Unlock()
	if retirement != nil && retirement.ready.Load() && content.AgentSessionID != retirement.sessionID {
		return false
	}
	return s.sourceOwnerCurrent(content.Publication.Owner())
}

func (s *Transcript) ValidateTranscriptOutput(ticket *agent.OutputTicket) error {
	if err := agent.ValidateTranscriptOutputTicket(ticket); err != nil {
		return err
	}
	if ticket.Queue() != s.queue {
		return errors.New("the captured output uses another transcript queue")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retirement != nil && !agent.TranscriptOutputPrecedes(ticket, s.retirement.ticket.Load()) {
		return errTranscriptRetired
	}
	return nil
}
