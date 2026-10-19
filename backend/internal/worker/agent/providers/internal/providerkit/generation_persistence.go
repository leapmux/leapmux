package providerkit

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// OutputQueue returns the ordered writes that follow this buffer's stream boundaries.
func (b *GenerationBuffer) OutputQueue() *agent.OutputQueue {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue, _ := b.outputLocked()
	return queue
}

func (b *GenerationBuffer) outputLocked() (*agent.OutputQueue, *agent.OutputOwner) {
	if b.output == nil {
		b.output = &agent.OutputQueue{}
	}
	if b.outputOwner == nil {
		b.outputOwner = agent.NewOutputOwner()
	}
	return b.output, b.outputOwner
}

// HasPendingWrites reports whether a completed generation still awaits persistence.
func (b *GenerationBuffer) HasPendingWrites() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.output != nil && b.output.HasOwner(b.outputOwner)
}

// PersistAll freezes every open scope, then writes pending rows in boundary order.
// A failed write retains its original bytes, completion, and destination for the next persistence call.
func (b *GenerationBuffer) PersistAll(completion agent.MessageCompletion, persist func([]byte) error) error {
	return b.persistMatching(completion, func(*generationSegment) bool { return true }, generationCallback(persist))
}

// PersistKind freezes open scopes of one kind, then drains all pending writes in boundary order.
func (b *GenerationBuffer) PersistKind(kind agent.AssembledMessageKind, completion agent.MessageCompletion, persist func([]byte) error) error {
	return b.persistMatching(completion, func(segment *generationSegment) bool { return segment.kind == kind }, generationCallback(persist))
}

// PersistScope freezes one open scope, then drains pending writes in boundary order.
// Its boolean reports an open scope or a pending write for the requested scope.
func (b *GenerationBuffer) PersistScope(scopeID string, completion agent.MessageCompletion, persist func([]byte) error) (bool, error) {
	return b.persistScopes([]string{scopeID}, completion, generationCallback(persist))
}

func (b *GenerationBuffer) persistScopes(scopeIDs []string, completion agent.MessageCompletion, prepare func([]byte) func(*agent.OutputTicket) error) (bool, error) {
	b.writeMu.Lock()
	b.mu.Lock()
	selected := make(map[string]bool, len(scopeIDs))
	var entries []orderedSegment
	found := false
	for _, scopeID := range scopeIDs {
		if selected[scopeID] {
			continue
		}
		selected[scopeID] = true
		if segment := b.segments[scopeID]; segment != nil {
			entries = append(entries, orderedSegment{scopeID: scopeID, segment: segment})
			found = true
		}
	}
	queue, owner := b.outputLocked()
	for scopeID := range selected {
		found = found || queue.HasScope(owner, scopeID)
	}
	pending, err := b.freezeEntriesLocked(entries, completion, prepare)
	b.mu.Unlock()
	b.writeMu.Unlock()
	if err != nil {
		return found, err
	}
	return found, b.prepareAndFlush(pending, prepare)
}

func (b *GenerationBuffer) persistMatching(completion agent.MessageCompletion, matches func(*generationSegment) bool, prepare func([]byte) func(*agent.OutputTicket) error) error {
	b.writeMu.Lock()
	b.mu.Lock()
	pending, err := b.freezeEntriesLocked(b.orderedSegmentsLocked(matches), completion, prepare)
	b.mu.Unlock()
	b.writeMu.Unlock()
	if err != nil {
		return err
	}
	return b.prepareAndFlush(pending, prepare)
}

type generationPreparation struct {
	raw    []byte
	run    func(*agent.OutputTicket) error
	ready  atomic.Bool
	ticket *agent.OutputTicket
}

func generationCallback(persist func([]byte) error) func([]byte) func(*agent.OutputTicket) error {
	if persist == nil {
		return nil
	}
	return func(raw []byte) func(*agent.OutputTicket) error {
		return func(*agent.OutputTicket) error { return persist(slices.Clone(raw)) }
	}
}

func (b *GenerationBuffer) prepareAndFlush(pending []*generationPreparation, prepare func([]byte) func(*agent.OutputTicket) error) error {
	for _, row := range pending {
		row.run = prepare(slices.Clone(row.raw))
		row.ready.Store(true)
	}
	err := b.OutputQueue().Flush()
	// The other drainer owns every submitted row. It will reach this batch in order.
	if err == agent.ErrOutputInFlight {
		return nil
	}
	if err != nil {
		// Snapshot creation succeeded, so the queue owns the writes despite this storage failure.
		return &agent.PendingOutputError{Err: err}
	}
	return nil
}

// freezeEntriesLocked reserves every selected row before a capture can call the sink.
// Native output that arrives during capture must remain after this batch.
func (b *GenerationBuffer) freezeEntriesLocked(entries []orderedSegment, completion agent.MessageCompletion, prepare func([]byte) func(*agent.OutputTicket) error) ([]*generationPreparation, error) {
	if len(entries) != 0 && prepare == nil {
		return nil, errors.New("the generation write has no persistence function")
	}
	rows, err := b.finishEntriesLocked(entries, completion)
	if err != nil {
		return nil, err
	}
	queue, owner := b.outputLocked()
	writes := make([]agent.OutputWrite, 0, len(rows))
	pending := make([]*generationPreparation, 0, len(rows))
	index := 0
	for _, entry := range entries {
		if entry.segment.text.Len() == 0 {
			continue
		}
		row := &generationPreparation{raw: rows[index]}
		pending = append(pending, row)
		writes = append(writes, agent.OutputWrite{Owner: owner, ScopeID: entry.scopeID, RetainedBytes: entry.segment.text.Len(),
			Label: fmt.Sprintf("persist generation scope %q", entry.scopeID), Run: func() error {
				if !row.ready.Load() {
					return agent.ErrOutputInFlight
				}
				if row.run == nil {
					return errors.New("the captured generation has no persistence function")
				}
				return row.run(row.ticket)
			}})
		index++
	}
	tickets := queue.Append(writes...)
	for i, ticket := range tickets {
		pending[i].ticket = ticket
	}
	return pending, nil
}

// bindOutput shares the sink's queue before this buffer submits a new batch.
// Pending rows retain their original destinations when an earlier caller used a local queue.
func (b *GenerationBuffer) bindOutput(sink agent.TranscriptServices) {
	queue := agent.OutputQueueFor(sink)
	if queue == nil {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	if b.output == nil {
		b.output = queue
		b.outputOwner = agent.NewOutputOwner()
		b.mu.Unlock()
		return
	}
	previous, owner := b.outputLocked()
	b.mu.Unlock()
	if previous == queue {
		return
	}
	previous.MoveOwnerTo(owner, queue, owner)
	b.mu.Lock()
	b.output = queue
	b.mu.Unlock()
}

// generationDestination captures one batch's original session and span context before its first write.
// Capture runs after queue reservation and outside the stream-buffer lock.
type generationDestination struct {
	writer   agent.TranscriptServices
	template agent.MessageContent
	span     agent.SpanInfo
	captured bool
}

func (destination *generationDestination) prepare(raw []byte) func(*agent.OutputTicket) error {
	if !destination.captured {
		destination.template = destination.writer.CaptureMessage(destination.template, destination.span)
		destination.captured = true
	}
	content := destination.template.Clone()
	content.Original = raw
	record := agent.CaptureTranscript(destination.writer, content, destination.span)
	return func(ticket *agent.OutputTicket) error {
		return record.PersistQueuedMessage(ticket, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	}
}

// PersistScopeTo submits one scope through the sink's ordered writer.
// The template supplies the session, metadata and native key for this scope.
func (b *GenerationBuffer) PersistScopeTo(scopeID string, completion agent.MessageCompletion, sink agent.TranscriptServices, template agent.MessageContent, span agent.SpanInfo) (bool, error) {
	b.bindOutput(sink)
	destination := generationDestination{writer: agent.DirectTranscript(sink), template: template.Clone(), span: span}
	return b.persistScopes([]string{scopeID}, completion, destination.prepare)
}

// PersistAllTo submits all scopes through the sink's ordered writer.
// Each row receives the template's metadata. A multi-scope batch must not reuse one native idempotency key.
func (b *GenerationBuffer) PersistAllTo(completion agent.MessageCompletion, sink agent.TranscriptServices, template agent.MessageContent, span agent.SpanInfo) error {
	b.bindOutput(sink)
	destination := generationDestination{writer: agent.DirectTranscript(sink), template: template.Clone(), span: span}
	return b.persistMatching(completion, func(*generationSegment) bool { return true }, destination.prepare)
}
