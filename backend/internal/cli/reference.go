package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// SharedGlobalFlags returns the non-hidden persistent flags declared on root
// that apply to at least one visible command whose top-level command name is
// not in exclude.
func SharedGlobalFlags(root *cobra.Command, exclude map[string]bool) []FlagInfo {
	if root == nil {
		return nil
	}
	var included []*cobra.Command
	for _, top := range root.Commands() {
		if top.Hidden || top.Name() == "help" || exclude[top.Name()] {
			continue
		}
		walk(top, func(cmd *cobra.Command) {
			if cmd.Hidden || cmd.Name() == "help" {
				return
			}
			included = append(included, cmd)
		})
	}
	if len(included) == 0 {
		return nil
	}
	var global []FlagInfo
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		allowed := false
		for _, c := range included {
			if c.LocalFlags().Lookup(f.Name) == nil && !IsInheritedFlagDisallowed(c, f.Name) {
				allowed = true
				break
			}
		}
		if !allowed {
			return
		}
		global = append(global, describeFlag(f, true))
	})
	return global
}

// WriteSharedFlags writes the "## Flags of every command" section and its
// Markdown table for flags. When intro is non-empty, it prints before the
// table. When flags is empty, WriteSharedFlags writes nothing.
func WriteSharedFlags(w io.Writer, flags []FlagInfo, intro string) error {
	if len(flags) == 0 {
		return nil
	}
	rw := &referenceWriter{w: w}
	rw.sharedFlags(flags, intro)
	return rw.err
}

// WriteReferenceForGroups writes the Markdown reference for the top-level
// commands listed in topLevelNames, in that order, preserving each top-level
// command's subtree order from tree. Flags present in globalFlags are omitted
// from each leaf's flag table so callers can document them once on a shared
// overview page.
func WriteReferenceForGroups(w io.Writer, tree CommandTree, topLevelNames []string, globalFlags []FlagInfo) error {
	if len(topLevelNames) == 0 {
		return fmt.Errorf("topLevelNames must not be empty")
	}
	byTop := map[string][]CommandInfo{}
	for _, c := range tree.Commands {
		top, _, _ := strings.Cut(c.Path, " ")
		byTop[top] = append(byTop[top], c)
	}
	seen := make(map[string]bool, len(topLevelNames))
	var selected []CommandInfo
	for _, name := range topLevelNames {
		if seen[name] {
			return fmt.Errorf("duplicate top-level command %q", name)
		}
		seen[name] = true
		cmds := byTop[name]
		if len(cmds) == 0 {
			return fmt.Errorf("the tree holds no top-level command %q", name)
		}
		selected = append(selected, cmds...)
	}
	globalNames := flagNameSet(globalFlags)
	rw := &referenceWriter{w: w}
	common := collectCommonEntries(selected, globalNames)
	if len(common.ordered) > 0 {
		rw.commonEntries(common.ordered)
	}
	rw.common = common.lookup
	rw.commands(selected, globalNames)
	return rw.err
}

func flagNameSet(flags []FlagInfo) map[string]bool {
	names := make(map[string]bool, len(flags))
	for _, f := range flags {
		names[f.Name] = true
	}
	return names
}

// commandOf is the command line that runs c, without its arguments and its
// flags: the usage line up to the first "<" or "[".
func commandOf(c CommandInfo) string {
	if i := strings.IndexAny(c.Usage, "<["); i > 0 {
		return strings.TrimSpace(c.Usage[:i])
	}
	return c.Usage
}

type commonEntry struct {
	label string
	help  string
}

type commonEntrySet struct {
	ordered []commonEntry
	lookup  map[string]string // "arg:<name>" or "flag:<names>" -> exact help string lifted
}

const (
	commonMinOccurrences = 3
	commonMinHelpLen     = 45
)

// collectCommonEntries lifts long argument and flag descriptions that repeat
// verbatim at least commonMinOccurrences times on one reference page into a
// single shared table at the top of that page.
func collectCommonEntries(cmds []CommandInfo, globalNames map[string]bool) commonEntrySet {
	type countKey struct {
		slot string
		help string
	}
	counts := map[countKey]int{}
	labels := map[countKey]string{}
	var order []countKey

	record := func(slot, label, fallbackLabel, help string) {
		if len(help) < commonMinHelpLen {
			return
		}
		k := countKey{slot: slot, help: help}
		if counts[k] == 0 {
			order = append(order, k)
			labels[k] = label
		} else if labels[k] != label {
			labels[k] = fallbackLabel
		}
		counts[k]++
	}

	for _, c := range cmds {
		if c.Group {
			continue
		}
		for _, a := range c.Args {
			if a.Help != "" {
				record("arg:"+a.Name, a.Usage(), "<"+a.Name+">", a.Help)
			}
		}
		for _, f := range c.Flags {
			if f.Global && globalNames[f.Name] {
				continue
			}
			names := f.Names()
			record("flag:"+names, names, names, f.Text())
		}
	}

	// Only lift one canonical help per slot (the most frequent qualifying text).
	bestForSlot := map[string]countKey{}
	for _, k := range order {
		if counts[k] < commonMinOccurrences {
			continue
		}
		prev, exists := bestForSlot[k.slot]
		if !exists || counts[k] > counts[prev] {
			bestForSlot[k.slot] = k
		}
	}

	out := commonEntrySet{lookup: map[string]string{}}
	seenSlot := map[string]bool{}
	for _, k := range order {
		best, ok := bestForSlot[k.slot]
		if !ok || seenSlot[k.slot] || best != k {
			continue
		}
		seenSlot[k.slot] = true
		out.ordered = append(out.ordered, commonEntry{label: labels[k], help: k.help})
		out.lookup[k.slot] = k.help
	}
	return out
}

type referenceWriter struct {
	w      io.Writer
	err    error
	common map[string]string
}

func (rw *referenceWriter) commonEntries(entries []commonEntry) {
	rw.printf("## Common arguments and flags\n\n")
	rw.printf("These selectors and flags repeat across commands on this page.\n\n")
	rw.printf("| Selector / Flag | Description |\n|---|---|\n")
	for _, e := range entries {
		rw.printf("| `%s` | %s |\n", e.label, escapeCell(e.help))
	}
	rw.printf("\n")
}

func (rw *referenceWriter) printf(format string, args ...any) {
	if rw.err != nil {
		return
	}
	_, rw.err = fmt.Fprintf(rw.w, format, args...)
}

// headingLevel returns the Markdown heading prefix for c.Path so top-level
// groups and top-level leaves share `##`, nested subgroups step down to `###`
// / `####`, and leaves nest directly under their parent group.
func headingLevel(path string) string {
	depth := len(strings.Fields(path))
	if depth <= 1 {
		return "##"
	}
	if depth == 2 {
		return "###"
	}
	if depth == 3 {
		return "####"
	}
	return "#####"
}

func (rw *referenceWriter) group(c CommandInfo) {
	rw.printf("%s `%s`\n\n%s.\n\n", headingLevel(c.Path), commandOf(c), escapeProse(c.Summary))
	if len(c.Aliases) > 0 {
		rw.printf("Aliases: %s.\n\n", formatCodeList(c.Aliases))
	}
}

func formatCodeList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}

func (rw *referenceWriter) leaf(c CommandInfo, globalNames map[string]bool) {
	rw.printf("%s `%s`\n\n", headingLevel(c.Path), commandOf(c))
	text := c.Summary + "."
	if c.Description != "" {
		text = c.Description
	}
	rw.printf("%s\n\n", formatProse(text))
	var meta []string
	if len(c.Aliases) > 0 {
		meta = append(meta, "Aliases: "+formatCodeList(c.Aliases))
	}
	if c.Mutates {
		meta = append(meta, "Mutates state")
	}
	if c.QuietUnsupported {
		meta = append(meta, "Does not support `-q` / `--quiet`")
	}
	if len(meta) > 0 {
		rw.printf("_%s._\n\n", strings.Join(meta, " · "))
	}
	rw.printf("```text\n%s\n```\n\n", c.Usage)
	var ownArgs []ArgSpec
	var commonLabels []string
	for _, a := range c.Args {
		if a.Help == "" {
			continue
		}
		if rw.isCommonArg(a) {
			commonLabels = append(commonLabels, a.Usage())
			continue
		}
		ownArgs = append(ownArgs, a)
	}
	var ownFlags []FlagInfo
	for _, f := range c.Flags {
		if f.Global && globalNames[f.Name] {
			continue
		}
		if rw.isCommonFlag(f) {
			commonLabels = append(commonLabels, formatFlagLabel(f))
			continue
		}
		ownFlags = append(ownFlags, f)
	}
	if len(ownArgs) > 0 {
		rw.printf("| Argument | Description |\n|---|---|\n")
		for _, a := range ownArgs {
			rw.printf("| `%s` | %s |\n", a.Usage(), escapeCell(a.Help))
		}
		rw.printf("\n")
	}
	if len(ownFlags) > 0 {
		rw.flagTable(ownFlags)
	}
	if len(commonLabels) > 0 {
		rw.printf("_Common: %s (see [common arguments and flags](#common-arguments-and-flags))._\n\n", formatCodeList(commonLabels))
	}
	if len(c.Examples) > 0 {
		rw.printf("```sh\n")
		for i, ex := range c.Examples {
			if i > 0 && ex.Comment != "" {
				rw.printf("\n")
			}
			if ex.Comment != "" {
				rw.printf("# %s\n", ex.Comment)
			}
			rw.printf("%s\n", ex.Command)
		}
		rw.printf("```\n\n")
	}
}

func (rw *referenceWriter) sharedFlags(flags []FlagInfo, intro string) {
	rw.printf("## Flags of every command\n\n")
	if intro != "" {
		rw.printf("%s\n\n", intro)
	}
	rw.flagTable(flags)
}

func (rw *referenceWriter) commands(cmds []CommandInfo, globalNames map[string]bool) {
	for _, c := range cmds {
		if c.Group {
			rw.group(c)
			continue
		}
		rw.leaf(c, globalNames)
	}
}

func (rw *referenceWriter) isCommonArg(a ArgSpec) bool {
	help, ok := rw.common["arg:"+a.Name]
	return ok && help == a.Help
}

func (rw *referenceWriter) isCommonFlag(f FlagInfo) bool {
	help, ok := rw.common["flag:"+f.Names()]
	return ok && help == f.Text()
}

func formatFlagLabel(f FlagInfo) string {
	if f.Shorthand != "" {
		return "-" + f.Shorthand + ", --" + f.Name
	}
	return "--" + f.Name
}

func (rw *referenceWriter) flagTable(flags []FlagInfo) {
	rw.printf("| Flag | Description |\n|---|---|\n")
	for _, f := range flags {
		rw.printf("| `%s` | %s |\n", f.Names(), escapeCell(f.Text()))
	}
	rw.printf("\n")
}

// formatProse reflows terminal-wrapped --help descriptions into clean Markdown:
// consecutive plain prose lines join into one paragraph, bullet lists keep one
// item per line, and a plain paragraph immediately following a bullet item gets
// a separating blank line so CommonMark does not fold it into the last bullet.
func formatProse(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	var blocks []string
	var cur []string
	inBullets := false

	flush := func() {
		if len(cur) == 0 {
			return
		}
		if inBullets {
			blocks = append(blocks, strings.Join(cur, "\n"))
		} else {
			blocks = append(blocks, strings.Join(cur, " "))
		}
		cur = nil
		inBullets = false
	}

	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			flush()
			continue
		}
		isBullet := strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")
		isIndented := strings.HasPrefix(raw, "  ") || strings.HasPrefix(raw, "\t")
		switch {
		case isBullet:
			if !inBullets {
				flush()
				inBullets = true
			}
			cur = append(cur, trimmed)
		case inBullets && isIndented:
			cur[len(cur)-1] += " " + trimmed
		case inBullets:
			// Separate a non-indented prose line after bullets so CommonMark
			// does not treat it as a lazy continuation of the final bullet.
			flush()
			cur = append(cur, trimmed)
		default:
			cur = append(cur, trimmed)
		}
	}
	flush()
	return backtickFlags(escapeProse(strings.Join(blocks, "\n\n")))
}

var (
	proseEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	cellEscaper  = strings.NewReplacer("|", `\|`, "\n", " ")
)

// escapeProse escapes the characters that a Markdown renderer with raw HTML
// turned on reads as markup: an argument such as <agent> would print as an
// empty tag.
func escapeProse(s string) string {
	return proseEscaper.Replace(s)
}

// escapeCell escapes text for one cell of a Markdown table, where a pipe
// ends the cell and a line break ends the row.
func escapeCell(s string) string {
	return backtickFlags(cellEscaper.Replace(proseEscaper.Replace(s)))
}

// backtickFlags wraps bare long CLI flag tokens (such as --wait or
// --output-schema) in Markdown inline code spans while leaving existing
// backtick code spans, URLs, paths, standalone --, and flag=value forms intact.
func backtickFlags(s string) string {
	if !strings.Contains(s, "--") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	i := 0
	for i < len(s) {
		if s[i] == '`' {
			ticks := 1
			for i+ticks < len(s) && s[i+ticks] == '`' {
				ticks++
			}
			if closeEnd := findClosingBackticks(s, i+ticks, ticks); closeEnd > 0 {
				b.WriteString(s[i:closeEnd])
				i = closeEnd
				continue
			}
			b.WriteString(s[i : i+ticks])
			i += ticks
			continue
		}
		if end := matchBareFlag(s, i); end > i {
			b.WriteByte('`')
			b.WriteString(s[i:end])
			b.WriteByte('`')
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func findClosingBackticks(s string, start, ticks int) int {
	for j := start; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		k := j
		for k < len(s) && s[k] == '`' {
			k++
		}
		if k-j == ticks {
			return k
		}
		j = k
	}
	return -1
}

func matchBareFlag(s string, start int) int {
	if start+2 >= len(s) || s[start] != '-' || s[start+1] != '-' {
		return start
	}
	if start > 0 && isFlagBoundaryDisallowed(s[start-1]) {
		return start
	}
	if s[start+2] < 'a' || s[start+2] > 'z' {
		return start
	}
	end := start + 3
	for end < len(s) {
		c := s[end]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			end++
			continue
		}
		break
	}
	if s[end-1] == '-' || strings.Contains(s[start+2:end], "--") {
		return start
	}
	if end < len(s) && isFlagBoundaryDisallowed(s[end]) {
		return start
	}
	return end
}

func isFlagBoundaryDisallowed(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '_' || c == '/' || c == '`' || c == '-' || c == '='
}
