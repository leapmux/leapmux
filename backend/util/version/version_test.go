package version

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
			name:   "empty branch hidden (detached / tag / ad-hoc)",
			value:  "1.0.0",
			hash:   "abc1234",
			branch: "",
			want:   "1.0.0 · abc1234",
		},
		{
			name:   "empty everything falls back to Value",
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

func TestBuildInfoProto(t *testing.T) {
	savedValue, savedHash, savedCommitTime, savedBuild, savedBranch := Value, CommitHash, CommitTime, BuildTime, Branch
	defer func() {
		Value, CommitHash, CommitTime, BuildTime, Branch = savedValue, savedHash, savedCommitTime, savedBuild, savedBranch
	}()

	t.Run("reports every build variable", func(t *testing.T) {
		Value, CommitHash, CommitTime, BuildTime, Branch = "1.2.3", "abc1234", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", "feature/x"

		info := BuildInfoProto()
		assert.Equal(t, "1.2.3", info.GetVersion())
		assert.Equal(t, "abc1234", info.GetCommitHash())
		assert.Equal(t, "2026-10-01T00:00:00Z", info.GetCommitTime())
		assert.Equal(t, "2026-10-02T00:00:00Z", info.GetBuildTime())
		assert.Equal(t, "feature/x", info.GetBranch())
	})

	t.Run("reports an empty branch for a detached HEAD", func(t *testing.T) {
		Value, CommitHash, CommitTime, BuildTime, Branch = "dev", "", "", "", ""

		info := BuildInfoProto()
		assert.Equal(t, "dev", info.GetVersion())
		assert.Empty(t, info.GetBranch())
		assert.Empty(t, info.GetCommitHash())
	})

	t.Run("returns a fresh message on every call", func(t *testing.T) {
		Value = "1.2.3"

		first := BuildInfoProto()
		first.Version = "changed by a caller"
		assert.Equal(t, "1.2.3", BuildInfoProto().GetVersion())
		assert.NotSame(t, first, BuildInfoProto())
	})
}

// FormatBuild formats the build it is given, whichever binary reported it, by
// the rules that Format applies to this binary's own build.
func TestFormatBuild(t *testing.T) {
	tests := []struct {
		name  string
		build *leapmuxv1.BuildInfo
		want  string
	}{
		{"a nil build", nil, ""},
		{"an empty build", &leapmuxv1.BuildInfo{}, ""},
		{"a version alone", &leapmuxv1.BuildInfo{Version: "1.2.3"}, "1.2.3"},
		{
			"a commit and a feature branch",
			&leapmuxv1.BuildInfo{Version: "1.2.3", CommitHash: "abc1234", Branch: "feature/foo"},
			"1.2.3 · abc1234 · feature/foo",
		},
		{
			"the main branch is omitted",
			&leapmuxv1.BuildInfo{Version: "1.2.3", CommitHash: "abc1234", Branch: "main"},
			"1.2.3 · abc1234",
		},
		{
			"a report with no version does not open with a separator",
			&leapmuxv1.BuildInfo{CommitHash: "abc1234"},
			"abc1234",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatBuild(tt.build))
		})
	}
}

// The build time is shown in the local timezone, through the one helper.
func TestFormatBuild_ShowsTheBuildTimeInLocalTime(t *testing.T) {
	const iso = "2026-04-23T14:45:00Z"
	assert.Equal(t, "1.2.3 · "+FormatLocalTimestamp(iso),
		FormatBuild(&leapmuxv1.BuildInfo{Version: "1.2.3", BuildTime: iso}))
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
