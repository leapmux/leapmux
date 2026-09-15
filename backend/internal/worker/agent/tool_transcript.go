package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// toolSupplementSource supplies the provider half of a tool transcript.
//
// The transcript owns what every provider shares: the pending tool calls, the session
// key, and the child transcripts. The source owns what one provider needs to build a
// supplement, and it holds that state in its own FIELDS.
//
// A source embeds noopToolSupplementSource and then implements only the methods its
// provider needs. Four methods stay outside that embedded set, so the compiler asks
// every source for them: providerName, locate, toolCallID and readSupplements. A
// source that supplied none of them would recover nothing.
//
// The transcript calls every method under its own mutex, with ONE exception that
// readSupplements states. See the serialization rule at toolTranscript.
type toolSupplementSource interface {
	// providerName identifies the provider in a log line.
	providerName() string

	// toolCallID reports the tool call one message closes, or "" for a message that
	// closes none.
	toolCallID(original []byte) string

	// locate states where the provider keeps the records of one session. The
	// transcript passes the session ID the provider reported, which a source that
	// tracks its own session identity ignores.
	locate(sessionID string) toolTranscriptLocation

	// readSupplements reads the provider's stored records and builds the supplement of
	// every pending tool call it can answer. path is the location's path, and final
	// states whether another pass can still recover what this one misses.
	//
	// THIS IS THE ONE METHOD THE TRANSCRIPT CALLS WITH NO LOCK HELD. It runs on the
	// supplement worker, so a source whose fields it touches must guard those fields
	// itself.
	readSupplements(ctx context.Context, path string, pending map[string]MessageContent, final bool) (map[string][]byte, error)

	// readsInitialSupplement reports whether initialSupplement can produce anything.
	// A source that reports false costs an agent message no deadline at all.
	readsInitialSupplement() bool

	// initialSupplement builds what the provider can supply for a message that the
	// transcript is about to persist, before the row exists.
	initialSupplement(ctx context.Context, path string, original []byte, span SpanInfo) ([]byte, error)

	// resetRecords drops what the source cached for a session that ended.
	resetRecords()

	// observeMessage takes what a message states about a tool call that a later
	// record read needs.
	observeMessage(content MessageContent, span SpanInfo)

	// finishTurn drops what the source holds for the turn that ended.
	finishTurn()

	// newChild builds the source of a child transcript. A provider whose subagents
	// need no transcript of their own returns nil, and the wrapped sink then serves
	// the child directly.
	newChild() toolSupplementSource
}

// noopToolSupplementSource supplies the default of every OPTIONAL method of
// toolSupplementSource.
//
// A source embeds it, so the transcript calls each method directly and needs no nil
// check. Eight function fields held this before, five of which the transcript tested
// for nil at each call.
type noopToolSupplementSource struct{}

func (noopToolSupplementSource) readsInitialSupplement() bool { return false }

func (noopToolSupplementSource) initialSupplement(context.Context, string, []byte, SpanInfo) ([]byte, error) {
	return nil, nil
}

func (noopToolSupplementSource) resetRecords() {}

func (noopToolSupplementSource) observeMessage(MessageContent, SpanInfo) {}

func (noopToolSupplementSource) finishTurn() {}

func (noopToolSupplementSource) newChild() toolSupplementSource { return nil }

// toolTranscript recovers fields that a provider omits from tool notifications.
// Results reach the transcript immediately. Later transcript boundaries can enrich them.
//
// # Serialization
//
// Two goroutines drive one transcript.
//
//   - The READER goroutine drains the provider's stdout. It calls UpdateSessionID,
//     PersistMessage and PersistTurnEnd, in the order the provider sent them.
//   - The SUPPLEMENT WORKER runs each interim enrichment pass. That pass reads a store
//     the provider owns and can hold busy, and the reader goroutine must never wait
//     for it: the wait becomes back-pressure on the provider's pipe.
//
// mu guards the fields of this struct AND every call into source, with one exception:
// source.readSupplements runs with NO lock held, because it is the slow call that the
// worker exists to move off the reader goroutine. A source whose fields
// readSupplements touches must therefore guard those fields itself.
//
// passMu serializes one enrichment pass against another. The worker holds it for an
// interim pass and the turn end holds it for the final pass, so the two never read the
// provider's store at the same time.
//
// The lock order is passMu, then mu. No path takes mu and then passMu.
type toolTranscript struct {
	ProviderServices
	ctx    context.Context
	source toolSupplementSource

	passMu sync.Mutex

	mu sync.Mutex
	// work wakes the supplement worker. idle reports that no interim pass waits or
	// runs. Both wait on mu.
	work       *sync.Cond
	idle       *sync.Cond
	children   map[string]*toolTranscript
	sessionKey string
	sessionID  string
	pending    map[string]MessageContent
	// stopWatch cancels the context watch that ends the worker. closeSupplementWorker
	// calls it, so a child transcript that the caller cleaned up leaves nothing
	// attached to the agent's context.
	stopWatch func() bool
	// queued reports that a message asked for a pass the worker has not started.
	queued bool
	// running reports that the worker runs a pass now.
	running bool
	// started reports that the worker goroutine exists. The first pass starts it, so
	// a transcript that never enriches anything costs no goroutine.
	started bool
	// closed stops the worker for good. A closed transcript still enriches at the turn
	// end, because the final pass runs on the caller's goroutine.
	closed bool
}

// toolTranscriptLocation states where one provider keeps the records of one session.
//
// ready is what the guards test, not path. A provider whose records need no path (Pi
// reads the message bytes alone) reports ready with an empty path, and it no longer
// has to invent one to pass a guard that tested the path.
type toolTranscriptLocation struct {
	sessionKey string
	path       string
	ready      bool
}

// toolSupplementReadBudget caps the provider reads of one INTERIM transcript pass.
//
// The read waits on a store the provider owns and can hold busy. An interim pass is a
// retry: the next pass and the turn end read again, so a pass that runs out of time
// costs nothing except the wait. The supplement worker pays that wait, and the
// goroutine that drains the provider's stdout no longer pays it.
const toolSupplementReadBudget = 100 * time.Millisecond

// toolSupplementFinalReadBudget caps the provider reads of the FINAL transcript pass.
//
// The final pass is the last chance. The turn end clears what is left, so a record
// that this pass does not read is provider output that the reader never sees. It
// therefore gets ten times the interim budget, and it ignores a cancelled agent
// context for the same reason.
//
// The turn end runs this pass on the goroutine that drains the provider's stdout, so
// this budget is what that goroutine waits at a turn boundary. Each interim pass moved
// to the supplement worker, so this is the only provider read that still stops the
// reader.
const toolSupplementFinalReadBudget = time.Second

// newToolTranscript wraps services in a transcript. source supplies the provider half,
// and ctx is the agent's context, which ends the supplement worker.
func newToolTranscript(ctx context.Context, services ProviderServices, source toolSupplementSource) *toolTranscript {
	s := &toolTranscript{ProviderServices: services, ctx: ctx, source: source}
	s.work = sync.NewCond(&s.mu)
	s.idle = sync.NewCond(&s.mu)
	return s
}

func (s *toolTranscript) reset() {
	s.mu.Lock()
	s.sessionKey = ""
	s.sessionID = ""
	s.pending = nil
	children := s.children
	s.children = nil
	s.source.resetRecords()
	s.mu.Unlock()
	for _, child := range children {
		child.closeSupplementWorker()
	}
}

// adoptLocation preserves replay results until the first session ID arrives.
// A different session clears pending results. A new path within that session keeps them.
// It reports the location's path and whether that location can be read.
//
// The caller holds mu.
func (s *toolTranscript) adoptLocation() (string, bool) {
	location := s.source.locate(s.sessionID)
	if location.sessionKey != s.sessionKey {
		if s.sessionKey != "" {
			s.pending = nil
			s.children = nil
			s.source.resetRecords()
		}
		s.sessionKey = location.sessionKey
	}
	if s.pending == nil {
		s.pending = make(map[string]MessageContent)
	}
	return location.path, location.ready
}

// supplementContext caps one pass of provider reads.
//
// A FINAL pass ignores a cancelled agent context. The provider already stored those
// results, so a process that stops must not discard them.
func (s *toolTranscript) supplementContext(final bool) (context.Context, context.CancelFunc) {
	if final {
		return context.WithTimeout(context.WithoutCancel(s.ctx), toolSupplementFinalReadBudget)
	}
	return context.WithTimeout(s.ctx, toolSupplementReadBudget)
}

// startSupplementWorkerLocked starts the worker on the first pass that needs it.
// The caller holds mu.
func (s *toolTranscript) startSupplementWorkerLocked() {
	if s.started || s.closed {
		return
	}
	s.started = true
	// The worker waits on a condition variable, so the agent's context ending is what
	// wakes it to exit.
	s.stopWatch = context.AfterFunc(s.ctx, s.closeSupplementWorker)
	go s.runSupplementWorker()
}

// closeSupplementWorker ends the worker. The agent's context ending calls it, and so
// does the cleanup of the child that the transcript serves.
func (s *toolTranscript) closeSupplementWorker() {
	s.mu.Lock()
	s.closed = true
	s.queued = false
	stop := s.stopWatch
	s.stopWatch = nil
	s.work.Broadcast()
	s.idle.Broadcast()
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *toolTranscript) runSupplementWorker() {
	s.mu.Lock()
	for {
		for !s.queued && !s.closed {
			s.work.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.queued = false
		s.running = true
		s.mu.Unlock()
		s.runSupplementPass(false)
		s.mu.Lock()
		s.running = false
		s.idle.Broadcast()
	}
}

// requestSupplementPass wakes the supplement worker. The caller holds mu.
//
// A pass that already waits absorbs this one. The worker reads whatever is pending
// when it starts, so one pass covers every message that arrived before it.
func (s *toolTranscript) requestSupplementPass() {
	if s.closed {
		return
	}
	s.startSupplementWorkerLocked()
	s.queued = true
	s.work.Signal()
}

// waitForSupplements blocks until no interim pass waits or runs.
//
// The turn end calls it, so the final pass never overlaps an interim one. A test calls
// it to observe the result of a pass that the test caused.
func (s *toolTranscript) waitForSupplements() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.queued || s.running {
		s.idle.Wait()
	}
}

func (s *toolTranscript) UpdateSessionID(sessionID string) {
	s.ProviderServices.UpdateSessionID(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	_, ready := s.adoptLocation()
	if ready && len(s.pending) > 0 {
		s.requestSupplementPass()
	}
}

func (s *toolTranscript) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return s.ProviderServices.PersistMessage(source, content, span)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, ready := s.adoptLocation()
	if ready && len(s.pending) > 0 {
		// The supplement worker reads the provider's store. This goroutine drains the
		// provider's stdout, so it must not wait for that read.
		s.requestSupplementPass()
	}
	// The initial supplement stays on THIS goroutine, because it enriches the message
	// that this call is about to persist. The worker cannot supply it, because the row
	// does not exist yet.
	if ready && s.source.readsInitialSupplement() {
		ctx, cancel := s.supplementContext(false)
		extra, err := s.source.initialSupplement(ctx, path, content.Original, span)
		cancel()
		if err != nil {
			// A supplement that never arrives is output the reader does not see, so
			// this is a failure and not a trace.
			slog.Warn("Read initial tool supplement", "provider", s.source.providerName(), "error", err)
		}
		if len(extra) > 0 {
			combined, err := mergeToolSupplements(content.Supplemental, extra)
			if err != nil {
				slog.Warn("Merge initial tool supplement", "provider", s.source.providerName(), "error", err)
			} else {
				content.Supplemental = combined
			}
		}
	}
	toolCallID := ""
	if span.Closing && span.SpanID != "" {
		toolCallID = s.source.toolCallID(content.Original)
	}
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	s.source.observeMessage(content, span)
	if toolCallID != "" && toolCallID == span.SpanID {
		saved := content
		saved.Original = append([]byte(nil), content.Original...)
		saved.Supplemental = append([]byte(nil), content.Supplemental...)
		s.pending[toolCallID] = saved
	}
	return nil
}

func (s *toolTranscript) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	s.finishPending()
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *toolTranscript) finishPending() {
	// The final pass supersedes an interim pass that only waits: it reads the same
	// entries with a longer budget. Drop that one, then join a pass that already runs.
	//
	// The reader goroutine is the only one that asks for a pass, and it is the
	// goroutine that runs this function, so no pass can queue between the two steps.
	s.mu.Lock()
	s.queued = false
	s.mu.Unlock()
	s.waitForSupplements()
	s.runSupplementPass(true)
	s.mu.Lock()
	clear(s.pending)
	s.source.finishTurn()
	children := make([]*toolTranscript, 0, len(s.children))
	for _, child := range s.children {
		children = append(children, child)
	}
	s.mu.Unlock()
	for _, child := range children {
		child.finishPending()
	}
}

// CleanupChildAgent drops the child transcript together with the child.
//
// This wrapper is the only holder of that transcript, and nothing else prunes the map
// within a session. Without the override the cleanup reached the wrapped sink alone, so
// every later finishPending still walked the dead child and re-ran its whole turn-end
// pass.
func (s *toolTranscript) CleanupChildAgent(childAgentID string) {
	s.mu.Lock()
	child := s.children[childAgentID]
	delete(s.children, childAgentID)
	s.mu.Unlock()
	if child != nil {
		child.closeSupplementWorker()
	}
	s.ProviderServices.CleanupChildAgent(childAgentID)
}

// A provider supplies the child source because its native session identity is provider-specific.
func (s *toolTranscript) ChildSink(childAgentID string) ProviderServices {
	s.mu.Lock()
	child := s.children[childAgentID]
	s.mu.Unlock()
	if child != nil {
		return child
	}
	delegate := s.ProviderServices.ChildSink(childAgentID)
	if delegate == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if child = s.children[childAgentID]; child != nil {
		return child
	}
	source := s.source.newChild()
	if source == nil {
		return delegate
	}
	child = newToolTranscript(s.ctx, delegate, source)
	if s.children == nil {
		s.children = make(map[string]*toolTranscript)
	}
	s.children[childAgentID] = child
	return child
}

func (s *toolTranscript) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistMessage(source, MessageContent{Original: content}, span)
}

func (s *toolTranscript) PersistChildTurnEnd(childAgentID string, content MessageContent, span SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistTurnEnd(content, span)
}

// runSupplementPass reads the provider's records once and enriches every stored row
// that the read answered.
//
// It takes mu to copy the pending set, releases mu for the read, and takes mu again to
// apply the records. The read is the only part that a slow provider store can stretch,
// and the reader goroutine holds nothing while it runs.
func (s *toolTranscript) runSupplementPass(final bool) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	s.mu.Lock()
	path, ready := s.adoptLocation()
	protocol := make(map[string]MessageContent, len(s.pending))
	for id, content := range s.pending {
		protocol[id] = content
	}
	s.mu.Unlock()
	if !ready || len(protocol) == 0 {
		return
	}
	ctx, cancel := s.supplementContext(final)
	records, err := s.source.readSupplements(ctx, path, protocol, final)
	cancel()
	if err != nil {
		// A supplement that never arrives is output the reader does not see, so this is
		// a failure and not a trace. `final` states whether another pass can still
		// recover it: an interim failure keeps its entry, and the turn end is the last
		// chance.
		slog.Warn("Read stored tool records", "provider", s.source.providerName(), "final", final, "error", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applySupplementsLocked(records)
}

// applySupplementsLocked writes each supplement the pass read into its stored row.
// The caller holds mu.
func (s *toolTranscript) applySupplementsLocked(records map[string][]byte) {
	for id, enriched := range records {
		content, found := s.pending[id]
		if !found {
			continue
		}
		combined, err := mergeToolSupplements(content.Supplemental, enriched)
		if err != nil {
			slog.Warn("Merge tool supplements", "provider", s.source.providerName(), "error", err)
			continue
		}
		enrichedRow, err := s.EnrichMessage(MessageEnrichment{SpanID: id, OriginalContent: content.Original, SupplementalContent: combined})
		if err != nil {
			slog.Warn("Enrich tool result", "provider", s.source.providerName(), "error", err)
			continue
		}
		if !enrichedRow {
			// No row took the supplement. Keep the entry, so a later pass of the same
			// turn can write it once the row exists. Dropping it here lost the result
			// for good, because finishPending clears what is left at the turn end.
			slog.Debug("Stored tool row refused the supplement", "provider", s.source.providerName(), "span_id", id)
			continue
		}
		delete(s.pending, id)
	}
}

// mergeToolSupplements preserves initial data when native records arrive later.
func mergeToolSupplements(initial, later []byte) ([]byte, error) {
	if len(initial) == 0 {
		return later, nil
	}
	var fields, added map[string]json.RawMessage
	if err := json.Unmarshal(initial, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(later, &added); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	for key, value := range added {
		fields[key] = value
	}
	return json.Marshal(fields)
}
