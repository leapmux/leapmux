package cli

import (
	"fmt"
	"io"
	"strings"
)

// WriteReference writes the Markdown reference of the command at path in
// tree and of every command under it: a section for each group, and one for
// each leaf with its usage, its arguments, its flags and its examples. The
// flags that the command at path gives to every command under it are listed
// once, at the top.
//
// The text is escaped for a Markdown renderer that passes raw HTML through,
// so an argument such as <agent> prints as itself instead of as a tag.
func WriteReference(w io.Writer, tree CommandTree, path string) error {
	var top *CommandInfo
	var under []CommandInfo
	for i, c := range tree.Commands {
		switch {
		case c.Path == path:
			top = &tree.Commands[i]
		case strings.HasPrefix(c.Path, path+" "):
			under = append(under, c)
		}
	}
	if top == nil {
		return fmt.Errorf("the tree holds no command %q", path)
	}
	rw := &referenceWriter{w: w}
	var global []FlagInfo
	for _, f := range top.Flags {
		if f.Global || isPersistentOf(f, under) {
			global = append(global, f)
		}
	}
	if len(global) > 0 {
		rw.printf("## Flags of every command\n\n")
		rw.printf("Every command under `%s` takes these flags.\n\n", commandOf(*top))
		rw.flagTable(global)
	}
	globalNames := map[string]bool{}
	for _, f := range global {
		globalNames[f.Name] = true
	}
	for _, c := range under {
		if c.Group {
			rw.group(c)
			continue
		}
		rw.leaf(c, globalNames)
	}
	return rw.err
}

// WriteReferenceExcluding writes the Markdown reference of every command in
// tree whose top-level command name is not in excludeTopLevel. Persistent root
// flags carried across the included commands are lifted to a single table at
// the top (including flags such as --hub that offline commands disallow or
// shadow locally).
func WriteReferenceExcluding(w io.Writer, tree CommandTree, excludeTopLevel map[string]bool) error {
	var included []CommandInfo
	for _, c := range tree.Commands {
		top, _, _ := strings.Cut(c.Path, " ")
		if excludeTopLevel[top] {
			continue
		}
		included = append(included, c)
	}
	if len(included) == 0 {
		return fmt.Errorf("the tree holds no matching commands")
	}
	rw := &referenceWriter{w: w}
	var global []FlagInfo
	counts := map[string]int{}
	firstSeen := map[string]FlagInfo{}
	var order []string
	for _, c := range included {
		for _, f := range c.Flags {
			if !f.Global {
				continue
			}
			if _, seen := firstSeen[f.Name]; !seen {
				firstSeen[f.Name] = f
				order = append(order, f.Name)
			}
			counts[f.Name]++
		}
	}
	hasPartialGlobal := false
	for _, name := range order {
		if counts[name]*2 > len(included) {
			global = append(global, firstSeen[name])
			if counts[name] < len(included) {
				hasPartialGlobal = true
			}
		}
	}
	if len(global) > 0 {
		rw.printf("## Flags of every command\n\n")
		if hasPartialGlobal {
			rw.printf("Every control command takes these output flags, and every online command takes `--hub` (offline commands such as `guide`, `context show`, and `context clear` do not contact a Hub). Commands marked below as not supporting `-q` / `--quiet` reject `--quiet` with exit code 2.\n\n")
		} else {
			rw.printf("Every control command takes these flags.\n\n")
		}
		rw.flagTable(global)
	}
	globalNames := map[string]bool{}
	for _, f := range global {
		globalNames[f.Name] = true
	}
	for _, c := range included {
		if c.Group {
			rw.group(c)
			continue
		}
		rw.leaf(c, globalNames)
	}
	return rw.err
}

// isPersistentOf reports whether every command in under carries f as a
// global flag, which is what a persistent flag of their ancestor does.
func isPersistentOf(f FlagInfo, under []CommandInfo) bool {
	if len(under) == 0 {
		return false
	}
	for _, c := range under {
		found := false
		for _, g := range c.Flags {
			if g.Name == f.Name && g.Global {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// commandOf is the command line that runs c, without its arguments and its
// flags: the usage line up to the first "<" or "[".
func commandOf(c CommandInfo) string {
	if i := strings.IndexAny(c.Usage, "<["); i > 0 {
		return strings.TrimSpace(c.Usage[:i])
	}
	return c.Usage
}

type referenceWriter struct {
	w   io.Writer
	err error
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
	rw.printf("%s\n\n", escapeProse(strings.TrimSpace(text)))
	if len(c.Aliases) > 0 {
		rw.printf("Aliases: %s.\n\n", formatCodeList(c.Aliases))
	}
	if c.Mutates {
		rw.printf("This command changes state.\n\n")
	}
	if c.QuietUnsupported {
		rw.printf("Does not support `-q` / `--quiet`.\n\n")
	}
	rw.printf("```text\n%s\n```\n\n", c.Usage)
	var described []ArgSpec
	for _, a := range c.Args {
		if a.Help != "" {
			described = append(described, a)
		}
	}
	if len(described) > 0 {
		rw.printf("| Argument | Description |\n|---|---|\n")
		for _, a := range described {
			name := "<" + a.Name + ">"
			if !a.Required {
				name = "[" + a.Name + "]"
			}
			if a.Variadic {
				name += "..."
			}
			rw.printf("| `%s` | %s |\n", name, escapeCell(a.Help))
		}
		rw.printf("\n")
	}
	var own []FlagInfo
	for _, f := range c.Flags {
		if f.Global && globalNames[f.Name] {
			continue
		}
		own = append(own, f)
	}
	if len(own) > 0 {
		rw.flagTable(own)
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

func (rw *referenceWriter) flagTable(flags []FlagInfo) {
	rw.printf("| Flag | Description |\n|---|---|\n")
	for _, f := range flags {
		rw.printf("| `%s` | %s |\n", f.Names(), escapeCell(f.Text()))
	}
	rw.printf("\n")
}

// escapeProse escapes the characters that a Markdown renderer with raw HTML
// turned on reads as markup: an argument such as <agent> would print as an
// empty tag.
func escapeProse(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// escapeCell escapes text for one cell of a Markdown table, where a pipe
// ends the cell and a line break ends the row.
func escapeCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(escapeProse(s))
}
