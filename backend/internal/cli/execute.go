package cli

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// abbreviationNote is the last line of every group's help.
const abbreviationNote = "Any command name can be shortened as far as it stays unambiguous. A later\n" +
	"release can make a short form ambiguous, so a script uses full names."

// Execute runs root with args and returns the exit status of the process.
//
// It expands each abbreviated command name first (see ExpandCommandPath),
// so the rule for a short form is the same at every level. Every failure,
// from a flag that does not parse to a refusal of the hub, reaches the
// caller as one error, and Execute prints it once.
func Execute(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	printer := &Printer{Out: stdout, Err: stderr}
	if wantsJSON(args) {
		printer.Format = FormatJSON
	}
	prepare(root)
	expanded, err := ExpandCommandPath(root, args)
	if err != nil {
		e := FromError(err)
		printer.Failure(e)
		return e.ExitCode()
	}
	root.SetArgs(expanded)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		var status *ExitStatus
		if errors.As(err, &status) {
			return status.Code
		}
		e := FromError(err)
		printer.Failure(e)
		return e.ExitCode()
	}
	return ExitOK
}

// prepare applies the settings that every command of the tree shares. It is
// idempotent, so a test can execute the same tree more than once.
func prepare(root *cobra.Command) {
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		e := Usagef("%s", err.Error())
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

// wantsJSON reports whether the command line asks for JSON. Execute reads it
// before the flags parse, so a command line that fails to parse still reports
// its failure in the format it asked for.
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" || a == "--json=true" {
			return true
		}
	}
	return false
}

// shellCompletionRequests are the hidden commands that a completion script
// runs. Their last argument is the word being completed, so it is not
// expanded.
var shellCompletionRequests = []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd}

// ExpandCommandPath replaces each abbreviated command name in args with the
// full name.
//
// The rule is the same at every level of the tree. A name that matches a
// command or an alias exactly wins, even when it is also the prefix of
// another name. Otherwise a prefix that matches exactly one command resolves
// to it, and a prefix that matches more than one fails with the candidates.
// Flags are skipped with their values, so a flag can stand anywhere. The walk
// stops at the first argument of a runnable command and at "--".
func ExpandCommandPath(root *cobra.Command, args []string) ([]string, error) {
	out := slices.Clone(args)
	start, end := 0, len(out)
	if len(out) > 0 && slices.Contains(shellCompletionRequests, out[0]) {
		start, end = 1, len(out)-1
	}
	root.InitDefaultHelpCmd()
	cur := root
	for i := start; i < end; i++ {
		a := out[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			consumesNext, known := scanFlag(cur, a)
			if !known {
				// cobra reports the unknown flag. The token after it may be
				// its value, so nothing after it is a command name to expand.
				break
			}
			if consumesNext {
				i++
			}
			continue
		}
		// Only a group takes a command name next. A leaf has no
		// subcommands, so its arguments stay as they are.
		if !cur.HasSubCommands() {
			break
		}
		next, err := matchSubcommand(cur, a)
		if err != nil {
			return nil, err
		}
		out[i] = next.Name()
		cur = next
	}
	return out, nil
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

func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f
	}
	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().Lookup(name); f != nil {
			return f
		}
	}
	return nil
}

func lookupShorthand(cmd *cobra.Command, short string) *pflag.Flag {
	if f := cmd.Flags().ShorthandLookup(short); f != nil {
		return f
	}
	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().ShorthandLookup(short); f != nil {
			return f
		}
	}
	return nil
}

// matchSubcommand resolves token against the subcommands of cmd.
func matchSubcommand(cmd *cobra.Command, token string) (*cobra.Command, error) {
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

func unknownCommand(cmd *cobra.Command, token string) error {
	e := Usagef("unknown command %q for %q", token, cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(token); len(suggestions) > 0 {
		e = e.WithHint("did you mean %s?", strings.Join(suggestions, " or "))
	}
	return e.WithHint(`run "%s --help" for the commands`, cmd.CommandPath())
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
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{with flagUsages .LocalFlags}}

Flags:
{{.}}{{end}}{{with flagUsages .InheritedFlags}}

Global Flags:
{{.}}{{end}}{{if .HasAvailableSubCommands}}

Run "{{.CommandPath}} <command> --help" for the help of a command.
` + abbreviationNote + `{{end}}{{with index .Annotations "` + annotationFooter + `"}}

{{.}}{{end}}
`
