package channelwire

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AppendChunk accumulates each chunk's bytes and reports a breach exactly when the
// running total crosses limit -- the accumulate-and-check step both Go reassemblers
// share. The boundary matters: at the limit is fine, one byte over is a breach.
func TestChunkBufferAppendChunkAccumulatesAndReportsBreach(t *testing.T) {
	var b ChunkBuffer

	total, breached := b.AppendChunk([]byte("hello"), 10)
	require.False(t, breached, "5 <= 10 must not breach")
	assert.Equal(t, 5, total)

	total, breached = b.AppendChunk([]byte("world"), 10)
	require.False(t, breached, "10 == 10 (the limit itself) must not breach")
	assert.Equal(t, 10, total)

	total, breached = b.AppendChunk([]byte("!"), 10)
	require.True(t, breached, "11 > 10 must breach")
	assert.Equal(t, 11, total)

	assert.Len(t, b.Parts, 3, "every appended chunk is retained until poison or join")
}

// Poison releases the accumulated parts and total but marks the entry so its
// remaining chunks are recognised as a tombstone rather than re-buffered. It is
// idempotent.
func TestChunkBufferPoisonReleasesPartsAndIsIdempotent(t *testing.T) {
	var b ChunkBuffer
	b.AppendChunk([]byte("some bytes"), 100)
	require.NotZero(t, b.Total)
	require.NotEmpty(t, b.Parts)

	b.Poison()
	assert.True(t, b.Poisoned)
	assert.Zero(t, b.Total, "poisoning must release the accumulated total")
	assert.Empty(t, b.Parts, "poisoning must release the accumulated parts")

	b.Poison()
	assert.True(t, b.Poisoned, "poison is idempotent")
	assert.Zero(t, b.Total)
	assert.Empty(t, b.Parts)
}

func TestChunkBuffer_Join(t *testing.T) {
	t.Run("joins ordered parts into one buffer", func(t *testing.T) {
		buf := &ChunkBuffer{Parts: [][]byte{[]byte("ab"), []byte("cd"), []byte("e")}, Total: 5}
		assert.Equal(t, []byte("abcde"), buf.Join())
	})

	t.Run("empty parts yield a non-nil empty slice", func(t *testing.T) {
		buf := &ChunkBuffer{}
		got := buf.Join()
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})
}

// CountByState tallies a buffer map into its live and tombstoned counts. Both
// Go reassemblers derive their cap counts from it, so the partition must match
// each ChunkBuffer's Poisoned flag exactly.
func TestCountByState_PartitionsByPoisonedFlag(t *testing.T) {
	buffers := map[uint64]*ChunkBuffer{
		1: {},
		2: {Poisoned: true},
		3: {Parts: [][]byte{[]byte("x")}},
		4: {Poisoned: true},
	}

	live, tombstones := CountByState(buffers)
	assert.Equal(t, 2, live, "ids 1 and 3 are accumulating (not Poisoned)")
	assert.Equal(t, 2, tombstones, "ids 2 and 4 are tombstoned")
}

// An empty map yields zero on both axes; a nil map likewise, so the helper is
// safe to call on a not-yet-allocated reassembly map.
func TestCountByState_EmptyAndNilMapsAreZero(t *testing.T) {
	live, tombstones := CountByState(map[uint64]*ChunkBuffer{})
	assert.Zero(t, live)
	assert.Zero(t, tombstones)

	live, tombstones = CountByState(nil)
	assert.Zero(t, live)
	assert.Zero(t, tombstones)
}
