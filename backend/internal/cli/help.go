package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	cobra.AddTemplateFunc("inheritedFlagUsages", inheritedFlagUsages)
	cobra.AddTemplateFunc("flagSections", flagSections)
	cobra.AddTemplateFunc("argUsages", argUsages)
	cobra.AddTemplateFunc("commandSections", commandSections)
	cobra.AddTemplateFunc("nextHints", nextHints)
	cobra.AddTemplateFunc("footerOf", footerOf)
	cobra.AddTemplateFunc("abbreviationNoteOf", abbreviationNoteOf)
	// A group lists its commands in the order that it declares them: the
	// order a reader needs, not the alphabet.
	cobra.EnableCommandSorting = false
}

// inheritedFlagUsages formats the inherited flags of cmd, omitting any flag
// that cmd (or an ancestor) marked disallowed.
func inheritedFlagUsages(cmd *cobra.Command) string {
	rows, width := flagRowsFiltered(cmd.InheritedFlags(), true, func(f *pflag.Flag) bool {
		return IsInheritedFlagDisallowed(cmd, f.Name)
	})
	var sb strings.Builder
	writeFlagRows(&sb, rows, width)
	return sb.String()
}

// flagSections formats the flags of fs as the sections of a help text: the
// flags of no group under "Flags:", then each group under its own name, in
// the order of its first flag. Every section aligns to one column, so the
// sections read as one table. Each section starts with a blank line; a set
// with no flag to show prints nothing.
func flagSections(fs *pflag.FlagSet) string {
	rows, width := flagRowsFiltered(fs, false, nil)
	var order []string
	byGroup := map[string][]flagRow{}
	for _, r := range rows {
		if _, seen := byGroup[r.group]; !seen && r.group != "" {
			order = append(order, r.group)
		}
		byGroup[r.group] = append(byGroup[r.group], r)
	}
	var sb strings.Builder
	write := func(header string, rows []flagRow) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n\n%s:\n", header)
		writeFlagRows(&sb, rows, width)
	}
	write("Flags", byGroup[""])
	for _, group := range order {
		write(group, byGroup[group])
	}
	return sb.String()
}

// flagRow is one flag in a help text.
type flagRow struct{ names, usage, group string }

func flagRowsFiltered(fs *pflag.FlagSet, global bool, skip func(*pflag.Flag) bool) ([]flagRow, int) {
	var rows []flagRow
	width := 0
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || (skip != nil && skip(f)) {
			return
		}
		info := describeFlag(f, global)
		names := info.Names()
		if info.Shorthand == "" {
			// Align the long names under those of the flags with a
			// shorthand, as "-q, " is four characters wide.
			names = "    " + names
		}
		rows = append(rows, flagRow{names: names, usage: info.Text(), group: info.Group})
		width = max(width, utf8.RuneCountInString(names))
	})
	return rows, width
}

func writeFlagRows(sb *strings.Builder, rows []flagRow, width int) {
	for i, r := range rows {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(sb, "  %s%s  %s", r.names, strings.Repeat(" ", width-utf8.RuneCountInString(r.names)), r.usage)
	}
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
		name := spec.Usage()
		rows = append(rows, row{name: name, help: spec.Help})
		width = max(width, utf8.RuneCountInString(name))
	}
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s%s  %s\n", r.name, strings.Repeat(" ", width-utf8.RuneCountInString(r.name)), r.help)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// commandSections renders the subcommands of cmd either as task-grouped
// sections (when any child carries a command group via SetCommandGroup) or as
// a single "Commands:" section. All sections share one aligned column width.
func commandSections(cmd *cobra.Command) string {
	type entry struct {
		name, short, group string
	}
	var entries []entry
	hasGroups := false
	width := 0
	for _, child := range cmd.Commands() {
		if !child.IsAvailableCommand() && child.Name() != "help" {
			continue
		}
		grp := CommandGroupOf(child)
		if grp != "" {
			hasGroups = true
		}
		entries = append(entries, entry{name: child.Name(), short: child.Short, group: grp})
		width = max(width, len(child.Name()))
	}
	if len(entries) == 0 {
		return ""
	}
	col := width + 2
	var sb strings.Builder
	writeRow := func(e entry) {
		fmt.Fprintf(&sb, "  %s%s%s\n", e.name, strings.Repeat(" ", col-len(e.name)), e.short)
	}
	if !hasGroups {
		sb.WriteString("\n\nCommands:\n")
		for _, e := range entries {
			writeRow(e)
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	var order []string
	byGroup := map[string][]entry{}
	for _, e := range entries {
		grp := e.group
		if grp == "" {
			grp = "Other"
		}
		if _, seen := byGroup[grp]; !seen {
			order = append(order, grp)
		}
		byGroup[grp] = append(byGroup[grp], e)
	}
	for i, grp := range order {
		if i == 0 {
			fmt.Fprintf(&sb, "\n\n%s:\n", grp)
		} else {
			fmt.Fprintf(&sb, "\n%s:\n", grp)
		}
		for _, e := range byGroup[grp] {
			writeRow(e)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// nextHints formats the follow-up workflow commands declared in Leaf.Next.
func nextHints(cmd *cobra.Command) string {
	next := NextOf(cmd)
	if len(next) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, line := range next {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("  ")
		sb.WriteString(line)
	}
	return sb.String()
}
