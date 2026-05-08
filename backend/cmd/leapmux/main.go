package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/util/version"
)

const usageText = `Usage: leapmux <command> [flags]

Commands:
  solo      Run Hub + Worker locally for single-user use
  hub       Run the Hub service
  worker    Run a Worker connected to a Hub
  dev       Run Hub + Worker for development
  admin     Manage LeapMux resources
  version   Print version and exit

Common options:
  -h, --help     Print help and exit
  -version       Print version and exit
  --version      Print version and exit
`

type cliRunners struct {
	runHub    func([]string) error
	runWorker func([]string) error
	runSolo   func([]string, bool) error
	runAdmin  func([]string) error
	version   func() string
}

func main() {
	logging.Setup()
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, cliRunners{
		runHub:    runHub,
		runWorker: runWorker,
		runSolo:   runSolo,
		runAdmin:  runAdmin,
		version:   version.Format,
	}))
}

func runCLI(args []string, stdout, stderr io.Writer, runners cliRunners) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "error: command is required")
		_, _ = fmt.Fprintln(stderr, `hint: use "leapmux solo" to run solo mode`)
		printUsage(stderr)
		return 1
	}

	switch args[0] {
	case "-h", "-help", "--help", "help":
		printUsage(stdout)
		return 0
	case "-version", "--version":
		_, _ = fmt.Fprintln(stdout, runners.version())
		return 0
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
	case "admin":
		if code, handled := handleAdminArgs(args[1:], stdout, stderr); handled {
			return code
		}
		if err := runners.runAdmin(args[1:]); err != nil {
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
	default:
		if len(args[0]) > 0 && args[0][0] == '-' {
			_, _ = fmt.Fprintf(stderr, "error: %s is not a top-level flag\n", args[0])
			_, _ = fmt.Fprintf(stderr, "hint: use \"leapmux solo %s\" to pass solo-mode flags\n", args[0])
			printUsage(stderr)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printUsage(stderr)
		return 1
	}
}

// handleAdminArgs walks adminTree to validate args and print group/leaf help
// before runAdmin dispatches to a leaf command. Returns (code, true) when it
// has fully handled the request (printing usage or an error); returns
// (0, false) when args resolve to a leaf command and dispatch should proceed.
func handleAdminArgs(args []string, stdout, stderr io.Writer) (int, bool) {
	return walkAdminArgs(adminTree, []string{"admin"}, args, stdout, stderr)
}

func walkAdminArgs(group adminGroup, path, args []string, stdout, stderr io.Writer) (int, bool) {
	usage := formatAdminGroupUsage(group, strings.Join(path, " "))

	if len(args) == 0 {
		var label string
		if group.Name == "" {
			label = "admin group is required"
		} else {
			label = strings.Join(path, " ") + " command is required"
		}
		_, _ = fmt.Fprintln(stderr, "error: "+label)
		_, _ = fmt.Fprintln(stderr)
		_, _ = fmt.Fprint(stderr, usage)
		return 1, true
	}
	if internalconfig.IsHelpArg(args[0]) {
		_, _ = fmt.Fprint(stdout, usage)
		return 0, true
	}

	for i := range group.Subgroups {
		if group.Subgroups[i].Name == args[0] {
			return walkAdminArgs(group.Subgroups[i], append(path, args[0]), args[1:], stdout, stderr)
		}
	}
	for i := range group.Commands {
		if group.Commands[i].Name == args[0] {
			return 0, false
		}
	}

	var errMsg string
	if group.Name == "" {
		errMsg = fmt.Sprintf("unknown admin group: %s", args[0])
	} else {
		errMsg = fmt.Sprintf("unknown %s command: %s", strings.Join(path, " "), args[0])
	}
	_, _ = fmt.Fprintln(stderr, errMsg)
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
