package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Set call appends one value; the flag accumulates rather than replacing, so
// `--listen a --listen b` is the list [a, b].
func TestStringSliceFlag_AppendsPerSet(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, nil)

	require.NoError(t, f.Set(":8080"))
	require.NoError(t, f.Set("unix:/tmp/x.sock"))
	assert.Equal(t, []string{":8080", "unix:/tmp/x.sock"}, target)
	assert.Equal(t, []string{":8080", "unix:/tmp/x.sock"}, f.Get())
}

// String reports the comma-joined values, which is the spelling the help line
// and FlagProvider.Value.String() read. A value that holds a comma is
// indistinguishable from two values in that one string -- the flag Set path
// itself never splits, which is what keeps such a value whole.
func TestStringSliceFlag_ReportsTheJoinedString(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, []string{"a", "b"})

	assert.Equal(t, "a,b", f.String(), "before the first Set the default is reported")
	assert.Equal(t, []string{"a", "b"}, f.Get(), "before the first Set the default is returned")

	require.NoError(t, f.Set("unix:/tmp/a,b.sock"))
	require.NoError(t, f.Set(":8080"))
	assert.Equal(t, "unix:/tmp/a,b.sock,:8080", f.String())
	assert.Equal(t, []string{"unix:/tmp/a,b.sock", ":8080"}, f.Get(),
		"Set takes the value verbatim; the comma inside one value survives")
}

// SplitListValue is the ENV side: the separator is a comma there, empty is
// no list, and the pieces are trimmed.
func TestSplitListValue(t *testing.T) {
	assert.Nil(t, SplitListValue(""))
	assert.Equal(t, []string{":8080", ":9090"}, SplitListValue(":8080,:9090"))
	assert.Equal(t, []string{":8080", "unix:/tmp/x.sock"}, SplitListValue(":8080, unix:/tmp/x.sock"))
	assert.Equal(t, []string{"unix:/tmp/a", "b.sock"}, SplitListValue("unix:/tmp/a,b.sock"),
		"the env form splits on commas even inside one path")
}

// A separator run keeps its empty pieces rather than dropping them. Dropping
// them would turn a typo into "no entry here", which the caller reads as the
// platform default and binds more than the operator asked for; keeping them
// fails validation at the entry's index instead.
func TestSplitListValue_KeepsEmptyPiecesSoATypoFailsRatherThanBindsTheDefaults(t *testing.T) {
	assert.Equal(t, []string{"", ""}, SplitListValue(","))
	assert.Equal(t, []string{"", "", ""}, SplitListValue(",,"))
	assert.Equal(t, []string{":8080", ""}, SplitListValue(":8080,"))
	assert.Equal(t, []string{"", ":8080"}, SplitListValue(" , :8080"))
	assert.Equal(t, []string{"", "", ""}, SplitListValue(" , , "))
}
