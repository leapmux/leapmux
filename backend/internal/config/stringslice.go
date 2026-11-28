package config

import (
	"encoding/csv"
	"strings"

	"github.com/spf13/pflag"
)

// StringSliceFlag is a repeatable string flag. Each Set call appends one
// value, taking the whole token except its leading and trailing spaces, so a
// value that contains the separator the environment variable uses stays
// whole: `--listen "unix:/tmp/a,b.sock"` is one address, not two.
//
// String formats the values as bracketed CSV (`[a,b]`, or `[]` when empty),
// matching pflag's stringArrayValue so FlagSet.GetStringArray round-trips
// without panicking or stripping characters. Get and GetSlice return the
// slice itself, which FlagProvider stores for a list-typed field.
type StringSliceFlag struct {
	target *[]string
	def    []string
	// set reports whether any Set call arrived, so String can tell "no value"
	// (return the default) from "an empty value was given".
	set bool
}

// NewStringSliceFlag returns a repeatable flag whose values append to target.
// def is what String reports before the first Set call.
func NewStringSliceFlag(target *[]string, def []string) *StringSliceFlag {
	return &StringSliceFlag{target: target, def: def}
}

// Set appends one value. It never splits -- the separator is an environment
// variable convention, not a flag one -- but it does trim the surrounding
// spaces, so `--listen " :4327"` and `LEAPMUX_HUB_LISTEN=":4327"` name the
// same address. The interior of a value is untouched: a socket path that
// holds a space keeps it.
func (f *StringSliceFlag) Set(s string) error {
	f.set = true
	*f.target = append(*f.target, strings.TrimSpace(s))
	return nil
}

// String formats the values as bracketed CSV, matching pflag's stringArray
// contract so FlagSet.GetStringArray parses them cleanly.
func (f *StringSliceFlag) String() string {
	vals := f.Get()
	if len(vals) == 0 {
		return "[]"
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(vals)
	w.Flush()
	return "[" + strings.TrimSuffix(b.String(), "\n") + "]"
}

// Type identifies the value in help. "stringArray" is pflag's own label for a
// repeatable string, so the help marks the flag repeatable.
func (f *StringSliceFlag) Type() string { return "stringArray" }

// Append adds one value, satisfying pflag.SliceValue.
func (f *StringSliceFlag) Append(s string) error { return f.Set(s) }

// Replace overwrites the current values with vals, satisfying pflag.SliceValue.
func (f *StringSliceFlag) Replace(vals []string) error {
	f.set = true
	next := make([]string, len(vals))
	for i, v := range vals {
		next[i] = strings.TrimSpace(v)
	}
	*f.target = next
	return nil
}

// GetSlice returns the values as a slice, satisfying pflag.SliceValue.
func (f *StringSliceFlag) GetSlice() []string { return f.Get() }

var _ pflag.SliceValue = (*StringSliceFlag)(nil)

// Get returns the values as a slice, for a provider that stores a list.
func (f *StringSliceFlag) Get() []string {
	if !f.set {
		return f.def
	}
	return *f.target
}

// SplitListValue splits an environment variable's value into list entries.
// The separator is a comma, which a socket path almost never holds; a path
// that does goes in the config file or a repeated flag, where it needs no
// separator at all. Each piece loses its leading and trailing spaces and
// keeps the rest, so `" foo , bar baz "` is the list ["foo", "bar baz"].
func SplitListValue(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}
