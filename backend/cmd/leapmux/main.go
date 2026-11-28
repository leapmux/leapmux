package main

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/worker"
)

// cliRunners holds what the root command reaches outside the process, so a
// test can replace the native service manager, the foreground run, the
// Worker's registration files and the version.
type cliRunners struct {
	modes   modeDependencies
	version func() string
}

// defaultCLIRunners returns the runners of the shipped binary. A test starts
// from it and replaces one field, so no field it leaves alone is nil.
func defaultCLIRunners() cliRunners {
	return cliRunners{
		modes:   defaultModeDependencies(),
		version: version.Format,
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
			os.Exit(reportEntryFailure(os.Stderr, err))
		}
		os.Exit(0)
	}
	if handled, err := runNativeServiceEntry(os.Args[1:]); handled {
		if err != nil {
			os.Exit(reportEntryFailure(os.Stderr, err))
		}
		os.Exit(0)
	}
	// Inside a LeapMux tab, the CLI of the tab's Worker runs control commands
	// (see controlHandoff). The hand-off comes before the command tree parses
	// the arguments: a command for another version can hold a name or a flag
	// that this version refuses. Reuse the command tree if the hand-off builds
	// it and decides not to exec.
	var cachedRoot *cobra.Command
	getRoot := func() *cobra.Command {
		if cachedRoot == nil {
			cachedRoot = newRootCommand(defaultCLIRunners())
		}
		return cachedRoot
	}
	if code, handled := handOffControlCommand(os.Args[1:], getRoot); handled {
		os.Exit(code)
	}
	logging.Setup()
	os.Exit(cli.Execute(context.Background(), getRoot(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// reportEntryFailure prints the failure of an entry point that runs before
// the command tree, in the form that the root prints every failure, and
// returns its exit status.
func reportEntryFailure(stderr io.Writer, err error) int {
	failure := cli.FromError(err)
	(&cli.Printer{Out: io.Discard, Err: stderr}).Failure(failure)
	return failure.ExitCode()
}
