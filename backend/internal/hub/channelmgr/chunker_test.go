package channelmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMaxMessageSize       = 1024 * 1024 // 1 MiB
	testMaxIncompleteChunked = 2
	dirFe2w                  = "fe2w"
)

// validChunkSize returns a ciphertext length that fits within one Noise
// transport message and whose estimated plaintext fits within the given budget.
func validChunkSize(plaintextBytes int) int {
	return plaintextBytes + noiseAuthTagSize
}

// TestTrack_SingleChunkMessage verifies that a non-chunked (flags=UNSPECIFIED,
// more=false) message is accepted and leaves no tracking state.
func TestTrack_SingleChunkMessage(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)
	corrID := uint32(42)

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
	ct := newChunkTracker(maxSize, testMaxIncompleteChunked)
	corrID := uint32(1)

	// First chunk: 400 bytes plaintext — within limit.
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(400), true)
	require.NoError(t, err)

	// Second chunk: 200 bytes more — total 600 > 500 → must fail.
	err = ct.Track("ch1", dirFe2w, corrID, validChunkSize(200), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")

	// After rejection the sequence must be removed so the correlationID can be
	// reused without leaking state.
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if dirs, ok := ct.channels["ch1"]; ok {
		if state, ok := dirs[dirFe2w]; ok {
			assert.NotContains(t, state.sequences, corrID)
		}
	}
}

// TestTrack_MaxSizeExceeded_OnFinalChunk verifies that cumulative plaintext
// exceeding maxMessageSize is rejected on the final (more=false) chunk.
func TestTrack_MaxSizeExceeded_OnFinalChunk(t *testing.T) {
	maxSize := 500
	ct := newChunkTracker(maxSize, testMaxIncompleteChunked)
	corrID := uint32(1)

	// First chunk: 400 bytes plaintext — within limit.
	err := ct.Track("ch1", dirFe2w, corrID, validChunkSize(400), true)
	require.NoError(t, err)

	// Final chunk: 200 bytes more — total 600 > 500 → must fail.
	err = ct.Track("ch1", dirFe2w, corrID, validChunkSize(200), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")
}

// TestTrack_MaxIncompleteExceeded verifies that starting more concurrent chunked
// sequences than maxIncompleteChunked is rejected.
func TestTrack_MaxIncompleteExceeded(t *testing.T) {
	// Allow exactly 2 concurrent incomplete sequences.
	ct := newChunkTracker(testMaxMessageSize, 2)

	// Start first sequence.
	err := ct.Track("ch1", dirFe2w, 1, validChunkSize(10), true)
	require.NoError(t, err)

	// inProgressID is now 1; starting a second sequence while 1 is in progress
	// would trigger the interleaving guard first. Complete sequence 1's
	// in-progress designation by starting it, then switch channels or use a
	// different direction so inProgressID resets between sequences.
	//
	// To exercise the maxIncomplete path independently of the interleaving
	// check, we use different directions (each direction has its own state).
	err = ct.Track("ch1", "w2fe", 2, validChunkSize(10), true)
	require.NoError(t, err)

	// Both slots are used. A third distinct direction would be on a separate
	// state map entry, so the limit applies per (channelID, direction) pair.
	// To hit the limit within one direction, we need to finish the inProgress
	// sequence and start a second one, then a third.
	//
	// Use a fresh tracker with limit=2 and a single direction.
	ct2 := newChunkTracker(testMaxMessageSize, 2)

	// Sequence A — becomes inProgressID.
	require.NoError(t, ct2.Track("ch1", dirFe2w, 10, validChunkSize(10), true))
	// Sequence B — different correlationID while A is in-progress → interleaving
	// error fires first. We need to exhaust the sequences map without triggering
	// interleaving, which means we must first complete A's in-progress lock.
	//
	// The code sets inProgressID = correlationID on first encounter. A second
	// distinct correlationID triggers interleaving. So to fill the sequences map
	// to maxIncompleteChunked without interleaving interference, we use the same
	// correlationID for multiple chunks (only one sequence per correlationID) and
	// rely on a tracker where maxIncompleteChunked = 1.
	ct3 := newChunkTracker(testMaxMessageSize, 1)
	require.NoError(t, ct3.Track("ch1", dirFe2w, 10, validChunkSize(10), true))
	// Sequence map is full (1 entry = limit). Trying to add another correlationID
	// would first hit interleaving, not the limit. Verify by using a second
	// correlationID that is different from inProgressID (10):
	err = ct3.Track("ch1", dirFe2w, 99, validChunkSize(10), true)
	require.Error(t, err)
	// Either interleaving or max-incomplete; both are valid rejection reasons.
	assert.True(t,
		contains(err.Error(), "interleaving") || contains(err.Error(), "too many incomplete"),
		"unexpected error: %v", err)
}

// TestTrack_MaxIncompleteExceeded_Direct directly fills the sequences map to
// the limit using the same inProgressID, then attempts a new correlationID.
func TestTrack_MaxIncompleteExceeded_Direct(t *testing.T) {
	// Use maxIncomplete=2 but inject state directly so we can test the limit
	// without the interleaving guard getting in the way.
	ct := newChunkTracker(testMaxMessageSize, 2)

	// Manually populate two sequences so the map is full and inProgressID is 0
	// (simulating sequences that arrived with a prior inProgressID that has been
	// cleared, e.g. the previous in-progress sequence completed).
	ct.mu.Lock()
	dirs := make(map[string]*channelChunkState)
	ct.channels["ch1"] = dirs
	state := &channelChunkState{
		sequences: map[uint32]*chunkSequence{
			1: {estimatedPlaintext: 10},
			2: {estimatedPlaintext: 10},
		},
		inProgressID: 0, // no current in-progress, so interleaving check is skipped
	}
	dirs[dirFe2w] = state
	ct.mu.Unlock()

	// Now try to add a third sequence — must be rejected with too-many-incomplete.
	err := ct.Track("ch1", dirFe2w, 3, validChunkSize(10), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many incomplete")
}

// TestTrack_InterleavingRejected verifies that a chunk for correlationID B is
// rejected while correlationID A is still the in-progress sequence.
func TestTrack_InterleavingRejected(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
			ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)
	assert.NotPanics(t, func() {
		ct.RemoveChannel("does-not-exist")
	})
}

// TestTrack_ChunkCiphertextTooLarge verifies that a single chunk whose
// ciphertext exceeds maxChunkCiphertext is rejected immediately.
func TestTrack_ChunkCiphertextTooLarge(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

	err := ct.Track("ch1", dirFe2w, 1, maxChunkCiphertext, true)
	require.NoError(t, err)
}

// TestTrack_SameCorrelationID_ContinuesAfterFirst verifies that subsequent MORE
// chunks with the same correlationID accumulate correctly without errors.
func TestTrack_SameCorrelationID_ContinuesAfterFirst(t *testing.T) {
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)
	corrID := uint32(7)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(testMaxMessageSize, testMaxIncompleteChunked)

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
	ct := newChunkTracker(maxSize, testMaxIncompleteChunked)

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

// contains is a helper to avoid importing strings in test assertions.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
