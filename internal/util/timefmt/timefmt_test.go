package timefmt_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func TestFormat_UTC(t *testing.T) {
	ts := time.Date(2025, 6, 15, 10, 30, 45, 123000000, time.UTC)
	got := timefmt.Format(ts)
	assert.Equal(t, "2025-06-15T10:30:45.123Z", got)
}

func TestFormat_NonUTC(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*60*60)
	// 2025-06-15 19:30:45.456 UTC+9 == 2025-06-15 10:30:45.456 UTC
	ts := time.Date(2025, 6, 15, 19, 30, 45, 456000000, loc)
	got := timefmt.Format(ts)
	assert.Equal(t, "2025-06-15T10:30:45.456Z", got)
}

func TestFormat_ZeroTime(t *testing.T) {
	got := timefmt.Format(time.Time{})
	assert.Equal(t, "0001-01-01T00:00:00.000Z", got)
}

func TestFormat_MillisecondPrecision(t *testing.T) {
	// Verify that sub-millisecond nanoseconds are truncated (not rounded) by Go's Format.
	ts := time.Date(2025, 1, 1, 0, 0, 0, 999999999, time.UTC)
	got := timefmt.Format(ts)
	// Go's Format truncates to the precision of the layout pattern (.000 = 3 digits).
	assert.Equal(t, "2025-01-01T00:00:00.999Z", got)

	// Exact millisecond boundary.
	ts2 := time.Date(2025, 1, 1, 0, 0, 0, 500000000, time.UTC)
	got2 := timefmt.Format(ts2)
	assert.Equal(t, "2025-01-01T00:00:00.500Z", got2)

	// Zero nanoseconds should produce .000.
	ts3 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	got3 := timefmt.Format(ts3)
	assert.Equal(t, "2025-01-01T00:00:00.000Z", got3)
}
