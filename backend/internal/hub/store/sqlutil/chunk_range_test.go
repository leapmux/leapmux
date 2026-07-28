package sqlutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ChunkRange carries the index arithmetic for every bulk tab-index path, but
// the store suites only ever write a handful of rows against chunk sizes of
// 142 / 499 / 4096 / 8192 -- so every existing test drives exactly ONE
// iteration and the loop's real work (advancing the cursor, clamping the final
// window) is unexercised.
// An off-by-one that dropped the last chunk, double-counted a row, or looped
// forever would leave the whole suite green while a large workspace silently
// lost the tail of its tab-index writes.
//
// These cases pin the window sequence directly, at sizes the callers cannot
// reach in a fixture.
func TestChunkRange(t *testing.T) {
	// collect records every [start, end) window ChunkRange produces.
	collect := func(total, size int) [][2]int {
		var got [][2]int
		require.NoError(t, ChunkRange(total, size, func(start, end int) error {
			got = append(got, [2]int{start, end})
			return nil
		}))
		return got
	}

	t.Run("exact multiple splits evenly with no empty tail", func(t *testing.T) {
		assert.Equal(t, [][2]int{{0, 2}, {2, 4}, {4, 6}}, collect(6, 2))
	})

	t.Run("remainder clamps the final window to total", func(t *testing.T) {
		assert.Equal(t, [][2]int{{0, 3}, {3, 5}}, collect(5, 3),
			"the tail must stop at total, not run past it")
	})

	t.Run("total below the chunk size is one full window", func(t *testing.T) {
		assert.Equal(t, [][2]int{{0, 2}}, collect(2, 4096),
			"the case every existing store test exercises")
	})

	t.Run("total equal to the chunk size is one window, not two", func(t *testing.T) {
		assert.Equal(t, [][2]int{{0, 4}}, collect(4, 4),
			"a <= vs < slip here would emit a trailing empty chunk")
	})

	t.Run("zero total invokes fn not at all", func(t *testing.T) {
		assert.Empty(t, collect(0, 4))
	})

	t.Run("size of one yields a window per element", func(t *testing.T) {
		assert.Equal(t, [][2]int{{0, 1}, {1, 2}, {2, 3}}, collect(3, 1))
	})

	t.Run("every element is covered exactly once", func(t *testing.T) {
		// Independent of the window shapes above: the union of the windows must
		// be a partition of [0, total). This is the property the callers
		// actually depend on -- rows[start:end] slices must tile the input.
		const total, size = 47, 5
		seen := make([]int, total)
		for _, w := range collect(total, size) {
			for i := w[0]; i < w[1]; i++ {
				seen[i]++
			}
		}
		for i, n := range seen {
			assert.Equal(t, 1, n, "index %d covered %d times", i, n)
		}
	})

	t.Run("an error stops the walk immediately", func(t *testing.T) {
		boom := errors.New("boom")
		calls := 0
		err := ChunkRange(10, 2, func(_, _ int) error {
			calls++
			if calls == 2 {
				return boom
			}
			return nil
		})
		require.ErrorIs(t, err, boom)
		assert.Equal(t, 2, calls, "the walk must not continue past a failed chunk")
	})
}
