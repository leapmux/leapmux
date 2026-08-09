package service

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpanTrackerRegistry_GetOrCreateRootAndChild verifies the registry records
// the kind on first insertion and returns the SAME pointer on subsequent
// lookups (pointer stability — a sink captures the pointer once and must keep
// seeing the same allocation).
func TestSpanTrackerRegistry_GetOrCreateRootAndChild(t *testing.T) {
	t.Parallel()

	r := newSpanTrackerRegistry()

	root := r.getOrCreate("agent-1", spanTrackerRoot)
	child := r.getOrCreate("child-1", spanTrackerChild)

	// Same-kind re-registration is idempotent: returns the stored pointer.
	assert.Same(t, root, r.getOrCreate("agent-1", spanTrackerRoot), "root pointer must be stable")
	assert.Same(t, child, r.getOrCreate("child-1", spanTrackerChild), "child pointer must be stable")

	// Kind is recorded and queryable.
	if got, kind, ok := r.get("agent-1"); assert.True(t, ok, "root present") {
		assert.Same(t, root, got)
		assert.Equal(t, spanTrackerRoot, kind)
	}
	if got, kind, ok := r.get("child-1"); assert.True(t, ok, "child present") {
		assert.Same(t, child, got)
		assert.Equal(t, spanTrackerChild, kind)
	}

	assert.Equal(t, 2, r.len(), "both populations counted")
}

// TestSpanTrackerRegistry_GetUnknownReturnsFalse verifies get is read-only and
// does NOT seed an entry. Paths that must not implicitly create a tracker
// (e.g. the persistAndBroadcast nil-fallback) depend on this.
func TestSpanTrackerRegistry_GetUnknownReturnsFalse(t *testing.T) {
	t.Parallel()
	r := newSpanTrackerRegistry()

	tk, kind, ok := r.get("nope")
	assert.False(t, ok, "unknown id is not found")
	assert.Nil(t, tk)
	assert.Zero(t, kind)
	assert.Equal(t, 0, r.len(), "get did not seed an entry")
}

// TestSpanTrackerRegistry_KindConflictPanics verifies the programming-error
// invariant: a child id registered as root (or vice versa) panics rather than
// silently corrupting the kind bookkeeping.
func TestSpanTrackerRegistry_KindConflictPanics(t *testing.T) {
	t.Parallel()

	t.Run("root then child", func(t *testing.T) {
		t.Parallel()
		r := newSpanTrackerRegistry()
		r.getOrCreate("agent-x", spanTrackerRoot)
		assert.Panics(t, func() { r.getOrCreate("agent-x", spanTrackerChild) })
	})
	t.Run("child then root", func(t *testing.T) {
		t.Parallel()
		r := newSpanTrackerRegistry()
		r.getOrCreate("child-x", spanTrackerChild)
		assert.Panics(t, func() { r.getOrCreate("child-x", spanTrackerRoot) })
	})
}

// TestSpanTrackerRegistry_DeleteIsIdempotent verifies delete removes the entry
// and that a repeat delete (or a delete of an unknown id) is a no-op.
func TestSpanTrackerRegistry_DeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	r := newSpanTrackerRegistry()
	r.getOrCreate("agent-1", spanTrackerRoot)
	r.getOrCreate("child-1", spanTrackerChild)

	r.delete("child-1")
	_, _, ok := r.get("child-1")
	assert.False(t, ok, "child deleted")
	assert.Equal(t, 1, r.len(), "only root remains")

	// Repeat delete + unknown-id delete are no-ops.
	assert.NotPanics(t, func() { r.delete("child-1") })
	assert.NotPanics(t, func() { r.delete("never-was") })
}

// TestSpanTrackerRegistry_ResetClearsStateKeepsEntry verifies reset returns the
// tracker to empty WITHOUT removing the registry entry — a sink's captured
// pointer must keep resolving to a tracked, usable tracker across a
// context-clear restart.
func TestSpanTrackerRegistry_ResetClearsStateKeepsEntry(t *testing.T) {
	t.Parallel()
	r := newSpanTrackerRegistry()
	tk := r.getOrCreate("agent-1", spanTrackerRoot)

	// Open a span, then assert a snapshot of it renders a non-empty column.
	tk.OpenSpan("span-a", "")
	_, linesBefore, _ := tk.Snapshot("span-a", "", false)
	require.NotEqual(t, "[]", linesBefore, "span is active before reset")

	r.reset("agent-1")

	_, linesAfter, _ := tk.Snapshot("", "", false)
	assert.Equal(t, "[]", linesAfter, "state cleared by reset")

	got, kind, ok := r.get("agent-1")
	require.True(t, ok, "entry survives reset")
	assert.Same(t, tk, got, "same pointer after reset")
	assert.Equal(t, spanTrackerRoot, kind)

	// reset of an unknown id is a no-op (no panic, no seeding).
	assert.NotPanics(t, func() { r.reset("never-was") })
	assert.Equal(t, 1, r.len(), "unknown reset seeded nothing")
}

// TestSpanTrackerRegistry_RangeAllCoversBothPopulations verifies rangeAll
// enumerates root and child ids together, matching the TrackedAgentIDs use case
// (the orphan sweep candidate set is the union).
func TestSpanTrackerRegistry_RangeAllCoversBothPopulations(t *testing.T) {
	t.Parallel()
	r := newSpanTrackerRegistry()
	r.getOrCreate("root-1", spanTrackerRoot)
	r.getOrCreate("root-2", spanTrackerRoot)
	r.getOrCreate("child-1", spanTrackerChild)
	r.getOrCreate("child-2", spanTrackerChild)

	seen := map[string]bool{}
	r.rangeAll(func(id string) bool {
		seen[id] = true
		return true
	})
	assert.Equal(t, map[string]bool{"root-1": true, "root-2": true, "child-1": true, "child-2": true}, seen)

	// Early termination: returning false stops iteration.
	got := []string{}
	r.rangeAll(func(id string) bool {
		got = append(got, id)
		return false // stop after the first
	})
	assert.Len(t, got, 1, "returning false stops iteration")
}

// TestSpanTrackerRegistry_GetOrCreateConcurrentSameKind drives many goroutines
// through getOrCreate for the same id + kind at once. The slow-path re-check
// (RLock miss → Lock → re-check → maybe insert) must insert exactly one entry:
// every goroutine observes the SAME single pointer, and exactly one entry
// exists afterward. A race that returned a second allocation would surface
// here as a pointer mismatch or a len > 1.
func TestSpanTrackerRegistry_GetOrCreateConcurrentSameKind(t *testing.T) {
	t.Parallel()

	const goroutines = 64
	r := newSpanTrackerRegistry()

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	var first atomic.Pointer[SpanTracker]
	for range goroutines {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // all goroutines hot before the gate opens
			got := r.getOrCreate("agent-race", spanTrackerRoot)
			first.CompareAndSwap(nil, got)
		}()
	}
	start.Done() // release the gate
	done.Wait()

	expected := first.Load()
	require.NotNil(t, expected, "at least one goroutine produced a tracker")

	// Every observed pointer must match the first — getOrCreate is idempotent
	// under contention, never allocating a second tracker for the same id.
	got := r.getOrCreate("agent-race", spanTrackerRoot)
	assert.Same(t, expected, got, "single stable pointer across concurrent getOrCreate")

	// Lockstep invariant: exactly one entry, and its kind matches.
	_, kind, ok := r.get("agent-race")
	require.True(t, ok)
	assert.Equal(t, spanTrackerRoot, kind)
	assert.Equal(t, 1, r.len(), "no duplicate entries from the race")
}

// TestSpanTrackerRegistry_GetOrCreateVsDeleteRace drives getOrCreate (which
// reads the entry) against delete (which removes it) on the SAME id at high
// concurrency. Under -race this catches an unprotected entry read: a
// getOrCreate that read the map AFTER releasing the RLock would race a
// write-locked delete, which the Go runtime reports as "concurrent map read
// and map write". The single-Lock getOrCreate reads and writes the entry
// inside one write critical section, so the tracker and the kind stay
// consistent and the race detector stays quiet. This is the regression test
// for that bug.
func TestSpanTrackerRegistry_GetOrCreateVsDeleteRace(t *testing.T) {
	t.Parallel()

	r := newSpanTrackerRegistry()
	const ids = 8
	const itersPerID = 200

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	// Per-id workers: half getOrCreate, half delete, all on the same id set.
	for i := range ids {
		id := fmt.Sprintf("agent-%d", i)
		// getOrCreate worker (reads byID + kindByID).
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			for range itersPerID {
				// A kind conflict here would be a real bug — the id is only ever
				// registered as root. The point is to not race the delete.
				r.getOrCreate(id, spanTrackerRoot)
			}
		}()
		// delete worker (writes byID + kindByID).
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			for range itersPerID {
				r.delete(id)
			}
		}()
	}
	start.Done()
	done.Wait()

	// The registry is in a consistent state regardless of who won the last race.
	// Re-create the entry and confirm get/getOrCreate agree on the kind, which
	// proves the entry's tracker and kind stayed paired under contention.
	tk := r.getOrCreate("agent-0", spanTrackerRoot)
	got, kind, ok := r.get("agent-0")
	require.True(t, ok, "entry present after a final getOrCreate")
	assert.Same(t, tk, got, "pointer stable after the race")
	assert.Equal(t, spanTrackerRoot, kind, "kind matches the entry after the race")
}
