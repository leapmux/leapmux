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
	t.Parallel()

	sb := NewScreenBuffer()
	end, _ := sb.Write([]byte("hello"))

	data, offset, isSnap := sb.SnapshotSince(end)
	assert.Nil(t, data)
	assert.Equal(t, end, offset)
	assert.False(t, isSnap)
}

// TestScreenBuffer_SnapshotSince_ForcedResyncAtZero: after_offset=0 is the
// forced-resync subscribe. A client that lost bytes (its pending-frame queue
// evicted frames) asks for a full rebuild, and the answer must be the whole
// retained buffer with the snapshot flag EVEN while the ring still holds
// byte 0 — an incremental-from-zero delta would come back instead, and the
// client's stale cursor would trim the rebuilt bytes as "already rendered",
// which is exactly the garbled state the resync exists to repair.
func TestScreenBuffer_SnapshotSince_ForcedResyncAtZero(t *testing.T) {
	t.Parallel()

	sb := NewScreenBuffer()
	sb.Write([]byte("hello"))
	sb.Write([]byte(" world"))

	data, offset, isSnap := sb.SnapshotSince(0)
	assert.True(t, isSnap,
		"offset 0 is a forced resync and must rebuild the client's state")
	assert.Equal(t, int64(11), offset)
	assert.Contains(t, string(data), "hello world",
		"the resync snapshot carries the full retained bytes")
}

// TestScreenBuffer_SnapshotSince_IncrementalDelta: after_offset inside the
// retained window must return only the bytes since afterOffset, not the
// whole buffer, and must NOT set the snapshot flag.
func TestScreenBuffer_SnapshotSince_IncrementalDelta(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	sb := NewScreenBuffer()
	end1, _ := sb.Write([]byte("hello"))
	assert.Equal(t, int64(5), end1)
	end2, _ := sb.Write([]byte(" world"))
	assert.Equal(t, int64(11), end2)
	assert.Equal(t, int64(11), sb.TotalBytes())
}

// TestScreenBuffer_SnapshotSince_BoundaryAtWindowStart: afterOffset
// exactly at the retention-window start must be in-window (incremental).
// Off-by-one here would either duplicate the first retained byte or
// misclassify a valid offset as fallen-behind.
func TestScreenBuffer_SnapshotSince_BoundaryAtWindowStart(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
// SnapshotSince(0) still answers the forced-resync subscribe with the
// full buffer and the snapshot flag — byte 0 being the retention window's
// start does not change what a zero-offset subscriber asked for.
func TestScreenBuffer_SnapshotSince_WrapsAtExactBoundary(t *testing.T) {
	t.Parallel()

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
	assert.True(t, isSnap,
		"offset 0 is a forced resync regardless of where the window starts")
}

// TestScreenBuffer_SnapshotSince_MultiWrap: after the ring has wrapped
// several times, windowStart tracks total - retained correctly. Stale
// offsets from early wraps return snapshots; offsets inside the current
// window return incremental deltas.
func TestScreenBuffer_SnapshotSince_MultiWrap(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	sb := NewScreenBuffer()
	big := make([]byte, 2*screenBufferSize)
	for i := range big {
		big[i] = byte(i & 0xff)
	}
	end, _ := sb.Write(big)
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
// next subscribe. len(data) is then endOffset plus the prefix length,
// never endOffset itself.
//
// Both snapshot regimes are covered. The retained-history case is the
// one a Windows shell hits on every cold subscribe: cmd.exe under
// ConPTY opens with a title OSC and a cursor-hide, so the tracker is
// already non-default while the whole history still fits in the ring.
// A POSIX /bin/sh emits neither, which is why an assertion that equates
// len(data) with endOffset passes on Linux and macOS and fails only on
// Windows.
func TestScreenBuffer_SnapshotSince_PrefixDoesNotInflateOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		modes  []byte
		body   []byte
		prefix []byte
	}{
		{
			// Prefix order follows snapshotPrefix: modes first, title last.
			name:   "whole history still retained",
			modes:  []byte("\x1b]0;C:\\Windows\\system32\\cmd.exe\x07\x1b[?25l"),
			body:   []byte("Microsoft Windows\r\n\r\nC:\\>echo hi\r\nhi\r\n\r\nC:\\>"),
			prefix: []byte("\x1b[?25l\x1b]0;C:\\Windows\\system32\\cmd.exe\x07"),
		},
		{
			name:   "mode bytes overwritten in the ring",
			modes:  []byte("\x1b[?1049h"),
			body:   ringOverflowFiller(),
			prefix: []byte("\x1b[?1049h"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb := NewScreenBuffer()
			sb.Write(tc.modes)
			sb.Write(tc.body)
			expectedTotal := sb.TotalBytes()

			data, end, isSnap := sb.SnapshotSince(0)
			require.True(t, isSnap, "afterOffset 0 is a forced resync")
			assert.Equal(t, expectedTotal, end,
				"snapshot endOffset must equal total bytes written, not total+len(prefix)")

			// The retained body is the tail of everything written, and the
			// prefix sits in front of it. endOffset counts the body alone.
			retained := expectedTotal
			if retained > screenBufferSize {
				retained = screenBufferSize
			}
			require.Len(t, data, len(tc.prefix)+int(retained),
				"snapshot is the synthesized prefix plus every retained byte")
			assert.Equal(t, tc.prefix, data[:len(tc.prefix)],
				"snapshot must lead with the mode-restore prefix")

			stream := append(append([]byte{}, tc.modes...), tc.body...)
			wantBody := stream[int64(len(stream))-retained:]
			// bytes.Equal, not assert.Equal: the wrapped case compares 100KB
			// and a testify diff of that size buries the failure.
			assert.True(t, bytes.Equal(wantBody, data[len(tc.prefix):]),
				"the %d bytes after the prefix must be the retained PTY bytes, verbatim", retained)
		})
	}
}

// TestScreenBuffer_SnapshotSince_DefaultStateNoPrefix: a tracker at
// default state (no escape sequences observed, or every set has been
// reset) must produce a snapshot with no prefix at all. Otherwise we
// emit unnecessary bytes on every resubscribe.
func TestScreenBuffer_SnapshotSince_DefaultStateNoPrefix(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

// TestScreenBuffer_WriteAndDeliver_KeepsOneDeliveryOrder: concurrent
// writers must hand their chunks to the OutputHandler in the order the
// ring took them, because a subscriber treats the offsets as
// authoritative and discards a chunk whose range sits below its cursor.
// Plain Write cannot promise this: it assigns the offset under the lock,
// releases it, and the delivery that follows races every other writer.
func TestScreenBuffer_WriteAndDeliver_KeepsOneDeliveryOrder(t *testing.T) {
	t.Parallel()

	const writers, perWriter = 8, 50

	sb := NewScreenBuffer()
	var (
		mu        sync.Mutex
		offsets   []int64
		delivered []byte
	)
	deliver := func(data []byte, endOffset int64, _ []Signal) {
		mu.Lock()
		defer mu.Unlock()
		offsets = append(offsets, endOffset)
		delivered = append(delivered, data...)
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Distinct per-writer bytes, so a reordering shows up in the
			// concatenation and not only in the offsets. Plain ASCII keeps
			// the tracker at its default, so the snapshot has no prefix.
			chunk := bytes.Repeat([]byte{byte('a' + w)}, 64)
			for i := 0; i < perWriter; i++ {
				sb.WriteAndDeliver(chunk, deliver)
			}
		}(w)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, offsets, writers*perWriter)
	for i := 1; i < len(offsets); i++ {
		require.Greater(t, offsets[i], offsets[i-1],
			"delivery %d carried an offset below its predecessor: a subscriber would drop it", i)
	}

	// The bytes the handler saw, in the order it saw them, are the bytes
	// the ring holds. This is the invariant the offsets stand for.
	body, total := sb.Snapshot()
	require.Equal(t, int64(writers*perWriter*64), total, "test must stay inside the ring")
	assert.True(t, bytes.Equal(body, delivered),
		"delivered %d bytes must match the ring's %d bytes, in order", len(delivered), len(body))
}

// TestScreenBuffer_ConcurrentWriteAndSnapshot: Write and SnapshotSince
// must not data-race under -race. No assertion on content — the goal is
// just to exercise the locks. The race detector surfaces violations as
// test failures.
func TestScreenBuffer_ConcurrentWriteAndSnapshot(t *testing.T) {
	t.Parallel()

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
