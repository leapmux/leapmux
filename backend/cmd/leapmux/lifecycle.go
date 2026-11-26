package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/leapmux/leapmux/util/procutil"
)

// The lifecycle commands every mode has. start and stop come first: they are
// the recommended way to run an instance. run comes last, for a container.
//
// `run` is the only verb that starts a foreground process, and it never
// configures a native service or requests elevation. The other verbs work
// through the directory's locks, the saved specification, and the native
// managers.
var lifecycleVerbs = []string{"start", "stop", "restart", "status", "logs", "enable", "disable", "run"}

// modeDependencies holds what the commands of a mode reach outside the
// process: the native service manager, the context and callbacks of a
// foreground run, and the files of a Worker's registration. A test replaces
// them.
type modeDependencies struct {
	controller        nativeControllerFactory
	foreground        runInvocation
	registrationFiles func(*workerconfig.Config) workerRegistrationFiles
}

func defaultModeDependencies() modeDependencies {
	return modeDependencies{
		controller:        nativeController,
		foreground:        runInvocation{Context: context.Background()},
		registrationFiles: configuredWorkerRegistrationFiles,
	}
}

// lifecycleCommandLine is the parsed command line of one lifecycle command,
// with what its registration bound.
type lifecycleCommandLine struct {
	flags *lifecycleFlags
	set   *pflag.FlagSet
}

func (l lifecycleCommandLine) mode() string { return l.flags.mode }

func (l lifecycleCommandLine) verb() string { return l.flags.verb }

// load loads the configuration and the options of the command.
func (l lifecycleCommandLine) load() (*lifecycleConfiguration, lifecycleOptions, error) {
	return l.flags.load(l.set)
}

// newDaemonCommand builds `leapmux daemon`, grouping the local daemon modes
// (`solo`, `hub`, `worker`, `dev`) so control nouns can live directly at the
// root of `leapmux`.
func newDaemonCommand(deps modeDependencies) *cobra.Command {
	group := cli.Group("daemon", "Manage local LeapMux services (solo, hub, worker, dev)")
	for _, mode := range []string{"solo", "hub", "worker", "dev"} {
		group.AddCommand(newModeCommand(mode, deps))
	}
	return group
}

// newUpCommand builds `leapmux up`, a top-level shortcut for
// `leapmux daemon solo start`.
func newUpCommand(deps modeDependencies) *cobra.Command {
	return newLifecycleLeaf(
		"up",
		"Start the local solo LeapMux daemon (shortcut for daemon solo start)",
		"solo",
		"start",
		[]cli.Example{
			{Command: "leapmux up"},
			{Comment: "Start at login without a prompt.", Command: "leapmux up --startup login --yes"},
		},
		deps,
	)
}

// newDownCommand builds `leapmux down`, a top-level shortcut for
// `leapmux daemon solo stop`.
func newDownCommand(deps modeDependencies) *cobra.Command {
	return newLifecycleLeaf(
		"down",
		"Stop the local solo LeapMux daemon (shortcut for daemon solo stop)",
		"solo",
		"stop",
		[]cli.Example{
			{Command: "leapmux down"},
		},
		deps,
	)
}

// newModeCommand builds `leapmux daemon <mode>`: the lifecycle commands, plus
// the Worker's registration commands and pin store.
func newModeCommand(mode string, deps modeDependencies) *cobra.Command {
	group := cli.Group(mode, modeSummary(mode))
	for _, verb := range lifecycleVerbs {
		group.AddCommand(newLifecycleCommand(mode, verb, deps))
	}
	if mode == "worker" {
		group.AddCommand(
			newWorkerRegisterCommand(deps.registrationFiles),
			newWorkerDeregisterCommand(deps.registrationFiles),
			newCrossWorkerPinsCommand(),
		)
	}
	return group
}

// newLifecycleCommand builds `leapmux daemon <mode> <verb>`.
func newLifecycleCommand(mode, verb string, deps modeDependencies) *cobra.Command {
	return newLifecycleLeaf(verb, lifecycleSummary(mode, verb), mode, verb, lifecycleExamples(mode, verb), deps)
}

func newLifecycleLeaf(use, short, mode, verb string, examples []cli.Example, deps modeDependencies) *cobra.Command {
	var flags *lifecycleFlags
	return cli.NewLeaf(cli.Leaf{
		Use:         use,
		Short:       short,
		Mutates:     verb != "status" && verb != "logs",
		Destructive: verb == "stop" || verb == "disable",
		Examples:    examples,
		// `run` shuts down gracefully on its own handler of the signals.
		HandlesSignals:   verb == "run",
		QuietUnsupported: true,
		Flags: func(fs *pflag.FlagSet) {
			var err error
			flags, err = registerLifecycleFlags(mode, verb, fs)
			if err != nil {
				panic(fmt.Sprintf("leapmux %s (%s %s): %v", use, mode, verb, err))
			}
		},
		Run: func(inv *cli.Invocation) (any, error) {
			line := lifecycleCommandLine{flags: flags, set: inv.Command.Flags()}
			return runLifecycleVerb(inv.Context, inv.Printer, inv.Stdin, line, deps)
		},
	})
}

type lifecycleActionResult struct {
	Mode    string                `json:"mode"`
	Verb    string                `json:"verb"`
	Status  string                `json:"status"`
	Startup lifecycle.StartupKind `json:"startup,omitempty"`
	URL     string                `json:"url,omitempty"`
	Stopped *bool                 `json:"stopped,omitempty"`

	readiness     string
	nextSelection *instanceSelection
	nextVerbs     []string
}

func (r lifecycleActionResult) RenderText(w io.Writer) error {
	switch r.Status {
	case "unchanged":
		return nil
	case "started", "restarted":
		if _, err := fmt.Fprintf(w, "%s started.\n", capitalize(r.Mode)); err != nil {
			return err
		}
		if r.URL != "" {
			if r.readiness == lifecycle.ReadinessReadyForSetup {
				if _, err := fmt.Fprintf(w, "Open %s in your browser to create the first administrator.\n", r.URL); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(w, "Open %s in your browser.\n", r.URL); err != nil {
					return err
				}
			}
		}
	case "stopped":
		if _, err := fmt.Fprintln(w, "Stopped."); err != nil {
			return err
		}
	case "not_running":
		if _, err := fmt.Fprintln(w, "Not running."); err != nil {
			return err
		}
	case "configured":
		if _, err := fmt.Fprintf(w, "Configured. %s starts automatically at %s.\n", capitalize(r.Mode), r.Startup); err != nil {
			return err
		}
	case "disabled":
		if _, err := fmt.Fprintln(w, "Startup configuration removed. Application data, worker credentials, and logs are preserved."); err != nil {
			return err
		}
	case "not_configured":
		if _, err := fmt.Fprintln(w, "No startup configuration."); err != nil {
			return err
		}
	}
	if r.nextSelection != nil && len(r.nextVerbs) > 0 {
		printNextCommandsTo(w, *r.nextSelection, r.nextVerbs...)
	}
	return nil
}

func lifecycleProseOut(printer *cli.Printer) io.Writer {
	if printer != nil && printer.Format != cli.FormatText {
		return io.Discard
	}
	if printer != nil && printer.Out != nil {
		return printer.Out
	}
	return os.Stdout
}

func lifecycleErrOut(printer *cli.Printer) io.Writer {
	if printer != nil && printer.Err != nil {
		return printer.Err
	}
	return os.Stderr
}

// runLifecycleVerb runs the verb of line. An end of ctx, such as an
// interrupt, ends every wait of the verb. `run` handles its shutdown signals
// itself, so it runs under the context of deps instead.
func runLifecycleVerb(ctx context.Context, printer *cli.Printer, in io.Reader, line lifecycleCommandLine, deps modeDependencies) (any, error) {
	if in == nil {
		in = os.Stdin
	}
	switch line.verb() {
	case "start", "restart":
		return executeNativeStart(ctx, printer, in, line, deps.controller)
	case "stop":
		return stopLifecycleInstance(ctx, printer, line, deps.controller)
	case "status":
		return nil, executeModeStatus(ctx, printer, line, deps.controller)
	case "logs":
		return nil, runLifecycleLogs(ctx, printer, line)
	case "enable":
		return enableLifecycleService(ctx, printer, in, line, deps.controller)
	case "disable":
		return disableLifecycleService(ctx, printer, line, deps.controller)
	case "run":
		return nil, runLifecycleForeground(line, deps.foreground)
	default:
		return nil, fmt.Errorf("leapmux daemon %s has no command %q", line.mode(), line.verb())
	}
}

// lifecycleExamples gives the examples of `leapmux daemon <mode> <verb>`.
func lifecycleExamples(mode, verb string) []cli.Example {
	switch verb {
	case "start":
		return []cli.Example{
			{Command: "leapmux daemon " + mode + " start"},
			{Comment: "Start at computer start, under this account, without a prompt.", Command: "leapmux daemon " + mode + " start --startup boot --yes"},
		}
	case "status":
		return []cli.Example{
			{Command: "leapmux daemon " + mode + " status"},
			{Comment: "Read the status from a script.", Command: "leapmux daemon " + mode + " status --json"},
		}
	case "logs":
		return []cli.Example{
			{Command: "leapmux daemon " + mode + " logs"},
			{Comment: "Follow new log lines as they arrive.", Command: "leapmux daemon " + mode + " logs -f -n 50"},
		}
	case "run":
		return []cli.Example{{Comment: "Run in the foreground of a container.", Command: "leapmux daemon " + mode + " run --data-dir /data"}}
	default:
		return []cli.Example{{Command: "leapmux daemon " + mode + " " + verb}}
	}
}

// lifecycleSummary is the one-line description of a lifecycle command. The
// mode's command list and the command's own --help both print it. A verb
// without a summary is a programming error, so it panics rather than lend
// another command's text.
func lifecycleSummary(mode, verb string) string {
	switch verb {
	case "run":
		switch mode {
		case "hub":
			return "Run the Hub in the foreground, for a container (never installs a service)"
		case "worker":
			return "Run a registered Worker in the foreground, for a container (never installs a service)"
		case "solo":
			return "Run Hub + Worker in the foreground, for a container (never installs a service)"
		default:
			return "Run Hub + Worker for development in the foreground, for a container (never installs a service)"
		}
	case "start":
		return "Start as a background service and wait until it is ready"
	case "stop":
		return "Stop the instance and wait until its shutdown completes"
	case "restart":
		return "Stop the instance, then start it as a background service"
	case "status":
		return "Report the process state, readiness, registration, and diagnostics"
	case "logs":
		return "Show or follow the diagnostic logs of the instance"
	case "enable":
		return "Install the service for startup at login or boot, without starting it"
	case "disable":
		return "Remove the service (requires a stopped instance)"
	default:
		panic(fmt.Sprintf("lifecycle command %q has no summary", verb))
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

type runInvocation struct {
	Context     context.Context
	OnReady     func()
	ServiceName string
}

// runLifecycleForeground runs the mode in the foreground under the common
// runtime: the directory leases, the runtime lock, the attempt identity, the
// private control interface, and the diagnostics log all belong to it. It
// never configures a service and never requests elevation.
func runLifecycleForeground(line lifecycleCommandLine, invocation runInvocation) (result error) {
	mode := line.mode()
	configuration, _, err := line.load()
	var managed *lifecycle.ServiceSpec
	var effective []string
	if errors.Is(err, errManagedRunSelected) {
		managed, effective, err = managedRun(mode, line.flags.managed)
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
		configuration, _, err = loadLifecycleCommand(mode, "run", effective)
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
		runtime, err = lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: configuration.role, DataDir: dataDir, Execution: execution})
	}
	if err != nil {
		return err
	}
	execute := func(ctx context.Context, runtime *lifecycle.Runtime) error {
		// The capture is active here, so the banner reaches both the
		// console and the attempt log, which a service launch reads.
		printModeBanner(mode)
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
// restart spends ONE budget on shutdown and startup together, so its
// budget is the sum of the other two.
const (
	defaultStartTimeout = 60 * time.Second
	// stop waits as long as launchd and systemd give a shutdown before they
	// send SIGKILL, so it never gives up on a shutdown that can still finish.
	defaultStopTimeout    = lifecycle.ShutdownTimeout
	defaultRestartTimeout = defaultStopTimeout + defaultStartTimeout
	defaultStatusTimeout  = 3 * time.Second
)

// stopLifecycleInstance stops the running instance and waits for the FULL
// shutdown, including the database close. An already stopped instance
// succeeds.
func stopLifecycleInstance(ctx context.Context, printer *cli.Printer, line lifecycleCommandLine, newController nativeControllerFactory) (lifecycleActionResult, error) {
	mode := line.mode()
	configuration, options, err := openLifecycleCommand(printer, line)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	stopped, err := stopLifecycleInstanceWithController(ctx, lifecycleErrOut(printer), configuration, newController)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	status := "not_running"
	if stopped {
		status = "stopped"
	}
	nextSel, nextVerbs := selectedNextCommands(mode, configuration.configurationFile, configuration.dataDirectory, newController, []string{"start"}, []string{"disable"})
	return lifecycleActionResult{
		Mode:          mode,
		Verb:          "stop",
		Status:        status,
		Stopped:       &stopped,
		nextSelection: nextSel,
		nextVerbs:     nextVerbs,
	}, nil
}

func stopLifecycleInstanceWithController(ctx context.Context, errOut io.Writer, configuration *lifecycleConfiguration, newController nativeControllerFactory) (bool, error) {
	active, err := stopLifecycleEvidence(ctx, configuration, newController)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && errOut != nil {
			_, _ = fmt.Fprintf(errOut, "Shutdown could not be confirmed. See `leapmux daemon %s status`.\n", configuration.role)
		}
		return active, err
	}
	return active, nil
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
func printStatusReport(w io.Writer, mode string, status lifecycle.Status, diagnosticExcerpt string) {
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprintf(w, "%s: %s\n", capitalize(mode), capitalize(status.Process.State))
	_, _ = fmt.Fprintf(w, "Configured:    %t\n", status.Configured)
	if status.Startup != nil {
		_, _ = fmt.Fprintf(w, "Startup:       %s\n", capitalize(string(*status.Startup)))
	}
	printStatusValue(w, "Execution", status.Execution)
	printStatusValue(w, "PID", status.Process.PID)
	printStatusValue(w, "Started at", status.Process.StartedAt)
	printStatusValue(w, "Readiness", status.Readiness)
	printStatusValue(w, "URL", status.URL)
	if status.Registration.State != lifecycle.RegistrationNotApplicable {
		_, _ = fmt.Fprintf(w, "Registration:  %s\n", capitalize(status.Registration.State))
		printStatusValue(w, "Worker ID", status.Registration.WorkerID)
		printStatusValue(w, "Hub", status.Registration.HubURL)
	}
	if status.Connection.State != lifecycle.ConnectionNotApplicable {
		_, _ = fmt.Fprintf(w, "Connection:    %s\n", capitalize(status.Connection.State))
		printStatusValue(w, "Last error", status.Connection.LastError)
	}
	printStatusValue(w, "Attempt", status.Attempt.ID)
	printStatusValue(w, "Attempt start", status.Attempt.StartedAt)
	printStatusValue(w, "Ended at", status.Attempt.EndedAt)
	printStatusValue(w, "Exit code", status.Attempt.ExitCode)
	printStatusValue(w, "Reason", status.Attempt.Error)
	for _, d := range status.Diagnostics {
		_, _ = fmt.Fprintf(w, "\n%s:\n%s\n", d.Title, d.Body)
	}
	if diagnosticExcerpt != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", diagnosticExcerpt)
	}
	printStatusValue(w, "Full log", status.Attempt.LogPath)
}

func printStatusValue[T any](w io.Writer, label string, value *T) {
	if value != nil {
		_, _ = fmt.Fprintf(w, "%-14s%v\n", label+":", *value)
	}
}
