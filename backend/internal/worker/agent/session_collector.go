package agent

import (
	"container/heap"
	"sort"
	"strings"
)

// StoredSessionCollector retains the newest sessions without retaining discarded records.
// A nonpositive limit retains every valid record. The collector has one caller at a time.
type StoredSessionCollector struct {
	limit int
	next  uint64
	items storedSessionHeap
}

// NewStoredSessionCollector creates a collector with the requested result limit.
func NewStoredSessionCollector(limit int) *StoredSessionCollector {
	return &StoredSessionCollector{limit: limit}
}

// Add accepts a nonblank handle without changing its spelling.
// Equal timestamps and handles retain their encounter order. Duplicate handles remain separate records.
func (c *StoredSessionCollector) Add(session StoredSession) {
	if strings.TrimSpace(session.Handle) == "" {
		return
	}
	item := rankedStoredSession{session: session, sequence: c.next}
	c.next++
	if c.limit <= 0 {
		c.items = append(c.items, item)
		return
	}
	if len(c.items) < c.limit {
		heap.Push(&c.items, item)
		return
	}
	if item.before(c.items[0]) {
		c.items[0] = item
		heap.Fix(&c.items, 0)
	}
}

// Len returns the number of retained sessions.
func (c *StoredSessionCollector) Len() int { return len(c.items) }

// Sessions returns a separate slice in display order. The collector remains usable.
func (c *StoredSessionCollector) Sessions() []StoredSession {
	ordered := append(storedSessionHeap(nil), c.items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].before(ordered[j]) })
	result := make([]StoredSession, len(ordered))
	for i, item := range ordered {
		result[i] = item.session
	}
	return result
}

type rankedStoredSession struct {
	session  StoredSession
	sequence uint64
}

func (a rankedStoredSession) before(b rankedStoredSession) bool {
	if !a.session.UpdatedAt.Equal(b.session.UpdatedAt) {
		return a.session.UpdatedAt.After(b.session.UpdatedAt)
	}
	if a.session.Handle != b.session.Handle {
		return a.session.Handle < b.session.Handle
	}
	return a.sequence < b.sequence
}

// The least preferred record stays at the root, so a better record replaces it directly.
type storedSessionHeap []rankedStoredSession

func (h storedSessionHeap) Len() int           { return len(h) }
func (h storedSessionHeap) Less(i, j int) bool { return h[j].before(h[i]) }
func (h storedSessionHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *storedSessionHeap) Push(value any)    { *h = append(*h, value.(rankedStoredSession)) }
func (h *storedSessionHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = rankedStoredSession{}
	*h = (*h)[:last]
	return value
}
