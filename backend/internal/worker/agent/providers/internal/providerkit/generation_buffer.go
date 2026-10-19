package providerkit

import (
	"sort"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type generationSegment struct {
	kind  agent.AssembledMessageKind
	text  strings.Builder
	order uint64
	trace *generationDigest
}

// orderedSegment pairs one segment with its scope, for an ordered walk.
type orderedSegment struct {
	scopeID string
	segment *generationSegment
}

// GenerationBuffer keeps provider deltas until their item reaches a boundary.
type GenerationBuffer struct {
	// writeMu serializes persistence, finish, and transfer operations.
	// A sink callback can read, append, or discard through mu.
	// It must not start another persistence, finish, or transfer operation on this buffer.
	writeMu     sync.Mutex
	mu          sync.Mutex
	segments    map[string]*generationSegment
	output      *agent.OutputQueue
	outputOwner *agent.OutputOwner
	nextOrder   uint64
}

// Append adds one provider fragment with its protocol-defined join behavior.
// Process output does not use GenerationBuffer and always remains verbatim.
func (b *GenerationBuffer) Append(scopeID string, kind agent.AssembledMessageKind, text string, join TextJoin) {
	if scopeID == "" || text == "" {
		return
	}
	b.mu.Lock()
	if b.segments == nil {
		b.segments = make(map[string]*generationSegment)
	}
	segment := b.segments[scopeID]
	if segment == nil {
		segment = &generationSegment{order: b.nextOrder}
		b.nextOrder++
		b.segments[scopeID] = segment
	}
	segment.kind = kind
	AppendText(&segment.text, text, join)
	snapshot := captureGenerationSnapshot(segment)
	b.mu.Unlock()
	traceGenerationSnapshot(scopeID, kind, snapshot)
}

// ScopeSnapshot reads one open stream scope without changing its lifetime.
// The returned text stays unchanged after later buffer operations.
func (b *GenerationBuffer) ScopeSnapshot(scopeID string) (agent.AssembledMessageKind, string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	segment := b.segments[scopeID]
	if scopeID == "" || segment == nil {
		return "", "", false
	}
	return segment.kind, segment.text.String(), true
}

// ScopeIDs returns only open stream scopes, in first-seen order. Pending writes have no open scope.
func (b *GenerationBuffer) ScopeIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	segments := b.orderedSegmentsLocked(func(*generationSegment) bool { return true })
	scopeIDs := make([]string, 0, len(segments))
	for _, entry := range segments {
		scopeIDs = append(scopeIDs, entry.scopeID)
	}
	return scopeIDs
}

// orderedSegmentsLocked returns the segments that match, in first-seen order.
// The caller holds b.mu.
func (b *GenerationBuffer) orderedSegmentsLocked(matches func(*generationSegment) bool) []orderedSegment {
	segments := make([]orderedSegment, 0, len(b.segments))
	for scopeID, segment := range b.segments {
		if matches(segment) {
			segments = append(segments, orderedSegment{scopeID: scopeID, segment: segment})
		}
	}
	sort.Slice(segments, func(left, right int) bool {
		return segments[left].segment.order < segments[right].segment.order
	})
	return segments
}

// Finish drains pending writes, then returns one open scope with the requested completion.
func (b *GenerationBuffer) Finish(scopeID string, completion agent.MessageCompletion) ([]byte, bool, error) {
	if err := b.OutputQueue().Flush(); err != nil {
		return nil, false, err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	segment := b.segments[scopeID]
	if segment == nil {
		return nil, false, nil
	}
	rows, err := b.finishEntriesLocked([]orderedSegment{{scopeID: scopeID, segment: segment}}, completion)
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return rows[0], true, nil
}

// Discard removes one open scope without changing pending writes or resetting the order.
func (b *GenerationBuffer) Discard(scopeID string) {
	if scopeID == "" {
		return
	}
	b.mu.Lock()
	delete(b.segments, scopeID)
	b.mu.Unlock()
}

// FinishAll drains pending writes, then returns all open scopes in first-seen order. It keeps the order counter.
func (b *GenerationBuffer) FinishAll(completion agent.MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(*generationSegment) bool { return true })
}

// FinishKind drains pending writes, then returns open scopes of one kind in first-seen order. It keeps the order counter.
func (b *GenerationBuffer) FinishKind(kind agent.AssembledMessageKind, completion agent.MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(segment *generationSegment) bool { return segment.kind == kind })
}

// DiscardKind removes open scopes of one kind. It keeps pending writes and the order counter.
func (b *GenerationBuffer) DiscardKind(kind agent.AssembledMessageKind) {
	b.mu.Lock()
	for key, segment := range b.segments {
		if segment.kind == kind {
			delete(b.segments, key)
		}
	}
	b.mu.Unlock()
}

func (b *GenerationBuffer) finishMatching(completion agent.MessageCompletion, matches func(*generationSegment) bool) ([][]byte, error) {
	if err := b.OutputQueue().Flush(); err != nil {
		return nil, err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finishEntriesLocked(b.orderedSegmentsLocked(matches), completion)
}

// finishEntriesLocked encodes every row before it removes any open scope.
func (b *GenerationBuffer) finishEntriesLocked(entries []orderedSegment, completion agent.MessageCompletion) ([][]byte, error) {
	result := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		if entry.segment.text.Len() == 0 {
			continue
		}
		raw, err := agent.MarshalAssembledMessage(entry.segment.kind, entry.segment.text.String(), completion)
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	for _, entry := range entries {
		delete(b.segments, entry.scopeID)
	}
	return result, nil
}

// DiscardMatching removes matching open scopes and returns them in first-seen order. It keeps pending writes.
// The predicate runs under mu and must not call this buffer.
func (b *GenerationBuffer) DiscardMatching(match func(scopeID string) bool) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var removed []string
	for _, entry := range b.orderedSegmentsLocked(func(*generationSegment) bool { return true }) {
		if match(entry.scopeID) {
			delete(b.segments, entry.scopeID)
			removed = append(removed, entry.scopeID)
		}
	}
	return removed
}

// Reset drops the session output, including pending writes, and resets the first-seen order.
func (b *GenerationBuffer) Reset() {
	b.mu.Lock()
	b.segments = nil
	if b.output != nil {
		b.output.ClearOwner(b.outputOwner)
	}
	b.nextOrder = 0
	b.mu.Unlock()
}

// MoveAllTo transfers pending writes and open scopes. Pending writes keep their original destinations.
// Existing destination writes stay first. Open scopes receive fresh destination order values.
func (b *GenerationBuffer) MoveAllTo(destination *GenerationBuffer) {
	if destination == nil || destination == b {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	segments := b.orderedSegmentsLocked(func(*generationSegment) bool { return true })
	output, owner := b.outputLocked()
	b.segments = nil
	b.nextOrder = 0
	b.mu.Unlock()
	destination.mu.Lock()
	destinationOutput, destinationOwner := destination.outputLocked()
	destination.mu.Unlock()
	output.MoveOwnerTo(owner, destinationOutput, destinationOwner)
	for _, entry := range segments {
		destination.Append(entry.scopeID, entry.segment.kind, entry.segment.text.String(), JoinVerbatim)
	}
}

// RetainedTextLenForTest counts open and pending text for scopeID.
func (b *GenerationBuffer) RetainedTextLenForTest(scopeID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := 0
	if segment := b.segments[scopeID]; segment != nil {
		length = segment.text.Len()
	}
	if b.output != nil {
		length += b.output.RetainedBytes(b.outputOwner, scopeID)
	}
	return length
}
