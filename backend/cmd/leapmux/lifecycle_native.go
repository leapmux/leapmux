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

// waitBudget is the duration type of the lifecycle verbs' --timeout
// flags.
type waitBudget = time.Duration

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
		Manager:  platformManager(),
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

// platformManager selects this platform's native adapter.
func platformManager() servicelaunch.Manager {
	return platformManagerFor()
}

// desiredSpec derives the specification an enable or start proposes:
// this executable, this configuration, this account, and the chosen
// startup kind.
func desiredSpec(mode string, startup lifecycle.StartupKind, dataDir string, configArgs []string) (lifecycle.ServiceSpec, error) {
	args := append([]string{"--data-dir", dataDir}, configArgs...)
	configuration, options, err := loadLifecycleCommand(mode, "start", args)
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	return serviceSpecFromConfiguration(configuration, options, startup)
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
func enableLifecycleService(ctx context.Context, out io.Writer, line lifecycleCommandLine, newController nativeControllerFactory) (result error) {
	mode := line.mode()
	configuration, options, err := openLifecycleCommand(line)
	if err != nil {
		return err
	}
	if err := validateLifecycleSelection(configuration); err != nil {
		return err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return err
	}
	defer release()
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return err
	}
	selection, err := selectNativeStartup(ctx, controller, options.startup, options.yes)
	if err != nil {
		return err
	}
	spec, err := commandServiceSpec(configuration, options, selection.startup, selection.saved, true)
	if err != nil {
		return err
	}
	held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(configuration.dataDirectory))
	if err != nil {
		return err
	}
	if held && (selection.saved == nil || !specMatchesSaved(*selection.saved, spec)) {
		return fmt.Errorf("the instance is running or starting; stop it before changing its startup configuration")
	}
	if !held {
		directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, directory.Close()) }()
	}
	change, err := controller.Preview(ctx, spec)
	if err != nil {
		return err
	}
	promptOut := out
	if promptOut == io.Discard {
		promptOut = os.Stderr
	}
	if !options.yes || out != io.Discard {
		if err := printChangePreview(promptOut, mode, change); err != nil {
			return err
		}
	}
	if !options.yes {
		if err := requireInteractiveInput("--yes"); err != nil {
			return err
		}
		accepted, err := promptApply(ctx, promptOut, os.Stdin)
		if err != nil {
			return err
		}
		if !accepted {
			_, _ = fmt.Fprintln(promptOut, "Configuration unchanged.")
			return nil
		}
	}
	// An interrupt never stops a change halfway: the change completes, and
	// the command ends after it.
	if err := controller.Apply(context.WithoutCancel(ctx), spec); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Configured. %s starts automatically at %s.\n", capitalize(mode), selection.startup)
	printNextCommandsTo(out, serviceSelection(spec), "start", "status", "disable")
	return nil
}

// disableLifecycleService removes the native startup configuration. It
// refuses while an instance runs or starts, and preserves the data, the
// worker credentials, and the logs.
func disableLifecycleService(ctx context.Context, out io.Writer, line lifecycleCommandLine, newController nativeControllerFactory) (result error) {
	mode := line.mode()
	configuration, _, err := openLifecycleCommand(line)
	if err != nil {
		return err
	}
	if err := validateLifecycleSelection(configuration); err != nil {
		return err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return err
	}
	defer release()
	held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(configuration.dataDirectory))
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("the instance is running or starting; stop it with `leapmux daemon %s stop` before disabling", mode)
	}
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return err
	}
	spec, err := controller.ReadSpec()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintln(out, "No startup configuration.")
			printSelectedNextCommandsTo(out, mode, configuration.configurationFile, configuration.dataDirectory, newController, []string{"start", "enable"}, nil)
			return nil
		}
		return err
	}
	directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	// An interrupt never stops the removal halfway.
	if err := controller.Remove(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Startup configuration removed. Application data, worker credentials, and logs are preserved.")
	// The removed specification still selects the instance: a later start
	// recreates the service with the configuration file it used.
	printNextCommandsTo(out, serviceSelection(spec), "start", "enable")
	return nil
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
func executeNativeStart(ctx context.Context, printer *cli.Printer, out io.Writer, line lifecycleCommandLine, newController nativeControllerFactory) (result error) {
	mode, verb := line.mode(), line.verb()
	configuration, options, err := openLifecycleCommand(line)
	if err != nil {
		return err
	}
	release, err := lifecycle.OperationLock(configuration.dataDirectory)
	if err != nil {
		return err
	}
	defer release()
	directory, err := acquireNativeStartup(ctx, configuration, verb == "restart")
	if err != nil {
		return err
	}
	defer func() {
		if directory != nil {
			result = errors.Join(result, directory.Close())
		}
	}()
	controller, err := newController(mode, configuration.dataDirectory)
	if err != nil {
		return err
	}
	selection, err := selectNativeStartup(ctx, controller, options.startup, options.yes)
	if err != nil {
		return err
	}
	spec, err := commandServiceSpec(configuration, options, selection.startup, selection.saved, false)
	if err != nil {
		return err
	}
	needsConfiguration := selection.saved == nil || !specMatchesSaved(*selection.saved, spec)
	if !needsConfiguration {
		launch, convertErr := servicelaunch.LaunchSpecFromService(spec)
		if convertErr != nil {
			return convertErr
		}
		native, inspectErr := controller.Manager.Inspect(ctx, launch)
		if inspectErr != nil {
			return inspectErr
		}
		needsConfiguration = !native.Installed || !native.DefinitionMatches
	}
	if needsConfiguration {
		change, previewErr := controller.Preview(ctx, spec)
		if previewErr != nil {
			return previewErr
		}
		promptOut := out
		if promptOut == io.Discard {
			promptOut = os.Stderr
		}
		if !options.yes || out != io.Discard {
			if err := printChangePreview(promptOut, mode, change); err != nil {
				return err
			}
		}
		if !options.yes {
			if err := requireInteractiveInput("--yes"); err != nil {
				return err
			}
			accepted, promptErr := promptApply(ctx, promptOut, os.Stdin)
			if promptErr != nil {
				return promptErr
			}
			if !accepted {
				_, _ = fmt.Fprintln(promptOut, "Configuration unchanged; nothing started.")
				return nil
			}
		}
	}
	previous, previousErr := lifecycle.ReadRuntimeRecord(lifecycle.PathsOf(configuration.dataDirectory).RuntimeRecord)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	remaining := options.timeout
	if verb == "restart" {
		stopStarted := time.Now()
		stopCtx, cancel := context.WithTimeout(ctx, remaining)
		stopErr := stopLifecycleInstanceWithController(stopCtx, out, configuration, newController)
		cancel()
		if stopErr != nil {
			return stopErr
		}
		remaining -= time.Since(stopStarted)
		if remaining <= 0 {
			return fmt.Errorf("restart shutdown exhausted the wait budget: %w", context.DeadlineExceeded)
		}
		if directory == nil {
			directory, err = acquireNativeStartup(ctx, configuration, false)
			if err != nil {
				return err
			}
		}
	}
	// Consent and native configuration authorization do not consume the runtime wait budget.
	if needsConfiguration {
		// An interrupt never stops a change halfway.
		if err := controller.Apply(context.WithoutCancel(ctx), spec); err != nil {
			return err
		}
	}
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return err
	}
	if _, err := launch.LoadEnvironment(); err != nil {
		return err
	}
	if authorizer, ok := controller.Manager.(servicelaunch.StartAuthorizer); ok {
		if err := authorizer.AuthorizeStart(ctx, launch); err != nil {
			return err
		}
	}
	if directory != nil {
		if err := directory.Close(); err != nil {
			return err
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
			_, _ = fmt.Fprintln(out, note)
		}
		if diagnosticErr != nil {
			fmt.Fprintf(os.Stderr, "Cannot fetch native startup diagnostics: %v\n", diagnosticErr)
		}
		return err
	}
	deadline, _ := startCtx.Deadline()
	remaining = time.Until(deadline)
	if remaining <= 0 {
		spin.Stop()
		return fmt.Errorf("native startup exhausted the wait budget: %w", context.DeadlineExceeded)
	}
	if err := waitForReadinessWithOutput(ctx, out, spin.Stop, mode, configuration.dataDirectory, remaining, previous.AttemptID, lifecycle.ExecutionService, func(ctx context.Context) (lifecycle.Status, error) {
		return observeNativeStartupWithController(ctx, configuration, newController)
	}); err != nil {
		return err
	}
	printNextCommandsTo(out, serviceSelection(spec), "status", "stop", "disable")
	return nil
}

// waitForReadiness polls the control interface for readiness within
// the budget, distinguishing a startup failure from a wait timeout
// while startup continues.
func waitForReadiness(ctx context.Context, mode, canonical string, timeout waitBudget) error {
	return waitForReadinessObserved(ctx, mode, canonical, timeout, "", lifecycle.ExecutionForeground, func(ctx context.Context) (lifecycle.Status, error) { return lifecycle.Observe(ctx, canonical) })
}

// An end of parent, such as an interrupt, ends the wait with the cause of
// parent and no report: the command stops, not the startup.
func waitForReadinessObserved(parent context.Context, mode, canonical string, timeout waitBudget, previousAttempt string, expectedExecution lifecycle.ExecutionKind, observe func(context.Context) (lifecycle.Status, error)) error {
	return waitForReadinessWithOutput(parent, os.Stdout, nil, mode, canonical, timeout, previousAttempt, expectedExecution, observe)
}

func waitForReadinessWithOutput(parent context.Context, out io.Writer, beforePrint func(), mode, canonical string, timeout waitBudget, previousAttempt string, expectedExecution lifecycle.ExecutionKind, observe func(context.Context) (lifecycle.Status, error)) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	paths := lifecycle.PathsOf(canonical)
	tick := newTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		status, err := observe(ctx)
		freshAttempt := status.Attempt.ID != nil && *status.Attempt.ID != previousAttempt
		if errors.Is(err, errNativeStartupFailed) {
			if beforePrint != nil {
				beforePrint()
			}
			if freshAttempt {
				return errors.Join(reportStartupFailure(mode, paths, status), err)
			}
			// A child that never published has no new application attempt.
			// Historical logs and errors belong to the previous process.
			_, _ = fmt.Fprintf(out, "%s failed to start: %v\n", capitalize(mode), err)
			return fmt.Errorf("%s failed to start: %w", mode, err)
		}
		if err == nil && (previousAttempt == "" || freshAttempt) {
			switch {
			case status.Process.State == lifecycle.ProcessRunning && (status.Readiness != nil && (*status.Readiness == lifecycle.ReadinessReady || *status.Readiness == lifecycle.ReadinessReadyForSetup)):
				if beforePrint != nil {
					beforePrint()
				}
				actualExecution := "unknown"
				if status.Execution != nil {
					actualExecution = string(*status.Execution)
				}
				if status.Execution == nil || *status.Execution != expectedExecution {
					return fmt.Errorf("%s became ready with %q execution, but %q execution was requested; inspect it with `leapmux daemon %s status`", mode, actualExecution, expectedExecution, mode)
				}
				_, _ = fmt.Fprintf(out, "%s started.\n", capitalize(mode))
				printAccessHint(out, status)
				return nil
			case status.Readiness != nil && *status.Readiness == lifecycle.ReadinessFailed:
				if beforePrint != nil {
					beforePrint()
				}
				return reportStartupFailure(mode, paths, status)
			case status.Process.State == lifecycle.ProcessFailed:
				if beforePrint != nil {
					beforePrint()
				}
				return reportStartupFailure(mode, paths, status)
			case status.Process.State == lifecycle.ProcessStopped && status.Attempt.EndedAt != nil:
				return fmt.Errorf("%s exited before it became ready", mode)
			}
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return context.Cause(parent)
			}
			fmt.Printf("Startup wait expired after %s; startup may continue. See `leapmux daemon %s status`.\n", timeout, mode)
			if freshAttempt && status.Attempt.LogPath != nil {
				if excerpt, excerptErr := lifecycle.ReadExcerpt(*status.Attempt.LogPath); excerptErr == nil && excerpt != "" {
					fmt.Printf("\nRecent startup logs:\n%s\n", excerpt)
				}
				fmt.Printf("Full log: %s\n", *status.Attempt.LogPath)
			}
			return fmt.Errorf("startup readiness wait ended: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// reportStartupFailure prints the attempt's failure reason with its
// limited log excerpt and the complete log location.
func reportStartupFailure(mode string, paths lifecycle.Paths, status lifecycle.Status) error {
	fmt.Printf("%s failed to start.\n", capitalize(mode))
	if status.Attempt.LogPath != nil {
		excerpt, err := lifecycle.ReadExcerpt(*status.Attempt.LogPath)
		if err == nil && excerpt != "" {
			fmt.Println("\nRecent startup logs:")
			for _, line := range strings.Split(strings.TrimSpace(excerpt), "\n") {
				fmt.Println("  " + line)
			}
		}
		fmt.Printf("\nFull log: %s\n", *status.Attempt.LogPath)
	}
	for _, d := range status.Diagnostics {
		fmt.Printf("%s: %s\n", d.Title, d.Body)
	}
	if status.Attempt.Error != nil {
		return fmt.Errorf("%s failed to start: %s", mode, *status.Attempt.Error)
	}
	return fmt.Errorf("%s failed to start", mode)
}

// specMatchesSaved states whether the desired specification and the
// saved one describe the same launch.
func specMatchesSaved(saved, desired lifecycle.ServiceSpec) bool {
	return saved.Equal(desired)
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

func selectNativeStartup(ctx context.Context, controller *servicelaunch.Controller, requested lifecycle.StartupKind, yes bool) (nativeStartupSelection, error) {
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
	selection.startup, err = promptStartupChoice(ctx, os.Stdout, os.Stdin)
	return selection, err
}
