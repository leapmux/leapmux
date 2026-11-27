package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// errorPrinterOpts holds the output format preferences scanned from the raw
// command line so failures before Cobra finishes parsing still print in the
// format that the caller requested.
type errorPrinterOpts struct {
	format Format
	color  ColorMode
	quiet  bool
}

func (o errorPrinterOpts) resolvedFormat() Format {
	if o.format == FormatText && o.quiet {
		return FormatQuiet
	}
	return o.format
}

// Execute runs root with args and returns the exit status of the process.
//
// It expands each abbreviated command name first (see inspectCommandLine),
// so the rule for a short form is the same at every level. Every failure,
// from a flag that does not parse to a refusal of the hub, reaches the
// caller as one error, and Execute prints it once.
func Execute(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := run(ctx, root, args, stdin, stdout, stderr)
	if err == nil {
		return ExitOK
	}
	var status *ExitStatus
	if errors.As(err, &status) {
		return status.Code
	}
	printer := &Printer{
		Out:    stdout,
		Err:    stderr,
		Format: opts.resolvedFormat(),
		Color:  opts.color,
	}
	e := FromError(err)
	printer.Failure(e)
	return e.ExitCode()
}

// Run runs root with args, as Execute does, and returns the error of the
// command instead of printing it. A command's result still prints.
func Run(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_, err := run(ctx, root, args, stdin, stdout, stderr)
	return err
}

func run(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) (opts errorPrinterOpts, err error) {
	prepare(root)
	expanded, opts, err := inspectCommandLine(root, args)
	if err != nil {
		return opts, err
	}
	root.SetArgs(expanded)
	root.SetIn(stdin)
	root.SetErr(stderr)

	if disallowed := disallowedFlagsForCompletion(root, expanded); len(disallowed) > 0 {
		var buf bytes.Buffer
		root.SetOut(&buf)
		execErr := root.ExecuteContext(ctx)
		filtered := filterCompletionOutput(buf.Bytes(), disallowed)
		if len(filtered) > 0 {
			if _, werr := stdout.Write(filtered); werr != nil && execErr == nil {
				return opts, werr
			}
		}
		return opts, execErr
	}

	root.SetOut(stdout)
	return opts, root.ExecuteContext(ctx)
}

// disallowedFlagsForCompletion returns the flag completion tokens (--name and
// -s) that must be omitted when expanded is a Cobra shell completion request
// targeting a command with disallowed inherited flags.
func disallowedFlagsForCompletion(root *cobra.Command, expanded []string) map[string]bool {
	if len(expanded) < 2 || !slices.Contains(shellCompletionRequests, expanded[0]) {
		return nil
	}
	trimmed := expanded[1 : len(expanded)-1]
	if slices.Contains(trimmed, "--") {
		return nil
	}
	target, _, err := root.Find(trimmed)
	if err != nil || target == nil {
		return nil
	}
	var disallowed map[string]bool
	target.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if !IsInheritedFlagDisallowed(target, f.Name) {
			return
		}
		if disallowed == nil {
			disallowed = map[string]bool{}
		}
		disallowed["--"+f.Name] = true
		if f.Shorthand != "" && target.LocalFlags().ShorthandLookup(f.Shorthand) == nil {
			disallowed["-"+f.Shorthand] = true
		}
	})
	return disallowed
}

// filterCompletionOutput removes lines whose completion candidate (before any
// tab description) matches disallowed, preserving the final ":<directive>" line.
func filterCompletionOutput(raw []byte, disallowed map[string]bool) []byte {
	if len(raw) == 0 || len(disallowed) == 0 {
		return raw
	}
	lines := strings.Split(string(raw), "\n")
	hasTrailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if hasTrailingNewline {
		lines = lines[:len(lines)-1]
	}
	directiveIdx := -1
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], ":") {
		directiveIdx = len(lines) - 1
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i, line := range lines {
		if i != directiveIdx {
			candidate, _, _ := strings.Cut(line, "\t")
			candidate = strings.TrimSuffix(candidate, "=")
			if disallowed[candidate] {
				continue
			}
		}
		out.WriteString(line)
		if i < len(lines)-1 || hasTrailingNewline {
			out.WriteByte('\n')
		}
	}
	return []byte(out.String())
}

// prepare applies the settings that every command of the tree shares. It is
// idempotent, so a test can execute the same tree more than once.
func prepare(root *cobra.Command) {
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		e := Usagef("%s", err.Error())
		if cmd.HasSubCommands() && strings.HasPrefix(err.Error(), "unknown") {
			// A group takes no flag of its own beyond --help, so the flag
			// almost always belongs to a command of the group, typed before
			// the name of that command.
			e = e.WithHint(`a flag goes after the command that takes it, as in "%s <command> [flags]"`, cmd.CommandPath())
		}
		if strings.HasPrefix(err.Error(), "unknown shorthand flag") {
			// pflag reads "-version" as the letters v, e, r, ..., so its
			// complaint is about a letter the user never meant as a flag.
			e = e.WithHint(`a long flag takes two dashes, as in "--version"; one dash takes a single letter`)
		}
		if strings.HasPrefix(err.Error(), "unknown") {
			// The token may be a value, such as a message that starts with a
			// dash: only "--" stops the flags.
			e = e.WithHint(`a value that starts with "-" goes after "--", as in: -- -v`)
		}
		return e.WithHint(`run "%s --help" for usage`, cmd.CommandPath())
	})
	root.SetHelpTemplate(helpTemplate)
	root.SetUsageTemplate(usageTemplate)
	root.SetVersionTemplate("{{.Version}}\n")
	// cobra adds the help command and the --version flag with texts of its
	// own. Create both now, so they read like every other command and flag.
	root.InitDefaultHelpCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "help" {
			cmd.Short = "Print the help of a command"
			cmd.Long = "Print the help of a command, as its --help flag does."
			cmd.Run = nil
			cmd.RunE = func(_ *cobra.Command, args []string) error {
				if len(args) == 0 {
					return root.Help()
				}
				cur := root
				for _, token := range args {
					if !cur.HasSubCommands() {
						return Usagef("unknown help topic %q for %q", token, cur.CommandPath()).
							WithHint(`run "%s --help" for usage`, cur.CommandPath())
					}
					next, err := matchSubcommand(cur, token)
					if err != nil {
						return err
					}
					cur = next
				}
				cur.InitDefaultHelpFlag()
				return cur.Help()
			}
		}
	}
	root.InitDefaultVersionFlag()
	if f := root.Flags().Lookup("version"); f != nil {
		f.Usage = "print the version of this binary"
	}
	walk(root, func(cmd *cobra.Command) {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if cmd.SuggestionsMinimumDistance <= 0 {
			// cobra applies its default distance only inside its own
			// lookup, and unknownCommand asks for suggestions outside it.
			cmd.SuggestionsMinimumDistance = 2
		}
		if !cmd.Runnable() && cmd.HasSubCommands() && cmd.RunE == nil {
			// A group run alone prints its help. It is not a failure: the
			// caller asked what the group holds.
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				if err := validateAllowedFlags(cmd); err != nil {
					return err
				}
				if len(args) > 0 {
					return unknownCommand(cmd, args[0])
				}
				return cmd.Help()
			}
			cmd.Args = cobra.ArbitraryArgs
		}
	})
}

func walk(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, child := range cmd.Commands() {
		walk(child, fn)
	}
}

// shellCompletionRequests are the hidden commands that a completion script
// runs. Their last argument is the word being completed, so it is not
// expanded.
var shellCompletionRequests = []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd}

func completionScanRange(args []string) (start, end int) {
	if len(args) > 0 && slices.Contains(shellCompletionRequests, args[0]) {
		return 1, len(args) - 1
	}
	return 0, len(args)
}

// inspectCommandLine expands abbreviations and finds the requested error
// format even when a command name or flag fails to parse.
//
// The rule is the same at every level of the tree. A name that matches a
// command or an alias exactly wins, even when it is also the prefix of
// another name. Otherwise a prefix that matches exactly one command resolves
// to it, and a prefix that matches more than one fails with the candidates.
// Flags are skipped with their values. Once a leaf or an invalid command
// name ends path expansion, the scan still reads flags up to "--".
func inspectCommandLine(root *cobra.Command, args []string) (expanded []string, opts errorPrinterOpts, pathError error) {
	out := slices.Clone(args)
	start, end := completionScanRange(out)
	root.InitDefaultHelpCmd()
	cur := root
	// flagsCur is the command whose flags Cobra parses against: cur normally,
	// or the help command after top-level "help".
	flagsCur := root
	inHelp := false
	expandPath := true
	for i := start; i < end; i++ {
		a := out[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			consumesNext, known := scanFlag(flagsCur, a)
			if !known {
				// Let Cobra report the unknown flag and leave the rest of the
				// path alone. Later output flags still control error formatting.
				expandPath = false
			}
			nextVal := ""
			if consumesNext && i+1 < end {
				nextVal = out[i+1]
			}
			applyErrorPrinterFlag(flagsCur, &opts, a, nextVal, consumesNext)
			if consumesNext {
				i++
			}
			continue
		}
		if !expandPath || !cur.HasSubCommands() {
			continue
		}
		next, err := matchSubcommand(cur, a)
		if err != nil {
			pathError = err
			expandPath = false
			continue
		}
		out[i] = next.Name()
		if !inHelp && cur == root && next.Name() == "help" {
			// "help <command>..." resolves <command>... from root, while flags
			// on the command line still belong to the help command.
			inHelp = true
			flagsCur = next
			continue
		}
		cur = next
		if !inHelp {
			flagsCur = next
		}
	}
	return out, opts, pathError
}

func applyErrorPrinterFlag(cmd *cobra.Command, opts *errorPrinterOpts, token, nextVal string, consumesNext bool) {
	switch {
	case token == "--json":
		opts.format = FormatJSON
	case strings.HasPrefix(token, "--json="):
		if useJSON, err := strconv.ParseBool(strings.TrimPrefix(token, "--json=")); err == nil {
			if useJSON {
				opts.format = FormatJSON
			} else if opts.format == FormatJSON {
				opts.format = FormatText
			}
		}
	case token == "--format" && consumesNext:
		if f, err := ParseFormat(nextVal); err == nil {
			opts.format = f
		}
	case strings.HasPrefix(token, "--format="):
		if f, err := ParseFormat(strings.TrimPrefix(token, "--format=")); err == nil {
			opts.format = f
		}
	case token == "--color" && consumesNext:
		if mode, err := ParseColorMode(nextVal); err == nil {
			opts.color = mode
		}
	case strings.HasPrefix(token, "--color="):
		if mode, err := ParseColorMode(strings.TrimPrefix(token, "--color=")); err == nil {
			opts.color = mode
		}
	case token == "--quiet":
		opts.quiet = true
	case strings.HasPrefix(token, "--quiet="):
		if q, err := strconv.ParseBool(strings.TrimPrefix(token, "--quiet=")); err == nil {
			opts.quiet = q
		}
	case strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--"):
		applyShortErrorPrinterFlags(cmd, opts, strings.TrimPrefix(token, "-"))
	}
}

func applyShortErrorPrinterFlags(cmd *cobra.Command, opts *errorPrinterOpts, cluster string) {
	for i, r := range cluster {
		if r == '=' {
			return
		}
		f := lookupShorthand(cmd, string(r))
		if f == nil {
			return
		}
		if r == 'q' && f.Name == "quiet" {
			rest := cluster[i+utf8.RuneLen(r):]
			if val, ok := strings.CutPrefix(rest, "="); ok {
				if q, err := strconv.ParseBool(val); err == nil {
					opts.quiet = q
				}
				return
			}
			opts.quiet = true
			continue
		}
		if f.NoOptDefVal == "" {
			// The rest of the cluster is this flag's value, not more short flags.
			return
		}
	}
}

// TopLevelCommand returns the child of root that args act on. It resolves a
// name as inspectCommandLine does, so a unique prefix counts. It looks through
// the help command and through a shell completion request: "help tab list"
// and a completion of "tab list" act on tab, as "tab list" does.
//
// It returns nil when args reach no child of root:
//
//   - args hold no command name, or a completion completes the name itself.
//   - The first name is unknown or ambiguous.
//   - A flag before the first name is one that root does not know.
//   - "--" comes before the first name.
//
// It reads nothing after the first name, so an argument or a flag that only
// another version of a command knows cannot change the answer.
func TopLevelCommand(root *cobra.Command, args []string) *cobra.Command {
	start, end := completionScanRange(args)
	root.InitDefaultHelpCmd()
	// flagsOf is the command whose flags the next flag token belongs to: root,
	// or the help command after "help".
	flagsOf := root
	for i := start; i < end; i++ {
		a := args[i]
		if a == "--" {
			return nil
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			consumesNext, known := scanFlag(flagsOf, a)
			if !known {
				return nil
			}
			if consumesNext {
				i++
			}
			continue
		}
		next, err := matchSubcommand(root, a)
		if err != nil {
			return nil
		}
		if flagsOf == root && next.Name() == "help" {
			// "help <command>" acts on <command>, a path from root.
			flagsOf = next
			continue
		}
		return next
	}
	return nil
}

// scanFlag reports whether the flag token a consumes the next argument as
// its value, and whether cmd knows every flag of the token.
func scanFlag(cmd *cobra.Command, a string) (consumesNext, known bool) {
	// cobra adds the help flag when the command runs; add it now, so that a
	// "--help" in the middle of a command line counts as known.
	cmd.InitDefaultHelpFlag()
	if long, ok := strings.CutPrefix(a, "--"); ok {
		name, _, hasValue := strings.Cut(long, "=")
		f := lookupFlag(cmd, name)
		if f == nil {
			return false, false
		}
		return !hasValue && f.NoOptDefVal == "", true
	}
	// A cluster of short flags: "-qv", "-n5", "-n=5" or "-qn" followed by
	// the value of n.
	cluster := strings.TrimPrefix(a, "-")
	for i, r := range cluster {
		if r == '=' {
			if i == 0 {
				return false, false
			}
			return false, true
		}
		f := lookupShorthand(cmd, string(r))
		if f == nil {
			return false, false
		}
		if f.NoOptDefVal == "" {
			// The rest of the token is the value. With nothing left, the
			// next argument is.
			return i+utf8.RuneLen(r) == len(cluster), true
		}
	}
	return false, true
}

func lookupInChain(cmd *cobra.Command, lookup func(*pflag.FlagSet) *pflag.Flag) *pflag.Flag {
	if f := lookup(cmd.Flags()); f != nil {
		return f
	}
	for c := cmd; c != nil; c = c.Parent() {
		if f := lookup(c.PersistentFlags()); f != nil {
			return f
		}
	}
	return nil
}

func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	return lookupInChain(cmd, func(fs *pflag.FlagSet) *pflag.Flag { return fs.Lookup(name) })
}

func lookupShorthand(cmd *cobra.Command, short string) *pflag.Flag {
	return lookupInChain(cmd, func(fs *pflag.FlagSet) *pflag.Flag { return fs.ShorthandLookup(short) })
}

// matchSubcommand resolves token against the subcommands of cmd.
func matchSubcommand(cmd *cobra.Command, token string) (*cobra.Command, error) {
	if token == "" {
		return nil, unknownCommand(cmd, token)
	}
	for _, sub := range cmd.Commands() {
		if sub.Name() == token || sub.HasAlias(token) {
			return sub, nil
		}
	}
	var matches []*cobra.Command
	for _, sub := range cmd.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		if strings.HasPrefix(sub.Name(), token) || slices.ContainsFunc(sub.Aliases, func(a string) bool { return strings.HasPrefix(a, token) }) {
			matches = append(matches, sub)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, unknownCommand(cmd, token)
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name()
	}
	return nil, Usagef("ambiguous command %q for %q; it matches: %s", token, cmd.CommandPath(), strings.Join(names, ", ")).
		WithHint("type more letters of the command name")
}

const destructiveIntentThreshold = 1

func unknownCommand(cmd *cobra.Command, token string) error {
	e := Usagef("unknown command %q for %q", token, cmd.CommandPath())
	raw := cmd.SuggestionsFor(token)
	var suggestions []string
	for _, name := range raw {
		sub := findSubcommandByName(cmd, name)
		if isDestructiveSuggestion(sub) && LevenshteinDistance(token, name) > destructiveIntentThreshold {
			continue
		}
		suggestions = append(suggestions, name)
	}
	if len(suggestions) > 0 {
		e = e.WithHint("did you mean %s?", strings.Join(suggestions, " or "))
	}
	return e.WithHint(`run "%s --help" for the commands`, cmd.CommandPath())
}

func findSubcommandByName(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

func isDestructiveSuggestion(sub *cobra.Command) bool {
	return sub != nil && IsDestructive(sub)
}

// LevenshteinDistance computes the rune edit distance between a and b: the
// fewest insertions, deletions, and substitutions of one rune that turn a into b.
func LevenshteinDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

const helpTemplate = `{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`

const usageTemplate = `Usage:{{if and .Runnable (not .HasAvailableSubCommands)}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} <command>{{end}}{{if and .Runnable (not .HasAvailableSubCommands)}}{{with argUsages .}}

Arguments:
{{.}}{{end}}{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{commandSections .}}{{end}}{{flagSections .LocalFlags}}{{with inheritedFlagUsages .}}

Global Flags:
{{.}}{{end}}{{with nextHints .}}

Next:
{{.}}{{end}}{{if .HasAvailableSubCommands}}

Run "{{.CommandPath}} <command> --help" for the help of a command.
{{abbreviationNoteOf .}}{{end}}{{with footerOf .}}

{{.}}{{end}}
`
