package tooltranscript

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Source supplies the provider half of a tool transcript.
//
// The transcript owns what every provider shares: the pending tool calls, the session
// key, and the child transcripts. The source owns what one provider needs to build a
// supplement, and it holds that state in its own FIELDS.
//
// A source embeds SourceDefaults and then implements only the methods its
// provider needs. Four methods stay outside that embedded set, so the compiler asks
// every source for them: ProviderName, Locate, ToolCallID and ReadSupplements. A
// source that supplied none of them would recover nothing.
//
// The transcript calls every method under its own mutex, with ONE exception that
// ReadSupplements states. See the serialization rule at Transcript.
type Source interface {
	// ProviderName identifies the provider in a log line.
	ProviderName() string

	// ToolCallID reports the tool call one message closes, or "" for a message that
	// closes none.
	ToolCallID(original []byte) string

	// Locate states where the provider keeps the records of one session. The
	// transcript passes the session ID the provider reported, which a source that
	// tracks its own session identity ignores.
	Locate(sessionID string) Location

	// ReadSupplements reads the provider's stored records and builds the supplement of
	// every pending tool call it can answer. path is the location's path, and final
	// states whether another pass can still recover what this one misses.
	//
	// THIS IS THE ONE METHOD THE TRANSCRIPT CALLS WITH NO LOCK HELD. It runs on the
	// supplement worker, so a source whose fields it touches must guard those fields
	// itself.
	ReadSupplements(ctx context.Context, path string, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error)

	// InitialSupplement builds what the provider can supply for a message that the
	// transcript is about to persist, before the row exists. A source with nothing
	// to add returns (nil, nil), which the noop below already does.
	//
	// There is deliberately NO companion predicate. One existed, and it defaulted to
	// false on the noop, so a source that implemented this method and forgot the
	// predicate was skipped in silence -- a mistake no compiler and no test could
	// catch, because neither asked for the pair to agree. What it bought was one
	// context.WithTimeout per agent message for a source with no supplement, in a
	// function that already runs a store write.
	InitialSupplement(ctx context.Context, path string, original []byte, span agent.SpanInfo) ([]byte, error)

	// ResetRecords drops what the source cached for a session that ended.
	ResetRecords()

	// ObserveMessage takes what a message states about a tool call that a later
	// record read needs.
	ObserveMessage(content agent.MessageContent, span agent.SpanInfo)

	// FinishTurn drops what the source holds for the turn that ended.
	FinishTurn()

	// NewChild builds the source of a child transcript. A provider whose subagents
	// need no transcript of their own returns nil, and the wrapped sink then serves
	// the child directly.
	NewChild() Source
}

// SourceDefaults supplies the default of every OPTIONAL method of
// Source.
//
// A source embeds it, so the transcript calls each method directly and needs no nil
// check. Eight function fields held this before, five of which the transcript tested
// for nil at each call.
type SourceDefaults struct{}

func (SourceDefaults) InitialSupplement(context.Context, string, []byte, agent.SpanInfo) ([]byte, error) {
	return nil, nil
}

func (SourceDefaults) ResetRecords() {}

func (SourceDefaults) ObserveMessage(agent.MessageContent, agent.SpanInfo) {}

func (SourceDefaults) FinishTurn() {}

func (SourceDefaults) NewChild() Source { return nil }

// Transcript recovers fields that a provider omits from tool notifications.
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
// source.ReadSupplements runs with NO lock held, because it is the slow call that the
// worker exists to move off the reader goroutine. A source whose fields
// ReadSupplements touches must therefore guard those fields itself.
//
// passMu serializes one enrichment pass against another. The worker holds it for an
// interim pass and the turn end holds it for the final pass, so the two never read the
// provider's store at the same time.
//
// The lock order is passMu, then mu. No path takes mu and then passMu.
type Transcript struct {
	agent.ProviderServices
	ctx    context.Context
	source Source

	passMu sync.Mutex

	mu sync.Mutex
	// work wakes the supplement worker. idle reports that no interim pass waits or
	// runs. Both wait on mu.
	work     *sync.Cond
	idle     *sync.Cond
	children map[string]*Transcript
	// retired holds a child transcript that CleanupChildAgent detached and that still
	// owes a final pass. finishPending drains it beside the live children.
	retired    []*Transcript
	sessionKey string
	sessionID  string
	pending    map[string]pendingToolRow
	// nextPendingEpoch stamps each entry that pending takes. It never restarts, not
	// even for a new session, so no two entries of one transcript share a value.
	nextPendingEpoch uint64
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

// pendingToolRow is one persisted tool result that still waits for a supplement.
//
// revision is the row's supplemental revision as THIS transcript last left it. The
// store pass sends it back with its own write, so a supplement that EnrichToolSpan
// wrote out of band does not make the pass stale -- EnrichMessage refuses a write
// whose PreviousRevision no longer matches the row, and a refused pass would drop
// the store output for good, because finishPending clears what is left at the turn
// end.
//
// epoch identifies THIS entry, and no replacement ever repeats one. Every writer
// releases mu for its store round trip and then compares what it read against what
// pending holds. The revision alone cannot make that comparison: a second row that
// closes the same tool call enters at revision 0, which is the revision the first
// one started at, so the compare would take the replacement for the entry it read.
type pendingToolRow struct {
	content  agent.MessageContent
	revision int64
	epoch    uint64
}

// matches reports whether other is the same entry at the same revision.
func (r pendingToolRow) matches(other pendingToolRow) bool {
	return r.epoch == other.epoch && r.revision == other.revision
}

// Location states where one provider keeps the records of one session.
//
// ready is what the guards test, not path. A provider whose records need no path (Pi
// reads the message bytes alone) reports ready with an empty path, and it no longer
// has to invent one to pass a guard that tested the path.
type Location struct {
	SessionKey string
	Path       string
	Ready      bool
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
// to the supplement worker, and a child that CleanupChildAgent retires waits for the
// same turn end, so a turn boundary is the only point where a provider read stops the
// reader.
const toolSupplementFinalReadBudget = time.Second

// New wraps services in a transcript. source supplies the provider half,
// and ctx is the agent's context, which ends the supplement worker.
func New(ctx context.Context, services agent.ProviderServices, source Source) *Transcript {
	s := &Transcript{ProviderServices: services, ctx: ctx, source: source}
	s.work = sync.NewCond(&s.mu)
	s.idle = sync.NewCond(&s.mu)
	return s
}

func (s *Transcript) Reset() {
	s.mu.Lock()
	s.sessionKey = ""
	s.sessionID = ""
	s.pending = nil
	children := make([]*Transcript, 0, len(s.children)+len(s.retired))
	for _, child := range s.children {
		children = append(children, child)
	}
	children = append(children, s.retired...)
	s.children = nil
	s.retired = nil
	s.source.ResetRecords()
	s.mu.Unlock()
	retireToolTranscripts(children)
}

// adoptLocation preserves replay results until the first session ID arrives.
// A different session clears pending results. A new path within that session keeps them.
// It reports the location's path and whether that location can be read.
//
// A session change orphans every child transcript of the old session, and this returns
// them rather than dropping them. The caller holds mu, which finishPending needs, so
// the caller must pass them to retireToolTranscripts AFTER it releases mu. Dropping
// them here lost each child's last supplement and stranded its worker goroutine and
// its context watch for the life of the agent.
//
// The caller holds mu.
func (s *Transcript) adoptLocation() (string, bool, []*Transcript) {
	var orphans []*Transcript
	location := s.source.Locate(s.sessionID)
	if location.SessionKey != s.sessionKey {
		if s.sessionKey != "" {
			s.pending = nil
			for _, child := range s.children {
				orphans = append(orphans, child)
			}
			orphans = append(orphans, s.retired...)
			s.children = nil
			s.retired = nil
			s.source.ResetRecords()
		}
		s.sessionKey = location.SessionKey
	}
	if s.pending == nil {
		s.pending = make(map[string]pendingToolRow)
	}
	return location.Path, location.Ready, orphans
}

// supplementContext caps one pass of provider reads.
//
// A FINAL pass ignores a cancelled agent context. The provider already stored those
// results, so a process that stops must not discard them.
func (s *Transcript) supplementContext(final bool) (context.Context, context.CancelFunc) {
	if final {
		return context.WithTimeout(context.WithoutCancel(s.ctx), toolSupplementFinalReadBudget)
	}
	return context.WithTimeout(s.ctx, toolSupplementReadBudget)
}

// startSupplementWorkerLocked starts the worker on the first pass that needs it.
// The caller holds mu.
func (s *Transcript) startSupplementWorkerLocked() {
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
func (s *Transcript) closeSupplementWorker() {
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

func (s *Transcript) runSupplementWorker() {
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
func (s *Transcript) requestSupplementPass() {
	if s.closed {
		return
	}
	s.startSupplementWorkerLocked()
	s.queued = true
	s.work.Signal()
}

// WaitForSupplementsForTest blocks until no interim pass waits or runs, so a
// test observes the result of a pass that it caused.
func (s *Transcript) WaitForSupplementsForTest() {
	s.waitForSupplements()
}

// waitForSupplements blocks until no interim pass waits or runs.
//
// The turn end calls it, so the final pass never overlaps an interim one. A test calls
// it to observe the result of a pass that the test caused.
func (s *Transcript) waitForSupplements() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.queued || s.running {
		s.idle.Wait()
	}
}

// SourceForTest returns the provider source that the transcript reads, so a
// test can reach what that source owns: a provider's database handle, for one.
// The source is set at construction and never changes, so the read takes no lock.
func (s *Transcript) SourceForTest() Source { return s.source }

// PendingSpanIDsForTest reports the tool calls that the transcript still waits
// for, sorted. It joins the supplement worker first, so the read sees the finished
// state of every pass.
func (s *Transcript) PendingSpanIDsForTest() []string {
	s.waitForSupplements()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Transcript) UpdateSessionID(sessionID string) {
	s.ProviderServices.UpdateSessionID(sessionID)
	// Registered before the unlock, so LIFO runs it after the unlock:
	// retireToolTranscripts takes each orphan's own mu through finishPending.
	var orphans []*Transcript
	defer func() { retireToolTranscripts(orphans) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	_, ready, orphans := s.adoptLocation()
	if ready && len(s.pending) > 0 {
		s.requestSupplementPass()
	}
}

func (s *Transcript) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return s.ProviderServices.PersistMessage(source, content, span)
	}
	// Registered before the unlock, so LIFO runs it after the unlock:
	// retireToolTranscripts takes each orphan's own mu through finishPending.
	var orphans []*Transcript
	defer func() { retireToolTranscripts(orphans) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	path, ready, orphans := s.adoptLocation()
	if ready && len(s.pending) > 0 {
		// The supplement worker reads the provider's store. This goroutine drains the
		// provider's stdout, so it must not wait for that read.
		s.requestSupplementPass()
	}
	// The initial supplement stays on THIS goroutine, because it enriches the message
	// that this call is about to persist. The worker cannot supply it, because the row
	// does not exist yet.
	if ready {
		ctx, cancel := s.supplementContext(false)
		extra, err := s.source.InitialSupplement(ctx, path, content.Original, span)
		cancel()
		if err != nil {
			// A supplement that never arrives is output the reader does not see, so
			// this is a failure and not a trace.
			slog.Warn("Read initial tool supplement", "provider", s.source.ProviderName(), "error", err)
		}
		if len(extra) > 0 {
			combined, err := MergeSupplements(content.Supplemental, extra)
			if err != nil {
				slog.Warn("Merge initial tool supplement", "provider", s.source.ProviderName(), "error", err)
			} else {
				content.Supplemental = combined
			}
		}
	}
	toolCallID := ""
	if span.Closing && span.SpanID != "" {
		toolCallID = s.source.ToolCallID(content.Original)
	}
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	s.source.ObserveMessage(content, span)
	if toolCallID != "" && toolCallID == span.SpanID {
		saved := content
		saved.Original = append([]byte(nil), content.Original...)
		saved.Supplemental = append([]byte(nil), content.Supplemental...)
		// A row this call just persisted carries revision 0: PersistMessage writes the
		// supplement WITH the row, and only EnrichMessage increments the revision.
		s.nextPendingEpoch++
		s.pending[toolCallID] = pendingToolRow{content: saved, epoch: s.nextPendingEpoch}
	}
	return nil
}

func (s *Transcript) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.finishPending()
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *Transcript) finishPending() {
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
	s.source.FinishTurn()
	children := make([]*Transcript, 0, len(s.children))
	for _, child := range s.children {
		children = append(children, child)
	}
	retired := s.retired
	s.retired = nil
	s.mu.Unlock()
	for _, child := range children {
		child.finishPending()
	}
	retireToolTranscripts(retired)
}

// retireToolTranscripts drains each transcript, then ends its worker.
//
// The order is the point, and it is the same order for each of the three paths that
// drop a child: CleanupChildAgent, reset and adoptLocation. Dropping without the drain
// loses every supplement the child still holds, and dropping without the close strands
// the child's worker goroutine and the context watch that would have ended it, both for
// the life of the agent.
//
// The caller must NOT hold mu, because finishPending takes it.
func retireToolTranscripts(children []*Transcript) {
	for _, child := range children {
		child.finishPending()
		child.closeSupplementWorker()
	}
}

// CleanupChildAgent drains the child transcript, then drops it with the child.
//
// This wrapper is the only holder of that transcript, and nothing else prunes the map
// within a session. Without the override the cleanup reached the wrapped sink alone, so
// every later finishPending still walked the dead child and re-ran its whole turn-end
// pass.
//
// It DRAINS before it drops, and that order is the point. Dropping alone loses every
// supplement the child still holds: nothing calls PersistChildTurnEnd anywhere, so
// the three paths here are the only end a child transcript reaches, and the row the
// child was enriching keeps the bare content its provider first forwarded. A
// subagent's LAST tool call is always the one at risk, because PersistMessage asks
// for its pass before it adds the entry -- so the entry is never covered by a pass
// already running, and closeSupplementWorker also clears one that is merely queued.
//
// The drain waits for the turn end rather than running here. This method runs on the
// goroutine that drains the provider's stdout, and it runs each time ONE subagent
// finishes, so a final pass here stops the whole tab's output for up to
// toolSupplementFinalReadBudget per subagent -- which the doc on Source
// forbids. The retired list moves that cost to the turn boundary, where finishPending
// already pays it once for every live child. The child keeps its worker until then,
// so its interim passes go on enriching in the meantime.
func (s *Transcript) CleanupChildAgent(childAgentID string) {
	s.mu.Lock()
	if child := s.children[childAgentID]; child != nil {
		delete(s.children, childAgentID)
		s.retired = append(s.retired, child)
	}
	s.mu.Unlock()
	s.ProviderServices.CleanupChildAgent(childAgentID)
}

// A provider supplies the child source because its native session identity is provider-specific.
func (s *Transcript) ChildSink(childAgentID string) agent.ProviderServices {
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
	source := s.source.NewChild()
	if source == nil {
		return delegate
	}
	child = New(s.ctx, delegate, source)
	if s.children == nil {
		s.children = make(map[string]*Transcript)
	}
	s.children[childAgentID] = child
	return child
}

// Only the AGENT-source child writes are overridden. See the ChildServices doc on
// PersistChildMessage for why PersistChildPrompt and PersistChildUserMessage are
// not, and what a future USER-source interception would have to change.
func (s *Transcript) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistMessage(source, agent.MessageContent{Original: content}, span)
}

func (s *Transcript) PersistChildTurnEnd(childAgentID string, content agent.MessageContent, span agent.SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistTurnEnd(content, span)
}

// runSupplementPass reads the provider's records once and enriches every stored row
// that the read answered.
//
// It takes mu to copy the pending set and releases mu for the read. applySupplements
// then takes mu for the bookkeeping of one record at a time. Neither the provider
// read nor a store write runs under mu, so the reader goroutine waits for neither.
func (s *Transcript) runSupplementPass(final bool) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	s.mu.Lock()
	path, ready, orphans := s.adoptLocation()
	protocol := make(map[string]agent.MessageContent, len(s.pending))
	for id, entry := range s.pending {
		protocol[id] = entry.content
	}
	s.mu.Unlock()
	retireToolTranscripts(orphans)
	if !ready || len(protocol) == 0 {
		return
	}
	ctx, cancel := s.supplementContext(final)
	records, err := s.source.ReadSupplements(ctx, path, protocol, final)
	cancel()
	if err != nil {
		// A supplement that never arrives is output the reader does not see, so this is
		// a failure and not a trace. `final` states whether another pass can still
		// recover it: an interim failure keeps its entry, and the turn end is the last
		// chance.
		slog.Warn("Read stored tool records", "provider", s.source.ProviderName(), "final", final, "error", err)
	}
	s.applySupplements(records)
}

// applySupplements writes each supplement the pass read into its stored row.
//
// It takes mu for the bookkeeping of ONE record and releases it for that record's
// write. Each write is a SELECT and an UPDATE, so a turn with N pending rows
// serialized N round trips under one acquisition, and every frame the reader
// goroutine had waited for all of them. The caller must NOT hold mu.
func (s *Transcript) applySupplements(records map[string][]byte) {
	for id, enriched := range records {
		s.mu.Lock()
		entry, found := s.pending[id]
		s.mu.Unlock()
		if !found {
			continue
		}
		_, enrichedRow, err := s.writeSupplement(agent.MessageEnrichment{
			SpanID: id, OriginalContent: entry.content.Original,
			PreviousRevision: entry.revision,
		}, entry.content.Supplemental, enriched)
		if err != nil {
			// ONE bad record never stops the pass: the other rows of the same turn
			// still take their supplements.
			slog.Warn("Enrich tool result", "provider", s.source.ProviderName(), "error", err)
			continue
		}
		if !enrichedRow {
			// No row took the supplement. Keep the entry, so a later pass of the same
			// turn can write it once the row exists. Dropping it here lost the result
			// for good, because finishPending clears what is left at the turn end.
			slog.Debug("Stored tool row refused the supplement", "provider", s.source.ProviderName(), "span_id", id)
			continue
		}
		// A compare-and-set, because mu was free while the write ran. An out-of-band
		// EnrichToolSpan can have raised the entry's revision in the meantime, and
		// that entry then describes a supplement this write does not carry. Dropping
		// it would leave the next pass nothing to merge on top of.
		s.mu.Lock()
		if current, still := s.pending[id]; still && current.matches(entry) {
			delete(s.pending, id)
		}
		s.mu.Unlock()
	}
}

// EnrichToolSpan merges one out-of-band supplement into the row of a tool call.
//
// A provider calls this when it recovers a field OUTSIDE the store pass. Cursor is
// the caller today: its `cursor/*` extension frames arrive on the stdout reader, one
// per finished call, right after the update that completed the same toolCallId.
//
// It goes through the transcript because the transcript is the SINGLE WRITER of a
// row's supplemental content. A direct EnrichMessage would raise the row's revision
// under the store pass, and EnrichMessage refuses a write whose PreviousRevision no
// longer matches the row. That refusal is permanent -- finishPending clears what is
// left at the turn end -- so the store output, which is the whole diff or the whole
// search result, would never reach the row.
//
// `build` takes the row's ORIGINAL frame and answers the bytes to merge. A caller
// that must identify the frame it enriches -- so a later resolve can check that the
// supplement belongs to this row -- cannot build those bytes before the transcript
// resolves the row, and the transcript is the only holder of the frame in both the
// pending and the settled case. It answers nil bytes to write nothing.
//
// It reports whether a row took the supplement.
func (s *Transcript) EnrichToolSpan(spanID string, build func(original []byte) ([]byte, error)) (bool, error) {
	if spanID == "" || build == nil {
		return false, nil
	}
	// mu guards the map read ALONE. Both paths below make TWO database round trips,
	// a SELECT and an UPDATE, and mu guards neither of them: the reader goroutine
	// must never wait for a store query, because the wait becomes back-pressure on
	// the provider's pipe. Holding mu across those queries stalled every frame that
	// goroutine had for the length of both.
	//
	// passMu is not the answer either. Taking it here would make the reader wait for
	// the supplement worker's read of the PROVIDER's store, which the doc on
	// Source forbids.
	//
	// A racing writer costs this call its write rather than the row its content,
	// because EnrichMessage refuses a write whose PreviousRevision no longer matches.
	s.mu.Lock()
	entry, waiting := s.pending[spanID]
	s.mu.Unlock()
	if !waiting {
		// The span left `pending`, so no entry describes it and the ROW's own
		// revision is the record. enrichSettledToolSpan reads it back.
		return s.enrichSettledToolSpan(spanID, build)
	}
	extra, err := build(entry.content.Original)
	if err != nil || len(extra) == 0 {
		return false, err
	}
	combined, written, err := s.writeSupplement(agent.MessageEnrichment{
		SpanID: spanID, OriginalContent: entry.content.Original,
		PreviousRevision: entry.revision,
	}, entry.content.Supplemental, extra)
	if err != nil || !written {
		return written, err
	}
	// The entry STAYS pending, with the supplement and the revision this write left.
	// The store pass then merges its records ON TOP of this supplement rather than
	// over it, and its own write states a revision the row still carries.
	//
	// A compare-and-set, because mu was free while the write ran: the store pass can
	// have taken the entry and dropped it in the meantime, and the turn end can have
	// cleared it. A row that this transcript no longer tracks still took the write,
	// so the report stays true either way.
	s.mu.Lock()
	defer s.mu.Unlock()
	current, stillWaiting := s.pending[spanID]
	if !stillWaiting || !current.matches(entry) {
		return true, nil
	}
	current.content.Supplemental = combined
	current.revision++
	s.pending[spanID] = current
	return true, nil
}

// enrichSettledToolSpan writes a supplement onto a row the transcript no longer
// tracks.
//
// The supplement worker runs between two frames of the reader goroutine, so a pass
// that answered this call already enriched its row and dropped the entry. The row
// itself is then the only record of the revision, which is why this reads it back.
//
// It reads the LAST row of the span. The transcript enriches the row that CLOSES a
// tool call, which ReadToolRequest -- the first row of the span -- is not.
//
// The caller holds NO lock: this reads the row and writes it back, and mu guards
// neither. See EnrichToolSpan for why that is safe.
func (s *Transcript) enrichSettledToolSpan(spanID string, build func(original []byte) ([]byte, error)) (bool, error) {
	stored, err := s.ReadToolResult(spanID)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	extra, err := build(stored.Content.Original)
	if err != nil || len(extra) == 0 {
		return false, err
	}
	_, written, err := s.writeSupplement(agent.MessageEnrichment{
		Seq: stored.Seq, SpanID: spanID, OriginalContent: stored.Content.Original,
		PreviousRevision: stored.Revision,
	}, stored.Content.Supplemental, extra)
	return written, err
}

// writeSupplement merges `extra` over `base` and writes the result onto one row.
//
// The ONE merge-then-write in this file. Three callers reach it -- the store pass, the
// out-of-band frame, and the settled row -- and each keeps its own bookkeeping
// afterwards: the pass drops its entry, the out-of-band write keeps one with the new
// revision, and the settled row has no entry at all. What they share is this: the
// merge order, the wording of a merge failure, and the rule that `SupplementalContent`
// on the enrichment is always the MERGED bytes. Three copies spelled all three, and
// the merge-failure message differed in two of them.
//
// It answers the merged bytes as well, because a caller that keeps an entry must store
// what it wrote rather than merge a second time.
func (s *Transcript) writeSupplement(enrichment agent.MessageEnrichment, base, extra []byte) ([]byte, bool, error) {
	combined, err := MergeSupplements(base, extra)
	if err != nil {
		return nil, false, fmt.Errorf("merge tool supplement: %w", err)
	}
	enrichment.SupplementalContent = combined
	written, err := s.EnrichMessage(enrichment)
	return combined, written, err
}

// MergeSupplements preserves initial data when native records arrive later.
//
// The merged envelope's own keys come out SORTED, because it re-encodes through a map,
// while a producer that marshals a struct keeps its declaration order. Each payload
// inside passes through as raw bytes, so the agent's own frame is never re-encoded --
// but the two envelopes then differ by key order for the same content, which is why
// every caller that asks "did this change anything?" uses JSONCanonicalEqual.
func MergeSupplements(initial, later []byte) ([]byte, error) {
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
