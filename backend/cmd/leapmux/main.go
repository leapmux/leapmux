package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/leapmux/leapmux/internal/cli/control"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/util/version"
)

// topLevelCommand is one row of `leapmux --help` and one candidate for the
// top-level prefix match. Both read the same list, so a row the matcher does
// not know cannot appear, and a command the usage omits cannot hide.
type topLevelCommand struct {
	Name    string
	Summary string
}

// topLevelCommands holds every `leapmux <command>`, in the order the usage
// prints them.
//
// A command that owns a command tree takes its name and its summary from
// that tree, so `leapmux --help` and `leapmux <command> --help` cannot
// disagree — they did, and the top-level row was the stale copy. The other
// commands are daemons and one built-in; they have no tree to read from, so
// their summary lives here, which is the only place that prints it.
var topLevelCommands = []topLevelCommand{
	{Name: "solo", Summary: "Run Hub + Worker locally for single-user use"},
	{Name: "hub", Summary: "Run the Hub service"},
	{Name: "worker", Summary: "Run a Worker connected to a Hub"},
	{Name: "dev", Summary: "Run Hub + Worker for development"},
	{Name: recoverTree.Name, Summary: recoverTree.Summary},
	{Name: controlTree.Name, Summary: controlTree.Summary},
	{Name: "version", Summary: "Print version and exit"},
}

// usageText is the text that `leapmux --help` prints. It is rendered once,
// at start, because topLevelCommands never changes after initialization.
var usageText = formatTopLevelUsage()

// formatTopLevelUsage renders the top-level help from topLevelCommands. The
// name column is 10 wide, which is 3 spaces past the longest name.
func formatTopLevelUsage() string {
	var sb strings.Builder
	sb.WriteString("Usage: leapmux <command> [flags]\n\nCommands:\n")
	for _, c := range topLevelCommands {
		fmt.Fprintf(&sb, "  %-10s%s\n", c.Name, c.Summary)
	}
	sb.WriteString(`
Common options:
  -h, --help     Print help and exit
  -version       Print version and exit
  --version      Print version and exit

Any command name can be shortened as far as it stays unambiguous.
`)
	return sb.String()
}

// topLevelCommandNames lists the commands that the top-level prefix match
// resolves against, in the order that the usage prints them. That order sets
// the order of the candidates in an "ambiguous token" refusal.
func topLevelCommandNames() []string {
	names := make([]string, len(topLevelCommands))
	for i, c := range topLevelCommands {
		names[i] = c.Name
	}
	return names
}

type cliRunners struct {
	runHub     func([]string) error
	runWorker  func([]string) error
	runSolo    func([]string, bool) error
	runRecover func([]string) error
	runControl func([]string) error
	version    func() string
}

func main() {
	logging.Setup()
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, cliRunners{
		runHub:     runHub,
		runWorker:  runWorker,
		runSolo:    runSolo,
		runRecover: runRecover,
		runControl: runControl,
		version:    version.Format,
	}))
}

func runCLI(args []string, stdout, stderr io.Writer, runners cliRunners) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "error: command is required")
		_, _ = fmt.Fprintln(stderr, `hint: use "leapmux solo" to run solo mode`)
		printUsage(stderr)
		return 1
	}

	// Handle help/version flags and bare "help" before prefix matching —
	// these are not abbreviable command names.
	switch args[0] {
	case "-h", "-help", "--help", "help":
		printUsage(stdout)
		return 0
	case "-version", "--version":
		_, _ = fmt.Fprintln(stdout, runners.version())
		return 0
	}

	// Reject anything that looks like a flag — it can't be a command name.
	if len(args[0]) > 0 && args[0][0] == '-' {
		_, _ = fmt.Fprintf(stderr, "error: %s is not a top-level flag\n", args[0])
		_, _ = fmt.Fprintf(stderr, "hint: use \"leapmux solo %s\" to pass solo-mode flags\n", args[0])
		printUsage(stderr)
		return 1
	}

	// Btrfs-style prefix matching: any top-level command may be shortened
	// to an unambiguous prefix (e.g. `leapmux sol` = solo, `leapmux rec`
	// = recover). An exact match always wins; an ambiguous prefix answers
	// with a candidate list.
	matched, candidates, matchErr := matchCommandToken(args[0], topLevelCommandNames())
	if matchErr != nil {
		if len(candidates) > 0 {
			_, _ = fmt.Fprintf(stderr, "ambiguous token %q; did you mean one of: %s\n", args[0], strings.Join(candidates, ", "))
		} else {
			_, _ = fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		}
		printUsage(stderr)
		return 1
	}

	switch matched {
	case "solo":
		if err := runners.runSolo(args[1:], true); err != nil {
			return handleRunError(stderr, err)
		}
		return 0
	case "hub":
		if err := runners.runHub(args[1:]); err != nil {
			return handleRunError(stderr, err)
		}
		return 0
	case "worker":
		if err := runners.runWorker(args[1:]); err != nil {
			return handleRunError(stderr, err)
		}
		return 0
	case "dev":
		if err := runners.runSolo(args[1:], false); err != nil {
			return handleRunError(stderr, err)
		}
		return 0
	case "recover":
		if code, handled := handleRecoverArgs(args[1:], stdout, stderr); handled {
			return code
		}
		if err := runners.runRecover(args[1:]); err != nil {
			return handleRunError(stderr, err)
		}
		return 0
	case "control":
		if code, handled := handleControlArgs(args[1:], stdout, stderr); handled {
			return code
		}
		if err := runners.runControl(args[1:]); err != nil {
			// EmitError already wrote the JSON envelope to stdout
			// via the emittedError marker; suppress the plain-text
			// "error: …" fallback so the JSON consumer doesn't see
			// the same failure surfaced twice across two streams.
			if control.IsEmitted(err) {
				return 1
			}
			return handleRunError(stderr, err)
		}
		return 0
	case "version":
		if len(args) > 1 && internalconfig.IsHelpArg(args[1]) {
			printVersionUsage(stdout)
			return 0
		}
		_, _ = fmt.Fprintln(stdout, runners.version())
		return 0
	}
	return 0
}

// handleRecoverArgs walks recoverTree to validate args and print group/leaf
// help before runRecover dispatches to a leaf command. Returns (code, true)
// when it has fully handled the request (printing usage or an error); returns
// (0, false) when args resolve to a leaf command and dispatch should proceed.
func handleRecoverArgs(args []string, stdout, stderr io.Writer) (int, bool) {
	return walkGroupArgs(recoverTree, []string{"recover"}, args, stdout, stderr)
}

func walkGroupArgs(group cmdGroup, path, args []string, stdout, stderr io.Writer) (int, bool) {
	usage := formatGroupUsage(group, strings.Join(path, " "))

	if len(args) == 0 {
		// One wording, shared with the dispatcher — see tokenNoun.
		label := strings.Join(path, " ") + " " + tokenNoun(group) + " is required"
		_, _ = fmt.Fprintln(stderr, "error: "+label)
		_, _ = fmt.Fprintln(stderr)
		_, _ = fmt.Fprint(stderr, usage)
		return 1, true
	}
	if internalconfig.IsHelpArg(args[0]) {
		_, _ = fmt.Fprint(stdout, usage)
		return 0, true
	}

	// One namespace over subgroups and commands; see resolveGroupToken.
	match := resolveGroupToken(group, args[0])
	if match.Subgroup >= 0 {
		sub := group.Subgroups[match.Subgroup]
		return walkGroupArgs(sub, append(path, sub.Name), args[1:], stdout, stderr)
	}
	if match.Command >= 0 {
		// The leaf parses its own flags; this walk only had to prove the
		// path exists.
		return 0, false
	}

	// One wording, shared with the dispatcher — see unresolvedTokenError.
	_, _ = fmt.Fprintln(stderr, unresolvedTokenError(path, args[0], match.Candidates, tokenNoun(group)).Error())
	_, _ = fmt.Fprintln(stderr)
	_, _ = fmt.Fprint(stderr, usage)
	return 1, true
}

func handleRunError(stderr io.Writer, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "error:", err)
	return 1
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, usageText)
}

func printVersionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Print version and exit.

Usage: leapmux version
`)
}
