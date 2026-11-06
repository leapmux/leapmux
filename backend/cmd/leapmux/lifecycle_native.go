package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
)

// waitBudget is the duration type of the lifecycle verbs' --timeout
// flags; a named type keeps the budget's meaning at each call site.
type waitBudget = time.Duration

// newTicker builds the readiness poll's ticker.
var newTicker = time.NewTicker

// The configure-side lifecycle verbs: start, restart, enable, disable.
// They share the directory's operation lock, the saved service
// specification, the platform's native manager, and the interactive
// consent flow in lifecycle_prompt.go.

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
	role := modeRole(mode)
	canonical, err := lifecycle.CanonicalDir(dataDir)
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	resolved, err := exec.LookPath(executable)
	if err == nil {
		executable = resolved
	}
	home, _ := os.UserHomeDir()
	configFile := configPathFromArgs(configArgs, defaultDataDirOf(mode), string(role))
	accountID := lifecycle.CurrentAccountID()
	accountName := currentAccountName()
	spec := lifecycle.ServiceSpec{
		InstanceID:       string(lifecycle.NewInstanceID(accountID, role, canonical)),
		Mode:             role,
		Startup:          startup,
		AccountID:        accountID,
		AccountName:      accountName,
		Executable:       executable,
		WorkingDirectory: home,
		ConfigFile:       configFile,
		DataDirectory:    canonical,
		RuntimeArguments: []string{},
	}
	if err := servicelaunch.ValidateLaunchSpec(mustConvert(spec)); err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	return spec, nil
}

// mustConvert validates and converts through the shared rule.
func mustConvert(spec lifecycle.ServiceSpec) servicelaunch.LaunchSpec {
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return servicelaunch.LaunchSpec{}
	}
	return launch
}

// configPathFromArgs reads --config from the mode's own flags, falling
// back to the mode's default file.
func configPathFromArgs(args []string, dataDir, mode string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-config":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	return dataDir + "/" + mode + ".yaml"
}

// runModeEnable configures automatic startup WITHOUT starting now. It
// supports repairing credentials and native definitions of an existing
// configuration; changing the startup mode, account, executable, or
// runtime configuration requires a stopped instance.
func runModeEnable(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		startup, rest, err := parseStartupFlag(args)
		if err != nil {
			return err
		}
		dataDir, cfgArgs, err := splitLifecycleArgs(rest)
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

		controller, err := nativeController(mode, dataDir)
		if err != nil {
			return err
		}

		existing, readErr := controller.ReadSpec()
		if readErr == nil {
			// Repair of an existing configuration: preselect the saved
			// startup kind unless the flag chose one.
			if startup == "" {
				startup = existing.Startup
			}
		}
		if startup == "" {
			// --yes must not select a startup mode when none is
			// configured; the interactive prompt defaults to login.
			if yesPresent(rest) {
				return fmt.Errorf("--startup is required: no startup mode is configured for this instance yet")
			}
			choice, err := promptStartupChoice(os.Stdout, os.Stdin)
			if err != nil {
				return err
			}
			startup = choice
		}

		spec, err := desiredSpec(mode, startup, dataDir, cfgArgs)
		if err != nil {
			return err
		}

		change, err := controller.Preview(context.Background(), spec)
		if err != nil {
			return err
		}
		printChangePreview(mode, change)
		if change.RequiresElevation {
			fmt.Printf("\nAdministrator authorization is required: %s\n", change.ElevationReason)
		}
		if !yesPresent(rest) {
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
		fmt.Printf("Configured. %s starts automatically at %s.\n", capitalize(mode), startup)
		return nil
	}
}

// runModeDisable removes the native startup configuration. It refuses
// while an instance runs or starts, and preserves the data, the worker
// credentials, and the logs.
func runModeDisable(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		_, rest, err := parseTimeoutFlag(args, defaultStatusTimeout)
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
		if held, _ := lifecycle.ProbeRuntimeLock(paths); held {
			return fmt.Errorf("the instance is running or starting; stop it with `leapmux %s stop` before disabling", mode)
		}
		controller, err := nativeController(mode, dataDir)
		if err != nil {
			return err
		}
		if err := controller.Remove(context.Background()); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("No startup configuration.")
				return nil
			}
			return err
		}
		fmt.Println("Startup configuration removed. Application data, worker credentials, and logs are preserved.")
		return nil
	}
}

// runModeStart starts through the configured native mechanism. Missing
// or differing configuration goes through the consent flow first; the
// runtime wait budget starts after consent and authorization complete.
func runModeStart(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		timeout, rest, err := parseTimeoutFlag(args, defaultStartTimeout)
		if err != nil {
			return err
		}
		startup, rest, err := parseStartupFlag(rest)
		if err != nil {
			return err
		}
		dataDir, cfgArgs, err := splitLifecycleArgs(rest)
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

		controller, err := nativeController(mode, dataDir)
		if err != nil {
			return err
		}
		existing, readErr := controller.ReadSpec()
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if readErr == nil && startup == "" {
			startup = existing.Startup
		}
		if startup == "" {
			if yesPresent(rest) {
				return fmt.Errorf("--startup is required: no startup mode is configured for this instance yet")
			}
			choice, promptErr := promptStartupChoice(os.Stdout, os.Stdin)
			if promptErr != nil {
				return promptErr
			}
			startup = choice
		}

		spec, err := desiredSpec(mode, startup, dataDir, cfgArgs)
		if err != nil {
			return err
		}

		// Configure first when missing or differing.
		if readErr != nil || !specMatchesSaved(existing, spec) {
			change, previewErr := controller.Preview(context.Background(), spec)
			if previewErr != nil {
				return previewErr
			}
			printChangePreview(mode, change)
			if change.RequiresElevation {
				fmt.Printf("\nAdministrator authorization is required: %s\n", change.ElevationReason)
			}
			if !yesPresent(rest) {
				accepted, promptErr := promptApply(os.Stdout, os.Stdin)
				if promptErr != nil {
					return promptErr
				}
				if !accepted {
					fmt.Println("Configuration unchanged; nothing started.")
					return nil
				}
			}
			if applyErr := controller.Apply(context.Background(), spec); applyErr != nil {
				return applyErr
			}
		}

		// The native start itself; the wait budget covers readiness from
		// here -- consent and authorization are complete.
		launch, err := servicelaunch.LaunchSpecFromService(spec)
		if err != nil {
			return err
		}
		if err := platformManager().Start(context.Background(), launch); err != nil {
			notes, _ := platformManager().Diagnose(context.Background(), launch)
			for _, note := range notes {
				fmt.Println(note)
			}
			return err
		}
		return waitForReadiness(mode, canonical, timeout)
	}
}

// runModeRestart validates prerequisites, stops the current instance,
// waits for the shutdown, and starts through the native mechanism --
// one budget for both halves.
func runModeRestart(mode string) func(cmdCtx, []string) error {
	return func(c cmdCtx, args []string) error {
		timeout, rest, err := parseTimeoutFlag(args, defaultStartTimeout)
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
		_ = canonical
		// Stop first; a restart never launches a replacement while the
		// old instance owns its runtime lock.
		if err := runModeStop(mode)(c, append([]string{"--timeout", timeout.String()}, rest...)); err != nil {
			return err
		}
		return runModeStart(mode)(c, args)
	}
}

// waitForReadiness polls the control interface for readiness within
// the budget, distinguishing a startup failure from a wait timeout
// while startup continues.
func waitForReadiness(mode, canonical string, timeout waitBudget) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	paths := lifecycle.PathsOf(canonical)
	tick := newTicker(timeout / 20)
	defer tick.Stop()
	for {
		status, err := lifecycle.Observe(ctx, canonical)
		if err == nil {
			switch {
			case status.Readiness == lifecycle.ReadinessReady || status.Readiness == lifecycle.ReadinessReadyForSetup:
				fmt.Printf("%s started.\n", capitalize(mode))
				return nil
			case status.Readiness == lifecycle.ReadinessFailed:
				return reportStartupFailure(mode, paths, status)
			case status.Process.State == lifecycle.ProcessFailed:
				return reportStartupFailure(mode, paths, status)
			}
		}
		select {
		case <-ctx.Done():
			fmt.Printf("Startup continues; no readiness within %s. See `leapmux %s status`.\n", timeout, mode)
			return nil
		case <-tick.C:
		}
	}
}

// reportStartupFailure prints the attempt's failure reason with its
// bounded log excerpt and the complete log location.
func reportStartupFailure(mode string, paths lifecycle.Paths, status lifecycle.Status) error {
	fmt.Printf("%s failed to start.\n", capitalize(mode))
	if status.Attempt.LogPath != "" {
		excerpt, err := lifecycle.ReadExcerpt(status.Attempt.LogPath)
		if err == nil && excerpt != "" {
			fmt.Println("\nRecent startup logs:")
			for _, line := range strings.Split(strings.TrimSpace(excerpt), "\n") {
				fmt.Println("  " + line)
			}
		}
		fmt.Printf("\nFull log: %s\n", status.Attempt.LogPath)
	}
	for _, d := range status.Diagnostics {
		fmt.Printf("%s: %s\n", d.Title, d.Body)
	}
	return fmt.Errorf("%s failed to start", mode)
}

// specMatchesSaved states whether the desired specification and the
// saved one describe the same launch.
func specMatchesSaved(saved, desired lifecycle.ServiceSpec) bool {
	return saved.InstanceID == desired.InstanceID &&
		saved.Startup == desired.Startup &&
		saved.Executable == desired.Executable &&
		saved.ConfigFile == desired.ConfigFile
}

// parseStartupFlag reads --startup=login|boot from the args.
func parseStartupFlag(args []string) (lifecycle.StartupKind, []string, error) {
	rest := make([]string, 0, len(args))
	var chosen lifecycle.StartupKind
	for _, arg := range args {
		switch {
		case arg == "--startup" || arg == "-startup":
			return "", nil, fmt.Errorf("--startup takes the form --startup=login or --startup=boot")
		case strings.HasPrefix(arg, "--startup="):
			value := strings.TrimPrefix(arg, "--startup=")
			kind, err := lifecycle.ParseStartupKind(value)
			if err != nil {
				return "", nil, err
			}
			chosen = kind
		default:
			rest = append(rest, arg)
		}
	}
	return chosen, rest, nil
}

// yesPresent states whether --yes was passed.
func yesPresent(args []string) bool {
	for _, arg := range args {
		if arg == "--yes" || arg == "-yes" || arg == "--yes=true" {
			return true
		}
	}
	return false
}

// printChangePreview renders the change the controller proposes.
func printChangePreview(mode string, change servicelaunch.ConfigurationChange) {
	fmt.Printf("\nLeapMux will configure %s:\n", mode)
	for _, line := range change.Changes {
		fmt.Printf("  %s\n", line)
	}
}
