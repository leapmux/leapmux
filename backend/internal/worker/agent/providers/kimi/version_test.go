package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKimiVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		output string
		want   kimiVersion
		ok     bool
	}{
		{"2.0.2\n", kimiVersion{2, 0, 2}, true},
		{"kimi, version 1.5.0", kimiVersion{1, 5, 0}, true},
		{"2.1.0-beta.1", kimiVersion{2, 1, 0}, true},
		{"v10.20.30 (build abc)", kimiVersion{10, 20, 30}, true},
		{"", kimiVersion{}, false},
		{"kimi version two", kimiVersion{}, false},
		{"2.0", kimiVersion{}, false},
		{"99999999999999999999.0.0", kimiVersion{}, false},
	} {
		got, ok := parseKimiVersion(tc.output)
		assert.Equal(t, tc.ok, ok, tc.output)
		assert.Equal(t, tc.want, got, tc.output)
	}
}

func TestKimiVersionLess(t *testing.T) {
	t.Parallel()

	assert.True(t, kimiVersion{1, 9, 9}.less(kimiVersion{2, 0, 0}))
	assert.True(t, kimiVersion{2, 0, 1}.less(kimiVersion{2, 1, 0}))
	assert.True(t, kimiVersion{2, 0, 1}.less(kimiVersion{2, 0, 2}))
	assert.False(t, kimiVersion{2, 0, 0}.less(kimiVersion{2, 0, 0}))
	assert.False(t, kimiVersion{3, 0, 0}.less(kimiVersion{2, 9, 9}))
	assert.Equal(t, "2.0.2", kimiVersion{2, 0, 2}.String())
}

func TestCheckKimiVersion(t *testing.T) {
	t.Parallel()

	version, err := checkKimiVersion(kimiVersionFromCLI, "2.0.2")
	require.NoError(t, err)
	assert.Equal(t, kimiVersion{2, 0, 2}, version)

	_, err = checkKimiVersion(kimiVersionFromCLI, "2.0.0")
	require.NoError(t, err, "the first 2.x release is the minimum")

	_, err = checkKimiVersion(kimiVersionFromCLI, "kimi, version 1.5.0")
	require.ErrorIs(t, err, errKimiLegacyCLI)
	assert.Contains(t, err.Error(), "1.5.0")
	assert.Contains(t, err.Error(), "legacy Python kimi-cli")

	_, err = checkKimiVersion(kimiVersionFromCLI, "Usage: kimi [OPTIONS]\nmore help")
	require.ErrorIs(t, err, errKimiLegacyCLI)
	assert.Contains(t, err.Error(), "`kimi --version` printed \"Usage: kimi [OPTIONS]\"")
	assert.NotContains(t, err.Error(), "more help", "the error quotes only the first line")

	_, err = checkKimiVersion(kimiVersionFromServer, "")
	require.ErrorIs(t, err, errKimiLegacyCLI)
	assert.Contains(t, err.Error(), "the server stated the version \"\"", "the error states where the version came from")
}

func TestAfterDelimiter(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "\n2.0.2\n", afterDelimiter("motd\nDELIM\n2.0.2\n", "DELIM"))
	assert.Equal(t, "2.0.2", afterDelimiter("2.0.2", ""))
	assert.Equal(t, "2.0.2", afterDelimiter("2.0.2", "DELIM"), "output with no delimiter is kept whole")
}

func TestFirstLine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "first", firstLine("\n  \n first \nsecond"))
	assert.Empty(t, firstLine(" \n\t\n"))
	assert.Empty(t, firstLine(""))
}
