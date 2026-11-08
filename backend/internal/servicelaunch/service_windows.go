//go:build windows

package servicelaunch

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/lifecycle"
)

// windowsServiceManager owns boot configuration through native SCM calls.
// Read/start/stop use the selected account's granted service access. Only
// installation and removal enter the narrow administrator helper.
type windowsServiceManager struct {
	credential     credentialProvider
	inspectService func(context.Context, LaunchSpec) (NativeStatus, error)
	elevate        func(context.Context, nativeOperationRequest) error
	startService   func(context.Context, LaunchSpec) error
	stopService    func(context.Context, LaunchSpec) error
}

func newWindowsServiceManager() *windowsServiceManager {
	return &windowsServiceManager{
		credential:     readAccountPassword,
		inspectService: inspectWindowsService,
		elevate:        runElevatedOperation,
		startService:   startWindowsService,
		stopService:    stopWindowsService,
	}
}

func serviceName(spec LaunchSpec) string {
	return WindowsServiceName(spec.Mode, spec.InstanceID)
}

func windowsServiceArguments(spec LaunchSpec) []string {
	return append([]string{WindowsServiceEntryFlag, serviceName(spec)}, spec.RunArguments()...)
}

func validateWindowsLaunch(ctx context.Context, spec LaunchSpec, startup lifecycle.StartupKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateLaunchSpec(spec); err != nil {
		return err
	}
	if spec.Startup != startup {
		return fmt.Errorf("the native mechanism does not match the selected startup mode")
	}
	canonical, err := lifecycle.CanonicalDir(spec.DataDirectory)
	if err != nil {
		return err
	}
	if spec.AccountID != lifecycle.CurrentAccountID() || canonical != spec.DataDirectory || spec.InstanceID != lifecycle.NewInstanceID(spec.AccountID, spec.Mode, canonical).String() {
		return fmt.Errorf("the native definition does not match this account and data directory")
	}
	return verifyWindowsAccount(spec)
}

func (m *windowsServiceManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupBoot); err != nil {
		return NativeStatus{}, err
	}
	return m.inspectService(ctx, spec)
}

func (m *windowsServiceManager) Install(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupBoot); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	return withAccountPassword(ctx, spec.AccountName, m.credential, func(password []byte) error {
		return m.elevate(ctx, nativeOperationRequest{Operation: nativeInstallService, Spec: spec, Password: password})
	})
}

func (m *windowsServiceManager) Start(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupBoot); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	return m.startService(ctx, spec)
}

func (m *windowsServiceManager) Stop(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupBoot); err != nil {
		return err
	}
	err := m.stopService(ctx, spec)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	return err
}

func (m *windowsServiceManager) Remove(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupBoot); err != nil {
		return err
	}
	return m.elevate(ctx, nativeOperationRequest{Operation: nativeRemoveService, Spec: spec})
}

func (m *windowsServiceManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	status, err := m.Inspect(ctx, spec)
	if err != nil {
		return nil, err
	}
	notes := []string{"Windows service: " + serviceName(spec), "Native state: " + status.State}
	if status.Failure != "" {
		notes = append(notes, status.Failure)
	}
	if !status.DefinitionMatches {
		notes = append(notes, "The native definition differs from the saved specification; run enable to repair it.")
	}
	return notes, nil
}
