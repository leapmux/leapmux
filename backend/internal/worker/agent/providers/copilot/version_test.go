package copilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCopilotVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		output string
		want   copilotVersion
		ok     bool
	}{
		{"1.0.103\n", copilotVersion{1, 0, 103}, true},
		{"GitHub Copilot CLI 1.0.103.", copilotVersion{1, 0, 103}, true},
		{"1.0.104-beta.2", copilotVersion{1, 0, 104}, true},
		{"1.0.104+build.2", copilotVersion{1, 0, 104}, true},
		{"1.0.104-beta.2+build.2", copilotVersion{1, 0, 104}, true},
		{"v2.0.0 (build abc)", copilotVersion{2, 0, 0}, true},
		{"GitHub Copilot CLI 1.0.103\nsee 2.0.0 for details", copilotVersion{1, 0, 103}, true},
		{"", copilotVersion{}, false},
		{"1.0", copilotVersion{}, false},
		{"copilot version two", copilotVersion{}, false},
		{"99999999999999999999.0.0", copilotVersion{}, false},
	} {
		got, ok := parseCopilotVersion(tc.output)
		assert.Equal(t, tc.ok, ok, tc.output)
		assert.Equal(t, tc.want, got, tc.output)
	}
}

func TestCopilotVersionLess(t *testing.T) {
	t.Parallel()

	assert.True(t, copilotVersion{1, 9, 9}.less(copilotVersion{2, 0, 0}))
	assert.True(t, copilotVersion{2, 0, 1}.less(copilotVersion{2, 1, 0}))
	assert.True(t, copilotVersion{2, 0, 1}.less(copilotVersion{2, 0, 2}))
	assert.False(t, copilotVersion{2, 0, 0}.less(copilotVersion{2, 0, 0}))
	assert.False(t, copilotVersion{3, 0, 0}.less(copilotVersion{2, 9, 9}))
	assert.Equal(t, "1.0.103", copilotVersion{1, 0, 103}.String())
}

func TestCheckCopilotVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		output string
		want   copilotVersion
	}{
		{"GitHub Copilot CLI 1.0.103.", copilotVersion{1, 0, 103}},
		{"1.0.103-rc.1", copilotVersion{1, 0, 103}},
		{"GitHub Copilot CLI 1.0.104.", copilotVersion{1, 0, 104}},
		{"1.1.0", copilotVersion{1, 1, 0}},
		{"2.0.0", copilotVersion{2, 0, 0}},
	} {
		version, err := checkCopilotVersion(tc.output)
		require.NoError(t, err, tc.output)
		assert.Equal(t, tc.want, version, tc.output)
	}

	for _, tc := range []struct {
		output    string
		detected  string
		noVersion bool
	}{
		{output: "GitHub Copilot CLI 1.0.102.", detected: "1.0.102"},
		{output: "0.9.9", detected: "0.9.9"},
		{output: "0.103.0", detected: "0.103.0"},
		{output: "Usage: copilot [OPTIONS]\nmore help", noVersion: true},
	} {
		_, err := checkCopilotVersion(tc.output)
		require.Error(t, err, tc.output)
		assert.Contains(t, err.Error(), "GitHub Copilot CLI 1.0.103 or later", tc.output)
		assert.Contains(t, err.Error(), "make sure it is the `copilot` on PATH", tc.output)
		if tc.noVersion {
			assert.Contains(t, err.Error(), "Usage: copilot [OPTIONS]", tc.output)
			assert.NotContains(t, err.Error(), "more help", "the error quotes only the first non-blank line")
		} else {
			assert.Contains(t, err.Error(), tc.detected, tc.output)
		}
	}
}
