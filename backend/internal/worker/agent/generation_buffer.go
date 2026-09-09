package agent

import (
	"sort"
	"sync"
)

type generationSegment struct {
	kind  AssembledMessageKind
	text  string
	order uint64
}

// GenerationBuffer keeps provider deltas until their item reaches a boundary.
type GenerationBuffer struct {
	mu        sync.Mutex
	segments  map[string]generationSegment
	nextOrder uint64
}

func (b *GenerationBuffer) Append(scopeID string, kind AssembledMessageKind, text string) {
	if scopeID == "" || text == "" {
		return
	}
	b.mu.Lock()
	if b.segments == nil {
		b.segments = make(map[string]generationSegment)
	}
	segment, exists := b.segments[scopeID]
	if !exists {
		segment.order = b.nextOrder
		b.nextOrder++
	}
	segment.kind = kind
	segment.text += text
	b.segments[scopeID] = segment
	b.mu.Unlock()
}

func (b *GenerationBuffer) Finish(scopeID string, completion MessageCompletion) ([]byte, bool, error) {
	b.mu.Lock()
	segment, ok := b.segments[scopeID]
	delete(b.segments, scopeID)
	b.mu.Unlock()
	if !ok || segment.text == "" {
		return nil, false, nil
	}
	raw, err := MarshalAssembledMessage(segment.kind, segment.text, completion)
	return raw, true, err
}

func (b *GenerationBuffer) FinishAll(completion MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(generationSegment) bool { return true })
}

func (b *GenerationBuffer) FinishKind(kind AssembledMessageKind, completion MessageCompletion) ([][]byte, error) {
	return b.finishMatching(completion, func(segment generationSegment) bool { return segment.kind == kind })
}

func (b *GenerationBuffer) DiscardKind(kind AssembledMessageKind) {
	b.mu.Lock()
	for key, segment := range b.segments {
		if segment.kind == kind {
			delete(b.segments, key)
		}
	}
	b.mu.Unlock()
}

func (b *GenerationBuffer) finishMatching(
	completion MessageCompletion,
	matches func(generationSegment) bool,
) ([][]byte, error) {
	b.mu.Lock()
	segments := make([]generationSegment, 0, len(b.segments))
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
		if segment.text == "" {
			continue
		}
		raw, err := MarshalAssembledMessage(segment.kind, segment.text, completion)
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, nil
}

func (b *GenerationBuffer) Reset() {
	b.mu.Lock()
	b.segments = nil
	b.nextOrder = 0
	b.mu.Unlock()
}
