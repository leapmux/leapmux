package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
		Manager:         platformManager(),
		Platform:        runtime.GOOS,
		NativeMechanism: nativeMechanismName(),
		SaveSpec: func(spec lifecycle.ServiceSpec) error {
			return lifecycle.WriteServiceSpec(paths.ServiceSpec, spec)
		},
		ReadSpec: func() (lifecycle.ServiceSpec, error) {
			return lifecycle.ReadServiceSpec(paths.ServiceSpec)
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
	executable, err := os.Executable()
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	resolved, err := exec.LookPath(executable)
	if err == nil {
		executable = resolved
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

// runModeEnableWithController configures automatic startup WITHOUT starting now. It
// supports repairing credentials and native definitions of an existing
// configuration; changing the startup mode, account, executable, or
// runtime configuration requires a stopped instance.
func runModeEnableWithController(mode string, newController nativeControllerFactory) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) (result error) {
		configuration, options, err := loadLifecycleCommand(mode, "enable", args)
		if handleVersionQuery(err) {
			return nil
		}
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
		selection, err := selectNativeStartup(controller, options.startup, options.yes)
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
		change, err := controller.Preview(context.Background(), spec)
		if err != nil {
			return err
		}
		if err := printChangePreview(os.Stdout, mode, change); err != nil {
			return err
		}
		if !options.yes {
			if err := requireInteractiveInput("--yes"); err != nil {
				return err
			}
			accepted, err := promptApply(os.Stdout, os.Stdin)
			if err != nil {
				return err
			}
			if !accepted {
				fmt.Println("Configuration unchanged.")
				return nil
			}
		}
		if err := controller.Apply(context.Background(), spec); err != nil {
			return err
		}
		fmt.Printf("Configured. %s starts automatically at %s.\n", capitalize(mode), selection.startup)
		return nil
	}
}

// runModeDisableWithController removes the native startup configuration. It refuses
// while an instance runs or starts, and preserves the data, the worker
// credentials, and the logs.
func runModeDisableWithController(mode string, newController nativeControllerFactory) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) (result error) {
		configuration, _, err := loadLifecycleCommand(mode, "disable", args)
		if handleVersionQuery(err) {
			return nil
		}
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
			return fmt.Errorf("the instance is running or starting; stop it with `leapmux %s stop` before disabling", mode)
		}
		controller, err := newController(mode, configuration.dataDirectory)
		if err != nil {
			return err
		}
		if _, err := controller.ReadSpec(); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("No startup configuration.")
				return nil
			}
			return err
		}
		directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(configuration.role, configuration.dataDirectory), configuration.role, configuration.dataDirectory)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, directory.Close()) }()
		if err := controller.Remove(context.Background()); err != nil {
			return err
		}
		fmt.Println("Startup configuration removed. Application data, worker credentials, and logs are preserved.")
		return nil
	}
}

// acquireNativeStartup checks prerequisites and retains startup ownership.
func acquireNativeStartup(configuration *lifecycleConfiguration, restart bool) (*lifecycle.DirectoryLease, error) {
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
			return nil, fmt.Errorf("the instance is already running; use `leapmux %s status` or `leapmux %s restart`", configuration.role, configuration.role)
		}
		if configuration.role == lifecycle.RoleWorker {
			ctx, cancel := context.WithTimeout(context.Background(), defaultStatusTimeout)
			defer cancel()
			status, err := lifecycle.Observe(ctx, configuration.dataDirectory)
			if err != nil {
				return nil, err
			}
			if status.Registration.State != lifecycle.RegistrationRegistered {
				return nil, fmt.Errorf("worker is unregistered: run `leapmux worker register` before restarting")
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

// executeNativeStartWithController starts through the native manager. Restart
// shares the operation lock and runtime wait budget across stop and start.
func executeNativeStartWithController(mode, verb string, args []string, newController nativeControllerFactory) (result error) {
	configuration, options, err := loadLifecycleCommand(mode, verb, args)
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
	directory, err := acquireNativeStartup(configuration, verb == "restart")
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
	selection, err := selectNativeStartup(controller, options.startup, options.yes)
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
		native, inspectErr := controller.Manager.Inspect(context.Background(), launch)
		if inspectErr != nil {
			return inspectErr
		}
		needsConfiguration = !native.Installed || !native.DefinitionMatches
	}
	if needsConfiguration {
		change, previewErr := controller.Preview(context.Background(), spec)
		if previewErr != nil {
			return previewErr
		}
		if err := printChangePreview(os.Stdout, mode, change); err != nil {
			return err
		}
		if !options.yes {
			if err := requireInteractiveInput("--yes"); err != nil {
				return err
			}
			accepted, promptErr := promptApply(os.Stdout, os.Stdin)
			if promptErr != nil {
				return promptErr
			}
			if !accepted {
				fmt.Println("Configuration unchanged; nothing started.")
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
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		stopErr := stopLifecycleInstanceWithController(ctx, configuration, newController)
		cancel()
		if stopErr != nil {
			return stopErr
		}
		remaining -= time.Since(stopStarted)
		if remaining <= 0 {
			return fmt.Errorf("restart shutdown exhausted the wait budget: %w", context.DeadlineExceeded)
		}
		if directory == nil {
			directory, err = acquireNativeStartup(configuration, false)
			if err != nil {
				return err
			}
		}
	}
	// Consent and native configuration authorization do not consume the runtime wait budget.
	if needsConfiguration {
		if err := controller.Apply(context.Background(), spec); err != nil {
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
		if err := authorizer.AuthorizeStart(context.Background(), launch); err != nil {
			return err
		}
	}
	if directory != nil {
		if err := directory.Close(); err != nil {
			return err
		}
		directory = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	defer cancel()
	if err := controller.Manager.Start(ctx, launch); err != nil {
		notes, diagnosticErr := controller.Manager.Diagnose(ctx, launch)
		for _, note := range notes {
			fmt.Println(note)
		}
		if diagnosticErr != nil {
			fmt.Fprintf(os.Stderr, "Cannot fetch native startup diagnostics: %v\n", diagnosticErr)
		}
		return err
	}
	deadline, _ := ctx.Deadline()
	remaining = time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("native startup exhausted the wait budget: %w", context.DeadlineExceeded)
	}
	return waitForReadinessObserved(mode, configuration.dataDirectory, remaining, previous.AttemptID, lifecycle.ExecutionService, func(ctx context.Context) (lifecycle.Status, error) {
		return observeNativeStartupWithController(ctx, configuration, newController)
	})
}

// waitForReadiness polls the control interface for readiness within
// the budget, distinguishing a startup failure from a wait timeout
// while startup continues.
func waitForReadiness(mode, canonical string, timeout waitBudget) error {
	return waitForReadinessObserved(mode, canonical, timeout, "", lifecycle.ExecutionForeground, func(ctx context.Context) (lifecycle.Status, error) { return lifecycle.Observe(ctx, canonical) })
}

func waitForReadinessObserved(mode, canonical string, timeout waitBudget, previousAttempt string, expectedExecution lifecycle.ExecutionKind, observe func(context.Context) (lifecycle.Status, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	paths := lifecycle.PathsOf(canonical)
	tick := newTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		status, err := observe(ctx)
		freshAttempt := status.Attempt.ID != nil && *status.Attempt.ID != previousAttempt
		if errors.Is(err, errNativeStartupFailed) {
			if freshAttempt {
				return errors.Join(reportStartupFailure(mode, paths, status), err)
			}
			// A child that never published has no new application attempt.
			// Historical logs and errors belong to the previous process.
			fmt.Printf("%s failed to start: %v\n", capitalize(mode), err)
			return fmt.Errorf("%s failed to start: %w", mode, err)
		}
		if err == nil && (previousAttempt == "" || freshAttempt) {
			switch {
			case status.Process.State == lifecycle.ProcessRunning && (status.Readiness != nil && (*status.Readiness == lifecycle.ReadinessReady || *status.Readiness == lifecycle.ReadinessReadyForSetup)):
				actualExecution := "unknown"
				if status.Execution != nil {
					actualExecution = string(*status.Execution)
				}
				if status.Execution == nil || *status.Execution != expectedExecution {
					return fmt.Errorf("%s became ready with %q execution, but %q execution was requested; inspect it with `leapmux %s status`", mode, actualExecution, expectedExecution, mode)
				}
				fmt.Printf("%s started.\n", capitalize(mode))
				return nil
			case status.Readiness != nil && *status.Readiness == lifecycle.ReadinessFailed:
				return reportStartupFailure(mode, paths, status)
			case status.Process.State == lifecycle.ProcessFailed:
				return reportStartupFailure(mode, paths, status)
			case status.Process.State == lifecycle.ProcessStopped && status.Attempt.EndedAt != nil:
				return fmt.Errorf("%s exited before it became ready", mode)
			}
		}
		select {
		case <-ctx.Done():
			fmt.Printf("Startup wait expired after %s; startup may continue. See `leapmux %s status`.\n", timeout, mode)
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

func selectNativeStartup(controller *servicelaunch.Controller, requested lifecycle.StartupKind, yes bool) (nativeStartupSelection, error) {
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
		return nativeStartupSelection{}, fmt.Errorf("--startup is required: no startup mode is configured for this instance yet")
	}
	if err := requireInteractiveInput("--startup=login or --startup=boot", "--yes"); err != nil {
		return nativeStartupSelection{}, err
	}
	selection.startup, err = promptStartupChoice(os.Stdout, os.Stdin)
	return selection, err
}
