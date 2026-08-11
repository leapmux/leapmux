package service

import (
	"fmt"
	"sync"
)

// spanTrackerKind records whether a tracker belongs to a root main agent or a
// virtual child agent. Root and child trackers use the same *SpanTracker type;
// the kind lets cleanup and orphan-sweep code reason about each population
// without scanning the other per-agent maps.
type spanTrackerKind uint8

const (
	// spanTrackerRoot is the kind for a root main agent that owns a provider
	// process. Created by NewSink; deleted by CleanupAgent.
	spanTrackerRoot spanTrackerKind = iota
	// spanTrackerChild is the kind for a virtual child agent (a subagent
	// transcript fed by the parent provider's process; it never owns a
	// process). Created on the first ChildSink(childID); deleted by
	// CleanupChild / CleanupChildAgents.
	spanTrackerChild
)

// spanTrackerRegistry owns the per-agent SpanTrackers. It replaces the flat
// `sync.Map` that previously held root and child trackers under one
// indistinguishable namespace, so the lifecycle of each population is explicit.
//
// # Lifecycle
//
// A child tracker is created on the first ChildSink(childID) call and deleted
// on CleanupChildAgent(childID) (a single child's closing update) or
// CleanupChildAgents (the root-close batch). A root tracker is created on
// NewSink and deleted on CleanupAgent.
//
// # Pointer stability
//
// A sink captures its *SpanTracker pointer once at construction; the registry
// keeps the SAME allocation for the entry's lifetime (a getOrCreate for an
// existing id returns the stored pointer, never a fresh one). This preserves
// the invariant that a sink's captured tracker stays valid even after a
// concurrent reset, and it lets the sink bypass the map on the hot persist
// path.
//
// # Benign orphan after cleanup
//
// A sink holds a *SpanTracker pointer, not a registry lease. After
// CleanupChildAgent deletes a child's entry, a provider goroutine that retained
// the child sink may still call OpenSpan/CloseSpan on it. Those calls mutate a
// GC-retained orphan tracker whose entry is gone from the registry, so no
// future Snapshot reads the state. This is benign by design: the terminal
// observation that drives cleanup is contractually the last event a provider
// emits for that child, so late calls are rare and their writes are
// unobservable. A drain would serialize provider read-loops on cleanup for no
// correctness gain.
//
// # Concurrency
//
// The persist hot path does not touch the registry: a sink captures its
// *SpanTracker pointer once at construction and calls Snapshot on the pointer
// directly. The registry is touched only on lifecycle (NewSink / ChildSink /
// Cleanup) and on the read-only snapshot fallback (trackerForSnapshot). So
// getOrCreate takes the write Lock. It runs at most once per id (a child sink
// is cached on its root after the first call). A single critical section reads
// and writes the one entry, so the tracker and the kind stay consistent and the
// kind read cannot race a concurrent delete. The read-only methods (get, reset,
// rangeAll, len) take the RLock; they never mutate the map, so they admit
// concurrent readers and only block the rare lifecycle writer.
type spanTrackerRegistry struct {
	mu   sync.RWMutex
	byID map[string]spanTrackerEntry
}

// spanTrackerEntry pairs a tracker with the kind it was registered under. One
// map of entries is a single source of truth: an insert or a delete touches one
// key, so the tracker and the kind cannot drift apart.
type spanTrackerEntry struct {
	tracker *SpanTracker
	kind    spanTrackerKind
}

func newSpanTrackerRegistry() *spanTrackerRegistry {
	return &spanTrackerRegistry{
		byID: make(map[string]spanTrackerEntry),
	}
}

// getOrCreate returns the tracker for id, creating one of the given kind if it
// does not exist. A re-getOrCreate for an existing id MUST specify the same
// kind — a child id registered as root (or vice versa) is a programming error
// and panics. Same-kind re-registration is idempotent and returns the original
// pointer, preserving pointer stability.
//
// The whole method runs under the write lock. getOrCreate is a lifecycle call
// (NewSink / ChildSink, at most once per id — a child sink is cached on its
// root after the first call), so it is not on the persist hot path, which uses
// the sink's captured *SpanTracker pointer directly. A single critical section
// reads and writes the one entry, so the tracker and the kind can never be
// observed half-updated, and the kind read cannot race a concurrent delete.
func (r *spanTrackerRegistry) getOrCreate(id string, kind spanTrackerKind) *SpanTracker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byID[id]; ok {
		if e.kind != kind {
			panic(fmt.Sprintf("spanTrackerRegistry: kind conflict for %q: registered as %s, requested %s",
				id, e.kind, kind))
		}
		return e.tracker
	}
	t := &SpanTracker{}
	r.byID[id] = spanTrackerEntry{tracker: t, kind: kind}
	return t
}

// get returns the tracker and its kind, or (nil, 0, false) when id is unknown.
// Read-only; never creates an entry. Use this from paths that must NOT
// implicitly seed a tracker (e.g. a snapshot fallback whose agent is expected
// to already have a sink).
func (r *spanTrackerRegistry) get(id string) (*SpanTracker, spanTrackerKind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[id]
	if !ok {
		return nil, 0, false
	}
	return e.tracker, e.kind, true
}

// reset clears the tracker's in-memory state (active spans, colors, parent
// map) without removing its entry, so a sink's captured pointer stays valid
// across a context-clear restart. No-op for an unknown id.
func (r *spanTrackerRegistry) reset(id string) {
	r.mu.RLock()
	e, ok := r.byID[id]
	r.mu.RUnlock()
	if ok {
		e.tracker.Reset()
	}
}

// delete removes the entry. The *SpanTracker allocation survives (a sink may
// still hold a pointer — see the "Benign orphan" note on the type); only the
// registry's ownership ends. Idempotent.
func (r *spanTrackerRegistry) delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
}

// rangeAll invokes f for every tracked id (root and child, in map iteration
// order). Used by TrackedAgentIDs to build the orphan-sweep candidate set. f
// runs UNDER the read lock, which gives it a consistent snapshot of byID and
// blocks writers (delete, getOrCreate) for the duration; it also means f MUST
// NOT re-enter a write method on the same registry (delete, getOrCreate) — the
// held RLock blocks the write Lock and self-deadlocks. This differs from
// sync.Map.Range, which calls f without a lock. Read-only re-entry (get) is
// safe because the RWMutex admits concurrent readers.
func (r *spanTrackerRegistry) rangeAll(f func(id string) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id := range r.byID {
		if !f(id) {
			return
		}
	}
}

// len returns the total number of tracked entries (root + child).
func (r *spanTrackerRegistry) len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// String renders the kind for the getOrCreate panic message and for tests;
// kept minimal so the panic reads as a programming error, not a runtime
// condition.
func (k spanTrackerKind) String() string {
	switch k {
	case spanTrackerRoot:
		return "root"
	case spanTrackerChild:
		return "child"
	default:
		return "unknown"
	}
}
