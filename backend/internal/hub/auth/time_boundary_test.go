package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsExpired_Boundaries(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "before", now: expiresAt.Add(-time.Nanosecond), want: false},
		{name: "equal", now: expiresAt, want: true},
		{name: "after", now: expiresAt.Add(time.Nanosecond), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsExpired(tc.now, expiresAt))
		})
	}
}
