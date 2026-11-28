package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// controlHandoffMarker marks a process that a hand-off started, so that the
// process never hands the command off again. A target that leads back to a
// `leapmux` with the hand-off, such as a script that runs `leapmux` from PATH,
// would otherwise start one process after another. The prefix makes
// controlenv.Apply strip the marker from each process that a Worker spawns.
const controlHandoffMarker = controlenv.VariablePrefix + "CLI_HANDOFF"

// controlHandoffOptOutEnv disables handing off control commands to the
// Worker's CLI when set to any non-empty value.
const controlHandoffOptOutEnv = "LEAPMUX_NO_CONTROL_HANDOFF"

// controlHandoff hands a control command that runs inside a LeapMux tab to the
// CLI of the Worker that started the tab.
//
// That CLI speaks the Worker's protocol, and another `leapmux` may not: the
// two come from separate releases when the user installed a `leapmux` of
// another version. The Worker puts the directory of its own CLI first on PATH,
// but a login shell's profile can move another `leapmux` before it. macOS
// path_helper does so for /usr/local/bin in every zsh login shell. The hand-off
// makes a control command reach the Worker's CLI anyway.
//
// It hands off any top-level command not marked local-only via cli.MarkLocalOnly
// (in any spelling that the CLI accepts), `help <command> ...`, shell completion
// of it, and unknown top-level command tokens or unknown leading flags so a
// newer Worker CLI handles commands and global flags added after this binary.
// Every local-only command describes or acts on the binary that the user ran,
// and stays with it: `leapmux daemon worker run` inside a tab starts the Worker
// that the user chose, `leapmux version` reports the binary that runs, and
// `leapmux commands` lists its commands.
type controlHandoff struct {
	// environ is the environment of this process. A target inherits it.
	environ []string
	// executable returns the path of the running executable. When it fails,
	// the hand-off cannot tell the two executables apart, so it hands the
	// command off, and the marker stops a second hand-off.
	executable func() (string, error)
	// exec runs target with argv and env in place of this process. It returns
	// only when target does not take over the process: on Unix after a failure
	// to run it, and on Windows with the exit code of target or a failure to
	// start it.
	exec func(target string, argv, env []string) (int, error)
	// root returns the root command tree used to classify args. When nil, a
	// fresh tree is built on demand.
	root func() *cobra.Command
	// stderr receives the warning of a hand-off that cannot run.
	stderr io.Writer
}

// handOffControlCommand runs the hand-off of args with the environment and
// the executable of this process. It returns the exit code of the process and
// true when the Worker's CLI ran the command. Otherwise it returns false, and
// this process runs the command.
func handOffControlCommand(args []string, root func() *cobra.Command) (int, bool) {
	code, handled := controlHandoff{
		environ:    os.Environ(),
		executable: os.Executable,
		exec:       execControlCLI,
		root:       root,
		stderr:     os.Stderr,
	}.run(args)
	// The marker served this process. A process that this one starts, such as
	// a command that a control command runs, decides for itself.
	if err := os.Unsetenv(controlHandoffMarker); err != nil {
		_ = (&cli.Printer{Err: os.Stderr}).Notice("warning: cannot clear %s: %v", controlHandoffMarker, err)
	}
	return code, handled
}

// run hands args off and returns the exit code and true, or returns false
// when this process must run the command itself. It reads the variables
// first, then compares the target against this executable, so a command
// outside a tab or already running the Worker's own CLI builds no command tree.
func (h controlHandoff) run(args []string) (int, bool) {
	if h.getenv(controlHandoffOptOutEnv) != "" || h.getenv(controlHandoffMarker) != "" || h.getenv(controlenv.SocketVariable) == "" || h.getenv(controlenv.TokenVariable) == "" {
		return 0, false
	}
	target := h.getenv(controlenv.CLIVariable)
	if target == "" {
		return 0, false
	}
	info, statErr := statControlCLI(target)
	if statErr == nil && h.isSelf(info) {
		return 0, false
	}
	if !h.actsOnControl(args) {
		return 0, false
	}
	if statErr != nil {
		h.warn(statErr)
		return 0, false
	}
	argv := append([]string{target}, args...)
	// PinEnv, not append: the target reads the first entry of a name, and an
	// empty marker that this process inherited would come first.
	env := envutil.PinEnv(h.environ, controlHandoffMarker+"=1")
	code, err := h.exec(target, argv, env)
	if err != nil {
		h.warn(fmt.Errorf("the Worker's CLI %s did not start: %w", target, err))
		return 0, false
	}
	return code, true
}

// getenv returns the value of name in h.environ, as os.Getenv and the C
// library read an environment: the first entry wins, and Windows ignores the
// case of the name.
func (h controlHandoff) getenv(name string) string {
	for _, entry := range h.environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return value
		}
	}
	return ""
}

// isSelf reports whether target, whose information is info, is the running
// executable. The comparison reads file identity, so the path itself, a hard
// link and a symbolic link all count as the running executable. It answers
// false when it cannot read the running executable.
func (h controlHandoff) isSelf(info fs.FileInfo) bool {
	path, err := h.executable()
	if err != nil {
		return false
	}
	self, err := os.Stat(path)
	return err == nil && os.SameFile(self, info)
}

// warn states on stderr why the hand-off cannot run, and that this process
// runs the command instead.
func (h controlHandoff) warn(err error) {
	_ = (&cli.Printer{Err: h.stderr}).Notice("warning: %v. This leapmux runs the command, and its version can differ from the Worker's.", err)
}

// actsOnControl reports whether args act on a control command (see
// controlHandoff).
//
// Inside a LeapMux tab, local-only commands (marked via cli.MarkLocalOnly),
// empty invocations, "--", bare root flags (--help, --version, -h, -v), and
// ambiguous prefixes that match at least one local-only command stay with
// this binary. Known non-local control commands, unknown top-level command
// tokens, and unknown leading flags hand off to the Worker's CLI so a newer
// Worker can serve commands or global flags added after this binary.
func (h controlHandoff) actsOnControl(args []string) bool {
	var root *cobra.Command
	if h.root != nil {
		root = h.root()
	} else {
		root = newRootCommand(defaultCLIRunners())
	}
	root.InitDefaultHelpCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()

	start, end := 0, len(args)
	if len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd) {
		start, end = 1, len(args)-1
	}

	flagsOf := root
	inHelp := false
	for i := start; i < end; i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			consumesNext, known := scanRootFlag(flagsOf, a)
			if !known {
				return true
			}
			if consumesNext {
				i++
			}
			continue
		}
		if a == "" {
			return false
		}
		matched, matches := matchSubcommands(root, a)
		if matched != nil {
			if !inHelp && matched.Name() == "help" {
				inHelp = true
				flagsOf = matched
				continue
			}
			if cli.IsLocalOnly(matched) {
				return false
			}
			rest := args[i+1 : end]
			switch matched.Name() {
			case "skill":
				if inHelp {
					return false
				}
				return skillCommandActsOnRemoteWorker(matched, rest)
			case "auth":
				return authCommandActsOnControl(matched, rest)
			}
			return true
		}
		if len(matches) == 0 {
			return true
		}
		for _, m := range matches {
			if cli.IsLocalOnly(m) {
				return false
			}
		}
		return true
	}
	return false
}

// skillCommandActsOnRemoteWorker reports whether a `skill` invocation targets
// a remote Worker (`--worker` or `--worker=...` before `--`). Local `skill`
// commands, `skill show`, and `skill diff` always stay with the invoked binary
// so they read its own bundled catalog.
func skillCommandActsOnRemoteWorker(skillCmd *cobra.Command, rest []string) bool {
	var (
		sub       *cobra.Command
		seenSub   bool
		hasWorker bool
	)
	flagOwner := skillCmd
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if tok == "--" {
			break
		}
		if tok == "--worker" || strings.HasPrefix(tok, "--worker=") {
			hasWorker = true
			if tok == "--worker" {
				i++
			}
			continue
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			consumesNext, _ := scanRootFlag(flagOwner, tok)
			if consumesNext {
				i++
			}
			continue
		}
		if !seenSub {
			seenSub = true
			if matched, _ := matchSubcommands(skillCmd, tok); matched != nil {
				sub = matched
				flagOwner = sub
			}
		}
	}
	if sub != nil && (sub.Name() == "show" || sub.Name() == "diff") {
		return false
	}
	return hasWorker
}

// authCommandActsOnControl reports whether an `auth` invocation needs the
// Worker's CLI. `auth list` (`ls`) and `auth use` read or write local
// credential and context files only, so they stay with the invoked binary.
func authCommandActsOnControl(authCmd *cobra.Command, rest []string) bool {
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if tok == "--" {
			return true
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			consumesNext, known := scanRootFlag(authCmd, tok)
			if !known {
				return true
			}
			if consumesNext {
				i++
			}
			continue
		}
		if tok == "" {
			return false
		}
		matched, matches := matchSubcommands(authCmd, tok)
		if matched != nil {
			return matched.Name() != "list" && matched.Name() != "use"
		}
		if len(matches) == 0 {
			return true
		}
		for _, m := range matches {
			if m.Name() == "list" || m.Name() == "use" {
				return false
			}
		}
		return true
	}
	return true
}

// matchSubcommands resolves token against parent's children using the same
// exact-then-prefix rule as cli.Execute. It returns a single matched command
// or all prefix candidates when zero or more than one command matched.
func matchSubcommands(parent *cobra.Command, token string) (*cobra.Command, []*cobra.Command) {
	for _, sub := range parent.Commands() {
		if sub.Name() == token || sub.HasAlias(token) {
			return sub, nil
		}
	}
	var matches []*cobra.Command
	for _, sub := range parent.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		if strings.HasPrefix(sub.Name(), token) || slices.ContainsFunc(sub.Aliases, func(a string) bool { return strings.HasPrefix(a, token) }) {
			matches = append(matches, sub)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return nil, matches
}

// scanRootFlag reports whether flag token a consumes the next argument as its
// value, and whether cmd knows every flag in the token.
func scanRootFlag(cmd *cobra.Command, a string) (consumesNext, known bool) {
	cmd.InitDefaultHelpFlag()
	if long, ok := strings.CutPrefix(a, "--"); ok {
		name, _, hasValue := strings.Cut(long, "=")
		f := lookupCommandFlag(cmd, func(fs *pflag.FlagSet) *pflag.Flag { return fs.Lookup(name) })
		if f == nil {
			return false, false
		}
		return !hasValue && f.NoOptDefVal == "", true
	}
	cluster := strings.TrimPrefix(a, "-")
	for i, r := range cluster {
		if r == '=' {
			return false, i != 0
		}
		f := lookupCommandFlag(cmd, func(fs *pflag.FlagSet) *pflag.Flag { return fs.ShorthandLookup(string(r)) })
		if f == nil {
			return false, false
		}
		if f.NoOptDefVal == "" {
			return i+utf8.RuneLen(r) == len(cluster), true
		}
	}
	return false, true
}

func lookupCommandFlag(cmd *cobra.Command, lookup func(*pflag.FlagSet) *pflag.Flag) *pflag.Flag {
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

// statControlCLI returns the information of target, or an error that states
// why target cannot run the command.
func statControlCLI(target string) (fs.FileInfo, error) {
	if !filepath.IsAbs(target) {
		return nil, fmt.Errorf("%s holds %q, which is not an absolute path", controlenv.CLIVariable, target)
	}
	info, err := os.Stat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("the Worker's CLI %s does not exist", target)
	case err != nil:
		return nil, fmt.Errorf("cannot read the Worker's CLI: %w", err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("the Worker's CLI %s is not a file", target)
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		// Windows has no execute permission bit; it runs a file by its
		// extension.
		return nil, fmt.Errorf("the Worker's CLI %s is not executable", target)
	}
	return info, nil
}
