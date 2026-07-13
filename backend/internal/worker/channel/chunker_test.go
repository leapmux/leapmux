package channel

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of the reassembler seam: the whole state machine is exercised here
// without a Noise session, a sender, or a Manager. The wire-level behavior
// (errors actually reaching the peer, exactly once) stays pinned by the
// HandleMessage tests in session_test.go.

func TestReassemblerDeliversUnchunkedMessageWhole(t *testing.T) {
	r := newReassembler(100, 4)

	out := r.accept(1, []byte("hello"), false)
	require.Equal(t, reassemblyDeliver, out.action)
	assert.Equal(t, []byte("hello"), out.plaintext)
	assert.False(t, out.chunked, "a single non-MORE chunk is not a reassembly")
	assert.Equal(t, 5, out.size)
	assert.Empty(t, r.buffers, "an unchunked message must not take a buffer slot")
}

func TestReassemblerBuffersThenDeliversChunkedMessage(t *testing.T) {
	r := newReassembler(100, 4)

	out := r.accept(1, []byte("ab"), true)
	require.Equal(t, reassemblyBuffered, out.action)
	assert.Equal(t, 2, out.size)

	out = r.accept(1, []byte("cd"), true)
	require.Equal(t, reassemblyBuffered, out.action)
	assert.Equal(t, 4, out.size)

	out = r.accept(1, []byte("ef"), false)
	require.Equal(t, reassemblyDeliver, out.action)
	assert.Equal(t, []byte("abcdef"), out.plaintext)
	assert.True(t, out.chunked)
	assert.Equal(t, 6, out.size)
	assert.Empty(t, r.buffers, "delivery must free the sequence's slot")
}

func TestReassemblerRefusesNewSequencePastTheLiveCap(t *testing.T) {
	r := newReassembler(100, 2)

	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("a"), true).action)
	require.Equal(t, reassemblyBuffered, r.accept(2, []byte("b"), true).action)

	// A third NEW sequence hits the live cap: it is tombstoned (not refused
	// without a trace) and the caller errors it ONCE. The tombstone counts
	// toward the tombstone cap, not the live cap, so the two live sequences
	// are unaffected.
	out := r.accept(3, []byte("c"), true)
	require.Equal(t, reassemblyTooManyIncomplete, out.action)
	assert.Equal(t, 2, out.incomplete)
	assert.Len(t, r.buffers, 3, "a cap-exceeded id leaves a tombstone, not a trace")
	assert.True(t, r.buffers[3].Poisoned, "the refused id is tombstoned")

	// Every LATER MORE chunk for the refused id is dropped without re-erroring
	// -- the per-chunk sendError storm the tombstone exists to prevent.
	for i := 0; i < 3; i++ {
		require.Equal(t, reassemblyDropPoisoned, r.accept(3, []byte("c"), true).action)
	}

	// An EXISTING live sequence keeps accepting chunks at the live cap.
	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("x"), true).action)

	// The refused id's terminal chunk reaps its tombstone rather than being
	// delivered as a bogus single-chunk message.
	require.Equal(t, reassemblyDropPoisoned, r.accept(3, []byte("c"), false).action)
	_, stillThere := r.buffers[3]
	assert.False(t, stillThere, "the terminal chunk reaps the tombstone")

	// Completing a live sequence frees its slot for a fresh id.
	require.Equal(t, reassemblyDeliver, r.accept(2, []byte("y"), false).action)
	assert.Equal(t, reassemblyBuffered, r.accept(4, []byte("d"), true).action)
}

// Tombstones do NOT count toward the live cap (only live buffers do), but they
// are themselves capped so a peer withholding terminals cannot grow the map
// without bound. A poisoned id's zero-byte tombstone therefore does not block
// a new live sequence; it only counts toward the tombstone budget.
func TestReassemblerTombstoneDoesNotBlockLiveCap(t *testing.T) {
	r := newReassembler(2, 1)

	require.Equal(t, reassemblyTooLarge, r.accept(1, []byte("abc"), true).action)
	require.Len(t, r.buffers, 1, "the poisoned id must leave a tombstone")

	// A new live sequence fits the live cap despite the tombstone (liveCount 0).
	out := r.accept(2, []byte("x"), true)
	assert.Equal(t, reassemblyBuffered, out.action,
		"a tombstone does not consume a live cap slot")

	// Reaping the tombstone (terminal chunk) frees the tombstone budget.
	require.Equal(t, reassemblyDropPoisoned, r.accept(1, []byte("z"), false).action)
}

// When the live cap AND the tombstone cap are both full, a new chunk is dropped
// silently: adding a fresh tombstone per chunk would itself grow the map
// without bound under a peer that withholds terminals.
func TestReassemblerDropsSilentlyWhenBothCapsFull(t *testing.T) {
	r := newReassembler(100, 1)

	// One live sequence fills the live cap.
	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("a"), true).action)
	// A second id is cap-exceeded and tombstoned, filling the tombstone cap.
	require.Equal(t, reassemblyTooManyIncomplete, r.accept(2, []byte("b"), true).action)

	// A third id, with both budgets full, is dropped silently -- no tombstone,
	// no error, no slot.
	out := r.accept(3, []byte("c"), true)
	require.Equal(t, reassemblyDropCapped, out.action)
	assert.Len(t, r.buffers, 2, "a doubly-capped chunk takes neither a live slot nor a tombstone")
	_, present := r.buffers[3]
	assert.False(t, present)
}

func TestReassemblerPoisonsOversizeMidSequenceThenDropsAndReaps(t *testing.T) {
	r := newReassembler(4, 4)

	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("abc"), true).action)

	// Breach mid-sequence: errored once (TooLarge), parts released, tombstone kept.
	out := r.accept(1, []byte("def"), true)
	require.Equal(t, reassemblyTooLarge, out.action)
	assert.Equal(t, 6, out.size)
	buf := r.buffers[1]
	require.NotNil(t, buf, "a mid-sequence breach must leave a tombstone, not delete")
	assert.True(t, buf.Poisoned)
	assert.Zero(t, buf.Total, "poisoning must release the accumulated parts")
	assert.Empty(t, buf.Parts)

	// Every later MORE chunk is dropped without re-accumulating a byte.
	for i := 0; i < 3; i++ {
		require.Equal(t, reassemblyDropPoisoned, r.accept(1, []byte("ghi"), true).action)
		assert.Zero(t, r.buffers[1].Total)
	}

	// The terminal chunk reaps the tombstone -- still no second error.
	require.Equal(t, reassemblyDropPoisoned, r.accept(1, []byte("jkl"), false).action)
	assert.Empty(t, r.buffers, "the terminal chunk must reap the tombstone")

	// The slot is free: the same id starts a fresh, freshly-bounded sequence.
	out = r.accept(1, []byte("ab"), true)
	require.Equal(t, reassemblyBuffered, out.action)
	assert.False(t, r.buffers[1].Poisoned)
}

// A peer that opens a fresh sequence, breaches it mid-flight, and never sends
// its terminal chunk turns each one into a permanent tombstone. Poisoning a LIVE
// buffer frees its live-cap slot (counts() moves it from live to tombstone), so
// the live cap alone cannot bound the map here: without a tombstone cap on the
// mid-sequence breach path, buffers grows without bound for the channel's life --
// and once it passes 2*maxIncomplete, the no-buffer terminal-chunk guard starts
// dropping the owner's own single-chunk RPCs. This pins that the map stays
// bounded no matter how many sequences the peer breaches.
func TestReassemblerBoundsTombstonesFromMidSequenceBreaches(t *testing.T) {
	const maxIncomplete = 2
	r := newReassembler(2, maxIncomplete)

	// Open a fresh id, breach it mid-sequence (MORE flag set, 3 bytes over the
	// 2-byte limit), and never send its terminal chunk -- far past the caps.
	for id := uint64(1); id <= 50; id++ {
		out := r.accept(id, []byte("abc"), true)
		require.Equal(t, reassemblyTooLarge, out.action)
		// The map never exceeds the documented ceiling (live cap + tombstone cap),
		// however many distinct ids the peer breaches.
		assert.LessOrEqualf(t, len(r.buffers), 2*maxIncomplete,
			"tombstones from mid-sequence breaches must stay bounded (after id %d)", id)
	}

	_, tombstones := r.counts()
	assert.LessOrEqual(t, tombstones, maxIncomplete,
		"at most maxIncomplete tombstones survive the breach storm")

	// The most-recently-poisoned id keeps its tombstone, so its own later chunks
	// are still dropped without re-accumulating a byte -- the storm the tombstone
	// exists to prevent, preserved for the id most likely to still be in flight.
	require.Equal(t, reassemblyDropPoisoned, r.accept(50, []byte("x"), true).action)

	// A fresh sequence still works after the storm: the caps recover.
	require.Equal(t, reassemblyBuffered, r.accept(51, []byte("a"), true).action)
}

// A breach that arrives ON the terminal chunk reaps outright: the id is
// finished, so a tombstone would linger until HandleClose with nothing left to
// reap it.
func TestReassemblerReapsOversizeTerminalChunkOutright(t *testing.T) {
	r := newReassembler(4, 4)

	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("abc"), true).action)
	out := r.accept(1, []byte("def"), false)
	require.Equal(t, reassemblyTooLarge, out.action)
	assert.Equal(t, 6, out.size)
	assert.Empty(t, r.buffers, "a terminal-chunk breach must reap, not tombstone")
}

// An oversize single (non-chunked) message is impossible for the reassembler
// to see in production -- decrypt bounds a single ciphertext below the chunk
// limit -- but the boundary must still hold by construction: with no buffered
// sequence, a terminal chunk of any size is delivered as-is and takes no slot.
//
// This path is also why cap-exceeded ids MUST be tombstoned: a refused id's
// terminal chunk would otherwise arrive with no buffer and fall through here,
// dispatched as a bogus single-chunk message. The tombstone routes it to the
// poisoned branch instead, so a genuine single-chunk message reaches this
// deliver outcome -- and so does the terminal chunk of a drop-capped sequence,
// unless both budgets are full (see the both-caps-full test below).
func TestReassemblerTerminalChunkWithoutSequenceBypassesBuffers(t *testing.T) {
	r := newReassembler(100, 1)

	payload := bytes.Repeat([]byte("x"), 8)
	out := r.accept(9, payload, false)
	require.Equal(t, reassemblyDeliver, out.action)
	assert.Equal(t, payload, out.plaintext)
	assert.Empty(t, r.buffers)
}

// A single (non-chunked) message that breaches the size ceiling is refused with
// reassemblyTooLarge -- the same outcome a multi-chunk breach produces -- so the
// worker enforces the protocol's reassembled-message ceiling on the single-chunk
// path too, rather than leaning on the Hub's per-ciphertext cap. Mirrors the
// sibling tunnel receiver (tunnel.Channel.reassemble).
func TestReassemblerSingleChunkBreachesSizeCeiling(t *testing.T) {
	r := newReassembler(4, 2)

	out := r.accept(7, bytes.Repeat([]byte("x"), 5), false)
	require.Equal(t, reassemblyTooLarge, out.action)
	assert.Equal(t, 5, out.size)
	assert.Empty(t, r.buffers, "an oversized single chunk records no buffer")
}

// The terminal chunk of a sequence whose MORE chunks were reassemblyDropCapped
// (both budgets full, so no tombstone recorded the id) must NOT fall through to
// the no-buffer deliver path -- delivering it would dispatch a decrypted
// fragment as a bogus single-chunk message. While both caps are full it is
// dropped like the sequence's earlier chunks. A well-behaved sender never
// interleaves enough sequences to saturate the budgets, so this drops nothing
// legitimate.
func TestReassemblerDropsTerminalChunkOfDropCappedSequence(t *testing.T) {
	r := newReassembler(100, 1)

	// Saturate both budgets: one live sequence + one tombstone.
	require.Equal(t, reassemblyBuffered, r.accept(1, []byte("a"), true).action)
	require.Equal(t, reassemblyTooManyIncomplete, r.accept(2, []byte("b"), true).action)
	require.Len(t, r.buffers, 2, "both caps are full")

	// A new multi-chunk sequence's first (MORE) chunk is drop-capped: no buffer,
	// no tombstone.
	require.Equal(t, reassemblyDropCapped, r.accept(3, []byte("c"), true).action)
	_, present := r.buffers[3]
	require.False(t, present, "a drop-capped chunk records nothing")

	// Its terminal (non-MORE) chunk must be dropped too, not delivered as a
	// standalone message.
	out := r.accept(3, []byte("d"), false)
	require.Equal(t, reassemblyDropCapped, out.action,
		"the terminal chunk of a drop-capped sequence must not be delivered")
	assert.Nil(t, out.plaintext, "no fragment escapes to the dispatcher")
	assert.Len(t, r.buffers, 2, "the terminal chunk takes neither a slot nor a tombstone")

	// A genuine single-chunk message that arrives once a budget frees up is
	// still delivered normally.
	require.Equal(t, reassemblyDropPoisoned, r.accept(2, []byte("z"), false).action)
	require.Len(t, r.buffers, 1, "reaping the tombstone frees a budget slot")
	out = r.accept(9, []byte("hello"), false)
	require.Equal(t, reassemblyDeliver, out.action,
		"a single-chunk message is delivered once the budgets are no longer both full")
	assert.Equal(t, []byte("hello"), out.plaintext)
}

// poisonAndCap evicts the tombstone with the SMALLEST id other than `keep`
// (never an arbitrary map-range hit, and never `keep`). Min-id is deterministic
// -- a test can assert exactly which id is evicted -- and heuristically biases
// toward the oldest allocation, whose peer has had the longest to finish
// streaming. This pins the determinism: with tombstones {100, 50} and a fresh
// breach of 75, the MIN (50) is evicted, not 100.
func TestReassemblerPoisonAndCapEvictsMinimumTombstone(t *testing.T) {
	const maxIncomplete = 2
	r := newReassembler(2, maxIncomplete)

	// Three distinct breaches; ids chosen so the min is neither the most-recent
	// nor the max (which would distinguish min- from max- or LIFO-eviction).
	require.Equal(t, reassemblyTooLarge, r.accept(100, []byte("abc"), true).action)
	require.Equal(t, reassemblyTooLarge, r.accept(50, []byte("abc"), true).action)

	// Pre-state: both 100 and 50 are tombstones.
	_, tombstones := r.counts()
	require.Equal(t, maxIncomplete, tombstones)

	// Breaching 75 forces an eviction (keep=75).
	require.Equal(t, reassemblyTooLarge, r.accept(75, []byte("abc"), true).action)

	// The MIN non-keep tombstone (50) is the one evicted; 100 (the max) and 75
	// (keep) survive. A non-deterministic or max-eviction policy would have
	// removed 100 instead.
	assert.Nil(t, r.buffers[50], "the minimum-id tombstone (50) must be the one evicted")
	assert.NotNil(t, r.buffers[100], "the max-id tombstone (100) must survive under min-id eviction")
	assert.NotNil(t, r.buffers[75], "the just-poisoned keep (75) must never be evicted")
	_, tombstones = r.counts()
	assert.Equal(t, maxIncomplete, tombstones, "tombstone count stays at the cap after eviction")
}
