package terminal

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScreenBuffer_SnapshotSince_CaughtUp: a subscriber whose after_offset
// equals the buffer's total-written counter gets an empty response with
// no snapshot flag — there's nothing for the client to apply.
func TestScreenBuffer_SnapshotSince_CaughtUp(t *testing.T) {
	sb := NewScreenBuffer()
	end := sb.Write([]byte("hello"))

	data, offset, isSnap := sb.SnapshotSince(end)
	assert.Nil(t, data)
	assert.Equal(t, end, offset)
	assert.False(t, isSnap)
}

// TestScreenBuffer_SnapshotSince_ColdSubscribeWithinWindow: after_offset=0
// on a buffer whose ring has NOT wrapped returns the full retained bytes
// as an incremental delta, NOT a snapshot. The snapshot flag exists to
// tell a frontend to reset pre-existing state; a cold subscriber has no
// state to reset, and a frontend that happens to hold the first N bytes
// legitimately is resuming in-window. The distinction only matters once
// the ring has wrapped (see BehindRing test).
func TestScreenBuffer_SnapshotSince_ColdSubscribeWithinWindow(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("hello"))
	sb.Write([]byte(" world"))

	data, offset, isSnap := sb.SnapshotSince(0)
	assert.Equal(t, []byte("hello world"), data)
	assert.Equal(t, int64(11), offset)
	assert.False(t, isSnap,
		"offset 0 while the ring still holds byte 0 is valid in-window resume")
}

// TestScreenBuffer_SnapshotSince_IncrementalDelta: after_offset inside the
// retained window must return only the bytes since afterOffset, not the
// whole buffer, and must NOT set the snapshot flag.
func TestScreenBuffer_SnapshotSince_IncrementalDelta(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("hello"))
	midOffset := int64(5)
	sb.Write([]byte(" world"))
	sb.Write([]byte("!"))

	data, offset, isSnap := sb.SnapshotSince(midOffset)
	assert.Equal(t, []byte(" world!"), data,
		"subscriber at offset 5 should receive exactly the post-offset bytes")
	assert.Equal(t, int64(12), offset)
	assert.False(t, isSnap, "in-window resume must be incremental, not a snapshot")
}

// TestScreenBuffer_SnapshotSince_StaleOffset: after_offset larger than the
// current total (PTY recreated beneath the client) must be treated the
// same as a cold subscribe — return the whole buffer as a snapshot so the
// client drops its stale state.
func TestScreenBuffer_SnapshotSince_StaleOffset(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("fresh"))

	data, offset, isSnap := sb.SnapshotSince(9999)
	assert.Equal(t, []byte("fresh"), data)
	assert.Equal(t, int64(5), offset)
	assert.True(t, isSnap)
}

// TestScreenBuffer_SnapshotSince_BehindRing: after_offset that has fallen
// out of the 100KB retained window returns the full retained buffer with
// the snapshot flag set — the client cannot be resumed incrementally.
func TestScreenBuffer_SnapshotSince_BehindRing(t *testing.T) {
	sb := NewScreenBuffer()
	// Fill well past the ring capacity so the early offsets drop out.
	chunk := make([]byte, 8*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for i := 0; i < 20; i++ { // 160KB total, 60KB overwritten
		sb.Write(chunk)
	}
	total := sb.TotalBytes()
	require.Equal(t, int64(20*8*1024), total)

	// Offset 0 is now well outside the retained window.
	data, offset, isSnap := sb.SnapshotSince(0)
	assert.Len(t, data, screenBufferSize,
		"fell-behind subscriber should receive the full retained ring")
	assert.Equal(t, total, offset)
	assert.True(t, isSnap, "fell-behind resume must be flagged as snapshot")
}

// TestScreenBuffer_SnapshotSince_EmptyBuffer: fresh buffer with no writes
// returns nothing regardless of the requested offset — no snapshot flag
// because there's nothing to replace either.
func TestScreenBuffer_SnapshotSince_EmptyBuffer(t *testing.T) {
	sb := NewScreenBuffer()

	data, offset, isSnap := sb.SnapshotSince(0)
	assert.Empty(t, data)
	assert.Zero(t, offset)
	assert.False(t, isSnap)
}

// TestScreenBuffer_Write_ReturnsEndOffset: Write must return the
// cumulative total-bytes *after* the write so callers can forward it to
// watchers as the resume cursor without a separate TotalBytes call.
func TestScreenBuffer_Write_ReturnsEndOffset(t *testing.T) {
	sb := NewScreenBuffer()
	assert.Equal(t, int64(5), sb.Write([]byte("hello")))
	assert.Equal(t, int64(11), sb.Write([]byte(" world")))
	assert.Equal(t, int64(11), sb.TotalBytes())
}

// TestScreenBuffer_SnapshotSince_BoundaryAtWindowStart: afterOffset
// exactly at the retention-window start must be in-window (incremental).
// Off-by-one here would either duplicate the first retained byte or
// misclassify a valid offset as fallen-behind.
func TestScreenBuffer_SnapshotSince_BoundaryAtWindowStart(t *testing.T) {
	sb := NewScreenBuffer()
	// Fill past capacity so the ring has wrapped exactly once and the
	// retained window is [screenBufferSize, 2*screenBufferSize).
	chunk := make([]byte, screenBufferSize)
	for i := range chunk {
		chunk[i] = 'a'
	}
	sb.Write(chunk)
	sb.Write(chunk)
	total := sb.TotalBytes()
	windowStart := total - screenBufferSize

	// At windowStart exactly: must be in-window.
	data, end, isSnap := sb.SnapshotSince(windowStart)
	assert.Equal(t, total, end)
	assert.False(t, isSnap,
		"offset equal to windowStart is still in the retained ring")
	assert.Len(t, data, screenBufferSize,
		"missing bytes from windowStart to total should equal ring size")

	// One byte before windowStart: fallen out of the window.
	_, _, isSnap = sb.SnapshotSince(windowStart - 1)
	assert.True(t, isSnap,
		"offset one byte below windowStart must be flagged as snapshot")
}

// TestScreenBuffer_SnapshotSince_BoundaryAtTotalMinusOne: asking for the
// last byte must return exactly one byte, not nothing and not the whole
// tail. Keeps the delta math honest when consumers are nearly caught up.
func TestScreenBuffer_SnapshotSince_BoundaryAtTotalMinusOne(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("hello"))

	data, end, isSnap := sb.SnapshotSince(4)
	assert.Equal(t, []byte("o"), data)
	assert.Equal(t, int64(5), end)
	assert.False(t, isSnap)
}

// TestScreenBuffer_SnapshotSince_NegativeOffset: a negative afterOffset
// (malformed client state) must be treated defensively as a stale/cold
// subscribe and receive a full snapshot — never a partial delta computed
// from a negative windowStart comparison.
func TestScreenBuffer_SnapshotSince_NegativeOffset(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("hello"))

	data, end, isSnap := sb.SnapshotSince(-1)
	assert.Equal(t, []byte("hello"), data)
	assert.Equal(t, int64(5), end)
	assert.True(t, isSnap,
		"negative offsets must be treated as stale — full snapshot")
}

// TestScreenBuffer_SnapshotSince_WrapsAtExactBoundary: when a write
// exactly fills the ring (pos==len(buf), full transitions false→true),
// SnapshotSince(0) must still return all bytes as an in-window resume —
// not a snapshot, because byte 0 is still the retention window's start.
func TestScreenBuffer_SnapshotSince_WrapsAtExactBoundary(t *testing.T) {
	sb := NewScreenBuffer()
	chunk := make([]byte, screenBufferSize)
	for i := range chunk {
		chunk[i] = 'z'
	}
	sb.Write(chunk)

	require.Equal(t, int64(screenBufferSize), sb.TotalBytes())
	data, end, isSnap := sb.SnapshotSince(0)
	assert.Len(t, data, screenBufferSize)
	assert.Equal(t, int64(screenBufferSize), end)
	assert.False(t, isSnap,
		"ring exactly full: byte 0 is at windowStart, still in-window")
}

// TestScreenBuffer_SnapshotSince_MultiWrap: after the ring has wrapped
// several times, windowStart tracks total - retained correctly. Stale
// offsets from early wraps return snapshots; offsets inside the current
// window return incremental deltas.
func TestScreenBuffer_SnapshotSince_MultiWrap(t *testing.T) {
	sb := NewScreenBuffer()
	chunk := make([]byte, screenBufferSize/2)
	for i := range chunk {
		chunk[i] = 'w'
	}
	// 5 writes of half-ring = 2.5x the ring capacity: wraps twice.
	for i := 0; i < 5; i++ {
		sb.Write(chunk)
	}
	total := sb.TotalBytes()
	require.Equal(t, int64(5*len(chunk)), total)
	windowStart := total - screenBufferSize

	// Offset inside the window — incremental.
	inWindow := windowStart + int64(len(chunk))
	_, _, isSnap := sb.SnapshotSince(inWindow)
	assert.False(t, isSnap)

	// Offset from the first wrap — should be out of the current window.
	_, _, isSnap = sb.SnapshotSince(int64(len(chunk)))
	assert.True(t, isSnap,
		"offsets from early wraps must now be outside the retained ring")
}

// TestScreenBuffer_Write_LargerThanRing: a single Write larger than the
// ring must still advance the total counter by len(data). The returned
// bytes are the *last* len(buf) bytes of the write.
func TestScreenBuffer_Write_LargerThanRing(t *testing.T) {
	sb := NewScreenBuffer()
	big := make([]byte, 2*screenBufferSize)
	for i := range big {
		big[i] = byte(i & 0xff)
	}
	end := sb.Write(big)
	assert.Equal(t, int64(2*screenBufferSize), end)
	assert.Equal(t, int64(2*screenBufferSize), sb.TotalBytes())

	// Full buffer equals the last ring-size bytes of the input.
	data, _, _ := sb.SnapshotSince(0)
	require.Len(t, data, screenBufferSize)
	assert.Equal(t, big[screenBufferSize:], data)
}

// TestScreenBuffer_HasSuffix covers the three regimes the
// disconnect-notice path relies on: empty-buffer no-match, no-wrap
// match, and post-wrap match where the suffix straddles the ring
// boundary. Empty-needle is true by convention (mirrors bytes.HasSuffix).
func TestScreenBuffer_HasSuffix(t *testing.T) {
	// Empty needle on an empty buffer.
	sb := NewScreenBuffer()
	assert.True(t, sb.HasSuffix(nil))
	assert.False(t, sb.HasSuffix([]byte("anything")))

	// No-wrap: write less than capacity, check suffix.
	sb.Write([]byte("prefix-NOTICE"))
	assert.True(t, sb.HasSuffix([]byte("NOTICE")))
	assert.False(t, sb.HasSuffix([]byte("prefix")))
	assert.False(t, sb.HasSuffix([]byte("some-longer-string-than-was-written")))

	// Post-wrap: fill past the ring, then write a marker so it straddles.
	sb = NewScreenBuffer()
	filler := make([]byte, screenBufferSize-3)
	for i := range filler {
		filler[i] = 'x'
	}
	sb.Write(filler)
	sb.Write([]byte("AB"))     // ring now has 'x'*n-3 + "AB", pos just before wrap
	sb.Write([]byte("C\nEND")) // writes wrap across the boundary
	assert.True(t, sb.HasSuffix([]byte("END")))
	assert.True(t, sb.HasSuffix([]byte("ABC\nEND")),
		"suffix that straddles the ring wrap must still match")
	assert.False(t, sb.HasSuffix([]byte("WRONG")))
}

// TestScreenBuffer_SnapshotSince_FallenBehindIncludesModePrefix: when
// the subscriber has fallen out of the retained window, the snapshot
// reply must start with the mode-restore prefix synthesized from the
// tracker's state. This is the whole reason the tracker exists: a TUI
// that entered alt screen via bytes that have since been overwritten
// in the ring still gets a working post-reset render.
func TestScreenBuffer_SnapshotSince_FallenBehindIncludesModePrefix(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("\x1b[?1049h")) // enter alt screen.
	sb.Write(ringOverflowFiller())

	data, _, isSnap := sb.SnapshotSince(0)
	require.True(t, isSnap, "fallen-behind subscriber must get a snapshot")
	require.True(t, bytes.HasPrefix(data, []byte("\x1b[?1049h")),
		"snapshot must lead with the mode-restore prefix; got first 16 bytes: %q",
		string(data[:min(16, len(data))]))
}

// TestScreenBuffer_SnapshotSince_InWindowHasNoPrefix: a subscriber
// resuming inside the retained window already received the mode bytes
// during the live stream, so the incremental delta must NOT add another
// prefix. Doing so would cause xterm to re-toggle modes and confuse the
// running program.
func TestScreenBuffer_SnapshotSince_InWindowHasNoPrefix(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("\x1b[?1049h"))
	mid := sb.TotalBytes()
	sb.Write([]byte("more output"))

	data, _, isSnap := sb.SnapshotSince(mid)
	assert.False(t, isSnap, "in-window resume must not be flagged as snapshot")
	assert.Equal(t, []byte("more output"), data,
		"in-window delta must contain only the post-offset bytes — no prefix")
}

// TestScreenBuffer_SnapshotSince_PrefixDoesNotInflateOffset: the
// returned endOffset must equal sb.total even when a prefix was added.
// The prefix is synthesized; counting it would advance the resume
// cursor past bytes the client never saw and break delta math on the
// next subscribe.
func TestScreenBuffer_SnapshotSince_PrefixDoesNotInflateOffset(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("\x1b[?1049h"))
	sb.Write(ringOverflowFiller())
	expectedTotal := sb.TotalBytes()

	_, end, isSnap := sb.SnapshotSince(0)
	require.True(t, isSnap)
	assert.Equal(t, expectedTotal, end,
		"snapshot endOffset must equal total bytes written, not total+len(prefix)")
}

// TestScreenBuffer_SnapshotSince_DefaultStateNoPrefix: a tracker at
// default state (no escape sequences observed, or every set has been
// reset) must produce a snapshot with no prefix at all. Otherwise we
// emit unnecessary bytes on every resubscribe.
func TestScreenBuffer_SnapshotSince_DefaultStateNoPrefix(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write(ringOverflowFiller())

	data, _, isSnap := sb.SnapshotSince(0)
	require.True(t, isSnap)
	assert.False(t, bytes.HasPrefix(data, []byte("\x1b[")),
		"plain text ring must not produce a CSI prefix")
}

// TestScreenBuffer_Snapshot_PrefixesPersistedScreenPath: Snapshot() is
// the path used by Manager.SnapshotTerminal (DB persistence on shutdown
// / exit) and Manager.buildEntryLocked (ListTerminals). Worker restarts
// reload these bytes and the frontend writes them after terminal.reset()
// — so they MUST carry the mode prefix or alt-screen state is lost
// across restarts.
func TestScreenBuffer_Snapshot_PrefixesPersistedScreenPath(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("\x1b[?1049h"))
	sb.Write(ringOverflowFiller())

	data, end := sb.Snapshot()
	assert.True(t, bytes.HasPrefix(data, []byte("\x1b[?1049h")),
		"persisted-screen path must include the mode-restore prefix")
	assert.Equal(t, sb.TotalBytes(), end,
		"Snapshot() endOffset must not include synthesized prefix bytes")
}

// TestScreenBuffer_Snapshot_DefaultStateNoPrefix: the persisted-screen
// path must also short-circuit when the tracker is at default state,
// matching SnapshotSince's behavior.
func TestScreenBuffer_Snapshot_DefaultStateNoPrefix(t *testing.T) {
	sb := NewScreenBuffer()
	sb.Write([]byte("plain shell output\r\n$ "))

	data, _ := sb.Snapshot()
	assert.Equal(t, []byte("plain shell output\r\n$ "), data,
		"default-state Snapshot must return body bytes only")
}

// ringOverflowFiller returns plain-ASCII bytes sized just past the
// retained ring, so writing them is guaranteed to overwrite anything
// emitted earlier (including a leading mode toggle). 110% of the ring
// is enough margin to stay correct even if screenBufferSize grows
// modestly without making test runs gratuitously large.
func ringOverflowFiller() []byte {
	out := make([]byte, screenBufferSize+screenBufferSize/10)
	for i := range out {
		out[i] = 'x'
	}
	return out
}

// TestScreenBuffer_ConcurrentWriteAndSnapshot: Write and SnapshotSince
// must not data-race under -race. No assertion on content — the goal is
// just to exercise the locks. The race detector surfaces violations as
// test failures.
func TestScreenBuffer_ConcurrentWriteAndSnapshot(t *testing.T) {
	sb := NewScreenBuffer()
	chunk := []byte("chunk of output")

	var wg sync.WaitGroup
	// One writer producing a steady stream.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sb.Write(chunk)
		}
	}()
	// Several concurrent readers at varying offsets.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, _, _ = sb.SnapshotSince(int64(i))
				_, _ = sb.Snapshot()
				_ = sb.TotalBytes()
			}
		}()
	}
	wg.Wait()

	// Final offset must equal the total bytes the writer emitted.
	assert.Equal(t, int64(500*len(chunk)), sb.TotalBytes())
}
