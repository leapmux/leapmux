package channelmgr

import (
	"testing"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMaxMessageSize = 1024 * 1024 // 1 MiB
	dirFe2w            = "fe2w"
)

// TestSetChannelMaxMessageSize_DerivesReassembledCeiling pins that the
// manager stores MaxReassembledMessageSize(payload), not the bare payload
// budget, as the ChunkTracker per-channel ceiling when the negotiated
// ceiling is stricter than the hub default.
func TestSetChannelMaxMessageSize_DerivesReassembledCeiling(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	const payload = 1 << 20 // 1 MiB — below the 16 MiB hub default
	m.SetChannelMaxMessageSize("ch1", payload)

	want := channelwire.MaxReassembledMessageSize(payload)
	m.ChunkTracker.mu.Lock()
	got := m.ChunkTracker.channelMax["ch1"]
	m.ChunkTracker.mu.Unlock()
	assert.Equal(t, want, got)
}

func TestSetChannelMaxMessageSize_SkipsEntryWhenEqualToDefault(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	// Negotiated payload equals the hub configured budget → reassembled
	// ceiling matches the tracker default; no per-channel entry needed.
	m.SetChannelMaxMessageSize("ch1", m.MaxMessageSize())

	m.ChunkTracker.mu.Lock()
	_, ok := m.ChunkTracker.channelMax["ch1"]
	m.ChunkTracker.mu.Unlock()
	assert.False(t, ok, "equal-to-default ceilings must not populate channelMax")
}

func TestSetChannelMax_ClearsPriorOverrideWhenRaisedToDefault(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)
	ct.SetChannelMax("ch1", 1000)
	ct.SetChannelMax("ch1", testMaxMessageSize)

	ct.mu.Lock()
	_, ok := ct.channelMax["ch1"]
	ct.mu.Unlock()
	assert.False(t, ok, "raising back to the default must drop the override entry")
}

func TestSetChannelMaxMessageSize_NoopsWhenChannelGone(t *testing.T) {
	m := New(0)
	m.SetChannelMaxMessageSize("missing", 1<<20)
	m.ChunkTracker.mu.Lock()
	_, ok := m.ChunkTracker.channelMax["missing"]
	m.ChunkTracker.mu.Unlock()
	assert.False(t, ok, "must not orphan a channelMax entry for an unregistered channel")
}

// TestNew_ResolvesMaxMessageSize pins that New(0) uses the protocol default
// payload budget and the matching DefaultMaxReassembledMessageSize as the
// ChunkTracker fallback ceiling. Per-channel negotiation may lower the
// tracker via SetChannelMaxMessageSize after a successful open.
func TestNew_ResolvesMaxMessageSize(t *testing.T) {
	m := New(0)
	require.NotNil(t, m.ChunkTracker)
	assert.Equal(t, channelwire.MaxMessageSize, m.MaxMessageSize())
	assert.Equal(t, channelwire.DefaultMaxReassembledMessageSize, m.ChunkTracker.defaultReassembledMax,
		"ChunkTracker default must be MaxReassembledMessageSize of the hub payload budget")
}

// TestChunkTracker_PerChannelMax verifies Track uses a channel-specific
// reassembled ceiling when SetChannelMax has recorded one, falls back to the
// tracker default otherwise, and that RemoveChannel clears the override.
func TestChunkTracker_PerChannelMax(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)
	const channelCeiling = 1000
	ct.SetChannelMax("ch1", channelCeiling)

	// Fits the default (1 MiB) but exceeds the per-channel ceiling.
	err := ct.Track("ch1", dirFe2w, 1, validChunkSize(channelCeiling+1), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")

	// A different channel still uses the tracker default.
	err = ct.Track("ch2", dirFe2w, 1, validChunkSize(channelCeiling+1), true)
	require.NoError(t, err)

	ct.RemoveChannel("ch1")
	ct.mu.Lock()
	_, hasMax := ct.channelMax["ch1"]
	ct.mu.Unlock()
	assert.False(t, hasMax, "RemoveChannel must clear the per-channel max")

	// After removal, ch1 uses the default ceiling again.
	err = ct.Track("ch1", dirFe2w, 2, validChunkSize(channelCeiling+1), true)
	require.NoError(t, err)
}

// validChunkSize returns a ciphertext length that fits within one Noise
// transport message and whose estimated plaintext fits within the given budget.
func validChunkSize(plaintextBytes int) int {
	return plaintextBytes + channelwire.NoiseAEADAuthTagSize
}

// TestTrack_SingleChunkMessage verifies that a non-chunked (flags=UNSPECIFIED,
// more=false) message is accepted and leaves no tracking state.
func TestTrack_SingleChunkMessage(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	err := ct.Track("ch1", dirFe2w, 1, validChunkSize(100), false)
	require.NoError(t, err)

	// No state should remain.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Empty(t, ct.channels)
}

// TestTrack_MultiChunk verifies that a sequence of MORE chunks followed by a
// final chunk (more=false) is accepted and cleans up all tracking state.
func TestTrack_MultiChunk(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)
	corrID := uint64(42)

	// Three MORE chunks.
	for i := 0; i < 3; i++ {
		err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(100), true)
		require.NoError(t, err, "chunk %d should be accepted", i)
	}

	// Intermediate state: sequence must be tracked.
	ct.mu.Lock()
	assert.Contains(t, ct.channels, "ch1")
	ct.mu.Unlock()

	// Final chunk (more=false).
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(100), false)
	require.NoError(t, err)

	// State must be cleaned up.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Empty(t, ct.channels)
}

// TestTrack_MaxSizeExceeded_OnMoreChunk verifies that cumulative plaintext
// exceeding maxMessageSize is rejected on a MORE chunk.
func TestTrack_MaxSizeExceeded_OnMoreChunk(t *testing.T) {
	maxSize := 500
	ct := newChunkTracker(maxSize)
	corrID := uint64(1)

	// First chunk: 400 bytes plaintext — within limit.
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(400), true)
	require.NoError(t, err)

	// Second chunk: 200 bytes more — total 600 > 500 → must fail.
	err = ct.Track("ch1", dirFe2w, corrID, validChunkSize(200), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")

	// After rejection the sequence AND the now-empty channel/direction state must
	// be removed, matching the success path -- a breach that returns before
	// Track's own cleanupEmpty must not strand an empty entry until RemoveChannel.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.NotContains(t, ct.channels, "ch1",
		"a mid-sequence breach must clean up the empty channel state")
}

// TestTrack_MaxSizeExceeded_OnFinalChunk verifies that cumulative plaintext
// exceeding maxMessageSize is rejected on the final (more=false) chunk.
func TestTrack_MaxSizeExceeded_OnFinalChunk(t *testing.T) {
	maxSize := 500
	ct := newChunkTracker(maxSize)
	corrID := uint64(1)

	// First chunk: 400 bytes plaintext — within limit.
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(400), true)
	require.NoError(t, err)

	// Final chunk: 200 bytes more — total 600 > 500 → must fail.
	err = ct.Track("ch1", dirFe2w, corrID, validChunkSize(200), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")

	// The breach on the final chunk must also clean up the now-empty channel
	// state, the same as a successful final chunk does.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.NotContains(t, ct.channels, "ch1",
		"a final-chunk breach must clean up the empty channel state")
}

// TestTrack_InterleavingRejected verifies that a chunk for correlationID B is
// rejected while correlationID A is still the in-progress sequence.
//
// This -- not a count cap -- is what bounds the Hub's in-flight chunk state:
// admitting a new correlation id requires that none is in progress, so a
// channel+direction holds at most one sequence at a time.
func TestTrack_InterleavingRejected(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// Start sequence A (correlationID 1).
	err := ct.Track("ch1", dirFe2w, 1, validChunkSize(100), true)
	require.NoError(t, err)

	// Start sequence B (correlationID 2) while A is still in-progress.
	err = ct.Track("ch1", dirFe2w, 2, validChunkSize(100), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interleaving")
}

// TestTrack_InterleavingRejected_TableDriven uses multiple channel/direction
// combinations to confirm that interleaving isolation is per (channelID, direction).
func TestTrack_InterleavingRejected_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		direction string
	}{
		{"same-channel-fe2w", "ch1", "fe2w"},
		{"same-channel-w2fe", "ch1", "w2fe"},
		{"other-channel-fe2w", "ch2", "fe2w"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := newChunkTracker(testMaxMessageSize)

			require.NoError(t, ct.Track(tc.channelID, tc.direction, 1, validChunkSize(50), true))

			err := ct.Track(tc.channelID, tc.direction, 2, validChunkSize(50), true)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "interleaving")
		})
	}
}

// TestTrack_RemoveChannel verifies that RemoveChannel clears all tracking state
// for the given channelID, including in-flight sequences.
func TestTrack_RemoveChannel(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// Start a sequence on ch1 in two directions.
	require.NoError(t, ct.Track("ch1", "fe2w", 1, validChunkSize(100), true))
	require.NoError(t, ct.Track("ch1", "w2fe", 2, validChunkSize(100), true))
	// Also add state on ch2 to verify it is unaffected.
	require.NoError(t, ct.Track("ch2", "fe2w", 3, validChunkSize(100), true))

	ct.RemoveChannel("ch1")

	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.NotContains(t, ct.channels, "ch1", "ch1 state must be removed")
	assert.Contains(t, ct.channels, "ch2", "ch2 state must survive RemoveChannel(ch1)")
}

// TestTrack_RemoveChannel_NonExistent verifies that RemoveChannel on an unknown
// channelID does not panic.
func TestTrack_RemoveChannel_NonExistent(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)
	assert.NotPanics(t, func() {
		ct.RemoveChannel("does-not-exist")
	})
}

// TestTrack_ChunkCiphertextTooLarge verifies that a single chunk whose
// ciphertext exceeds maxChunkCiphertext is rejected immediately.
func TestTrack_ChunkCiphertextTooLarge(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	tests := []struct {
		name          string
		ciphertextLen int
		more          bool
	}{
		{"more=true at limit+1", maxChunkCiphertext + 1, true},
		{"more=false at limit+1", maxChunkCiphertext + 1, false},
		{"more=true way over limit", maxChunkCiphertext * 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ct.Track("ch1", dirFe2w, 1, tc.ciphertextLen, tc.more)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "chunk ciphertext too large")
		})
	}
}

// TestTrack_ChunkCiphertextAtLimit verifies that a ciphertext exactly at
// maxChunkCiphertext is accepted.
func TestTrack_ChunkCiphertextAtLimit(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	err := ct.Track("ch1", dirFe2w, 1, maxChunkCiphertext, true)
	require.NoError(t, err)
}

// TestTrack_SameCorrelationID_ContinuesAfterFirst verifies that subsequent MORE
// chunks with the same correlationID accumulate correctly without errors.
func TestTrack_SameCorrelationID_ContinuesAfterFirst(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)
	corrID := uint64(7)

	chunkPlaintext := 100
	chunkCount := 5
	for i := 0; i < chunkCount; i++ {
		err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(chunkPlaintext), true)
		require.NoError(t, err, "chunk %d should succeed", i)
	}

	// Verify accumulated size.
	ct.mu.Lock()
	seq := ct.channels["ch1"][dirFe2w].sequences[corrID]
	require.NotNil(t, seq)
	assert.Equal(t, chunkPlaintext*chunkCount, seq.estimatedPlaintext)
	ct.mu.Unlock()

	// Complete the sequence.
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(chunkPlaintext), false)
	require.NoError(t, err)
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Empty(t, ct.channels)
}

// TestTrack_DifferentChannels verifies that tracking state for different
// channelIDs is fully isolated.
func TestTrack_DifferentChannels(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// ch1 starts a sequence with correlationID 1.
	require.NoError(t, ct.Track("ch1", dirFe2w, 1, validChunkSize(100), true))
	// ch2 starts a sequence with correlationID 1 (same ID, different channel).
	require.NoError(t, ct.Track("ch2", dirFe2w, 1, validChunkSize(100), true))

	// Complete ch1 — ch2 must remain tracked.
	require.NoError(t, ct.Track("ch1", dirFe2w, 1, validChunkSize(100), false))

	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.NotContains(t, ct.channels, "ch1")
	assert.Contains(t, ct.channels, "ch2")
}

// TestTrack_DifferentDirections verifies that "fe2w" and "w2fe" are tracked
// independently within the same channelID.
func TestTrack_DifferentDirections(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// Start sequence on fe2w with correlationID 1.
	require.NoError(t, ct.Track("ch1", "fe2w", 1, validChunkSize(100), true))
	// Start sequence on w2fe with correlationID 1.
	require.NoError(t, ct.Track("ch1", "w2fe", 1, validChunkSize(100), true))

	// Interleaving check on fe2w with correlationID 2 should fire.
	err := ct.Track("ch1", "fe2w", 2, validChunkSize(100), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interleaving")

	// w2fe is unaffected — another chunk for correlationID 1 is fine.
	require.NoError(t, ct.Track("ch1", "w2fe", 1, validChunkSize(100), true))
}

// TestTrack_CleanupEmpty_SingleChunkOnTrackedChannel verifies that receiving a
// non-chunked message for a channelID that has no tracked state does not panic
// or produce an error.
func TestTrack_CleanupEmpty_SingleChunkOnTrackedChannel(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// Start a sequence so the channel appears in the map.
	require.NoError(t, ct.Track("ch1", dirFe2w, 1, validChunkSize(100), true))

	// Send a non-chunked message for a different correlationID (not tracked).
	err := ct.Track("ch1", dirFe2w, 999, validChunkSize(50), false)
	require.NoError(t, err)

	// ch1 state must still exist because sequence 1 is still in-flight.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Contains(t, ct.channels, "ch1")
}

// TestTrack_InProgressResetAfterMaxSizeRejection verifies that after a max-size
// rejection cleans up the sequence, a new sequence with a different correlationID
// succeeds (i.e. inProgressID is properly reset).
func TestTrack_InProgressResetAfterMaxSizeRejection(t *testing.T) {
	maxSize := 100
	ct := newChunkTracker(maxSize)

	// Start a sequence with correlationID 1.
	err := ct.Track("ch1", dirFe2w, 1, validChunkSize(60), true)
	require.NoError(t, err)

	// Send another chunk that exceeds the limit → rejected, sequence cleaned up.
	err = ct.Track("ch1", dirFe2w, 1, validChunkSize(60), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")

	// Now a new sequence with correlationID 2 should be accepted
	// (inProgressID must have been reset when the sequence was cleaned up).
	err = ct.Track("ch1", dirFe2w, 2, validChunkSize(50), true)
	require.NoError(t, err)

	// Complete the new sequence.
	err = ct.Track("ch1", dirFe2w, 2, validChunkSize(10), false)
	require.NoError(t, err)

	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Empty(t, ct.channels)
}

// TestTrack_CorrelationIDZeroRejected verifies that correlation id 0 -- the
// reserved "no sequence in progress" sentinel for inProgressID -- is refused
// outright, in both directions and regardless of the MORE flag. Both id
// allocators skip 0, so no well-behaved peer sends it; the Hub relays a
// peer-supplied id and must reject the sentinel rather than trust the convention.
func TestTrack_CorrelationIDZeroRejected(t *testing.T) {
	for _, direction := range []string{"fe2w", "w2fe"} {
		for _, more := range []bool{true, false} {
			ct := newChunkTracker(testMaxMessageSize)
			err := ct.Track("ch1", direction, 0, validChunkSize(100), more)
			require.Error(t, err, "id 0 must be rejected (dir=%s more=%v)", direction, more)
			assert.Contains(t, err.Error(), "correlation id 0")

			// Rejecting the sentinel must leave no tracking state behind.
			ct.mu.Lock()
			assert.Empty(t, ct.channels)
			ct.mu.Unlock()
		}
	}
}

// TestTrack_CorrelationIDZeroCannotBypassInterleaving is the regression test for
// the sentinel-collision bug. Before id 0 was rejected, a MORE chunk under id 0
// set inProgressID back to 0 (== "none in progress"), so a second sequence under
// a different id slipped past the single-in-flight interleaving guard -- two
// concurrent chunked sequences on one channel+direction, the exact interleaving
// the tracker exists to forbid. With id 0 refused, the map can never hold two.
func TestTrack_CorrelationIDZeroCannotBypassInterleaving(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize)

	// A malicious peer tries to open a sequence under the sentinel id 0.
	require.Error(t, ct.Track("ch1", dirFe2w, 0, validChunkSize(100), true),
		"id 0 must never open a sequence")

	// The rejected id 0 left inProgressID at the sentinel without a live
	// sequence, so a legitimate sequence proceeds normally...
	require.NoError(t, ct.Track("ch1", dirFe2w, 7, validChunkSize(100), true))
	// ...and a second concurrent id is still refused by the interleaving guard,
	// with only the one legitimate sequence ever tracked.
	err := ct.Track("ch1", dirFe2w, 9, validChunkSize(100), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interleaving")

	ct.mu.Lock()
	defer ct.mu.Unlock()
	assert.Len(t, ct.channels["ch1"][dirFe2w].sequences, 1,
		"at most one in-flight sequence -- id 0 must not have opened a second")
}

// TestTrack_OneSequenceInFlightPerChannelDirection pins the invariant the Hub
// actually enforces, in both directions: while one correlation id is in
// progress a second is refused, and completing the first frees the slot for it.
// A count cap over this rule would be unreachable dead code -- the map can never
// hold two sequences for one channel+direction.
func TestTrack_OneSequenceInFlightPerChannelDirection(t *testing.T) {
	for _, direction := range []string{"fe2w", "w2fe"} {
		t.Run(direction, func(t *testing.T) {
			ct := newChunkTracker(testMaxMessageSize)

			// Sequence A is in progress.
			require.NoError(t, ct.Track("ch1", direction, 1, validChunkSize(100), true))

			// B is refused while A is in flight...
			err := ct.Track("ch1", direction, 2, validChunkSize(100), true)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "interleaving")

			// ...and only one sequence is ever tracked, so no count cap could fire.
			ct.mu.Lock()
			assert.Len(t, ct.channels["ch1"][direction].sequences, 1,
				"at most one in-flight sequence per channel+direction")
			ct.mu.Unlock()

			// Completing A frees the slot for B.
			require.NoError(t, ct.Track("ch1", direction, 1, validChunkSize(100), false))
			require.NoError(t, ct.Track("ch1", direction, 2, validChunkSize(100), true))

			ct.mu.Lock()
			assert.Len(t, ct.channels["ch1"][direction].sequences, 1)
			assert.Equal(t, uint64(2), ct.channels["ch1"][direction].inProgressID)
			ct.mu.Unlock()
		})
	}
}
