package cli

import (
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

// abbreviationNote is the last line of every group's help.
const abbreviationNote = "Any command name can be shortened as far as it stays unambiguous. A later\n" +
	"release can make a short form ambiguous, so a script uses full names."

// Execute runs root with args and returns the exit status of the process.
//
// It expands each abbreviated command name first (see inspectCommandLine),
// so the rule for a short form is the same at every level. Every failure,
// from a flag that does not parse to a refusal of the hub, reaches the
// caller as one error, and Execute prints it once.
func Execute(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	jsonErrors, err := run(ctx, root, args, stdin, stdout, stderr)
	if err == nil {
		return ExitOK
	}
	var status *ExitStatus
	if errors.As(err, &status) {
		return status.Code
	}
	printer := &Printer{Out: stdout, Err: stderr}
	if jsonErrors {
		printer.Format = FormatJSON
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

func run(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) (jsonErrors bool, err error) {
	prepare(root)
	expanded, jsonErrors, err := inspectCommandLine(root, args)
	if err != nil {
		return jsonErrors, err
	}
	root.SetArgs(expanded)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return jsonErrors, root.ExecuteContext(ctx)
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

// shellCompletionRequests are the hidden commands that a completion script
// runs. Their last argument is the word being completed, so it is not
// expanded.
var shellCompletionRequests = []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd}

// inspectCommandLine expands abbreviations and finds the requested error
// format even when a command name or flag fails to parse.
//
// The rule is the same at every level of the tree. A name that matches a
// command or an alias exactly wins, even when it is also the prefix of
// another name. Otherwise a prefix that matches exactly one command resolves
// to it, and a prefix that matches more than one fails with the candidates.
// Flags are skipped with their values. Once a leaf or an invalid command
// name ends path expansion, the scan still reads flags up to "--".
func inspectCommandLine(root *cobra.Command, args []string) (expanded []string, jsonErrors bool, pathError error) {
	out := slices.Clone(args)
	start, end := 0, len(out)
	if len(out) > 0 && slices.Contains(shellCompletionRequests, out[0]) {
		start, end = 1, len(out)-1
	}
	root.InitDefaultHelpCmd()
	cur := root
	expandPath := true
	for i := start; i < end; i++ {
		a := out[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			consumesNext, known := scanFlag(cur, a)
			if !known {
				// Let Cobra report the unknown flag and leave the rest of the
				// path alone. A later --json still controls the error format.
				expandPath = false
			}
			if consumesNext {
				i++
				continue
			}
			if a == "--json" {
				jsonErrors = true
			} else if value, ok := strings.CutPrefix(a, "--json="); ok {
				if useJSON, err := strconv.ParseBool(value); err == nil {
					jsonErrors = useJSON
				}
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
		cur = next
	}
	return out, jsonErrors, pathError
}

// TopLevelCommand returns the child of root that args act on. It resolves a
// name as inspectCommandLine does, so a unique prefix counts. It looks through
// the help command and through a shell completion request: "help control tab"
// and a completion of "control tab" act on control, as "control tab" does.
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
	start, end := 0, len(args)
	if len(args) > 0 && slices.Contains(shellCompletionRequests, args[0]) {
		start, end = 1, len(args)-1
	}
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
  {{rpad .Name (commandColumn $)}}{{.Short}}{{end}}{{end}}{{end}}{{flagSections .LocalFlags}}{{with flagUsages .InheritedFlags}}

Global Flags:
{{.}}{{end}}{{if .HasAvailableSubCommands}}

Run "{{.CommandPath}} <command> --help" for the help of a command.
` + abbreviationNote + `{{end}}{{with index .Annotations "` + annotationFooter + `"}}

{{.}}{{end}}
`
