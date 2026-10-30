package tooltranscript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Source supplies the provider data that a tool transcript needs.
// sourceMu serializes these operations:
//   - Preparation.
//   - Observation.
//   - Reset.
//   - Child creation.
//
// passMu serializes native reads. ProviderName returns a stable label.
type Source interface {
	// ProviderName identifies the provider in a log line.
	ProviderName() string
	// AuthorityIsCurrent selects the lifetime of provider-native lookup state.
	AuthorityIsCurrent(owner agent.TranscriptOwner) bool
	// CanFinalizeTurn reports whether every concurrent native owner ended.
	CanFinalizeTurn() bool
	// ToolCallID identifies the tool call that this message closes. Empty means no call.
	ToolCallID(original []byte) string
	// Locate returns the native record location for the supplied session.
	Locate(sessionID string) Location
	// ReadSupplements reads data for the exact pending rows without the state mutex.
	ReadSupplements(ctx context.Context, location Location, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error)
	// InitialSupplement prepares data before the original row commits.
	InitialSupplement(ctx context.Context, path string, original []byte, span agent.SpanInfo) ([]byte, error)
	// CaptureSupplement freezes lookup inputs before persistence can outlive the
	// native session. The returned reader changes no live source state.
	CaptureSupplement(Location, agent.MessageContent, agent.SpanInfo) SupplementReader
	// ResetRecords clears the native record cache.
	ResetRecords()
	// ObserveMessage retains data that a later native read needs.
	ObserveMessage(content agent.MessageContent, span agent.SpanInfo)
	// FinishTurn clears source data after an accepted final boundary.
	FinishTurn()
	// NewChild constructs native child enrichment. Nil leaves native child data unobserved.
	NewChild(childAgentID string, services agent.ProviderServices) Source
}

type SupplementReader func(context.Context, bool) ([]byte, error)

type capturedSupplementRead struct {
	pass *sync.Mutex
	read SupplementReader
}

func (captured *capturedSupplementRead) Read(ctx context.Context) ([]byte, error) {
	if !captured.pass.TryLock() {
		return nil, errToolObservationInFlight
	}
	defer captured.pass.Unlock()
	return captured.read(ctx, true)
}

// SourceDefaults implements each optional Source method with no additional data or operation.
type SourceDefaults struct{}

func (SourceDefaults) AuthorityIsCurrent(owner agent.TranscriptOwner) bool {
	return owner != nil && owner.IsCurrent()
}

func (SourceDefaults) CanFinalizeTurn() bool { return true }

func (SourceDefaults) InitialSupplement(context.Context, string, []byte, agent.SpanInfo) ([]byte, error) {
	return nil, nil
}

func (SourceDefaults) ResetRecords() {}

func (SourceDefaults) ObserveMessage(agent.MessageContent, agent.SpanInfo) {}

func (SourceDefaults) FinishTurn() {}

func (SourceDefaults) NewChild(_ string, _ agent.ProviderServices) Source { return nil }

// Transcript recovers fields that native tool notifications omit.
// It retains exact committed rows and observed supplemental bytes.
// The output reader never waits for an interim native read.
//
// mu protects transcript state. sourceMu serializes source preparation and mutation.
// passMu serializes native reads only.
// No delegate persistence or watcher call holds a wrapper mutex.
// Callback-free child acquisition serializes with source construction.
// Preparation takes sourceMu before mu. Native reads hold neither mutex.
type Transcript struct {
	childLease       agent.ChildSinkLease
	activateOnce     sync.Once
	childAdmissions  int
	cleanupCommitted bool
	childAgentID     string
	retirement       *toolRetirement
	retiredComplete  bool
	retireCleanup    func()
	retireCleanupSet bool
	retireWaiters    []func()

	agent.ProviderServices
	queue         *agent.OutputQueue
	orderedWriter agent.TranscriptServices
	ctx           context.Context
	source        Source

	passMu   sync.Mutex
	sourceMu sync.Mutex

	mu sync.Mutex
	// work wakes the worker. idle reports that all interim work finished. Both use mu.
	work     *sync.Cond
	idle     *sync.Cond
	children map[string]*Transcript
	// retired retains detached children until a successful final boundary completes their enrichment.
	retired      map[*Transcript]struct{}
	sessionKey   string
	sessionID    string
	pending      map[string]pendingToolRow
	observations []*toolObservation
	sourceState  *toolSourceState
	// nextPendingEpoch never resets, so replacement entries cannot share an epoch.
	nextPendingEpoch uint64
	// stopWatch cancels the context callback when the worker closes.
	stopWatch func() bool
	// queued retains the exact pending entries of the next interim pass.
	queued *toolSupplementRequest
	// running reports that the worker runs a pass now.
	running bool
	// started reports whether the lazily created worker exists.
	started bool
	// closed prevents more interim work. A final boundary can still read native records.
	closed bool
}

// pendingToolRow identifies one committed result row and its cached supplemental revision.
// Its write receipt supplies the exact sequence before any outer callback can replace the pending entry.
// epoch identifies this pending entry. A replacement never repeats it.
// Accepted enrichment receipts advance the cached revision before a synchronous observer returns.
// Distinct attempts use the stored compare-and-swap. Identical in-flight attempts return without waiting.
type pendingToolRow struct {
	content          agent.MessageContent
	revision         int64
	epoch            uint64
	sourceGeneration *toolSourceGeneration
	attempts         []*toolEnrichmentAttempt
}

type toolEnrichmentAttempt struct {
	receipt          *agent.MessageEnrichmentReceipt
	previousRevision int64
	supplemental     []byte
	inFlight         bool
}

// toolObservation keeps a once-observed supplement until its exact row stores it.
type toolObservation struct {
	spanID     string
	sequence   int64
	epoch      uint64
	content    agent.MessageContent
	extra      []byte
	attempt    *toolEnrichmentAttempt
	inFlight   bool
	read       agent.CapturedMessageSupplement
	nativeRead bool
}

type toolSourceGeneration struct {
	owner agent.TranscriptOwner
}

type toolSourceState struct {
	*toolSourceGeneration
	location Location
}

// toolSupplementRequest freezes the source and pending epochs before the worker starts.
type toolSupplementRequest struct {
	state   *toolSourceState
	pending map[string]pendingToolRow
}

type toolBoundary struct {
	state    *toolSourceState
	pending  map[string]pendingToolRow
	children map[string]*Transcript
	retired  []*Transcript
}

func (row pendingToolRow) snapshot() pendingToolRow {
	row = row.effective()
	row.content = row.content.Clone()
	row.attempts = nil
	return row
}

func (row pendingToolRow) effective() pendingToolRow {
	for _, attempt := range row.attempts {
		previous, revision, supplemental, committed := attempt.receipt.CommittedEnrichment()
		if committed && previous == row.revision {
			row.revision = revision
			row.content.Supplemental = supplemental
		}
	}
	return row
}

func (row pendingToolRow) compact() pendingToolRow {
	row = row.effective()
	retained := make([]*toolEnrichmentAttempt, 0, len(row.attempts))
	for _, attempt := range row.attempts {
		if attempt.inFlight {
			retained = append(retained, attempt)
		}
	}
	row.attempts = retained
	return row
}

// matches reports whether other is the same entry at the same revision.
func (r pendingToolRow) matches(other pendingToolRow) bool {
	return r.epoch == other.epoch && r.revision == other.revision
}

// Location supplies the native record location. Ready admits a read, even when Path is empty.
type Location struct {
	SessionKey     string
	OutputFileRoot string
	Path           string
	Ready          bool
}

// toolSupplementReadBudget sets the maximum duration of an interim native read.
// A later pass can recover an unfinished interim result.
const toolSupplementReadBudget = 100 * time.Millisecond

// toolSupplementFinalReadBudget sets the maximum duration of a final native read.
// A final read ignores agent-context cancellation, so process exit does not discard stored native results.
const toolSupplementFinalReadBudget = time.Second

// New decorates the exact provider services with a source. The agent context controls the interim worker lifetime.
func New(ctx context.Context, services agent.ProviderServices, source Source) *Transcript {
	s := &Transcript{ProviderServices: services, ctx: ctx, source: source, pending: make(map[string]pendingToolRow)}
	s.queue = agent.OutputQueueFor(services)
	if s.queue == nil {
		s.queue = &agent.OutputQueue{}
	}
	s.orderedWriter = agent.OrderedTranscript(s.queue, toolTranscriptWriter{s})
	s.work = sync.NewCond(&s.mu)
	s.idle = sync.NewCond(&s.mu)
	return s
}

// toolTranscriptWriter preserves observation overrides in retained queue writes.
type toolTranscriptWriter struct{ *Transcript }

func (writer toolTranscriptWriter) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return writer.persistMessage(source, content, span)
}

func (writer toolTranscriptWriter) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	return writer.persistNotification(source, content)
}
func (writer toolTranscriptWriter) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	return writer.persistWorkerNotification(content)
}

func (writer toolTranscriptWriter) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	return writer.persistTurnEnd(content, span)
}

func (s *Transcript) OutputQueue() *agent.OutputQueue { return s.queue }

func (s *Transcript) Reset() {
	s.sourceMu.Lock()
	s.mu.Lock()
	children := make([]*Transcript, 0, len(s.children)+len(s.retired))
	for _, child := range s.children {
		children = append(children, child)
	}
	for child := range s.retired {
		children = append(children, child)
	}
	s.sessionKey = ""
	s.sessionID = ""
	s.retainHistoricalPendingLocked()
	s.pending = make(map[string]pendingToolRow)
	s.queued = nil
	s.children = nil
	s.retired = nil
	s.sourceState = nil
	if len(s.observations) > 0 {
		s.requestSupplementPass()
	}
	s.mu.Unlock()
	s.source.ResetRecords()
	s.sourceMu.Unlock()
	s.retainRetired(retireToolTranscripts(children))
}

// adoptLocation reads the provider location under sourceMu.
// It preserves pending entries until a nonempty session key changes.
func (s *Transcript) adoptLocation() (string, bool, []*Transcript) {
	s.mu.Lock()
	if s.retirement != nil && s.retirement.ready.Load() && s.sourceState != nil {
		location := s.sourceState.location
		s.mu.Unlock()
		return location.Path, location.Ready, nil
	}

	sessionID := s.sessionID
	s.mu.Unlock()
	location := s.source.Locate(sessionID)
	s.mu.Lock()
	var orphans []*Transcript
	reset := location.SessionKey != s.sessionKey && s.sessionKey != ""
	if reset {
		s.retainHistoricalPendingLocked()
		s.pending = make(map[string]pendingToolRow)
		for _, child := range s.children {
			orphans = append(orphans, child)
		}
		for child := range s.retired {
			orphans = append(orphans, child)
		}
		s.children = nil
		s.retired = nil
	}
	s.sessionKey = location.SessionKey
	if s.sourceState != nil && s.sourceState.location != location {
		s.sourceState = &toolSourceState{toolSourceGeneration: s.sourceState.toolSourceGeneration, location: location}
	}
	s.mu.Unlock()
	if reset {
		s.source.ResetRecords()
	}
	return location.Path, location.Ready, orphans
}

// adoptSource keeps the first current owner of one source generation.
// The caller holds sourceMu.
func (s *Transcript) adoptSource(content agent.MessageContent) (*toolSourceState, []*Transcript) {
	if !s.sourceContentCurrent(content) {
		return nil, nil
	}
	s.mu.Lock()
	previous := s.sourceState
	s.mu.Unlock()
	keep := previous != nil && s.sourceOwnerCurrent(previous.owner)
	var orphans []*Transcript
	if !keep {
		s.mu.Lock()
		if previous != nil {
			s.retainHistoricalPendingLocked()
			s.pending = make(map[string]pendingToolRow)
			for _, child := range s.children {
				orphans = append(orphans, child)
			}
			for child := range s.retired {
				orphans = append(orphans, child)
			}
			s.children = nil
			s.retired = nil
		}
		s.sessionID = content.AgentSessionID
		s.sourceState = &toolSourceState{toolSourceGeneration: &toolSourceGeneration{owner: content.Publication.Owner()}}
		s.mu.Unlock()
		if previous != nil {
			s.source.ResetRecords()
		}
	}
	_, _, locationOrphans := s.adoptLocation()
	orphans = append(orphans, locationOrphans...)
	s.mu.Lock()
	state := s.sourceState
	s.mu.Unlock()
	return state, orphans
}

func (s *Transcript) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	initial := content.Publication == nil
	if initial {
		content = s.captureLifetimeMessage(content, span)
	} else {
		content = content.Clone()
	}
	var readErr, mergeErr error
	s.sourceMu.Lock()
	state, orphans := s.adoptSource(content)
	if state != nil {
		if initial && state.location.Ready {
			ctx, cancel := s.supplementContext(false)
			extra, err := s.source.InitialSupplement(ctx, state.location.Path, slices.Clone(content.Original), span)
			cancel()
			readErr = err
			if len(extra) > 0 {
				combined, err := MergeSupplements(content.Supplemental, extra)
				mergeErr = err
				if err == nil {
					content.Supplemental = combined
				}
			}
		}
		if content.SupplementRead == nil && span.Closing && span.SpanID != "" {
			capturedSpan := span
			if capturedSpan.SpanType == "" {
				capturedSpan.SpanType = s.GetSpanType(span.SpanID)
			}
			if reader := s.source.CaptureSupplement(state.location, content, capturedSpan); reader != nil {
				content.SupplementRead = &capturedSupplementRead{pass: &s.passMu, read: reader}
			}
		}
	}
	s.sourceMu.Unlock()
	if readErr != nil {
		slog.Warn("Read initial tool supplement", "provider", s.source.ProviderName(), "error", readErr)
	}
	if mergeErr != nil {
		slog.Warn("Merge initial tool supplement", "provider", s.source.ProviderName(), "error", mergeErr)
	}
	s.retainRetired(retireToolTranscripts(orphans))
	return content
}

// supplementContext selects the native read deadline. Final reads ignore agent-context cancellation.
func (s *Transcript) supplementContext(final bool) (context.Context, context.CancelFunc) {
	if final {
		return context.WithTimeout(context.WithoutCancel(s.ctx), toolSupplementFinalReadBudget)
	}
	return context.WithTimeout(s.ctx, toolSupplementReadBudget)
}

// startSupplementWorkerLocked starts the worker on the first pass that needs it.
// The caller holds mu.
func (s *Transcript) startSupplementWorkerLocked() {
	if s.started || s.closed && len(s.observations) == 0 {
		return
	}
	s.started = true
	// Context cancellation wakes the worker through its close operation.
	if !s.closed {
		s.stopWatch = context.AfterFunc(s.ctx, s.closeSupplementWorker)
	}
	go s.runSupplementWorker()
}

// closeSupplementWorker permanently stops interim work and releases the context callback.
func (s *Transcript) closeSupplementWorker() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.queued = nil
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
		for s.queued == nil && !s.closed {
			s.work.Wait()
		}
		if s.closed && s.queued == nil {
			s.started = false
			s.mu.Unlock()
			return
		}
		request := s.queued
		s.queued = nil
		s.running = true
		final := s.closed
		s.mu.Unlock()
		if final {
			if err := s.flushObservations(); err != nil {
				slog.Warn("Retain historical tool data after the final read failed", "provider", s.source.ProviderName(), "error", err)
			}
		} else {
			s.runSupplementRequest(request, false)
		}
		s.mu.Lock()
		s.running = false
		s.idle.Broadcast()
		s.mu.Unlock()
		s.wakeRetirement()
		s.mu.Lock()
	}
}

// requestSupplementPass wakes the supplement worker. The caller holds mu.
//
// A later request replaces a queued request with its exact newer snapshot.
// A row that enters pending after that request needs its own later pass.
func (s *Transcript) requestSupplementPass() {
	if s.closed && len(s.observations) == 0 {
		return
	}
	s.startSupplementWorkerLocked()
	s.queued = s.snapshotSupplementRequestLocked()
	s.work.Signal()
}

// WaitForSupplementsForTest blocks until no interim pass waits or runs, so a
// test observes the result of a pass that it caused.
func (s *Transcript) WaitForSupplementsForTest() {
	s.waitForSupplements()
}

// waitForSupplements waits for complete interim work for external test observation.
// A final boundary never waits here because an enrichment observer can call that boundary itself.
func (s *Transcript) waitForSupplements() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.queued != nil || s.running {
		s.idle.Wait()
	}
}

// SourceForTest returns the immutable provider source, including its test-only store controls.
func (s *Transcript) SourceForTest() Source { return s.source }

// PendingSpanIDsForTest joins interim work and returns the remaining span IDs in order.
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

// UpdateSessionID forwards the new session to the wrapped sink. The wrapped
// sink stamps the session on every row, including each row that this wrapper
// writes through it. The embedded ProviderServices.MessageSessionID therefore
// reports the session of those rows, and this wrapper needs no override.
func (s *Transcript) UpdateSessionID(sessionID string) {
	s.ProviderServices.UpdateSessionID(sessionID)
	s.adoptMessageSession()
}

func (s *Transcript) AdoptNativeSession(sessionID string) {
	s.ProviderServices.AdoptNativeSession(sessionID)
	s.adoptMessageSession()
}

func (s *Transcript) adoptMessageSession() {
	content := s.ProviderServices.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	s.sourceMu.Lock()
	state, orphans := s.adoptSource(content)
	s.mu.Lock()
	s.requestPendingSupplementLocked(state)
	s.mu.Unlock()
	s.sourceMu.Unlock()
	s.retainRetired(retireToolTranscripts(orphans))
}

func (s *Transcript) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if agent.IsTranscriptWriteExecuting(content) && !s.acceptsExecutingWrite(content) {
		return false, errTranscriptRetired
	}
	return s.orderedWriter.PersistNotification(source, content)
}

func (s *Transcript) persistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	return s.ProviderServices.PersistNotification(source, s.CaptureMessage(content, agent.SpanInfo{}))
}

func (s *Transcript) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	if agent.IsTranscriptWriteExecuting(content) && !s.acceptsExecutingWrite(content) {
		return false, errTranscriptRetired
	}
	return s.orderedWriter.PersistWorkerNotification(content)
}

func (s *Transcript) persistWorkerNotification(content agent.MessageContent) (bool, error) {
	return s.ProviderServices.PersistWorkerNotification(s.CaptureMessage(content, agent.SpanInfo{}))
}

func (s *Transcript) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if agent.IsTranscriptWriteExecuting(content) && !s.acceptsExecutingWrite(content) {
		return errTranscriptRetired
	}
	return s.orderedWriter.PersistMessage(source, content, span)
}

func (s *Transcript) persistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return s.ProviderServices.PersistMessage(source, content, span)
	}
	if content.WriteReceipt == nil {
		content.WriteReceipt = agent.NewTranscriptWriteReceipt()
	}
	content = s.CaptureMessage(content, span)
	s.sourceMu.Lock()
	state, orphans := s.adoptSource(content)
	s.mu.Lock()
	s.requestPendingSupplementLocked(state)
	s.mu.Unlock()
	toolCallID := ""
	if state != nil && span.Closing && span.SpanID != "" {
		if s.source.ToolCallID(content.Original) != "" {
			toolCallID = span.SpanID
		}
	}
	s.sourceMu.Unlock()
	s.retainRetired(retireToolTranscripts(orphans))
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	if !content.WriteReceipt.ClaimSourceObservation() {
		return nil
	}
	if state == nil {
		s.mu.Lock()
		s.retainHistoricalRowLocked(span.SpanID, content)
		if len(s.observations) > 0 {
			s.requestSupplementPass()
		}
		s.mu.Unlock()
		return nil
	}
	s.sourceMu.Lock()
	s.mu.Lock()
	same := s.sourceState == state
	s.mu.Unlock()
	if same && s.sourceOwnerCurrent(state.owner) {
		sequence, committed := content.WriteReceipt.StoredMessageSequence()
		s.mu.Lock()
		newer := false
		if previous, exists := s.pending[toolCallID]; exists && toolCallID != "" {
			previousSequence, known := previous.content.WriteReceipt.StoredMessageSequence()
			newer = committed && known && previousSequence > sequence
		}
		s.mu.Unlock()
		if newer {
			s.sourceMu.Unlock()
			return nil
		}
		s.source.ObserveMessage(content.Clone(), span)
		if toolCallID != "" {
			s.mu.Lock()
			s.nextPendingEpoch++
			s.pending[toolCallID] = pendingToolRow{content: content.Clone(), epoch: s.nextPendingEpoch, sourceGeneration: state.toolSourceGeneration}
			s.mu.Unlock()
		}
	} else {
		s.mu.Lock()
		s.retainHistoricalRowLocked(span.SpanID, content)
		if len(s.observations) > 0 {
			s.requestSupplementPass()
		}
		s.mu.Unlock()
	}
	s.sourceMu.Unlock()
	return nil
}

func (s *Transcript) prepareBoundary() (toolBoundary, error) {
	if err := s.flushObservations(); err != nil {
		// The background supplement worker can hold an observation claim when a
		// provider's turn ends. The claim stays with that flush -- it settles the
		// row against the observation's original sequence -- and the boundary
		// keeps the observation retained for its own final pass or a later one,
		// so an in-flight claim must not fail the turn end. Any other error is a
		// real settlement failure the caller has to see.
		if !errors.Is(err, errToolObservationInFlight) {
			return toolBoundary{}, err
		}
		slog.Warn("Retain the in-flight tool observation at the turn boundary", "provider", s.source.ProviderName(), "error", err)
	}
	s.mu.Lock()
	s.queued = nil
	boundary := toolBoundary{state: s.sourceState, pending: make(map[string]pendingToolRow, len(s.pending)), children: make(map[string]*Transcript, len(s.children)), retired: make([]*Transcript, 0, len(s.retired))}
	for id, entry := range s.pending {
		boundary.pending[id] = entry.snapshot()
	}
	for id, child := range s.children {
		boundary.children[id] = child
	}
	for child := range s.retired {
		boundary.retired = append(boundary.retired, child)
	}
	s.mu.Unlock()
	s.runSupplementPass(true)
	s.mu.Lock()
	for id, original := range boundary.pending {
		if current, exists := s.pending[id]; exists && current.epoch == original.epoch {
			boundary.pending[id] = current.snapshot()
		}
	}
	s.mu.Unlock()
	return boundary, nil
}

func (s *Transcript) completeBoundary(boundary toolBoundary, retiring bool) bool {
	s.sourceMu.Lock()
	if !retiring && !s.source.CanFinalizeTurn() {
		s.sourceMu.Unlock()
		return false
	}
	s.mu.Lock()
	if s.sourceState != boundary.state {
		s.mu.Unlock()
		s.sourceMu.Unlock()
		return false
	}
	for id, expected := range boundary.pending {
		if current, exists := s.pending[id]; exists && current.effective().matches(expected) {
			delete(s.pending, id)
		}
	}
	children := make([]*Transcript, 0, len(boundary.children))
	for id, child := range boundary.children {
		if s.children[id] == child {
			children = append(children, child)
		}
	}
	retired := make([]*Transcript, 0, len(boundary.retired))
	for _, child := range boundary.retired {
		if _, exists := s.retired[child]; exists {
			retired = append(retired, child)
		}
	}
	finished := len(s.pending) == 0
	s.mu.Unlock()
	if finished && (retiring || boundary.state == nil || s.sourceOwnerCurrent(boundary.state.owner)) {
		s.source.FinishTurn()
	}
	s.sourceMu.Unlock()
	if retiring {
		return true
	}
	for _, child := range children {
		if child.retirementComplete() {
			continue
		}
		if err := child.FinalizePending(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
			slog.Warn("Retain pending child tool enrichment", "provider", child.source.ProviderName(), "error", err)
		}
	}
	for _, child := range retired {
		child.startRetirement()
	}
	return true
}

func (s *Transcript) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if agent.IsTranscriptWriteExecuting(content) && !s.acceptsExecutingWrite(content) {
		return errTranscriptRetired
	}
	return s.orderedWriter.PersistTurnEnd(content, span)
}

func (s *Transcript) persistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if content.WriteReceipt == nil {
		content.WriteReceipt = agent.NewTranscriptWriteReceipt()
	}
	content = s.CaptureMessage(content, span)
	var boundary toolBoundary
	current := content.Publication != nil && s.source.AuthorityIsCurrent(content.Publication.Owner()) && s.source.CanFinalizeTurn()
	if current {
		var err error
		boundary, err = s.prepareBoundary()
		if err != nil {
			return err
		}
	}
	if err := s.ProviderServices.PersistTurnEnd(content, span); err != nil {
		return err
	}
	if current && s.source.AuthorityIsCurrent(content.Publication.Owner()) && content.WriteReceipt.ClaimSourceObservation() {
		s.completeBoundary(boundary, false)
	}
	return nil
}

// FinalizePending orders final native enrichment behind all previously accepted rows.
// It rechecks the captured session authority and the provider's concurrency policy.
func (s *Transcript) FinalizePending() error {
	content := s.ProviderServices.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	queue := s.queue
	queue.Append(agent.OutputWrite{Label: "finish native tool enrichment", Run: func() error {
		if content.Publication == nil || !s.source.AuthorityIsCurrent(content.Publication.Owner()) || !s.source.CanFinalizeTurn() {
			return nil
		}
		err := s.finishPending(false)
		if errors.Is(err, agent.ErrOutputInFlight) {
			return nil
		}
		return err
	}})
	return queue.Flush()
}

func (s *Transcript) finishPending(retiring bool) error {
	boundary, err := s.prepareBoundary()
	if err != nil {
		return err
	}
	if !s.completeBoundary(boundary, retiring) {
		return agent.ErrOutputInFlight
	}
	return nil
}

// retireToolTranscripts attempts final enrichment before closing each worker.
// It returns unresolved children for retention. The caller holds no wrapper mutex.
func retireToolTranscripts(children []*Transcript) []*Transcript {
	var unresolved []*Transcript
	for _, child := range children {
		child.startRetirement()
		if !child.retirementComplete() {
			unresolved = append(unresolved, child)
		}
	}
	return unresolved
}

func (s *Transcript) retainRetired(children []*Transcript) {
	for _, child := range children {
		s.mu.Lock()
		if s.retired == nil {
			s.retired = make(map[*Transcript]struct{})
		}
		s.retired[child] = struct{}{}
		s.mu.Unlock()
		child.onRetired(func() { s.mu.Lock(); delete(s.retired, child); s.mu.Unlock() })
	}
}

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

// runSupplementPass prepares a current final-read snapshot. Interim workers use their exact queued snapshot.
func (s *Transcript) runSupplementPass(final bool) {
	s.runSupplementRequest(nil, final)
}

// snapshotSupplementRequestLocked keeps each row in its original source generation.
// The caller holds mu. A stale generation remains pending until an accepted final boundary removes it.
func (s *Transcript) snapshotSupplementRequestLocked() *toolSupplementRequest {
	request := &toolSupplementRequest{state: s.sourceState, pending: make(map[string]pendingToolRow)}
	if request.state == nil {
		return request
	}
	for id, entry := range s.pending {
		if entry.sourceGeneration == request.state.toolSourceGeneration {
			request.pending[id] = entry.snapshot()
		}
	}
	return request
}

func (s *Transcript) runSupplementRequest(request *toolSupplementRequest, final bool) {
	if err := s.flushObservations(); err != nil {
		slog.Warn("Retain the tool observations after enrichment failed", "provider", s.source.ProviderName(), "error", err)
	}
	s.sourceMu.Lock()
	_, ready, orphans := s.adoptLocation()
	s.mu.Lock()
	if request == nil {
		request = s.snapshotSupplementRequestLocked()
	}
	current := s.sourceState == request.state
	s.mu.Unlock()
	s.sourceMu.Unlock()
	s.retainRetired(retireToolTranscripts(orphans))
	if !current || !ready || len(request.pending) == 0 {
		return
	}
	protocol := make(map[string]agent.MessageContent, len(request.pending))
	for id, entry := range request.pending {
		protocol[id] = entry.content.Clone()
	}
	s.passMu.Lock()
	ctx, cancel := s.supplementContext(final)
	records, err := s.source.ReadSupplements(ctx, request.state.location, protocol, final)
	cancel()
	s.passMu.Unlock()
	if err != nil {
		slog.Warn("Read stored tool records", "provider", s.source.ProviderName(), "final", final, "error", err)
	}
	s.mu.Lock()
	current = s.sourceState == request.state
	s.mu.Unlock()
	if current {
		s.applySupplements(records, request.pending)
	}
	if err := s.flushObservations(); err != nil && !errors.Is(err, errToolObservationInFlight) {
		slog.Warn("Retain historical tool data after the native pass", "provider", s.source.ProviderName(), "error", err)
	}

}

// enrichPending admits one write to the exact pending epoch and revision.
// The delegate runs without a wrapper mutex.
func (s *Transcript) enrichPending(id string, expected pendingToolRow, extra []byte, remove bool, observation *toolObservation) (bool, error) {
	defer s.wakeRetirement()
	combined, err := MergeSupplements(expected.content.Supplemental, slices.Clone(extra))
	if err != nil {
		return false, fmt.Errorf("merge tool supplement: %w", err)
	}
	attempt := &toolEnrichmentAttempt{receipt: agent.NewMessageEnrichmentReceipt(), previousRevision: expected.revision, supplemental: slices.Clone(combined), inFlight: true}
	s.mu.Lock()
	if expected.epoch != 0 {
		current, exists := s.pending[id]
		if !exists || !current.effective().matches(expected) {
			s.mu.Unlock()
			return false, nil
		}
		current = current.compact()
		for _, running := range current.attempts {
			_, _, _, committed := running.receipt.CommittedEnrichment()
			if running.inFlight && !committed && running.previousRevision == expected.revision && agent.JSONCanonicalEqual(running.supplemental, combined) {
				if observation != nil {
					observation.attempt = running
				}
				s.mu.Unlock()
				return false, nil
			}
		}
		current.attempts = append(current.attempts, attempt)
		s.pending[id] = current
	}
	if observation != nil {
		observation.attempt = attempt
	}
	s.mu.Unlock()
	sequence, _ := expected.content.WriteReceipt.StoredMessageSequence()
	if observation != nil {
		sequence = observation.sequence
	}
	_, written, err := s.writeSupplement(agent.MessageEnrichment{Publication: expected.content.Publication, AgentSessionID: expected.content.AgentSessionID,
		Seq: sequence, SpanID: id, OriginalContent: slices.Clone(expected.content.Original), PreviousRevision: expected.revision, WriteReceipt: attempt.receipt}, nil, combined)
	s.mu.Lock()
	attempt.inFlight = false
	_, revision, _, committed := attempt.receipt.CommittedEnrichment()
	if observation != nil {
		if committed {
			s.removeObservationLocked(observation)
		}
	}
	if current, exists := s.pending[id]; exists && expected.epoch != 0 && current.epoch == expected.epoch {
		current = current.compact()
		if remove && err == nil && written && committed && current.revision == revision && !s.hasObservationLocked(sequence) {
			delete(s.pending, id)
		} else {
			s.pending[id] = current
		}
	}
	s.mu.Unlock()
	return written, err
}

func (s *Transcript) removeObservationLocked(observation *toolObservation) {
	s.observations = slices.DeleteFunc(s.observations, func(current *toolObservation) bool { return current == observation })
}

func (s *Transcript) hasObservationLocked(sequence int64) bool {
	for _, observation := range s.observations {
		if observation.sequence == sequence {
			return true
		}
	}
	return false
}

// errToolObservationInFlight states that another flush owns the observation's
// claim right now. A concurrent claimer reports it instead of waiting, so a
// claim held through a slow delegated read cannot pile up waiters.
var errToolObservationInFlight = fmt.Errorf("%w: the tool observation is in flight", agent.ErrOutputInFlight)

// retainHistoricalPendingLocked preserves final reads before a source replacement.
// The caller holds mu. Each reader already owns its native lookup inputs.
func (s *Transcript) retainHistoricalPendingLocked() {
	for id, row := range s.pending {
		s.retainHistoricalRowLocked(id, row.content)
	}
	if len(s.observations) > 0 {
		s.requestSupplementPass()
	}
}

func (s *Transcript) retainHistoricalRowLocked(id string, content agent.MessageContent) {
	if content.SupplementRead == nil || content.WriteReceipt == nil {
		return
	}
	sequence, known := content.WriteReceipt.StoredMessageSequence()
	if !known {
		return
	}
	for _, existing := range s.observations {
		if existing.sequence == sequence && existing.nativeRead {
			return
		}
	}
	s.observations = append(s.observations, &toolObservation{spanID: id, sequence: sequence, content: content.Clone(), read: content.SupplementRead, nativeRead: true})
}

// flushObservations retries frozen bytes against their exact committed rows.
// It never resolves an old observation through the latest row of a reused span.
func (s *Transcript) flushObservations() error {
	s.mu.Lock()
	observations := append([]*toolObservation(nil), s.observations...)
	s.mu.Unlock()
	for _, observation := range observations {
		if err := s.flushObservation(observation); err != nil {
			return err
		}
	}
	return nil
}

// flushObservation claims the exact observation before its first delegated read.
// An accepted receipt remains visible during synchronous final-boundary reentry.
func (s *Transcript) flushObservation(observation *toolObservation) error {
	s.mu.Lock()
	_, _, _, committed := observation.attemptReceipt().CommittedEnrichment()
	if committed || !slices.Contains(s.observations, observation) {
		s.removeObservationLocked(observation)
		s.mu.Unlock()
		return nil
	}
	if observation.inFlight {
		s.mu.Unlock()
		return errToolObservationInFlight
	}
	observation.inFlight = true
	s.mu.Unlock()
	defer s.releaseObservation(observation)

	if observation.read != nil {
		ctx, cancel := s.supplementContext(true)
		extra, err := observation.read.Read(ctx)
		cancel()
		if err != nil && len(extra) == 0 {
			return err
		}
		if err != nil {
			slog.Warn("Keep available historical tool data after a partial native read", "provider", s.source.ProviderName(), "error", err)
		}
		observation.extra = slices.Clone(extra)
		observation.read = nil
		if len(extra) == 0 {
			s.mu.Lock()
			s.removeObservationLocked(observation)
			s.mu.Unlock()
			return nil
		}
	}
	stored, err := s.ReadToolResultBySeq(observation.sequence)
	if err != nil {
		return err
	}
	if stored == nil || stored.Content.AgentSessionID != observation.content.AgentSessionID || !bytes.Equal(stored.Content.Original, observation.content.Original) {
		return fmt.Errorf("the stored tool row does not match its retained observation")
	}
	combined, err := MergeSupplements(stored.Content.Supplemental, observation.extra)
	if err != nil {
		return fmt.Errorf("merge the retained tool observation: %w", err)
	}
	if agent.JSONCanonicalEqual(stored.Content.Supplemental, combined) {
		s.mu.Lock()
		s.removeObservationLocked(observation)
		s.mu.Unlock()
		return nil
	}
	expected := pendingToolRow{content: observation.content.Clone(), revision: stored.Revision}
	expected.content.Supplemental = slices.Clone(stored.Content.Supplemental)
	s.mu.Lock()
	if current, exists := s.pending[observation.spanID]; exists && current.epoch == observation.epoch {
		sequence, known := current.content.WriteReceipt.StoredMessageSequence()
		current = current.compact()
		if known && sequence == observation.sequence && current.revision <= stored.Revision {
			current.revision = stored.Revision
			current.content.Supplemental = slices.Clone(stored.Content.Supplemental)
			s.pending[observation.spanID] = current
			expected = current.snapshot()
		}
	}
	s.mu.Unlock()
	written, err := s.enrichPending(observation.spanID, expected, observation.extra, false, observation)
	if err != nil {
		return err
	}
	if !written {
		s.mu.Lock()
		_, _, _, accepted := observation.attemptReceipt().CommittedEnrichment()
		s.mu.Unlock()
		if !accepted {
			return fmt.Errorf("the stored tool row refused its retained observation")
		}
	}
	return nil
}

func (s *Transcript) releaseObservation(observation *toolObservation) {
	s.mu.Lock()
	observation.inFlight = false
	s.mu.Unlock()
	s.wakeRetirement()
}

func (observation *toolObservation) attemptReceipt() *agent.MessageEnrichmentReceipt {
	if observation.attempt == nil {
		return nil
	}
	return observation.attempt.receipt
}

func (s *Transcript) applySupplements(records map[string][]byte, requested map[string]pendingToolRow) {
	for id, enriched := range records {
		entry, exists := requested[id]
		if !exists {
			continue
		}
		written, err := s.enrichPending(id, entry, enriched, true, nil)
		if err != nil {
			slog.Warn("Enrich tool result", "provider", s.source.ProviderName(), "error", err)
			continue
		}
		if !written {
			slog.Debug("Stored tool row refused the supplement", "provider", s.source.ProviderName(), "span_id", id)
		}
	}
}

// EnrichToolSpan stores an observed supplement on its exact tool result row.
// Cursor sends each extension frame once and acknowledges it even when the immediate write fails.
// This transcript therefore retains the built bytes after a compare-and-swap refusal.
// A later pass or final boundary merges those bytes over the actual stored supplement.
//
// build receives a clone of the original frame. It runs outside every wrapper mutex.
// A nil result requests no write. The return value reports an immediate stored change.
func (s *Transcript) EnrichToolSpan(spanID string, build func(original []byte) ([]byte, error)) (bool, error) {
	if spanID == "" || build == nil {
		return false, nil
	}
	s.mu.Lock()
	entry, waiting := s.pending[spanID]
	entry = entry.snapshot()
	s.mu.Unlock()
	if !waiting {
		return s.enrichSettledToolSpan(spanID, build)
	}
	sequence, committed := entry.content.WriteReceipt.StoredMessageSequence()
	if !committed {
		return false, fmt.Errorf("the pending tool row has no committed sequence")
	}
	extra, err := build(slices.Clone(entry.content.Original))
	if err != nil || len(extra) == 0 {
		return false, err
	}
	observation := &toolObservation{spanID: spanID, sequence: sequence, epoch: entry.epoch, content: entry.content.Clone(), extra: slices.Clone(extra), inFlight: true}
	s.mu.Lock()
	s.observations = append(s.observations, observation)
	s.mu.Unlock()
	defer s.releaseObservation(observation)
	return s.enrichPending(spanID, entry, extra, false, observation)
}

// enrichSettledToolSpan captures the original owner and native session before it reads a settled result.
// The first read fixes the exact sequence before build runs.
// Later attempts use that sequence and cannot select a replacement row through the same span.
func (s *Transcript) enrichSettledToolSpan(spanID string, build func(original []byte) ([]byte, error)) (bool, error) {
	captured := s.ProviderServices.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{SpanID: spanID})
	stored, err := s.ReadToolResultForSession(spanID, captured.AgentSessionID)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	captured.Original = slices.Clone(stored.Content.Original)
	captured.Supplemental = slices.Clone(stored.Content.Supplemental)
	captured.Metadata = slices.Clone(stored.Content.Metadata)
	extra, err := build(slices.Clone(captured.Original))
	if err != nil || len(extra) == 0 {
		return false, err
	}
	observation := &toolObservation{spanID: spanID, sequence: stored.Seq, content: captured.Clone(), extra: slices.Clone(extra), inFlight: true}
	s.mu.Lock()
	s.observations = append(s.observations, observation)
	s.mu.Unlock()
	defer s.releaseObservation(observation)
	return s.enrichPending(spanID, pendingToolRow{content: captured, revision: stored.Revision}, extra, false, observation)
}

// writeSupplement merges the supplied bytes and delegates one exact row update.
// It returns the merged bytes beside the delegate result.
func (s *Transcript) writeSupplement(enrichment agent.MessageEnrichment, base, extra []byte) ([]byte, bool, error) {
	combined, err := MergeSupplements(base, extra)
	if err != nil {
		return nil, false, fmt.Errorf("merge tool supplement: %w", err)
	}
	enrichment.SupplementalContent = combined
	written, err := s.EnrichMessage(enrichment)
	return combined, written, err
}

// MergeSupplements retains prior fields and replaces matching fields with later values.
// JSONCanonicalEqual compares object values without treating key order as a change.
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

// requestPendingSupplementLocked schedules a pass only for a ready current source.
// The caller holds sourceMu and mu. A nil state must not reuse an earlier source.
func (s *Transcript) requestPendingSupplementLocked(state *toolSourceState) {
	if state != nil && state.location.Ready && len(s.pending) > 0 {
		s.requestSupplementPass()
	}
}
