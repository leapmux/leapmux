package tooltranscript

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// toolRetirement orders final enrichment and cleanup on the transcript's queue.
// Descendants finish on their own queues before this transcript loses authority.
type toolRetirement struct {
	transcript   *Transcript
	queue        *agent.OutputQueue
	ticket       atomic.Pointer[agent.OutputTicket]
	ready        atomic.Bool
	dependencyMu sync.Mutex
	children     map[*Transcript]struct{}
	sessionID    string
	publication  agent.TranscriptPublication
	done         chan struct{}
}

// enqueueRetirement publishes readiness after every dependency is installed.
// An existing queue drainer can reach the new ticket before construction ends.
func (s *Transcript) enqueueRetirement() *toolRetirement {
	s.mu.Lock()
	if s.retirement != nil {
		retirement := s.retirement
		s.mu.Unlock()
		return retirement
	}
	retirement := &toolRetirement{transcript: s, queue: s.queue, done: make(chan struct{})}
	retirement.children = make(map[*Transcript]struct{})
	s.retirement = retirement
	ticket := retirement.queue.Append(agent.OutputWrite{Label: "finish child transcript", Run: retirement.run})[0]
	retirement.ticket.Store(ticket)
	s.mu.Unlock()
	s.closeSupplementWorker()
	content := s.captureLifetimeMessage(agent.MessageContent{}, agent.SpanInfo{})
	retirement.publication = content.Publication
	retirement.sessionID = content.AgentSessionID
	s.sourceMu.Lock()
	_, orphans := s.adoptSource(content)
	s.sourceMu.Unlock()
	s.retainRetired(orphans)
	retirement.ready.Store(true)
	return retirement
}

func (s *Transcript) startRetirement() {
	retirement := s.enqueueRetirement()
	if !retirement.ready.Load() {
		return
	}
	go retirement.wake()
}

func (retirement *toolRetirement) wake() {
	if !retirement.ready.Load() || retirement.transcript.retirementComplete() {
		return
	}
	if err := retirement.queue.FlushAfterReady(retirement.ticket.Load()); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("Retain the child transcript until final enrichment succeeds", "provider", retirement.transcript.source.ProviderName(), "error", err)
	}
}

func (s *Transcript) wakeRetirement() {
	s.mu.Lock()
	retirement := s.retirement
	s.mu.Unlock()
	if retirement != nil {
		retirement.wake()
	}
}

func (s *Transcript) enrichmentInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return true
	}
	for _, observation := range s.observations {
		if observation.inFlight {
			return true
		}
	}
	for _, row := range s.pending {
		for _, attempt := range row.attempts {
			if attempt.inFlight {
				return true
			}
		}
	}
	return false
}

func (retirement *toolRetirement) run() error {
	if !retirement.ready.Load() {
		return agent.ErrOutputInFlight
	}
	s := retirement.transcript
	if !retirement.prepareDependencies() || s.enrichmentInFlight() {
		return agent.ErrOutputInFlight
	}
	if retirement.publication != nil && retirement.publication.Owner().IsSessionCurrent() {
		if err := s.finishPending(true); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.retainHistoricalPendingLocked()
	s.mu.Unlock()
	if err := s.flushObservations(); err != nil {
		return err
	}
	if s.enrichmentInFlight() || !retirement.commitCleanup() {
		return agent.ErrOutputInFlight
	}
	s.sourceMu.Lock()
	s.source.ResetRecords()
	s.sourceMu.Unlock()
	for {
		s.mu.Lock()
		cleanup := s.retireCleanup
		s.retireCleanup = nil
		if cleanup == nil {
			s.retiredComplete = true
			waiters := s.retireWaiters
			s.retireWaiters = nil
			s.children = nil
			s.retired = nil
			s.mu.Unlock()
			for _, notify := range waiters {
				notify()
			}
			close(retirement.done)
			return nil
		}
		s.mu.Unlock()
		cleanup()
	}
}

func (s *Transcript) setRetireCleanup(cleanup func()) {
	s.mu.Lock()
	if s.retireCleanupSet {
		s.mu.Unlock()
		return
	}
	s.retireCleanupSet = true
	complete := s.retiredComplete
	if !complete {
		s.retireCleanup = cleanup
	}
	s.mu.Unlock()
	if complete {
		cleanup()
	}
}

func (s *Transcript) retirementComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retiredComplete
}

func (s *Transcript) onRetired(notify func()) {
	s.mu.Lock()
	complete := s.retiredComplete
	if !complete {
		s.retireWaiters = append(s.retireWaiters, notify)
	}
	s.mu.Unlock()
	if complete {
		notify()
	}
}

// WaitForRetirementsForTest waits for detached children and their queued cleanup.
func (s *Transcript) WaitForRetirementsForTest(ctx context.Context) error {
	s.mu.Lock()
	children := make([]*Transcript, 0, len(s.retired))
	for child := range s.retired {
		children = append(children, child)
	}
	s.mu.Unlock()
	for _, child := range children {
		retirement := child.enqueueRetirement()
		select {
		case <-retirement.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (retirement *toolRetirement) prepareDependencies() bool {
	s := retirement.transcript
	s.mu.Lock()
	if s.childAdmissions != 0 {
		s.mu.Unlock()
		return false
	}
	current := make(map[*Transcript]struct{}, len(s.children)+len(s.retired))
	for _, child := range s.children {
		current[child] = struct{}{}
	}
	for child := range s.retired {
		current[child] = struct{}{}
	}
	s.mu.Unlock()
	var added []*Transcript
	retirement.dependencyMu.Lock()
	for child := range current {
		if _, exists := retirement.children[child]; !exists {
			retirement.children[child] = struct{}{}
			added = append(added, child)
		}
	}
	retirement.dependencyMu.Unlock()
	for _, child := range added {
		child.onRetired(retirement.wake)
		child.startRetirement()
	}
	retirement.dependencyMu.Lock()
	defer retirement.dependencyMu.Unlock()
	for child := range retirement.children {
		if !child.retirementComplete() {
			return false
		}
	}
	return true
}

func (retirement *toolRetirement) commitCleanup() bool {
	s := retirement.transcript
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.childAdmissions != 0 {
		return false
	}
	for _, child := range s.children {
		if !child.retirementComplete() {
			return false
		}
	}
	for child := range s.retired {
		if !child.retirementComplete() {
			return false
		}
	}
	s.cleanupCommitted = true
	return true
}
