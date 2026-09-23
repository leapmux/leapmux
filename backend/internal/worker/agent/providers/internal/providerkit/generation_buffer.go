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
}

// GenerationBuffer keeps provider deltas until their item reaches a boundary.
type GenerationBuffer struct {
	mu        sync.Mutex
	segments  map[string]*generationSegment
	nextOrder uint64
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
	b.mu.Unlock()
}

func (b *GenerationBuffer) Finish(scopeID string, completion agent.MessageCompletion) ([]byte, bool, error) {
	b.mu.Lock()
	segment, ok := b.segments[scopeID]
	delete(b.segments, scopeID)
	b.mu.Unlock()
	if !ok || segment.text.Len() == 0 {
		return nil, false, nil
	}
	raw, err := agent.MarshalAssembledMessage(segment.kind, segment.text.String(), completion)
	return raw, true, err
}

func (b *GenerationBuffer) Discard(scopeID string) {
	if scopeID == "" {
		return
	}
	b.mu.Lock()
	delete(b.segments, scopeID)
	b.mu.Unlock()
}

func (b *GenerationBuffer) FinishAll(completion agent.MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(*generationSegment) bool { return true })
}

func (b *GenerationBuffer) FinishKind(kind agent.AssembledMessageKind, completion agent.MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(segment *generationSegment) bool { return segment.kind == kind })
}

func (b *GenerationBuffer) DiscardKind(kind agent.AssembledMessageKind) {
	b.mu.Lock()
	for key, segment := range b.segments {
		if segment.kind == kind {
			delete(b.segments, key)
		}
	}
	b.mu.Unlock()
}

func (b *GenerationBuffer) finishMatching(
	completion agent.MessageCompletion,
	matches func(*generationSegment) bool,
) ([][]byte, error) {
	b.mu.Lock()
	segments := make([]*generationSegment, 0, len(b.segments))
	for key, segment := range b.segments {
		if matches(segment) {
			segments = append(segments, segment)
			delete(b.segments, key)
		}
	}
	sort.Slice(segments, func(left, right int) bool { return segments[left].order < segments[right].order })
	b.mu.Unlock()

	result := make([][]byte, 0, len(segments))
	for _, segment := range segments {
		if segment.text.Len() == 0 {
			continue
		}
		raw, err := agent.MarshalAssembledMessage(segment.kind, segment.text.String(), completion)
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, nil
}

// PersistAll writes each segment in first-seen order. A failed write keeps the
// failed segment and every later segment for the next lifecycle boundary.
func (b *GenerationBuffer) PersistAll(completion agent.MessageCompletion, persist func([]byte) error) error {
	return b.persistMatching(completion, func(*generationSegment) bool { return true }, persist)
}

// PersistKind writes and removes all segments of one message kind.
func (b *GenerationBuffer) PersistKind(
	kind agent.AssembledMessageKind,
	completion agent.MessageCompletion,
	persist func([]byte) error,
) error {
	return b.persistMatching(completion, func(segment *generationSegment) bool {
		return segment.kind == kind
	}, persist)
}

// PersistScope writes and removes one segment. It reports whether a segment
// existed, including an empty segment.
func (b *GenerationBuffer) PersistScope(
	scopeID string,
	completion agent.MessageCompletion,
	persist func([]byte) error,
) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	segment := b.segments[scopeID]
	if segment == nil {
		return false, nil
	}
	if segment.text.Len() == 0 {
		delete(b.segments, scopeID)
		return true, nil
	}
	raw, err := agent.MarshalAssembledMessage(segment.kind, segment.text.String(), completion)
	if err != nil {
		return true, err
	}
	if err := persist(raw); err != nil {
		return true, err
	}
	delete(b.segments, scopeID)
	return true, nil
}

func (b *GenerationBuffer) persistMatching(
	completion agent.MessageCompletion,
	matches func(*generationSegment) bool,
	persist func([]byte) error,
) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	type orderedSegment struct {
		scopeID string
		segment *generationSegment
	}
	segments := make([]orderedSegment, 0, len(b.segments))
	for scopeID, segment := range b.segments {
		if matches(segment) {
			segments = append(segments, orderedSegment{scopeID: scopeID, segment: segment})
		}
	}
	sort.Slice(segments, func(left, right int) bool {
		return segments[left].segment.order < segments[right].segment.order
	})
	for _, entry := range segments {
		if entry.segment.text.Len() == 0 {
			delete(b.segments, entry.scopeID)
			continue
		}
		raw, err := agent.MarshalAssembledMessage(entry.segment.kind, entry.segment.text.String(), completion)
		if err != nil {
			return err
		}
		if err := persist(raw); err != nil {
			return err
		}
		delete(b.segments, entry.scopeID)
	}
	return nil
}

func (b *GenerationBuffer) Reset() {
	b.mu.Lock()
	b.segments = nil
	b.nextOrder = 0
	b.mu.Unlock()
}

// MoveAllTo transfers buffered segments in first-seen order. The destination
// assigns fresh order values so existing destination segments stay first.
func (b *GenerationBuffer) MoveAllTo(destination *GenerationBuffer) {
	if destination == nil || destination == b {
		return
	}
	b.mu.Lock()
	type orderedSegment struct {
		scopeID string
		segment *generationSegment
	}
	segments := make([]orderedSegment, 0, len(b.segments))
	for scopeID, segment := range b.segments {
		segments = append(segments, orderedSegment{scopeID: scopeID, segment: segment})
	}
	sort.Slice(segments, func(left, right int) bool {
		return segments[left].segment.order < segments[right].segment.order
	})
	b.segments = nil
	b.nextOrder = 0
	b.mu.Unlock()
	for _, entry := range segments {
		destination.Append(entry.scopeID, entry.segment.kind, entry.segment.text.String(), JoinVerbatim)
	}
}

// RetainedTextLenForTest returns how many bytes of text the buffer holds for
// scopeID.
func (b *GenerationBuffer) RetainedTextLenForTest(scopeID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	segment := b.segments[scopeID]
	if segment == nil {
		return 0
	}
	return segment.text.Len()
}
