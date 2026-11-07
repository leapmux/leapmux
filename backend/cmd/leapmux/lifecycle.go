package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/leapmux/leapmux/util/procutil"
)

// The lifecycle tree every mode requires. The seven verbs share one
// group per mode; exact matches win and abbreviations resolve as far
// as they stay unambiguous (`worker sta` identifies both start and status,
// so the CLI lists both).
//
// `run` is the only verb that starts a foreground process, and it never
// configures a native service or requests elevation. The other verbs
// work through the directory's locks, the saved specification, and the
// native managers.

type lifecycleDependencies struct {
	controller nativeControllerFactory
	foreground runInvocation
}

// lifecycleCommands builds one mode's lifecycle group.
func lifecycleCommands(mode string) cmdGroup {
	return lifecycleCommandsWithDependencies(mode, lifecycleDependencies{controller: nativeController, foreground: runInvocation{Context: context.Background()}})
}

func lifecycleCommandsWithDependencies(mode string, dependencies lifecycleDependencies) cmdGroup {
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
			{Name: "run", Summary: runSummary, Run: runModeRunWithInvocation(mode, dependencies.foreground)},
			{Name: "start", Summary: "Start through the configured native mechanism, waiting for readiness", Run: func(_ cmdCtx, args []string) error {
				return executeNativeStartWithController(mode, "start", args, dependencies.controller)
			}},
			{Name: "stop", Summary: "Stop the running instance and wait for the full shutdown", Run: runModeStopWithController(mode, dependencies.controller)},
			{Name: "restart", Summary: "Stop the current instance, then start through the native mechanism", Run: func(_ cmdCtx, args []string) error {
				return executeNativeStartWithController(mode, "restart", args, dependencies.controller)
			}},
			{Name: "status", Summary: "Report process state, readiness, registration, and diagnostics", Run: func(_ cmdCtx, args []string) error {
				return executeModeStatusWithController(mode, args, dependencies.controller)
			}},
			{Name: "enable", Summary: "Configure automatic startup at login or boot without starting now", Run: runModeEnableWithController(mode, dependencies.controller)},
			{Name: "disable", Summary: "Remove the native startup configuration (needs a stopped instance)", Run: runModeDisableWithController(mode, dependencies.controller)},
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

type runInvocation struct {
	Context     context.Context
	OnReady     func()
	ServiceName string
}

// runModeRunWithInvocation runs the mode in the foreground under the common runtime:
// the directory leases, the runtime lock, the attempt identity, the
// private control interface, and the diagnostics log all belong to it.
// It never configures a service and never requests elevation.
func runModeRunWithInvocation(mode string, invocation runInvocation) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) (result error) {
		selection := managedRunSelection{}
		selected := false
		configuration, err := loadLifecycleConfiguration(mode, args, func(fs *flag.FlagSet) {
			fs.Func("service-spec", "private specification for native startup", func(path string) error {
				if selected || path == "" {
					return fmt.Errorf("managed launch requires exactly one nonempty service specification")
				}
				selected = true
				selection.specPath = path
				return nil
			})
		}, func(fs *flag.FlagSet) error {
			if !selected {
				return nil
			}
			var invalid string
			fs.Visit(func(value *flag.Flag) {
				switch value.Name {
				case "service-spec":
				case "data-dir":
					selection.dataDirectory = new(value.Value.String())
				default:
					invalid = value.Name
				}
			})
			if invalid != "" {
				return fmt.Errorf("managed launch cannot override --%s", invalid)
			}
			return errManagedRunSelected
		})
		if handleVersionQuery(err) {
			return nil
		}
		var managed *lifecycle.ServiceSpec
		effective := args
		if errors.Is(err, errManagedRunSelected) {
			managed, effective, err = managedRun(mode, selection)
		}
		if err != nil {
			return err
		}
		if invocation.ServiceName != "" {
			if managed == nil || managed.Startup != lifecycle.StartupBoot || invocation.ServiceName != servicelaunch.WindowsServiceName(managed.Mode, managed.InstanceID) {
				return fmt.Errorf("the Windows service entry does not match its saved boot specification")
			}
		}
		execution := lifecycle.ExecutionForeground
		if managed != nil {
			restore, err := applyManagedEnvironment(*managed)
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, restore()) }()
			execution = lifecycle.ExecutionService
			configuration, err = loadLifecycleConfiguration(mode, effective, nil, nil)
			if err != nil {
				return err
			}
		}
		if err := configuration.validateRuntime(); err != nil {
			return fmt.Errorf("validate runtime configuration: %w", err)
		}
		// Survive a lost console reader so the attempt can finish its shutdown.
		// The Go handler preserves the default SIGPIPE behavior in child processes.
		procutil.KeepRunningOnBrokenPipe()
		ctx, cancel := context.WithCancel(invocation.Context)
		defer cancel()
		var monitor servicelaunch.SessionMonitor
		var account servicelaunch.AccountRef
		if managed != nil && managed.Startup == lifecycle.StartupLogin {
			monitor = servicelaunch.NewSessionMonitor()
			account = servicelaunch.AccountRef{ID: managed.AccountID, Name: managed.AccountName}
			initial, err := monitor.Sessions(ctx, account)
			if err != nil {
				return fmt.Errorf("managed login launch cannot read desktop sessions: %w", err)
			}
			if stop, _ := servicelaunch.LastSessionRule(initial, nil); stop {
				return fmt.Errorf("managed login launch requires an active desktop session; choose boot startup for use without a desktop login")
			}
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, procutil.ShutdownSignals()...)
		defer signal.Stop(signals)
		dataDir := configuration.dataDirectory
		var runtime *lifecycle.Runtime
		if managed != nil {
			runtime, err = lifecycle.StartServiceRuntime(*managed)
		} else {
			runtime, err = lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: modeRole(mode), DataDir: dataDir, Execution: execution})
		}
		if err != nil {
			return err
		}
		execute := func(ctx context.Context, runtime *lifecycle.Runtime) error {
			watchCtx, cancelWatchers := context.WithCancel(ctx)
			var watchers sync.WaitGroup
			// Observers start after capture and join before it closes. Their
			// final diagnostics therefore use the required attempt log.
			defer func() { cancelWatchers(); watchers.Wait() }()
			if invocation.OnReady != nil {
				watchers.Go(func() {
					select {
					case <-runtime.Ready():
						invocation.OnReady()
					case <-watchCtx.Done():
					}
				})
			}
			if monitor != nil {
				watchers.Go(func() {
					if err := servicelaunch.WatchLoginSessions(watchCtx, monitor, account, quartz.NewReal(), func(err error) {
						if watchCtx.Err() == nil {
							log.Printf("Cannot read desktop sessions; keeping the instance running: %v", err)
						}
					}); err == nil {
						cancel()
					}
				})
			}
			watchers.Go(func() {
				select {
				case <-watchCtx.Done():
				case <-signals:
					cancel()
					log.Printf("shutdown signal received")
				case <-runtime.StopSignal():
					cancel()
				}
			})
			return configuration.executeForeground(ctx, runtime)
		}
		return executeForegroundAttempt(ctx, cancel, runtime, execute)
	}
}

// foregroundResult treats requested cancellation as successful shutdown only
// when every error cause follows cancellation. SQL automatically rolls back a
// cancelled transaction, so migration cleanup can also return sql.ErrTxDone.
// Independent initialization and cleanup failures remain errors.
func foregroundResult(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) && errwrap.IsCancellation(err) {
		return nil
	}
	return err
}

// The wait budgets the plan states. The runtime wait budget starts
// after configuration consent and native authorization complete; a
// restart spends ONE budget on shutdown and startup together.
const (
	defaultStartTimeout  = 60 * time.Second
	defaultStopTimeout   = 30 * time.Second
	defaultStatusTimeout = 3 * time.Second
)

// runModeStopWithController stops the running instance and waits for the FULL
// shutdown, including the database close. An already stopped instance
// succeeds.
func runModeStopWithController(mode string, newController nativeControllerFactory) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		configuration, options, err := loadLifecycleCommand(mode, "stop", args)
		if handleVersionQuery(err) {
			return nil
		}
		if err != nil {
			return err
		}
		release, err := lifecycle.OperationLock(configuration.dataDirectory)
		if err != nil {
			return err
		}
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
		defer cancel()
		return stopLifecycleInstanceWithController(ctx, configuration, newController)
	}
}

func stopLifecycleInstanceWithController(ctx context.Context, configuration *lifecycleConfiguration, newController nativeControllerFactory) error {
	active, err := stopLifecycleEvidence(ctx, configuration, newController)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Printf("Shutdown could not be confirmed. See `leapmux %s status`.\n", configuration.role)
		}
		return err
	}
	if active {
		fmt.Println("Stopped.")
	} else {
		fmt.Println("Not running.")
	}
	return nil
}

func stopLifecycleEvidence(ctx context.Context, configuration *lifecycleConfiguration, newController nativeControllerFactory) (bool, error) {
	active, err := lifecycle.Stop(ctx, configuration.role, configuration.dataDirectory)
	if err != nil {
		return active, err
	}
	controller, err := newController(configuration.role.String(), configuration.dataDirectory)
	if err != nil {
		return active, err
	}
	spec, err := controller.ReadSpec()
	if errors.Is(err, os.ErrNotExist) {
		return active, nil
	}
	if err != nil {
		return active, err
	}
	// Environment contents do not affect the public native definition.
	// Stopping must remain possible when that file needs repair.
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return active, err
	}
	nativeActive, err := stopApplicationAndNative(ctx, configuration, controller.Manager, launch)
	return active || nativeActive, err
}

func stopApplicationAndNative(ctx context.Context, configuration *lifecycleConfiguration, manager servicelaunch.Manager, launch servicelaunch.LaunchSpec) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	active := false
	for {
		found, err := lifecycle.Stop(ctx, configuration.role, configuration.dataDirectory)
		active = active || found
		if err != nil {
			return active, err
		}
		native, err := manager.Inspect(ctx, launch)
		if err != nil {
			return active, err
		}
		if !native.Running {
			return active, nil
		}
		active = true
		// Native startup may precede runtime publication. Keep requesting
		// application shutdown until it appears, then confirm native exit.
		select {
		case <-ctx.Done():
			return active, fmt.Errorf("native process shutdown could not be confirmed: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// capitalize upper-cases the first letter of an ASCII status word.
func capitalize(word string) string {
	if word == "" {
		return word
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

// printStatusReport renders the status model for the terminal.
func printStatusReport(mode string, status lifecycle.Status, diagnosticExcerpt string) {
	fmt.Printf("%s: %s\n", capitalize(mode), capitalize(status.Process.State))
	fmt.Printf("Configured:    %t\n", status.Configured)
	if status.Startup != nil {
		fmt.Printf("Startup:       %s\n", capitalize(string(*status.Startup)))
	}
	printStatusValue("Execution", status.Execution)
	printStatusValue("PID", status.Process.PID)
	printStatusValue("Started at", status.Process.StartedAt)
	printStatusValue("Readiness", status.Readiness)
	if status.Registration.State != lifecycle.RegistrationNotApplicable {
		fmt.Printf("Registration:  %s\n", capitalize(status.Registration.State))
		printStatusValue("Worker ID", status.Registration.WorkerID)
		printStatusValue("Hub", status.Registration.HubURL)
	}
	if status.Connection.State != lifecycle.ConnectionNotApplicable {
		fmt.Printf("Connection:    %s\n", capitalize(status.Connection.State))
		printStatusValue("Last error", status.Connection.LastError)
	}
	printStatusValue("Attempt", status.Attempt.ID)
	printStatusValue("Attempt start", status.Attempt.StartedAt)
	printStatusValue("Ended at", status.Attempt.EndedAt)
	printStatusValue("Exit code", status.Attempt.ExitCode)
	printStatusValue("Reason", status.Attempt.Error)
	for _, d := range status.Diagnostics {
		fmt.Printf("\n%s:\n%s\n", d.Title, d.Body)
	}
	if diagnosticExcerpt != "" {
		fmt.Printf("\n%s\n", diagnosticExcerpt)
	}
	printStatusValue("Full log", status.Attempt.LogPath)
}

func printStatusValue[T any](label string, value *T) {
	if value != nil {
		fmt.Printf("%-14s%v\n", label+":", *value)
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
	// A flag where the verb belongs identifies the corrected command in the
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
				return runWorkerCrossWorkerPins(append([]string{"list"}, args...))
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
		_, _ = fmt.Fprintf(stderr, "error: the %s command is required before any flags; run `leapmux %s run` for the foreground command\n", mode, mode)
		return 1, true
	}
	return walkGroupArgs(group, []string{mode}, args, stdout, stderr)
}
