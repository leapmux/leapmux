package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/solo"
)

// The lifecycle tree every mode requires. The seven verbs share one
// group per mode; exact matches win and abbreviations resolve as far
// as they stay unambiguous (`worker sta` names both start and status,
// so the CLI lists both).
//
// `run` is the only verb that starts a foreground process, and it never
// configures a native service or requests elevation. The other verbs
// work through the directory's locks, the saved specification, and the
// native managers.

// lifecycleCommands builds one mode's lifecycle group.
func lifecycleCommands(mode string) cmdGroup {
	runSummary := "Run in the foreground (never configures a service)"
	switch mode {
	case "hub":
		runSummary = "Run the Hub in the foreground (never configures a service)"
	case "worker":
		runSummary = "Run a registered Worker in the foreground (never configures a service)"
	case "solo":
		runSummary = "Run Hub + Worker in the foreground (never configures a service)"
	case "dev":
		runSummary = "Run Hub + Worker for development in the foreground (never configures a service)"
	}
	return cmdGroup{
		Name:    mode,
		Summary: modeSummary(mode),
		Commands: []cmdLeaf{
			{Name: "run", Summary: runSummary, Run: runModeRun(mode)},
			{Name: "start", Summary: "Start through the configured native mechanism, waiting for readiness", Run: runModeStart(mode)},
			{Name: "stop", Summary: "Stop the running instance and wait for the full shutdown", Run: runModeStop(mode)},
			{Name: "restart", Summary: "Stop the current instance, then start through the native mechanism", Run: runModeRestart(mode)},
			{Name: "status", Summary: "Report process state, readiness, registration, and diagnostics", Run: runModeStatus(mode)},
			{Name: "enable", Summary: "Configure automatic startup at login or boot without starting now", Run: runModeEnable(mode)},
			{Name: "disable", Summary: "Remove the native startup configuration (needs a stopped instance)", Run: runModeDisable(mode)},
		},
	}
}

func modeSummary(mode string) string {
	switch mode {
	case "hub":
		return "Run the Hub service"
	case "worker":
		return "Run a Worker connected to a Hub"
	case "solo":
		return "Run Hub + Worker locally for single-user use"
	default:
		return "Run Hub + Worker for development"
	}
}

// modeRole parses the mode into its lifecycle role.
func modeRole(mode string) lifecycle.Role {
	role, err := lifecycle.ParseRole(mode)
	if err != nil {
		return lifecycle.RoleDev
	}
	return role
}

// runModeRun runs the mode in the foreground under the common runtime:
// the directory leases, the runtime lock, the attempt identity, the
// private control interface, and the diagnostics log all belong to it.
// It never configures a service and never requests elevation.
func runModeRun(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		dataDir, cfgArgs, err := splitLifecycleArgs(args)
		if err != nil {
			return err
		}
		if dataDir == "" {
			// No --data-dir flag: the mode's own loader is the authority
			// -- its default AND its config file both resolve the data
			// directory. Injecting the CLI default here would override a
			// data_dir the config file states (the container's case), so
			// ask the loader instead.
			dataDir, err = resolveModeDataDir(mode, cfgArgs)
			if err != nil {
				return err
			}
		} else {
			// The user named the directory: the mode's loader sees the
			// same flag, so keep it beside the rest.
			cfgArgs = append([]string{"--data-dir", dataDir}, cfgArgs...)
		}
		runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
			Mode:      modeRole(mode),
			DataDir:   dataDir,
			Execution: lifecycle.ExecutionForeground,
		})
		if err != nil {
			return err
		}
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)
			select {
			case <-sigCh:
			case <-runtime.StopSignal():
			}
		}()

		// The foreground run keeps its output on this process's streams
		// and its attempt log for `status`.
		logFile, logErr := lifecycle.AttemptLog(runtime.Paths(), runtime.Record().AttemptID)
		if logErr == nil {
			defer func() { _ = logFile.Close() }()
		}

		err = dispatchModeForeground(mode, runtime, cfgArgs)
		shutdownErr := runtime.Shutdown()
		_ = lifecycle.RotateAttemptLogs(runtime.Paths(), runtime.Record().AttemptID)
		return errors.Join(err, shutdownErr)
	}
}

// dispatchModeForeground hands the started runtime and the remaining
// flags to the mode's foreground implementation.
func dispatchModeForeground(mode string, runtime *lifecycle.Runtime, args []string) error {
	switch mode {
	case "hub":
		return runHubForeground(runtime, args)
	case "worker":
		return runWorkerForeground(runtime, args)
	default:
		return runSoloForeground(mode == "dev", runtime, args)
	}
}

// splitLifecycleArgs separates the data-directory flag the runtime
// needs from the mode's own flags. The mode's flags pass through
// untouched -- the mode's own loader defines and validates them -- so
// the scan takes only the exact --data-dir spellings and leaves every
// other token, flags included, in rest.
func splitLifecycleArgs(args []string) (dataDir string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--data-dir" || arg == "-data-dir":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--data-dir needs a value")
			}
			dataDir = args[i+1]
			i++
		case strings.HasPrefix(arg, "--data-dir="):
			dataDir = strings.TrimPrefix(arg, "--data-dir=")
		default:
			rest = append(rest, arg)
		}
	}
	return dataDir, rest, nil
}

// The wait budgets the plan states. The runtime wait budget starts
// after configuration consent and native authorization complete; a
// restart spends ONE budget on shutdown and startup together.
const (
	defaultStartTimeout  = 60 * time.Second
	defaultStopTimeout   = 30 * time.Second
	defaultStatusTimeout = 3 * time.Second
)

// parseTimeoutFlag reads --timeout from args with the shared duration
// parser. Zero, negative, and overflowing durations fail; a very large
// representable duration is accepted.
func parseTimeoutFlag(args []string, fallback time.Duration) (time.Duration, []string, error) {
	rest := make([]string, 0, len(args))
	timeout := fallback
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--timeout" || arg == "-timeout":
			if i+1 >= len(args) {
				return 0, nil, fmt.Errorf("--timeout needs a duration value")
			}
			parsed, err := internalconfig.ParseDuration(args[i+1])
			if err != nil {
				return 0, nil, fmt.Errorf("--timeout: %w", err)
			}
			if parsed <= 0 {
				return 0, nil, fmt.Errorf("--timeout must be positive, got %s", args[i+1])
			}
			timeout = parsed
			i++
		case strings.HasPrefix(arg, "--timeout="):
			value := strings.TrimPrefix(arg, "--timeout=")
			parsed, err := internalconfig.ParseDuration(value)
			if err != nil {
				return 0, nil, fmt.Errorf("--timeout: %w", err)
			}
			if parsed <= 0 {
				return 0, nil, fmt.Errorf("--timeout must be positive, got %s", value)
			}
			timeout = parsed
		default:
			rest = append(rest, arg)
		}
	}
	return timeout, rest, nil
}

// runModeStop stops the running instance and waits for the FULL
// shutdown, including the database close. An already stopped instance
// succeeds.
func runModeStop(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		timeout, rest, err := parseTimeoutFlag(args, defaultStopTimeout)
		if err != nil {
			return err
		}
		dataDir, _, err := splitLifecycleArgs(rest)
		if err != nil {
			return err
		}
		if dataDir == "" {
			dataDir = defaultDataDirOf(mode)
		}
		canonical, err := lifecycle.CanonicalDir(dataDir)
		if err != nil {
			return err
		}
		release, err := lifecycle.OperationLock(canonical)
		if err != nil {
			return err
		}
		defer release()

		paths := lifecycle.PathsOf(canonical)
		record, err := lifecycle.ReadRuntimeRecord(paths.RuntimeRecord)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("Not running.")
				return nil
			}
			return err
		}
		if held, _ := lifecycle.ProbeRuntimeLock(paths); !held {
			fmt.Println("Not running.")
			return nil
		}
		resp, err := lifecycle.RequestStop(context.Background(), record)
		if err != nil {
			// The endpoint refuses; the process holds its lock still.
			// Report what the evidence says rather than killing by pid.
			return fmt.Errorf("the instance did not accept the stop request: %w", err)
		}
		if !resp.Accepted {
			return fmt.Errorf("the instance refused the stop request")
		}
		if lifecycle.WaitForLockRelease(context.Background(), canonical, timeout) {
			fmt.Println("Stopped.")
			return nil
		}
		fmt.Printf("Shutdown continues; the instance still holds its runtime lock after %s. See `leapmux %s status`.\n", timeout, mode)
		return nil
	}
}

// runModeStatus reports the reconciled evidence: process state,
// execution mode, readiness, configuration, and the failure
// diagnostics, through the one status model. Exit code 3 reports a
// stopped instance; a disconnected registered worker stays 0.
func runModeStatus(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		timeout, rest, err := parseTimeoutFlag(args, defaultStatusTimeout)
		if err != nil {
			return err
		}
		dataDir, _, err := splitLifecycleArgs(rest)
		if err != nil {
			return err
		}
		if dataDir == "" {
			dataDir = defaultDataDirOf(mode)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		status, err := lifecycle.Observe(ctx, dataDir)
		if errors.Is(err, lifecycle.ErrUnresponsive) {
			printStatusReport(mode, status, "")
			return errUnresponsiveExit
		}
		if err != nil {
			return err
		}
		printStatusReport(mode, status, "")
		if status.Process.State == lifecycle.ProcessStopped {
			return errStoppedExit
		}
		return nil
	}
}

// errStoppedExit and errUnresponsiveExit carry the status exit codes
// through runCLI's error path.
var (
	errStoppedExit      = fmt.Errorf("\x00stopped")
	errUnresponsiveExit = fmt.Errorf("\x00unresponsive")
)

// capitalize upper-cases the first rune of one word -- the status
// report's labels -- without strings.Title's Unicode word rules.
func capitalize(word string) string {
	if word == "" {
		return word
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

// printStatusReport renders the status model for the terminal.
func printStatusReport(mode string, status lifecycle.Status, diagnosticExcerpt string) {
	fmt.Printf("%s: %s\n", capitalize(mode), capitalize(status.Process.State))
	if status.Startup != nil {
		fmt.Printf("Startup:       %s\n", capitalize(string(*status.Startup)))
	}
	fmt.Printf("Execution:     %s\n", status.Execution)
	if status.Attempt.ID != "" {
		fmt.Printf("Attempt:       %s\n", status.Attempt.ID)
		if status.Attempt.LogPath != "" {
			fmt.Printf("Full log:      %s\n", status.Attempt.LogPath)
		}
	}
	if status.Registration.State != lifecycle.RegistrationNotApplicable {
		fmt.Printf("Registration:  %s\n", capitalize(status.Registration.State))
		if status.Registration.HubURL != "" {
			fmt.Printf("Hub:           %s\n", status.Registration.HubURL)
		}
	}
	for _, d := range status.Diagnostics {
		fmt.Printf("\n%s:\n%s\n", d.Title, d.Body)
	}
	if diagnosticExcerpt != "" {
		fmt.Printf("\n%s\n", diagnosticExcerpt)
	}
}

// defaultDataDirOf states the mode's default data directory.
func defaultDataDirOf(mode string) string {
	switch mode {
	case "hub":
		return "~/.config/leapmux/hub"
	case "worker":
		return "~/.config/leapmux/worker"
	default:
		return "~/.config/leapmux/" + mode
	}
}

// runModeCommand walks one mode's lifecycle tree. The seven verbs are
// required: bare flags that belonged to the old foreground-only
// spelling get the exact corrected command in the hint, and the worker
// group keeps register, deregister, and cross-worker-pins beside the
// verbs.
func runModeCommand(mode string, args []string) error {
	group := lifecycleCommands(mode)
	if mode == "worker" {
		group.Commands = append(group.Commands,
			cmdLeaf{Name: "register", Summary: "Register with a hub and save credentials (never starts the worker)", Run: func(c cmdCtx, args []string) error {
				return runWorkerRegister(args)
			}},
			cmdLeaf{Name: "deregister", Summary: "Remove this worker's registration, keeping the worker database", Run: func(c cmdCtx, args []string) error {
				return runWorkerDeregister(args)
			}},
		)
		group.Subgroups = []cmdGroup{workerPinsGroup()}
	}
	// A flag where the verb belongs names the corrected command in the
	// hint rather than dispatching.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && !internalconfig.IsHelpArg(args[0]) {
		return fmt.Errorf("the %s command is required before any flags; run `leapmux %s run` for the foreground command", mode, mode)
	}
	return dispatchGroup(group, args, []string{mode})
}

// workerPinsGroup is the cross-worker-pins subtree, unchanged in shape.
func workerPinsGroup() cmdGroup {
	return cmdGroup{
		Name:    "cross-worker-pins",
		Summary: "Manage the worker-local TOFU pin store",
		Commands: []cmdLeaf{
			{Name: "list", Summary: "List pinned cross-worker keys", Run: func(c cmdCtx, args []string) error {
				return runWorkerCrossWorkerPins([]string{"list"})
			}},
			{Name: "show", Summary: "Show one pinned worker's keys", Run: func(c cmdCtx, args []string) error {
				return runWorkerCrossWorkerPins(append([]string{"show"}, args...))
			}},
			{Name: "remove", Summary: "Remove one pinned worker's keys", Run: func(c cmdCtx, args []string) error {
				return runWorkerCrossWorkerPins(append([]string{"remove"}, args...))
			}},
		},
	}
}

// modeExitCode maps a mode command's error to its exit code: 0 for a
// successful operation and a running instance, 3 for a stopped instance
// reported by status, 1 for failures and invalid use.
func modeExitCode(stderr io.Writer, err error) int {
	if errors.Is(err, errStoppedExit) {
		return 3
	}
	if errors.Is(err, errUnresponsiveExit) {
		return 1
	}
	return handleRunError(stderr, stripExitMarker(err))
}

// stripExitMarker removes the NUL marker the status exit codes carry,
// so handleRunError never prints it.
func stripExitMarker(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "\x00") {
		return errors.New(strings.ReplaceAll(err.Error(), "\x00", ""))
	}
	return err
}

// walkModeArgs validates the mode group's tokens and prints its help;
// it reports (code, true) when it fully handled the request.
func walkModeArgs(group cmdGroup, mode string, args []string, stdout, stderr io.Writer) (int, bool) {
	// The worker group carries the register leaves too.
	if mode == "worker" {
		group.Commands = append(group.Commands,
			cmdLeaf{Name: "register", Summary: "Register with a hub and save credentials (never starts the worker)", Run: nil},
			cmdLeaf{Name: "deregister", Summary: "Remove this worker's registration, keeping the worker database", Run: nil},
		)
		group.Subgroups = []cmdGroup{workerPinsGroup()}
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if internalconfig.IsHelpArg(args[0]) {
			_, _ = fmt.Fprint(stdout, formatGroupUsage(group, mode))
			return 0, true
		}
		// A flag before the verb is the dispatcher's error, printed
		// here with the exact corrected command.
		_, _ = fmt.Fprintf(stderr, "error: the %s command is required before any flags; run `leapmux %s run` for the foreground command\\n", mode, mode)
		return 1, true
	}
	return walkGroupArgs(group, []string{mode}, args, stdout, stderr)
}

// resolveModeDataDir loads the mode's configuration read-only and states
// the data directory it resolves -- the default, or the one its config
// file names. The foreground run loads the configuration again; loading
// is idempotent and reads nothing the runtime owns.
func resolveModeDataDir(mode string, args []string) (string, error) {
	switch mode {
	case "hub":
		cfg, _, err := hubconfig.Load(args)
		if err != nil {
			return "", err
		}
		return cfg.DataDir, nil
	case "worker":
		cfg, _, err := workerconfig.Load(args)
		if err != nil {
			return "", err
		}
		return cfg.DataDir, nil
	default:
		devMode := mode == "dev"
		prepared, err := solo.Prepare(solo.Config{Args: args, DevMode: devMode})
		if err != nil {
			return "", err
		}
		return prepared.DataDirectory(), nil
	}
}
