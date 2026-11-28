package config

import (
	"testing"

	"github.com/spf13/pflag"
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

// String formats values as bracketed CSV matching pflag's stringArrayValue so
// values containing commas survive FlagSet.GetStringArray round-trips.
func TestStringSliceFlag_ReportsTheJoinedString(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, []string{"a", "b"})

	assert.Equal(t, "[a,b]", f.String(), "before the first Set the default is reported")
	assert.Equal(t, []string{"a", "b"}, f.Get(), "before the first Set the default is returned")

	require.NoError(t, f.Set("unix:/tmp/a,b.sock"))
	require.NoError(t, f.Set(":8080"))
	assert.Equal(t, `["unix:/tmp/a,b.sock",:8080]`, f.String())
	assert.Equal(t, []string{"unix:/tmp/a,b.sock", ":8080"}, f.Get(),
		"Set takes the value verbatim; the comma inside one value survives")
}

// SplitListValue is the ENV side: the separator is a comma there, empty is
// no list, and each piece loses its LEADING and TRAILING spaces. The middle
// of a value is untouched, so a socket path or a named pipe that holds a
// space keeps it: " foo , bar baz " is the list ["foo", "bar baz"].
func TestSplitListValue(t *testing.T) {
	assert.Nil(t, SplitListValue(""))
	assert.Equal(t, []string{":8080", ":9090"}, SplitListValue(":8080,:9090"))
	assert.Equal(t, []string{":8080", "unix:/tmp/x.sock"}, SplitListValue(":8080, unix:/tmp/x.sock"))
	assert.Equal(t, []string{"unix:/tmp/a", "b.sock"}, SplitListValue("unix:/tmp/a,b.sock"),
		"the env form splits on commas even inside one path")
	assert.Equal(t, []string{"foo", "bar baz"}, SplitListValue(" foo , bar baz "),
		"leading and trailing spaces go; the space in the middle of 'bar baz' stays")
}

// Set trims the same way: one shell token loses its leading and trailing
// spaces and keeps the rest, so the flag and the environment name an address
// alike. A token of spaces alone trims to an empty value rather than
// vanishing, so the caller's validation rejects it at its index instead of
// the list silently losing an entry.
func TestStringSliceFlag_SetTrimsTheEndsAndKeepsTheMiddle(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, nil)

	require.NoError(t, f.Set(" :4327 "))
	require.NoError(t, f.Set("bar baz"))
	require.NoError(t, f.Set("unix:/tmp/my dir/s.sock"))
	require.NoError(t, f.Set("   "))
	assert.Equal(t, []string{":4327", "bar baz", "unix:/tmp/my dir/s.sock", ""}, target)
}

// String reports the TRIMMED spelling in bracketed CSV format, matching the
// addresses the hub will actually bind rather than stray spaces.
func TestStringSliceFlag_ReportsTheTrimmedSpelling(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, nil)

	require.NoError(t, f.Set(" :4327 "))
	require.NoError(t, f.Set(" :9090"))
	assert.Equal(t, "[:4327,:9090]", f.String())
	assert.Equal(t, []string{":4327", ":9090"}, f.Get())
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

// Type is pflag's own name for a repeatable string, so the help marks the
// flag repeatable.
func TestStringSliceFlag_TypeMarksItRepeatable(t *testing.T) {
	var target []string
	assert.Equal(t, "stringArray", NewStringSliceFlag(&target, nil).Type())
}

func TestStringSliceFlag_ImplementsSliceValue(t *testing.T) {
	var target []string
	f := NewStringSliceFlag(&target, []string{"default"})
	assert.Equal(t, []string{"default"}, f.GetSlice())

	require.NoError(t, f.Append(" :8080 "))
	assert.Equal(t, []string{":8080"}, f.GetSlice())

	require.NoError(t, f.Replace([]string{" :9090 ", "unix:/tmp/a,b.sock"}))
	assert.Equal(t, []string{":9090", "unix:/tmp/a,b.sock"}, f.GetSlice())
	assert.Equal(t, []string{":9090", "unix:/tmp/a,b.sock"}, target)
}

func TestStringSliceFlag_GetStringArray(t *testing.T) {
	t.Run("empty default does not panic", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		var target []string
		fs.Var(NewStringSliceFlag(&target, nil), "listen", "usage")
		got, err := fs.GetStringArray("listen")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("round-trips short, trimmed, and comma-containing values", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		var target []string
		fs.Var(NewStringSliceFlag(&target, nil), "listen", "usage")
		require.NoError(t, fs.Parse([]string{"--listen", "a", "--listen", " :8080 ", "--listen", "unix:/tmp/a,b.sock"}))
		got, err := fs.GetStringArray("listen")
		require.NoError(t, err)
		assert.Equal(t, []string{"a", ":8080", "unix:/tmp/a,b.sock"}, got)
	})
}
