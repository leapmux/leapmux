package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

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

// The cut keeps the END of the tail, and it lands on a rune boundary.
func TestClipTailBytesKeepsTheEndAtARuneBoundary(t *testing.T) {
	t.Parallel()

	short, clipped := ClipTailBytes("short", 64)
	assert.Equal(t, "short", short)
	assert.False(t, clipped)

	// Each Hangul syllable is three bytes, so a limit of 4 lands inside the second
	// one. The cut advances to the next rune start rather than emit a partial rune.
	tail, clipped := ClipTailBytes("가나", 4)
	assert.Equal(t, "나", tail)
	assert.True(t, clipped)
	assert.True(t, utf8.ValidString(tail))
}

// A limit of zero or less keeps nothing, and a negative one must not panic.
//
// The cut used to compute len(text)-limit, which a negative limit pushes PAST the
// end of the string. A caller that clamps its limit to a remaining budget can reach
// that value from arithmetic it never inspects, so the clip answers it instead of
// killing the worker.
func TestClipTailBytesKeepsNothingForAnEmptyLimit(t *testing.T) {
	t.Parallel()

	tail, clipped := ClipTailBytes("dropped", 0)
	assert.Empty(t, tail)
	assert.True(t, clipped, "bytes were dropped, so the result states the loss")

	tail, clipped = ClipTailBytes("", 0)
	assert.Empty(t, tail)
	assert.False(t, clipped, "an empty input loses nothing")

	assert.NotPanics(t, func() { tail, clipped = ClipTailBytes("dropped", -1) })
	assert.Empty(t, tail)
	assert.True(t, clipped)

	assert.NotPanics(t, func() { tail, clipped = ClipTailBytes("", -1) })
	assert.Empty(t, tail)
	assert.False(t, clipped)
}

// A clipped tail must not keep the WHOLE input alive.
//
// A Go string slice shares the backing array of the string it came from, so a tail
// of two kilobytes resliced out of a megabyte of output pins that megabyte for as
// long as the caller holds the tail -- and both Codex and Copilot hold one for the
// length of a running call. The test reads the data pointer, because where the
// bytes live is the only observable difference between a reslice and a copy.
func TestClipTailBytesReleasesTheInputItClipped(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", 64<<10)
	tail, clipped := ClipTailBytes(text, 2048)
	assert.True(t, clipped)
	assert.Equal(t, strings.Repeat("a", 2048), tail)

	first := uintptr(unsafe.Pointer(unsafe.StringData(text)))
	tailAt := uintptr(unsafe.Pointer(unsafe.StringData(tail)))
	assert.False(t, tailAt >= first && tailAt < first+uintptr(len(text)),
		"the tail points into the input, so it holds every byte of it alive")
}

func TestSuffixPrefixOverlapHandlesRepeatedLongPrefixes(t *testing.T) {
	t.Parallel()

	previous := strings.Repeat("a", 30_000) + "b"
	next := "b" + strings.Repeat("a", 30_000)
	assert.Equal(t, 1, suffixPrefixOverlap(previous, next))
}
