package version

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat_BranchDisplayRules(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		hash   string
		branch string
		want   string
	}{
		{
			name:   "main branch hidden",
			value:  "0.0.1-dev",
			hash:   "abc1234",
			branch: "main",
			want:   "0.0.1-dev · abc1234",
		},
		{
			name:   "feature branch shown",
			value:  "0.0.1-dev",
			hash:   "abc1234",
			branch: "feature/foo",
			want:   "0.0.1-dev · abc1234 · feature/foo",
		},
		{
			name:   "tag name shown",
			value:  "1.0.0",
			hash:   "abc1234",
			branch: "v1.0.0",
			want:   "1.0.0 · abc1234 · v1.0.0",
		},
		{
			name:   "empty branch with commit renders as detached",
			value:  "0.0.1-dev",
			hash:   "abc1234",
			branch: "",
			want:   "0.0.1-dev · abc1234 · <detached>",
		},
		{
			name:   "empty branch without commit stays silent",
			value:  "dev",
			hash:   "",
			branch: "",
			want:   "dev",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Save and restore package-level vars so tests don't leak.
			savedValue, savedHash, savedBranch, savedBuild := Value, CommitHash, Branch, BuildTime
			defer func() { Value, CommitHash, Branch, BuildTime = savedValue, savedHash, savedBranch, savedBuild }()

			Value = tc.value
			CommitHash = tc.hash
			Branch = tc.branch
			BuildTime = "" // exclude BuildTime so assertions stay deterministic across timezones

			assert.Equal(t, tc.want, Format())
		})
	}
}

func TestFormatLocalTimestamp(t *testing.T) {
	t.Run("invalid input falls back to raw string", func(t *testing.T) {
		require.Equal(t, "not-a-time", FormatLocalTimestamp("not-a-time"))
	})

	t.Run("includes timezone suffix", func(t *testing.T) {
		got := FormatLocalTimestamp("2026-04-14T10:20:30Z")
		require.NotEmpty(t, got)
		require.Contains(t, got, "20:30")
		fields := strings.Fields(got)
		require.GreaterOrEqual(t, len(fields), 5, "expected timezone suffix in %q", got)
		last := fields[len(fields)-1]
		require.NotEqual(t, "PM", last, "expected timezone after time in %q", got)
		require.NotEqual(t, "AM", last, "expected timezone after time in %q", got)
	})

	t.Run("empty input stays empty", func(t *testing.T) {
		require.Empty(t, FormatLocalTimestamp(""))
	})
}
