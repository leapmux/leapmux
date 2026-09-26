package config

import "strings"

// StringSliceFlag is a repeatable string flag. Each Set call appends one value
// verbatim, so a value that contains the separator the environment variable
// uses stays whole: `--listen "unix:/tmp/a,b.sock"` is one address, not two.
//
// String joins the values with commas, which is the spelling the flag's help
// line and FlagProvider.Value.String() read. Get returns the slice itself,
// which is what FlagProvider stores for a list-typed field -- a field of type
// []string cannot be unmarshalled from one joined string.
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

// Set appends one value. It never splits: the separator is an environment
// variable convention, not a flag one.
func (f *StringSliceFlag) Set(s string) error {
	f.set = true
	*f.target = append(*f.target, s)
	return nil
}

// String is the comma-joined values, or the default before the first Set.
func (f *StringSliceFlag) String() string {
	if !f.set {
		return strings.Join(f.def, ",")
	}
	return strings.Join(*f.target, ",")
}

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
// separator at all.
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
