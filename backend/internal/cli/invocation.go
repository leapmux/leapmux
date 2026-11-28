package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ArgSpec is one positional argument that a Use line declares.
type ArgSpec struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Variadic bool   `json:"variadic"`
	// Help is one sentence about the values that the argument accepts, from
	// Leaf.Args or DescribeArgs. ParseArgSpecs leaves it empty.
	Help string `json:"help,omitempty"`
}

// Usage formats the argument as it appears in a usage line and help tables:
// <name> when required, [name] when optional, with a trailing ... when variadic.
func (s ArgSpec) Usage() string {
	out := "[" + s.Name + "]"
	if s.Required {
		out = "<" + s.Name + ">"
	}
	if s.Variadic {
		out += "..."
	}
	return out
}

// ParseArgSpecs reads the positional arguments that a Use line declares.
func ParseArgSpecs(use string) []ArgSpec {
	fields := strings.Fields(use)
	if len(fields) < 2 {
		return nil
	}
	var out []ArgSpec
	for _, f := range fields[1:] {
		spec := ArgSpec{}
		// The repetition mark stands outside the brackets ("<agent>...") or
		// inside them ("[text...]"); both forms are common.
		if strings.HasSuffix(f, "...") {
			spec.Variadic = true
			f = strings.TrimSuffix(f, "...")
		}
		switch {
		case strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">"):
			spec.Required = true
			spec.Name = f[1 : len(f)-1]
		case strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]"):
			spec.Name = f[1 : len(f)-1]
		default:
			continue
		}
		if name, ok := strings.CutSuffix(spec.Name, "..."); ok {
			spec.Variadic = true
			spec.Name = name
		}
		out = append(out, spec)
	}
	return out
}

// specAt returns the declaration of the positional argument at index i.
func specAt(specs []ArgSpec, i int) (ArgSpec, bool) {
	if i < len(specs) {
		return specs[i], true
	}
	if len(specs) > 0 && specs[len(specs)-1].Variadic {
		return specs[len(specs)-1], true
	}
	return ArgSpec{}, false
}

// argsValidator checks the count of positional arguments against specs.
func argsValidator(specs []ArgSpec) cobra.PositionalArgs {
	minArgs, maxArgs := 0, len(specs)
	for _, s := range specs {
		if s.Required {
			minArgs++
		}
		if s.Variadic {
			maxArgs = -1
		}
	}
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < minArgs {
			return Usagef("missing argument <%s>", specs[len(args)].Name).
				WithHint(`run "%s --help" for usage`, cmd.CommandPath())
		}
		if maxArgs >= 0 && len(args) > maxArgs {
			return Usagef("unexpected argument %q", args[maxArgs]).
				WithHint(`run "%s --help" for usage`, cmd.CommandPath())
		}
		return nil
	}
}

// Invocation is what a leaf's Run receives.
type Invocation struct {
	Context context.Context
	Command *cobra.Command
	Args    []string
	Printer *Printer
	Stdin   io.Reader
	specs   []ArgSpec
}

// Arg returns the positional argument that the Use line declares as name,
// or "" when the command line omitted it. For a variadic argument it
// returns the first value; use Variadic for all of them.
func (inv *Invocation) Arg(name string) string {
	for i, spec := range inv.specs {
		if spec.Name == name {
			if i < len(inv.Args) {
				return inv.Args[i]
			}
			return ""
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// HasArg reports whether the command line supplied the positional argument
// name.
func (inv *Invocation) HasArg(name string) bool {
	for i, spec := range inv.specs {
		if spec.Name == name {
			return i < len(inv.Args)
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// Variadic returns every value of the variadic argument name.
func (inv *Invocation) Variadic(name string) []string {
	for i, spec := range inv.specs {
		if spec.Name == name {
			if !spec.Variadic {
				panic(fmt.Sprintf("cli: argument %q of %s is not variadic", name, inv.Command.CommandPath()))
			}
			if i < len(inv.Args) {
				return inv.Args[i:]
			}
			return nil
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// Changed reports whether the command line set the flag name.
func (inv *Invocation) Changed(name string) bool {
	f := inv.Command.Flags().Lookup(name)
	return f != nil && f.Changed
}

// StringArray returns each value of the repeated flag name, exactly as the
// command line gave it, or an empty list when it gave none. The flag must be
// a StringArray flag.
//
// pflag's GetStringArray is impossible here: it reads the values back from
// their CSV form, which states one empty value as "[]", so `--x ""` would
// come back as no value at all, and a script whose variable is unset would
// run as if it had omitted the flag instead of failing.
func (inv *Invocation) StringArray(name string) []string {
	flag := inv.Command.Flags().Lookup(name)
	if flag == nil {
		panic(fmt.Sprintf("cli: %s declares no flag %q", inv.Command.CommandPath(), name))
	}
	values, ok := flag.Value.(pflag.SliceValue)
	if !ok {
		panic(fmt.Sprintf("cli: flag %q of %s is not a list", name, inv.Command.CommandPath()))
	}
	return append([]string{}, values.GetSlice()...)
}

// durationValuer is implemented by custom duration flags (such as
// internal/config.DurationFlag) whose String() emits units that stdlib
// time.ParseDuration does not accept (e.g. "7d").
type durationValuer interface {
	Get() time.Duration
}

// Duration returns the parsed duration value of flag name. It reads custom
// duration flags via Get() directly before falling back to pflag's GetDuration,
// avoiding pflag's stdlib time.ParseDuration re-parse on extended units like "d".
func (inv *Invocation) Duration(name string) time.Duration {
	flag := inv.Command.Flags().Lookup(name)
	if flag == nil {
		panic(fmt.Sprintf("cli: %s declares no flag %q", inv.Command.CommandPath(), name))
	}
	if dv, ok := flag.Value.(durationValuer); ok {
		return dv.Get()
	}
	d, err := inv.Command.Flags().GetDuration(name)
	if err != nil {
		panic(fmt.Sprintf("cli: flag %q of %s is not a duration: %v", name, inv.Command.CommandPath(), err))
	}
	return d
}
