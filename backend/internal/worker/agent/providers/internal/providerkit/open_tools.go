package providerkit

import (
	"cmp"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const defaultClosedSpanCap = 256

// ClosedSpanSet tracks span IDs that were force-closed by interrupt or teardown
// so late native updates and completion frames cannot revive progress or write
// duplicate closing rows. Callers must synchronize access.
type ClosedSpanSet struct {
	set   map[string]struct{}
	order []string
}

// MarkClosed records spanID as closed, evicting the oldest ID past the cap.
func (s *ClosedSpanSet) MarkClosed(spanID string) {
	if spanID == "" {
		return
	}
	if s.set == nil {
		s.set = make(map[string]struct{})
	}
	if _, exists := s.set[spanID]; exists {
		return
	}
	s.set[spanID] = struct{}{}
	s.order = append(s.order, spanID)
	if len(s.order) <= defaultClosedSpanCap {
		return
	}
	excess := len(s.order) - defaultClosedSpanCap
	for _, evictID := range s.order[:excess] {
		delete(s.set, evictID)
	}
	s.order = slices.Delete(s.order, 0, excess)
}

// WasClosed reports whether spanID is currently recorded as closed.
func (s *ClosedSpanSet) WasClosed(spanID string) bool {
	if spanID == "" || len(s.set) == 0 {
		return false
	}
	_, ok := s.set[spanID]
	return ok
}

// Reopen removes spanID from the closed set when a new tool call reuses it.
func (s *ClosedSpanSet) Reopen(spanID string) {
	if spanID == "" || len(s.set) == 0 {
		return
	}
	if _, ok := s.set[spanID]; !ok {
		return
	}
	delete(s.set, spanID)
	s.order = slices.DeleteFunc(s.order, func(id string) bool {
		return id == spanID
	})
}

// Reset clears all recorded closed span IDs.
func (s *ClosedSpanSet) Reset() {
	clear(s.set)
	s.order = s.order[:0]
}

// OpenToolEntry holds one open tool span entry in deterministic opening order.
type OpenToolEntry[T any] struct {
	SpanID   string
	SpanType string
	Order    uint64
	NoSpan   bool
	Data     T
}

// ToolSpanLedger tracks open tool spans in insertion order alongside a
// ClosedSpanSet for deduplicating late native updates and completions.
// Callers must synchronize access.
type ToolSpanLedger[T any] struct {
	entries   map[string]OpenToolEntry[T]
	nextOrder uint64
	closed    ClosedSpanSet
}

// Open records a new open tool entry and clears spanID from the closed set.
// It returns false without modifying the ledger when spanID is empty or already open.
func (l *ToolSpanLedger[T]) Open(spanID, spanType string, noSpan bool, data T) bool {
	if spanID == "" {
		return false
	}
	if _, exists := l.entries[spanID]; exists {
		return false
	}
	if l.entries == nil {
		l.entries = make(map[string]OpenToolEntry[T])
	}
	l.nextOrder++
	l.entries[spanID] = OpenToolEntry[T]{
		SpanID:   spanID,
		SpanType: spanType,
		Order:    l.nextOrder,
		NoSpan:   noSpan,
		Data:     data,
	}
	l.closed.Reopen(spanID)
	return true
}

// Get returns the open entry for spanID without removing it.
func (l *ToolSpanLedger[T]) Get(spanID string) (OpenToolEntry[T], bool) {
	if spanID == "" || len(l.entries) == 0 {
		return OpenToolEntry[T]{}, false
	}
	entry, ok := l.entries[spanID]
	return entry, ok
}

// WasClosed reports whether spanID is recorded as force-closed.
func (l *ToolSpanLedger[T]) WasClosed(spanID string) bool {
	return l.closed.WasClosed(spanID)
}

// Delete removes spanID from the open set without recording it in the closed
// set. Use it when another owner takes over the span so its later native
// completion still persists normally.
func (l *ToolSpanLedger[T]) Delete(spanID string) {
	if spanID == "" {
		return
	}
	delete(l.entries, spanID)
}

// CompleteNative removes an open entry on native completion or reports whether
// spanID was already force-closed.
func (l *ToolSpanLedger[T]) CompleteNative(spanID string) (entry OpenToolEntry[T], wasOpen bool, alreadyClosed bool) {
	if spanID == "" {
		return OpenToolEntry[T]{}, false, false
	}
	if open, ok := l.entries[spanID]; ok {
		delete(l.entries, spanID)
		return open, true, false
	}
	if l.closed.WasClosed(spanID) {
		return OpenToolEntry[T]{}, false, true
	}
	return OpenToolEntry[T]{}, false, false
}

// DrainAll removes all open entries in deterministic (Order, SpanID) order and
// records each drained SpanID in the closed set.
func (l *ToolSpanLedger[T]) DrainAll() []OpenToolEntry[T] {
	return l.DrainMatching(nil)
}

// DrainMatching removes every open entry for which match returns true (or every
// open entry when match is nil), returns them sorted by (Order, SpanID), and
// records each drained SpanID in the closed set.
func (l *ToolSpanLedger[T]) DrainMatching(match func(OpenToolEntry[T]) bool) []OpenToolEntry[T] {
	if len(l.entries) == 0 {
		return nil
	}
	var drained []OpenToolEntry[T]
	for spanID, entry := range l.entries {
		if match != nil && !match(entry) {
			continue
		}
		drained = append(drained, entry)
		delete(l.entries, spanID)
	}
	slices.SortFunc(drained, func(a, b OpenToolEntry[T]) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.SpanID, b.SpanID))
	})
	for _, entry := range drained {
		l.closed.MarkClosed(entry.SpanID)
	}
	return drained
}

// CloseToolSpans persists each closing row built with ok == true, closes its
// span when !entry.NoSpan, and completes its output progress. CloseSpan and
// CompleteOutputProgress still run when build returns ok == false or when
// PersistMessage returns an error.
func CloseToolSpans[T any](
	sink ToolLifecycleServices,
	entries []OpenToolEntry[T],
	completion agent.MessageCompletion,
	build func(OpenToolEntry[T]) (agent.MessageContent, agent.SpanInfo, bool),
) {
	if sink == nil {
		return
	}
	for _, entry := range entries {
		if build != nil {
			if content, span, ok := build(entry); ok {
				if span.SpanID == "" {
					span.SpanID = entry.SpanID
				}
				if span.SpanType == "" {
					span.SpanType = entry.SpanType
				}
				if entry.NoSpan {
					span.NoSpan = true
				}
				span.Closing = true
				content.Completion = completion
				_ = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
			}
		}
		if !entry.NoSpan {
			sink.CloseSpan(entry.SpanID)
		}
		sink.ReportProgress(agent.CompleteOutputProgress(entry.SpanID))
	}
}

// ResetSpansAndProgress resets span geometry and all generation progress on sink.
func ResetSpansAndProgress(sink ToolLifecycleServices) {
	if sink == nil {
		return
	}
	sink.ResetSpans()
	sink.ReportProgress(agent.ResetProgress())
}

// ChildCompletion maps a child background task's final status to the
// MessageCompletion used when closing its remaining unfinished output and tools.
func ChildCompletion(status bgtask.Status) agent.MessageCompletion {
	switch status {
	case bgtask.StatusStopped, bgtask.StatusInterrupted:
		return agent.MessageCompletionInterrupted
	case bgtask.StatusFailed:
		return agent.MessageCompletionError
	case bgtask.StatusEndedWithUnknownOutcome:
		return agent.MessageCompletionFinished
	default:
		return agent.MessageCompletionComplete
	}
}
