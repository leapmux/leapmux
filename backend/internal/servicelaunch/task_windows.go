//go:build windows

package servicelaunch

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/lifecycle"
)

// windowsTaskManager configures a password-logon task for the selected
// account. The task is independent of any one interactive session.
// Password-free S4U logon restricts network and encrypted file access, so this
// task uses account credentials. The enable command explains the batch logon right.
type windowsTaskManager struct {
	credential  credentialProvider
	inspectTask func(context.Context, LaunchSpec) (NativeStatus, error)
	upsertTask  func(context.Context, LaunchSpec, []byte) error
	startTask   func(context.Context, LaunchSpec) error
	removeTask  func(context.Context, LaunchSpec) error
	elevate     func(context.Context, nativeOperationRequest) error
	stopRuntime func(context.Context, lifecycle.Role, string) (bool, error)
}

func newWindowsTaskManager(credential credentialProvider) *windowsTaskManager {
	if credential == nil {
		credential = readAccountPassword
	}
	return &windowsTaskManager{
		credential: credential, inspectTask: inspectWindowsTask, upsertTask: upsertWindowsTask,
		startTask: startWindowsTask, removeTask: removeWindowsTask, elevate: runElevatedOperation, stopRuntime: lifecycle.Stop,
	}
}

func taskName(spec LaunchSpec) string { return "leapmux-" + spec.Mode.String() + "-" + spec.InstanceID }

func (m *windowsTaskManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupLogin); err != nil {
		return NativeStatus{}, err
	}
	return m.inspectTask(ctx, spec)
}

func (m *windowsTaskManager) Install(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupLogin); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	return withAccountPassword(ctx, spec.AccountName, m.credential, func(password []byte) error {
		err := m.upsertTask(ctx, spec, password)
		if credentialErrorCode(err) == 1385 {
			if err := m.elevate(ctx, nativeOperationRequest{Operation: nativeGrantBatchRight, Spec: spec}); err != nil {
				return err
			}
			err = m.upsertTask(ctx, spec, password)
		}
		if credentialErrorCode(err) == 5 {
			return m.elevate(ctx, nativeOperationRequest{Operation: nativeInstallTask, Spec: spec, Password: password})
		}
		return err
	})
}

func (m *windowsTaskManager) Start(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupLogin); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	return m.startTask(ctx, spec)
}

func (m *windowsTaskManager) Stop(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupLogin); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		native, err := m.inspectTask(ctx, spec)
		if err != nil {
			return err
		}
		if !native.Running {
			return nil
		}
		held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(spec.DataDirectory))
		if err != nil {
			return err
		}
		if held {
			_, err := m.stopRuntime(ctx, spec.Mode, spec.DataDirectory)
			return err
		}
		// Task Scheduler's Stop terminates the process. Wait for the common
		// control interface instead, so startup and database shutdown join.
		select {
		case <-ctx.Done():
			return fmt.Errorf("the scheduled task has not published its shutdown interface: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *windowsTaskManager) Remove(ctx context.Context, spec LaunchSpec) error {
	if err := validateWindowsLaunch(ctx, spec, lifecycle.StartupLogin); err != nil {
		return err
	}
	err := m.removeTask(ctx, spec)
	if credentialErrorCode(err) == 5 {
		return m.elevate(ctx, nativeOperationRequest{Operation: nativeRemoveTask, Spec: spec})
	}
	return err
}

func (m *windowsTaskManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	status, err := m.Inspect(ctx, spec)
	if err != nil {
		return nil, err
	}
	notes := []string{"Windows scheduled task: " + taskName(spec), "Native state: " + status.State}
	if status.Failure != "" {
		notes = append(notes, status.Failure)
	}
	if !status.DefinitionMatches {
		notes = append(notes, "The native definition differs from the saved specification; run enable to repair it.")
	}
	return notes, nil
}
