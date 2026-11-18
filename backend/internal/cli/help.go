package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	cobra.AddTemplateFunc("flagUsages", flagUsages)
	cobra.AddTemplateFunc("argUsages", argUsages)
	// A group lists its commands in the order that it declares them: the
	// order a reader needs, not the alphabet.
	cobra.EnableCommandSorting = false
}

// flagUsages formats the flags of fs for a help text. Each flag gets one
// line: its names, the type of its value, and FlagInfo.Text. The help flag
// is left out, because every command has it.
func flagUsages(fs *pflag.FlagSet) string {
	type row struct{ names, usage string }
	var rows []row
	width := 0
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		info := describeFlag(f, false)
		names := info.Names()
		if info.Shorthand == "" {
			// Align the long names under those of the flags with a
			// shorthand, as "-q, " is four characters wide.
			names = "    " + names
		}
		rows = append(rows, row{names: names, usage: info.Text()})
		width = max(width, utf8.RuneCountInString(names))
	})
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s%s  %s\n", r.names, strings.Repeat(" ", width-utf8.RuneCountInString(r.names)), r.usage)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// argUsages formats the described positional arguments of cmd for a help
// text, one line each. An argument with no description is left out: the
// usage line already states it.
func argUsages(cmd *cobra.Command) string {
	type row struct{ name, help string }
	var rows []row
	width := 0
	for _, spec := range ArgsOf(cmd) {
		if spec.Help == "" {
			continue
		}
		name := "<" + spec.Name + ">"
		if !spec.Required {
			name = "[" + spec.Name + "]"
		}
		rows = append(rows, row{name: name, help: spec.Help})
		width = max(width, utf8.RuneCountInString(name))
	}
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s%s  %s\n", r.name, strings.Repeat(" ", width-utf8.RuneCountInString(r.name)), r.help)
	}
	return strings.TrimRight(sb.String(), "\n")
}
