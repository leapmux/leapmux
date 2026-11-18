package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/worker"
)

// cliRunners holds what the root command runs, so a test can replace the
// lifecycle and recover dispatchers and the version.
type cliRunners struct {
	runMode    func(mode string, args []string) error
	runRecover func([]string) error
	version    func() string
}

// defaultCLIRunners returns the runners of the shipped binary. A test starts
// from it and replaces one field, so no field it leaves alone is nil.
func defaultCLIRunners() cliRunners {
	return cliRunners{
		runMode:    runModeCommand,
		runRecover: runRecover,
		version:    version.Format,
	}
}

func main() {
	if code, handled := runConsoleHelper(os.Args[1:], os.Stdin, [2]io.Writer{os.Stdout, os.Stderr}); handled {
		os.Exit(code)
	}
	// A provider's CLI can start this executable as a helper, with no argument
	// (see worker.RunAgentHelper). The check comes before logging.Setup: the
	// helper's stderr reaches that CLI as a message, and a log line must not.
	if code, handled := worker.RunAgentHelper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if handled, err := servicelaunch.RunNativeHelper(os.Args[1:]); handled {
		if err != nil {
			os.Exit(handleRunError(os.Stderr, err))
		}
		os.Exit(0)
	}
	if handled, err := runNativeServiceEntry(os.Args[1:]); handled {
		if err != nil {
			os.Exit(handleRunError(os.Stderr, err))
		}
		os.Exit(0)
	}
	logging.Setup()
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, defaultCLIRunners()))
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
