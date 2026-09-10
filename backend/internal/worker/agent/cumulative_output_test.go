package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCumulativeOutputCounterTracksAppendAndLimitedTail(t *testing.T) {
	t.Parallel()

	var counter CumulativeOutputCounter
	assert.Equal(t, CumulativeOutputObservation{Total: 6}, counter.Observe("abcdef", false))
	assert.Equal(t, CumulativeOutputObservation{Total: 9}, counter.Observe("abcdefghi", false))

	observed := counter.Observe("...\n\nfghijk", true)
	assert.Equal(t, int64(11), observed.Total)
	assert.True(t, observed.Minimum)
}

func TestCumulativeOutputCounterDoesNotInventBytesWithoutOverlap(t *testing.T) {
	t.Parallel()

	var counter CumulativeOutputCounter
	counter.Observe("abc", false)
	observed := counter.Observe(strings.Repeat("X", 64<<10), true)
	assert.Equal(t, int64(64<<10), observed.Total)
	assert.True(t, observed.Minimum)
}

func TestCumulativeOutputCounterKeepsLiteralLimitPrefix(t *testing.T) {
	t.Parallel()

	var counter CumulativeOutputCounter
	observed := counter.Observe("...\n\nreal output", false)
	assert.Equal(t, int64(len("...\n\nreal output")), observed.Total)
	assert.False(t, observed.Minimum)
}

func TestCumulativeOutputCounterMarksAnUnexpectedReplacementAsMinimum(t *testing.T) {
	t.Parallel()

	var counter CumulativeOutputCounter
	counter.Observe("abcdef", false)
	observed := counter.Observe("defghi", false)
	assert.Equal(t, int64(6), observed.Total)
	assert.True(t, observed.Minimum)
}

func TestSuffixPrefixOverlapHandlesRepeatedLongPrefixes(t *testing.T) {
	t.Parallel()

	previous := strings.Repeat("a", 30_000) + "b"
	next := "b" + strings.Repeat("a", 30_000)
	assert.Equal(t, 1, suffixPrefixOverlap(previous, next))
}
