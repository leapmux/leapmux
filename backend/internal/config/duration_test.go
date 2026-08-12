package config

import (
	"flag"
	"math"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		// A bare number is the seconds count every one of these keys carried
		// before it took a unit.
		{"0", 0},
		{"1", time.Second},
		{"3600", time.Hour},
		{"691200", 8 * 24 * time.Hour},
		// The units time.ParseDuration already knows.
		{"500ms", 500 * time.Millisecond},
		{"30s", 30 * time.Second},
		{"90m", 90 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"1.5h", 90 * time.Minute},
		{"250us", 250 * time.Microsecond},
		{"250µs", 250 * time.Microsecond},
		{"100ns", 100 * time.Nanosecond},
		// The units this package adds.
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"1.5d", 36 * time.Hour},
		// Several components, in either order, mixing the added units with the
		// standard ones.
		{"2w3d", 17 * 24 * time.Hour},
		{"1d12h", 36 * time.Hour},
		{"1w2d3h4m5s", 9*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
		{"12h1d", 36 * time.Hour},
		// Signs and surrounding space.
		{"+30s", 30 * time.Second},
		{"-30s", -30 * time.Second},
		{"-1d", -24 * time.Hour},
		{"  7d  ", 7 * 24 * time.Hour},
		{"\t3600\n", time.Hour},
		{"-3600", -time.Hour},
		// The boundary of the representable range, from both units that reach it.
		{"9223372036s", 9223372036 * time.Second},
		{"106751d", 106751 * 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseDuration(c.in)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestParseDurationRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"space only", "   "},
		{"sign only", "-"},
		{"plus only", "+"},
		{"no number", "d"},
		{"letters", "abc"},
		{"unknown unit", "30x"},
		{"unknown long unit", "5min"},
		{"two decimal points", "1..5s"},
		{"trailing bare number", "1d30"},
		{"leading bare number", "30 1d"},
		{"space before the unit", "30 d"},
		{"unit before number", "d30"},
		{"seconds overflow", "9223372037"},
		{"seconds unit overflow", "9223372037s"},
		{"days overflow", "106752d"},
		{"weeks overflow", "15251w"},
		{"sum overflow", "106751d24h"},
		{"huge integer", "99999999999999999999999"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseDuration(c.in)
			require.Error(t, err)
		})
	}
}

// TestParseDurationNeverOverflows is the property that makes an unbounded
// duration option safe: no input reaches a consumer as a wrapped negative or a
// tiny positive. Before this parser the seconds count was multiplied by
// time.Second with no check, so an operator's typing error became a session
// that expired before the response left the Hub.
func TestParseDurationNeverOverflows(t *testing.T) {
	for _, in := range []string{
		"9223372037",
		"9223372037s",
		"153722868m",
		"2562048h",
		"106752d",
		"15251w",
		"106751d23h60m",
		"1.0e19",
	} {
		t.Run(in, func(t *testing.T) {
			got, err := ParseDuration(in)
			require.Error(t, err, "want a range error, got %s", got)
			assert.Zero(t, got)
		})
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0"},
		{time.Second, "1s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{time.Hour, "1h"},
		{24 * time.Hour, "1d"},
		// Seven days is "7d" and never "1w": a lifetime reads more plainly in
		// days, and both spellings parse.
		{7 * 24 * time.Hour, "7d"},
		{14 * 24 * time.Hour, "14d"},
		{90 * time.Minute, "90m"},
		{-30 * time.Second, "-30s"},
		{-7 * 24 * time.Hour, "-7d"},
		{500 * time.Millisecond, "500ms"},
		{time.Second + 500*time.Millisecond, "1.5s"},
		{math.MinInt64, time.Duration(math.MinInt64).String()},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, FormatDuration(c.in))
		})
	}
}

// TestFormatDurationRoundTrips pins the property the CLI depends on: a flag
// reports its value with FormatDuration and the loader reads that text back
// with ParseDuration, so an inexact spelling would change the loaded value.
func TestFormatDurationRoundTrips(t *testing.T) {
	for _, d := range []time.Duration{
		0,
		time.Nanosecond,
		500 * time.Millisecond,
		time.Second,
		90 * time.Second,
		10 * time.Minute,
		time.Hour,
		36 * time.Hour,
		7 * 24 * time.Hour,
		365 * 24 * time.Hour,
		-42 * time.Second,
		time.Duration(math.MaxInt64),
	} {
		t.Run(FormatDuration(d), func(t *testing.T) {
			got, err := ParseDuration(FormatDuration(d))
			require.NoError(t, err)
			assert.Equal(t, d, got)
		})
	}
}

func TestDurationFlag(t *testing.T) {
	t.Run("reports the default before Parse", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		var d time.Duration
		fs.Var(NewDurationFlag(&d, 7*24*time.Hour), "session-duration", "usage")
		assert.Equal(t, 7*24*time.Hour, d)
		assert.Equal(t, "7d", fs.Lookup("session-duration").DefValue)
	})

	t.Run("accepts every spelling the config file accepts", func(t *testing.T) {
		for _, c := range []struct {
			arg  string
			want time.Duration
		}{
			{"1h", time.Hour},
			{"3600", time.Hour},
			{"2d", 48 * time.Hour},
			{"1w", 7 * 24 * time.Hour},
		} {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			var d time.Duration
			fs.Var(NewDurationFlag(&d, time.Minute), "session-duration", "usage")
			require.NoError(t, fs.Parse([]string{"-session-duration", c.arg}))
			assert.Equal(t, c.want, d, "arg %q", c.arg)
		}
	})

	t.Run("rejects a value the parser refuses", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(nopWriter{})
		var d time.Duration
		fs.Var(NewDurationFlag(&d, time.Minute), "session-duration", "usage")
		require.Error(t, fs.Parse([]string{"-session-duration", "30x"}))
	})

	// FlagProvider hands the loader fl.Value.String(), so a flag that a user set
	// has to survive the format-then-parse trip unchanged.
	t.Run("round trips through the flag provider", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		var d time.Duration
		fs.Var(NewDurationFlag(&d, time.Minute), "session-duration", "usage")
		require.NoError(t, fs.Parse([]string{"-session-duration", "90"}))

		out, err := NewFlagProvider(fs, map[string]string{"session-duration": "session_duration"}).Read()
		require.NoError(t, err)
		got, err := ParseDuration(out["session_duration"].(string))
		require.NoError(t, err)
		assert.Equal(t, 90*time.Second, got)
	})

	// The flag package builds a zero value by reflection to decide whether a
	// default belongs in the help text, so String must answer without a target.
	t.Run("String answers on a zero value", func(t *testing.T) {
		assert.Equal(t, "0", (&DurationFlag{}).String())
		assert.Equal(t, "0", (*DurationFlag)(nil).String())
		assert.Equal(t, time.Duration(0), (&DurationFlag{}).Get())
	})
}

func TestUnmarshalDuration(t *testing.T) {
	type cfg struct {
		SessionDuration time.Duration `koanf:"session_duration"`
		APITimeout      time.Duration `koanf:"api_timeout"`
		MaxConns        int           `koanf:"max_conns"`
		Enabled         bool          `koanf:"enabled"`
	}

	t.Run("reads a duration from every source shape", func(t *testing.T) {
		k := koanf.New(".")
		require.NoError(t, k.Load(confmap.Provider(map[string]any{
			// A typed default, as the defaults map supplies it.
			"session_duration": 7 * 24 * time.Hour,
			// A string, as a config file, an environment variable, and a CLI
			// flag all supply it.
			"api_timeout": "90s",
			// Weakly typed input still has to reach a non-duration field.
			"max_conns": "25",
			"enabled":   "true",
		}, "."), nil))

		var got cfg
		require.NoError(t, Unmarshal(k, &got))
		assert.Equal(t, 7*24*time.Hour, got.SessionDuration)
		assert.Equal(t, 90*time.Second, got.APITimeout)
		assert.Equal(t, 25, got.MaxConns)
		assert.True(t, got.Enabled)
	})

	// The reason this package replaces koanf's own duration hook: koanf composes
	// mapstructure.StringToTimeDurationHookFunc, which calls time.ParseDuration
	// and so rejects both of these.
	t.Run("reads the spellings time.ParseDuration rejects", func(t *testing.T) {
		for _, c := range []struct {
			text string
			want time.Duration
		}{
			{"7d", 7 * 24 * time.Hour},
			{"1w", 7 * 24 * time.Hour},
			{"3600", time.Hour},
		} {
			k := koanf.New(".")
			require.NoError(t, k.Load(confmap.Provider(map[string]any{"session_duration": c.text}, "."), nil))
			var got cfg
			require.NoError(t, Unmarshal(k, &got), "text %q", c.text)
			assert.Equal(t, c.want, got.SessionDuration, "text %q", c.text)
		}
	})

	t.Run("fails loudly on a value nobody meant", func(t *testing.T) {
		k := koanf.New(".")
		require.NoError(t, k.Load(confmap.Provider(map[string]any{"session_duration": "30x"}, "."), nil))
		var got cfg
		require.Error(t, Unmarshal(k, &got))
	})
}

// nopWriter discards a FlagSet's error output, so a test for a rejected flag
// does not print a usage message.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
