package streamevents

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestAgentCursor_Update_Monotonic pins the contract that lower-seq
// updates do not regress the cursor. The reconnect path relies on
// this — if we ever processed an out-of-order frame after a
// reconnect, the next resubscribe must not ask for already-seen
// events.
func TestAgentCursor_Update_Monotonic(t *testing.T) {
	c := NewAgentCursor()
	c.Track("a-1", 0)
	assert.Equal(t, int64(7), c.Update("a-1", 7))
	// Lower seq must not regress.
	assert.Equal(t, int64(7), c.Update("a-1", 3))
	assert.Equal(t, int64(7), c.Get("a-1"))
	// Higher seq advances normally.
	assert.Equal(t, int64(12), c.Update("a-1", 12))
}

// TestAgentCursor_Advance pins the atomic compare-and-advance the WatchEvents dedup
// relies on: it returns true (forward) exactly when the value STRICTLY advances the
// cursor, and false (drop) for a duplicate (equal or lower) seq.
func TestAgentCursor_Advance(t *testing.T) {
	c := NewAgentCursor()
	assert.True(t, c.Advance("a", 5), "the first seq advances")
	assert.False(t, c.Advance("a", 5), "the same seq is a duplicate")
	assert.False(t, c.Advance("a", 3), "a lower seq is a duplicate")
	assert.True(t, c.Advance("a", 6), "a higher seq advances")
	assert.Equal(t, int64(6), c.Get("a"))
	assert.False(t, c.Advance("", 1), "an empty id never advances")
}

// TestAgentCursor_Advance_AtomicSingleWinner pins the property the prior non-atomic
// Get-then-Update dedup lacked: when many goroutines race Advance with the SAME seq,
// exactly one observes advanced=true. That is what stops a reconnect's overlapping
// streams (a late frame from the torn-down stream + the new stream's replay of the
// same seq) from both forwarding the message and emitting a duplicate.
func TestAgentCursor_Advance_AtomicSingleWinner(t *testing.T) {
	c := NewAgentCursor()
	const goroutines = 32
	var winners int64
	wg := &sync.WaitGroup{}
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if c.Advance("a", 7) {
				atomic.AddInt64(&winners, 1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), atomic.LoadInt64(&winners),
		"exactly one racing Advance for the same seq may forward; the rest are duplicates")
}

// TestAgentCursor_Snapshot_RestrictAndSeed: the entry list must include cursors
// for tracked agents AND seed unknown (newly-arrived) agents with a fresh LATEST
// subscription. This is what makes resubscribe-on-tab-change lossless across
// the cancel + re-open boundary.
func TestAgentCursor_Snapshot_RestrictAndSeed(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 5)
	c.Update("a-2", 11)
	// a-3 is brand new; restrict includes it so we want a seeded
	// entry at seq=0. a-2 is dropped from the restrict set.
	entries := c.Snapshot(map[string]struct{}{
		"a-1": {},
		"a-3": {},
	})
	require.Len(t, entries, 2)
	// Entries are sorted by id, so we know the order.
	assert.Equal(t, "a-1", entries[0].GetAgentId())
	// a-1 has seen seq 5: resume AFTER_CURSOR from 5.
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, entries[0].GetReplay())
	assert.Equal(t, int64(5), entries[0].GetCursorSeq())
	assert.Equal(t, "a-3", entries[1].GetAgentId())
	// a-3 is brand new (seq 0): a fresh LATEST subscription, cursor ignored.
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, entries[1].GetReplay())
	assert.Equal(t, int64(0), entries[1].GetCursorSeq())
}

// TestAgentCursor_Snapshot_NilRestrictReturnsAllTracked: a nil
// `restrict` arg means "every tracked agent" — useful for `agent
// messages --follow` where we only ever subscribe one agent and
// don't filter.
func TestAgentCursor_Snapshot_NilRestrictReturnsAllTracked(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 1)
	c.Update("a-2", 2)
	entries := c.Snapshot(nil)
	require.Len(t, entries, 2)
	got := map[string]int64{}
	for _, e := range entries {
		// Both have seen messages, so both resume AFTER_CURSOR from their seq.
		assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, e.GetReplay())
		got[e.GetAgentId()] = e.GetCursorSeq()
	}
	assert.Equal(t, int64(1), got["a-1"])
	assert.Equal(t, int64(2), got["a-2"])
}

// TestAgentWatchEntry maps a resume cursor to the explicit WatchEvents replay
// mode: seq 0 (nothing seen) -> a fresh LATEST subscription; a positive seq ->
// AFTER_CURSOR resume from it. A negative seq is malformed and also maps to
// LATEST (the <= 0 branch), never an AFTER_CURSOR with a bogus cursor.
func TestAgentWatchEntry(t *testing.T) {
	fresh := AgentWatchEntry("a-1", 0)
	assert.Equal(t, "a-1", fresh.GetAgentId())
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, fresh.GetReplay())
	assert.Equal(t, int64(0), fresh.GetCursorSeq())

	neg := AgentWatchEntry("a-1", -3)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, neg.GetReplay(),
		"a malformed negative seq must not produce an AFTER_CURSOR resume")

	resume := AgentWatchEntry("a-2", 7)
	assert.Equal(t, "a-2", resume.GetAgentId())
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, resume.GetReplay())
	assert.Equal(t, int64(7), resume.GetCursorSeq())
}

// TestIsResumeCursor covers the shared "is this seq a real resume point" threshold
// (seqs are assigned from 1): only a positive seq names a resume point.
func TestIsResumeCursor(t *testing.T) {
	assert.True(t, IsResumeCursor(1), "the first assignable seq is a resume point")
	assert.True(t, IsResumeCursor(42))
	assert.False(t, IsResumeCursor(0), "0 means nothing observed yet")
	assert.False(t, IsResumeCursor(-1), "a negative seq is malformed, not a resume point")
}

// TestAgentCursor_Reset clears just the targeted agent without
// affecting siblings. Used when a tab leaves the snapshot.
func TestAgentCursor_Reset(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 5)
	c.Update("a-2", 9)
	c.Reset("a-1")
	assert.Equal(t, int64(0), c.Get("a-1"))
	assert.Equal(t, int64(9), c.Get("a-2"))
}

// TestAgentCursor_ConcurrentUpdate: the cursor is a building block
// shared between the WatchEvents callback (writer) and reconcile
// (reader). The contract is "race-free monotonic"; this test pins
// it so a future refactor can't drop the mutex without the build
// noticing.
func TestAgentCursor_ConcurrentUpdate(t *testing.T) {
	c := NewAgentCursor()
	const goroutines = 16
	const iterations = 200
	wg := &sync.WaitGroup{}
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(seq int64) {
			defer wg.Done()
			for j := int64(0); j < iterations; j++ {
				c.Update("a", seq*int64(iterations)+j)
			}
		}(int64(i))
	}
	wg.Wait()
	// The exact final seq depends on scheduling, but it must be the
	// max value any goroutine wrote (no torn writes).
	final := c.Get("a")
	assert.Equal(t, int64(goroutines*iterations-1), final,
		"concurrent updates must converge to the highest seq")
}

// TestAgentCursor_Track_RespectsExistingValue keeps tracked cursors
// from being clobbered by a Track call after Update has already
// advanced the value. Track is "set if absent" — if a tab's cursor
// is non-zero because we've been receiving events, a re-seeding
// pass must not reset it to 0.
func TestAgentCursor_Track_RespectsExistingValue(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 42)
	c.Track("a-1", 0) // must be a no-op
	assert.Equal(t, int64(42), c.Get("a-1"))
}

// TestTerminalCursor_BasicShape mirrors AgentCursor's contract for
// terminals. The proto field is `after_offset` and not all the same
// semantics apply (snapshot rollovers reset offset to 0), but the
// cursor itself is monotonic per-id same as agents.
func TestTerminalCursor_BasicShape(t *testing.T) {
	c := NewTerminalCursor()
	c.Track("t-1", 0)
	assert.Equal(t, int64(100), c.Update("t-1", 100))
	assert.Equal(t, int64(100), c.Update("t-1", 50)) // monotonic
	assert.Equal(t, int64(150), c.Update("t-1", 150))

	entries := c.Snapshot(map[string]struct{}{"t-1": {}, "t-2": {}})
	require.Len(t, entries, 2)
	got := map[string]int64{}
	for _, e := range entries {
		got[e.GetTerminalId()] = e.GetAfterOffset()
	}
	assert.Equal(t, int64(150), got["t-1"])
	assert.Equal(t, int64(0), got["t-2"], "untracked terminal seeds at 0")

	c.Reset("t-1")
	assert.Equal(t, int64(0), c.Get("t-1"))
}

// TestCursor_EmptyIDIsNoop documents that empty ids are silently
// dropped. The Update path is hot (called once per delivered event)
// and a pathological caller passing "" shouldn't pollute the map.
func TestCursor_EmptyIDIsNoop(t *testing.T) {
	a := NewAgentCursor()
	a.Update("", 5)
	assert.Empty(t, a.Snapshot(nil))

	tc := NewTerminalCursor()
	tc.Update("", 5)
	assert.Empty(t, tc.Snapshot(nil))
}

// TestAgentCursor_ResetThenTrack verifies that a tracked-then-reset
// agent can be re-seeded at zero (or any other value) cleanly. This
// is the lifecycle for "tab closed → tab re-opened with same id":
// the WatchEvents subscription drops the cursor on Reset, then a
// fresh Track on resubscribe must NOT carry forward the old seq.
func TestAgentCursor_ResetThenTrack(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 50)
	c.Reset("a-1")
	c.Track("a-1", 0)
	assert.Equal(t, int64(0), c.Get("a-1"),
		"after Reset, Track must seed fresh state, not retain the old seq")

	// And subsequent Updates must work normally afterwards.
	c.Update("a-1", 7)
	assert.Equal(t, int64(7), c.Get("a-1"))
}

// TestTerminalCursor_ResetThenTrack mirrors the agent test for
// terminals — the retention-rollover flow (`is_snapshot=true`) is the
// production trigger. After consumers Reset() the cursor in response
// to a snapshot replay, the next Track must not bring back the old
// offset.
func TestTerminalCursor_ResetThenTrack(t *testing.T) {
	c := NewTerminalCursor()
	c.Update("t-1", 200)
	c.Reset("t-1")
	c.Track("t-1", 0)
	assert.Equal(t, int64(0), c.Get("t-1"),
		"after Reset, Track must seed fresh offset")

	c.Update("t-1", 25)
	assert.Equal(t, int64(25), c.Get("t-1"))
}

// TestAgentCursor_Snapshot_NilRestrictReturnsAllSorted asserts the
// no-restrict snapshot output is sorted by agent_id. Sort order is
// part of the cursor's documented contract (cursor.go:122) — tests
// elsewhere rely on `entries[0]` being the alphabetically-first agent
// without re-sorting in every assertion.
func TestAgentCursor_Snapshot_NilRestrictReturnsAllSorted(t *testing.T) {
	c := NewAgentCursor()
	c.Update("z-agent", 1)
	c.Update("a-agent", 2)
	c.Update("m-agent", 3)
	entries := c.Snapshot(nil)
	require.Len(t, entries, 3)
	assert.Equal(t, "a-agent", entries[0].GetAgentId())
	assert.Equal(t, "m-agent", entries[1].GetAgentId())
	assert.Equal(t, "z-agent", entries[2].GetAgentId())
}

// TestTerminalCursor_Snapshot_NilRestrictReturnsAllSorted mirrors
// the agent test for the terminal cursor. Sort order matters for
// deterministic resubscribe payloads — any test that asserts a
// specific WatchEvents request body relies on the snapshot output
// being consistently ordered.
func TestTerminalCursor_Snapshot_NilRestrictReturnsAllSorted(t *testing.T) {
	c := NewTerminalCursor()
	c.Update("z-term", 10)
	c.Update("a-term", 20)
	entries := c.Snapshot(nil)
	require.Len(t, entries, 2)
	assert.Equal(t, "a-term", entries[0].GetTerminalId())
	assert.Equal(t, "z-term", entries[1].GetTerminalId())
}

// TestAgentCursor_Snapshot_EmptyRestrictReturnsZeroEntries pins the
// "restrict to nothing" semantics: the caller passing an empty (but
// non-nil) restrict map must get back an empty slice. Otherwise the
// resubscribe payload would carry every tracked agent regardless of
// the snapshot's tab set, defeating the cancel-and-restrict purpose.
func TestAgentCursor_Snapshot_EmptyRestrictReturnsZeroEntries(t *testing.T) {
	c := NewAgentCursor()
	c.Update("a-1", 5)
	c.Update("a-2", 10)
	entries := c.Snapshot(map[string]struct{}{})
	assert.Empty(t, entries,
		"restrict={} means 'no agents wanted'; result must be empty")
}

// TestAgentCursor_Snapshot_RestrictAllUnknownSeedsFreshLatest covers the
// new-tab-on-watched-worker case where every restrict id is unknown
// to the cursor. All entries must seed as a fresh LATEST subscription so the new
// subscription receives the worker's current state from scratch.
func TestAgentCursor_Snapshot_RestrictAllUnknownSeedsFreshLatest(t *testing.T) {
	c := NewAgentCursor()
	entries := c.Snapshot(map[string]struct{}{"a-x": {}, "a-y": {}})
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, e.GetReplay(),
			"unknown agents in restrict must seed as a fresh LATEST subscription")
		assert.Equal(t, int64(0), e.GetCursorSeq())
	}
}

// TestTerminalCursor_ConcurrentUpdate mirrors the agent concurrent-
// update test — terminal cursors are written from the WatchEvents
// callback and read from reconcile, so the race-free monotonic
// contract applies symmetrically.
func TestTerminalCursor_ConcurrentUpdate(t *testing.T) {
	c := NewTerminalCursor()
	const goroutines = 8
	const iterations = 100
	wg := &sync.WaitGroup{}
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(off int64) {
			defer wg.Done()
			for j := int64(0); j < iterations; j++ {
				c.Update("t", off*int64(iterations)+j)
			}
		}(int64(i))
	}
	wg.Wait()
	final := c.Get("t")
	assert.Equal(t, int64(goroutines*iterations-1), final,
		"concurrent terminal updates must converge to the highest offset")
}

// TestTerminalCursor_Track_RespectsExistingValue mirrors the agent
// "Track is set-if-absent" guarantee. Without it, a re-seeding pass
// (e.g. from snapshot reconcile) could clobber an in-progress
// retention offset to 0 and force the next subscribe to ask for
// already-replayed bytes.
func TestTerminalCursor_Track_RespectsExistingValue(t *testing.T) {
	c := NewTerminalCursor()
	c.Update("t-1", 99)
	c.Track("t-1", 0)
	assert.Equal(t, int64(99), c.Get("t-1"))
}
