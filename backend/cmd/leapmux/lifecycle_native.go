package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/cli"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
)

// newTicker builds the readiness poll's ticker.
var newTicker = time.NewTicker

// The configure-side lifecycle verbs: start, restart, enable, disable.
// They share the directory's operation lock, the saved service
// specification, the platform's native manager, and the interactive
// consent flow in lifecycle_prompt.go.

// nativeControllerFactory resolves the native control boundary for one instance.
type nativeControllerFactory func(mode, dataDirectory string) (*servicelaunch.Controller, error)

// nativeController wires the shared controller for one mode's data
// directory: the platform manager, the saved specification under the
// directory's lifecycle area, and the preview/apply flow.
func nativeController(mode string, dataDir string) (*servicelaunch.Controller, error) {
	canonical, err := lifecycle.CanonicalDir(dataDir)
	if err != nil {
		return nil, err
	}
	paths := lifecycle.PathsOf(canonical)
	return &servicelaunch.Controller{
		Manager:  platformManagerFor(),
		Platform: runtime.GOOS,
		SaveSpec: func(spec lifecycle.ServiceSpec) error {
			return lifecycle.WriteServiceSpec(paths.ServiceSpec, spec)
		},
		ReadSpec: func() (lifecycle.ServiceSpec, error) {
			return lifecycle.ReadServiceSpecOf(paths)
		},
		RemoveSpec: func() error {
			return os.Remove(paths.ServiceSpec)
		},
	}, nil
}

func serviceSpecFromConfiguration(configuration *lifecycleConfiguration, options lifecycleOptions, startup lifecycle.StartupKind) (lifecycle.ServiceSpec, error) {
	executable, err := currentExecutable()
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	accountID := lifecycle.CurrentAccountID()
	spec := lifecycle.ServiceSpec{
		InstanceID:         string(lifecycle.NewInstanceID(accountID, configuration.role, configuration.dataDirectory)),
		Mode:               configuration.role,
		Startup:            startup,
		AccountID:          accountID,
		AccountName:        currentAccountName(),
		Executable:         executable,
		WorkingDirectory:   home,
		ConfigFile:         configuration.configurationFile,
		DataDirectory:      configuration.dataDirectory,
		RuntimeArguments:   configuration.runtimeArguments,
		RuntimeEnvironment: map[string]string{"PATH": os.Getenv("PATH")},
	}
	if options.environmentFile != "" {
		path, pathErr := filepath.Abs(internalconfig.ExpandHome(options.environmentFile))
		if pathErr != nil {
			return lifecycle.ServiceSpec{}, pathErr
		}
		spec.EnvironmentFile = &path
		if err := selectServiceEnvironmentFile(&spec); err != nil {
			return lifecycle.ServiceSpec{}, err
		}
	}
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	if _, err := launch.LoadEnvironment(); err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	return spec, nil
}

// enableLifecycleService configures automatic startup WITHOUT starting now.
// It supports repairing credentials and native definitions of an existing
// configuration; changing the startup mode, account, executable, or runtime
// configuration requires a stopped instance.
func enableLifecycleService(ctx context.Context, printer *cli.Printer, in io.Reader, line lifecycleCommandLine, newController nativeControllerFactory) (res lifecycleActionResult, result error) {
	mode := line.mode()
	configuration, options, err := openLifecycleCommand(printer, line)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if err := validateLifecycleSelection(configuration); err != nil {
		return lifecycleActionResult{}, err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer release()
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	errOut := lifecycleErrOut(printer)
	selection, err := selectNativeStartup(ctx, errOut, in, controller, options.startup, options.yes)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	spec, err := commandServiceSpec(configuration, options, selection.startup, selection.saved, true)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(configuration.dataDirectory))
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if held && (selection.saved == nil || !selection.saved.Equal(spec)) {
		return lifecycleActionResult{}, fmt.Errorf("the instance is running or starting; stop it before changing its startup configuration")
	}
	if !held {
		directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
		if err != nil {
			return lifecycleActionResult{}, err
		}
		defer func() { result = errors.Join(result, directory.Close()) }()
	}
	change, err := controller.Preview(ctx, spec)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	accepted, err := confirmChangePreview(ctx, printer, errOut, in, mode, change, options.yes, "Configuration unchanged.")
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if !accepted {
		return lifecycleActionResult{
			Mode:    mode,
			Verb:    "enable",
			Status:  "unchanged",
			Startup: selection.startup,
		}, nil
	}
	// An interrupt never stops a change halfway: the change completes, and
	// the command ends after it.
	if err := controller.Apply(context.WithoutCancel(ctx), spec); err != nil {
		return lifecycleActionResult{}, err
	}
	return lifecycleActionResult{
		Mode:          mode,
		Verb:          "enable",
		Status:        "configured",
		Startup:       selection.startup,
		nextSelection: new(serviceSelection(spec)),
		nextVerbs:     []string{"start", "status", "disable"},
	}, nil
}

// disableLifecycleService removes the native startup configuration. It
// refuses while an instance runs or starts, and preserves the data, the
// worker credentials, and the logs.
func disableLifecycleService(ctx context.Context, printer *cli.Printer, line lifecycleCommandLine, newController nativeControllerFactory) (res lifecycleActionResult, result error) {
	mode := line.mode()
	configuration, _, err := openLifecycleCommand(printer, line)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if err := validateLifecycleSelection(configuration); err != nil {
		return lifecycleActionResult{}, err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer release()
	held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(configuration.dataDirectory))
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if held {
		return lifecycleActionResult{}, fmt.Errorf("the instance is running or starting; stop it with `leapmux daemon %s stop` before disabling", mode)
	}
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	spec, err := controller.ReadSpec()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			nextSel, nextVerbs := selectedNextCommands(mode, configuration.configurationFile, configuration.dataDirectory, newController, []string{"start", "enable"}, nil)
			return lifecycleActionResult{
				Mode:          mode,
				Verb:          "disable",
				Status:        "not_configured",
				nextSelection: nextSel,
				nextVerbs:     nextVerbs,
			}, nil
		}
		return lifecycleActionResult{}, err
	}
	directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	// An interrupt never stops the removal halfway.
	if err := controller.Remove(context.WithoutCancel(ctx)); err != nil {
		return lifecycleActionResult{}, err
	}
	// The removed specification still selects the instance: a later start
	// recreates the service with the configuration file it used.
	return lifecycleActionResult{
		Mode:          mode,
		Verb:          "disable",
		Status:        "disabled",
		Startup:       spec.Startup,
		nextSelection: new(serviceSelection(spec)),
		nextVerbs:     []string{"start", "enable"},
	}, nil
}

// acquireNativeStartup checks prerequisites and retains startup ownership.
func acquireNativeStartup(ctx context.Context, configuration *lifecycleConfiguration, restart bool) (*lifecycle.DirectoryLease, error) {
	if err := validateLifecycleSelection(configuration); err != nil {
		return nil, err
	}
	paths := lifecycle.PathsOf(configuration.dataDirectory)
	held, err := lifecycle.ProbeRuntimeLock(paths)
	if err != nil {
		return nil, err
	}
	if held {
		if !restart {
			return nil, fmt.Errorf("the instance is already running; use `leapmux daemon %s status` or `leapmux daemon %s restart`", configuration.role, configuration.role)
		}
		if configuration.role == lifecycle.RoleWorker {
			ctx, cancel := context.WithTimeout(ctx, defaultStatusTimeout)
			defer cancel()
			status, err := lifecycle.Observe(ctx, configuration.dataDirectory)
			if err != nil {
				return nil, err
			}
			if status.Registration.State != lifecycle.RegistrationRegistered {
				return nil, fmt.Errorf("worker is unregistered: run `leapmux daemon worker register` before restarting")
			}
		}
		return nil, nil
	}
	directory, err := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
	if err != nil {
		return nil, err
	}
	if err := configuration.checkStartup(directory); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

// executeNativeStart starts through the native manager. Restart shares the
// operation lock and runtime wait budget across stop and start.
func executeNativeStart(ctx context.Context, printer *cli.Printer, in io.Reader, line lifecycleCommandLine, newController nativeControllerFactory) (res lifecycleActionResult, result error) {
	mode, verb := line.mode(), line.verb()
	configuration, options, err := openLifecycleCommand(printer, line)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer release()
	directory, err := acquireNativeStartup(ctx, configuration, verb == "restart")
	if err != nil {
		return lifecycleActionResult{}, err
	}
	defer func() {
		if directory != nil {
			result = errors.Join(result, directory.Close())
		}
	}()
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	errOut := lifecycleErrOut(printer)
	selection, err := selectNativeStartup(ctx, errOut, in, controller, options.startup, options.yes)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	spec, err := commandServiceSpec(configuration, options, selection.startup, selection.saved, false)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	needsConfiguration := selection.saved == nil || !selection.saved.Equal(spec)
	if !needsConfiguration {
		launch, convertErr := servicelaunch.LaunchSpecFromService(spec)
		if convertErr != nil {
			return lifecycleActionResult{}, convertErr
		}
		native, inspectErr := controller.Manager.Inspect(ctx, launch)
		if inspectErr != nil {
			return lifecycleActionResult{}, inspectErr
		}
		needsConfiguration = !native.Installed || !native.DefinitionMatches
	}
	if needsConfiguration {
		change, previewErr := controller.Preview(ctx, spec)
		if previewErr != nil {
			return lifecycleActionResult{}, previewErr
		}
		accepted, confirmErr := confirmChangePreview(ctx, printer, errOut, in, mode, change, options.yes, "Configuration unchanged; nothing started.")
		if confirmErr != nil {
			return lifecycleActionResult{}, confirmErr
		}
		if !accepted {
			return lifecycleActionResult{
				Mode:    mode,
				Verb:    verb,
				Status:  "unchanged",
				Startup: selection.startup,
			}, nil
		}
	}
	previous, previousErr := lifecycle.ReadRuntimeRecord(lifecycle.PathsOf(configuration.dataDirectory).RuntimeRecord)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return lifecycleActionResult{}, previousErr
	}
	remaining := options.timeout
	var stoppedPtr *bool
	if verb == "restart" {
		stopStarted := time.Now()
		stopCtx, cancel := context.WithTimeout(ctx, remaining)
		stopped, stopErr := stopLifecycleInstanceWithController(stopCtx, errOut, configuration, newController)
		cancel()
		if stopErr != nil {
			return lifecycleActionResult{}, stopErr
		}
		stoppedPtr = &stopped
		if printer == nil || printer.Format == cli.FormatText {
			if stopped {
				_, _ = fmt.Fprintln(errOut, "Stopped.")
			} else {
				_, _ = fmt.Fprintln(errOut, "Not running.")
			}
		}
		remaining -= time.Since(stopStarted)
		if remaining <= 0 {
			return lifecycleActionResult{}, fmt.Errorf("restart shutdown exhausted the wait budget: %w", context.DeadlineExceeded)
		}
		if directory == nil {
			directory, err = acquireNativeStartup(ctx, configuration, false)
			if err != nil {
				return lifecycleActionResult{}, err
			}
		}
	}
	// Consent and native configuration authorization do not consume the runtime wait budget.
	if needsConfiguration {
		// An interrupt never stops a change halfway.
		if err := controller.Apply(context.WithoutCancel(ctx), spec); err != nil {
			return lifecycleActionResult{}, err
		}
	}
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return lifecycleActionResult{}, err
	}
	if _, err := launch.LoadEnvironment(); err != nil {
		return lifecycleActionResult{}, err
	}
	if authorizer, ok := controller.Manager.(servicelaunch.StartAuthorizer); ok {
		if err := authorizer.AuthorizeStart(ctx, launch); err != nil {
			return lifecycleActionResult{}, err
		}
	}
	if directory != nil {
		if err := directory.Close(); err != nil {
			return lifecycleActionResult{}, err
		}
		directory = nil
	}
	spin := cli.StartSpinner(printer, "starting "+mode+" daemon...")
	defer spin.Stop()
	startCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := controller.Manager.Start(startCtx, launch); err != nil {
		spin.Stop()
		notes, diagnosticErr := controller.Manager.Diagnose(startCtx, launch)
		for _, note := range notes {
			_, _ = fmt.Fprintln(errOut, note)
		}
		if diagnosticErr != nil {
			_, _ = fmt.Fprintf(errOut, "Cannot fetch native startup diagnostics: %v\n", diagnosticErr)
		}
		return lifecycleActionResult{}, err
	}
	deadline, _ := startCtx.Deadline()
	remaining = time.Until(deadline)
	if remaining <= 0 {
		spin.Stop()
		return lifecycleActionResult{}, fmt.Errorf("native startup exhausted the wait budget: %w", context.DeadlineExceeded)
	}
	readyStatus, err := waitForReadinessWithOutput(ctx, readinessWaitOptions{
		out:               errOut,
		beforePrint:       spin.Stop,
		mode:              mode,
		timeout:           remaining,
		previousAttempt:   previous.AttemptID,
		expectedExecution: lifecycle.ExecutionService,
		observe: func(ctx context.Context) (lifecycle.Status, error) {
			return observeNativeStartupWithController(ctx, configuration, newController)
		},
	})
	if err != nil {
		return lifecycleActionResult{}, err
	}
	statusWord := "started"
	if verb == "restart" {
		statusWord = "restarted"
	}
	var accessURL, readiness string
	if readyStatus.URL != nil {
		accessURL = *readyStatus.URL
	}
	if readyStatus.Readiness != nil {
		readiness = *readyStatus.Readiness
	}
	return lifecycleActionResult{
		Mode:          mode,
		Verb:          verb,
		Status:        statusWord,
		Startup:       selection.startup,
		URL:           accessURL,
		Stopped:       stoppedPtr,
		readiness:     readiness,
		nextSelection: new(serviceSelection(spec)),
		nextVerbs:     []string{"status", "stop", "disable"},
	}, nil
}

// confirmChangePreview displays the proposed native service change on stderr
// and asks for interactive confirmation unless yes is set.
func confirmChangePreview(ctx context.Context, printer *cli.Printer, promptOut io.Writer, in io.Reader, mode string, change servicelaunch.ConfigurationChange, yes bool, declineMessage string) (bool, error) {
	if !yes || printer == nil || printer.Format == cli.FormatText {
		if err := printChangePreview(promptOut, mode, change); err != nil {
			return false, err
		}
	}
	if yes {
		return true, nil
	}
	if err := requireInteractiveInput("--yes"); err != nil {
		return false, err
	}
	if in == nil {
		in = os.Stdin
	}
	accepted, err := promptApply(ctx, promptOut, in)
	if err != nil {
		return false, err
	}
	if !accepted {
		_, _ = fmt.Fprintln(promptOut, declineMessage)
		return false, nil
	}
	return true, nil
}

type readinessWaitOptions struct {
	out               io.Writer
	beforePrint       func()
	mode              string
	timeout           time.Duration
	previousAttempt   string
	expectedExecution lifecycle.ExecutionKind
	observe           func(context.Context) (lifecycle.Status, error)
}

// waitForReadinessWithOutput polls observe until readiness succeeds, startup
// fails, or the timeout elapses. An end of parent, such as an interrupt, ends
// the wait with the cause of parent and no report: the command stops, not the
// startup.
func waitForReadinessWithOutput(parent context.Context, opts readinessWaitOptions) (lifecycle.Status, error) {
	out := opts.out
	if out == nil {
		out = os.Stderr
	}
	ctx, cancel := context.WithTimeout(parent, opts.timeout)
	defer cancel()
	tick := newTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		status, err := opts.observe(ctx)
		freshAttempt := status.Attempt.ID != nil && *status.Attempt.ID != opts.previousAttempt
		if errors.Is(err, errNativeStartupFailed) {
			if opts.beforePrint != nil {
				opts.beforePrint()
			}
			if freshAttempt {
				return lifecycle.Status{}, errors.Join(reportStartupFailure(out, opts.mode, status), err)
			}
			// A child that never published has no new application attempt.
			// Historical logs and errors belong to the previous process.
			_, _ = fmt.Fprintf(out, "%s failed to start: %v\n", capitalize(opts.mode), err)
			return lifecycle.Status{}, fmt.Errorf("%s failed to start: %w", opts.mode, err)
		}
		if err == nil && (opts.previousAttempt == "" || freshAttempt) {
			switch {
			case status.Process.State == lifecycle.ProcessRunning && (status.Readiness != nil && (*status.Readiness == lifecycle.ReadinessReady || *status.Readiness == lifecycle.ReadinessReadyForSetup)):
				if opts.beforePrint != nil {
					opts.beforePrint()
				}
				actualExecution := "unknown"
				if status.Execution != nil {
					actualExecution = string(*status.Execution)
				}
				if status.Execution == nil || *status.Execution != opts.expectedExecution {
					return lifecycle.Status{}, fmt.Errorf("%s became ready with %q execution, but %q execution was requested; inspect it with `leapmux daemon %s status`", opts.mode, actualExecution, opts.expectedExecution, opts.mode)
				}
				return status, nil
			case (status.Readiness != nil && *status.Readiness == lifecycle.ReadinessFailed) || status.Process.State == lifecycle.ProcessFailed:
				if opts.beforePrint != nil {
					opts.beforePrint()
				}
				return lifecycle.Status{}, reportStartupFailure(out, opts.mode, status)
			case status.Process.State == lifecycle.ProcessStopped && status.Attempt.EndedAt != nil:
				return lifecycle.Status{}, fmt.Errorf("%s exited before it became ready", opts.mode)
			}
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return lifecycle.Status{}, context.Cause(parent)
			}
			if opts.beforePrint != nil {
				opts.beforePrint()
			}
			_, _ = fmt.Fprintf(out, "Startup wait expired after %s; startup may continue. See `leapmux daemon %s status`.\n", opts.timeout, opts.mode)
			if freshAttempt && status.Attempt.LogPath != nil {
				if excerpt, excerptErr := lifecycle.ReadExcerpt(*status.Attempt.LogPath); excerptErr == nil && excerpt != "" {
					_, _ = fmt.Fprintf(out, "\nRecent startup logs:\n%s\n", excerpt)
				}
				_, _ = fmt.Fprintf(out, "Full log: %s\n", *status.Attempt.LogPath)
			}
			return lifecycle.Status{}, fmt.Errorf("startup readiness wait ended: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// reportStartupFailure prints the attempt's failure reason with its
// limited log excerpt and the complete log location.
func reportStartupFailure(out io.Writer, mode string, status lifecycle.Status) error {
	if out == nil {
		out = os.Stdout
	}
	_, _ = fmt.Fprintf(out, "%s failed to start.\n", capitalize(mode))
	if status.Attempt.LogPath != nil {
		excerpt, err := lifecycle.ReadExcerpt(*status.Attempt.LogPath)
		if err == nil && excerpt != "" {
			_, _ = fmt.Fprintln(out, "\nRecent startup logs:")
			for _, line := range strings.Split(strings.TrimSpace(excerpt), "\n") {
				_, _ = fmt.Fprintln(out, "  "+line)
			}
		}
		_, _ = fmt.Fprintf(out, "\nFull log: %s\n", *status.Attempt.LogPath)
	}
	for _, d := range status.Diagnostics {
		_, _ = fmt.Fprintf(out, "%s: %s\n", d.Title, d.Body)
	}
	if status.Attempt.Error != nil {
		return fmt.Errorf("%s failed to start: %s", mode, *status.Attempt.Error)
	}
	return fmt.Errorf("%s failed to start", mode)
}

// printChangePreview renders the change the controller proposes.
func printChangePreview(out io.Writer, mode string, change servicelaunch.ConfigurationChange) error {
	var text strings.Builder
	fmt.Fprintf(&text, "\nLeapMux will configure %s:\n", mode)
	for _, line := range change.Changes {
		fmt.Fprintf(&text, "  %s\n", line)
	}
	if change.RequiresElevation {
		fmt.Fprintf(&text, "\nAdministrator authorization is required: %s\n", change.ElevationReason)
	}
	return writeSetupText(out, text.String())
}

type nativeStartupSelection struct {
	saved   *lifecycle.ServiceSpec
	startup lifecycle.StartupKind
}

func selectNativeStartup(ctx context.Context, out io.Writer, in io.Reader, controller *servicelaunch.Controller, requested lifecycle.StartupKind, yes bool) (nativeStartupSelection, error) {
	existing, err := controller.ReadSpec()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nativeStartupSelection{}, err
	}
	selection := nativeStartupSelection{startup: requested}
	if err == nil {
		selection.saved = &existing
		if selection.startup == "" {
			selection.startup = existing.Startup
		}
	}
	if selection.startup != "" {
		return selection, nil
	}
	if yes {
		return nativeStartupSelection{}, cli.Usagef("--startup is required: no startup mode is configured for this instance yet").
			WithHint("give --startup login or --startup boot")
	}
	if err := requireInteractiveInput("--startup=login or --startup=boot", "--yes"); err != nil {
		return nativeStartupSelection{}, err
	}
	if out == nil {
		out = os.Stdout
	}
	if in == nil {
		in = os.Stdin
	}
	selection.startup, err = promptStartupChoice(ctx, out, in)
	return selection, err
}
