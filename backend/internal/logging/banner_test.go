package logging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddrToURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{":4327", "http://localhost:4327"},
		{"0.0.0.0:4327", "http://localhost:4327"},
		{"127.0.0.1:4327", "http://localhost:4327"},
		{":80", "http://localhost"},
		{"example.com:443", "http://localhost:443"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			assert.Equal(t, tt.want, addrToURL(tt.addr))
		})
	}
}

func TestFormatLocalTimestamp(t *testing.T) {
	t.Run("invalid input falls back to raw string", func(t *testing.T) {
		require.Equal(t, "not-a-time", formatLocalTimestamp("not-a-time"))
	})

	t.Run("includes timezone suffix", func(t *testing.T) {
		got := formatLocalTimestamp("2026-04-14T10:20:30Z")
		require.NotEmpty(t, got)
		require.Contains(t, got, "20:30")
		fields := strings.Fields(got)
		require.GreaterOrEqual(t, len(fields), 5, "expected timezone suffix in %q", got)
		last := fields[len(fields)-1]
		require.NotEqual(t, "PM", last, "expected timezone after time in %q", got)
		require.NotEqual(t, "AM", last, "expected timezone after time in %q", got)
	})

	t.Run("empty input stays empty", func(t *testing.T) {
		require.Empty(t, formatLocalTimestamp(""))
	})
}
