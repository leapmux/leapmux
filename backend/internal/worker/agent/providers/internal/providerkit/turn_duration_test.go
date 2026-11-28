package providerkit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnDurationMillis(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	plusNine := time.FixedZone("UTC+9", 9*3600)

	assert.Nil(t, TurnDurationMillis(time.Time{}, start), "unobserved start returns nil")
	assert.Nil(t, TurnDurationMillis(start, time.Time{}), "zero end precedes non-zero start")
	assert.Nil(t, TurnDurationMillis(time.Time{}, time.Time{}), "both zero returns nil")
	assert.Nil(t, TurnDurationMillis(start, start.Add(-time.Millisecond)), "backwards clock returns nil")
	assert.Nil(t, TurnDurationMillis(start, start.Add(-time.Nanosecond)), "sub-millisecond negative returns nil")
	assert.Nil(t, TurnDurationMillis(start, time.Date(2026, 9, 25, 20, 59, 59, 999_000_000, plusNine)), "earlier instant in another timezone returns nil")

	for _, tc := range []struct {
		name string
		end  time.Time
		want int64
	}{
		{name: "exact zero", end: start, want: 0},
		{name: "sub-millisecond truncates to zero", end: start.Add(999 * time.Microsecond), want: 0},
		{name: "sub-millisecond truncates to one", end: start.Add(1999 * time.Microsecond), want: 1},
		{name: "long turn", end: start.Add(90*time.Minute + 1500*time.Millisecond), want: 5401500},
		{name: "same instant across timezones", end: time.Date(2026, 9, 25, 21, 0, 0, 0, plusNine), want: 0},
		{name: "later instant across timezones", end: time.Date(2026, 9, 25, 21, 0, 2, 500_000_000, plusNine), want: 2500},
	} {
		got := TurnDurationMillis(start, tc.end)
		require.NotNil(t, got, tc.name)
		assert.Equal(t, tc.want, *got, tc.name)
	}
}
