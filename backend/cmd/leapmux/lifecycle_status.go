package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/version"
)

func handleVersionQuery(err error) bool {
	if errors.Is(err, internalconfig.ErrVersionRequested) {
		fmt.Println(version.Format())
		return true
	}
	return false
}

func validateLifecycleSelection(configuration *lifecycleConfiguration) error {
	return lifecycle.ValidateDirectorySelection(configuration.role, configuration.dataDirectory)
}

func observeLifecycleModeWithController(ctx context.Context, configuration *lifecycleConfiguration, newController nativeControllerFactory) (lifecycle.Status, error) {
	if err := validateLifecycleSelection(configuration); err != nil {
		return lifecycle.Status{}, err
	}
	status, observeErr := lifecycle.Observe(ctx, configuration.dataDirectory)
	if observeErr != nil && !errors.Is(observeErr, lifecycle.ErrUnresponsive) {
		status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{Kind: "error", Title: "Runtime observation", Body: observeErr.Error()})
	}
	if status.Mode == nil {
		status.Mode = new(configuration.role.String())
	}
	if status.InstanceID == nil {
		status.InstanceID = new(lifecycle.NewInstanceID(lifecycle.CurrentAccountID(), configuration.role, configuration.dataDirectory).String())
	}
	if configuration.role == lifecycle.RoleWorker {
		registration, registrationErr := configuration.readRegistration()
		status.Registration = registration
		if registrationErr != nil {
			status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{
				Kind: "error", Title: "Saved registration", Body: registrationErr.Error(),
			})
		}
	}
	status, nativeErr := reconcileNativeStatus(ctx, configuration.role, configuration.dataDirectory, status, newController)
	observeErr = errors.Join(observeErr, nativeErr)
	if status.Process.State == lifecycle.ProcessFailed {
		if status.Attempt.Error != nil {
			status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{
				Kind: "error", Title: "Process failed", Body: *status.Attempt.Error,
			})
		}
		if status.Attempt.LogPath != nil {
			excerpt, excerptErr := lifecycle.ReadExcerpt(*status.Attempt.LogPath)
			if excerptErr != nil {
				status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{
					Kind: "error", Title: "Read process log", Body: excerptErr.Error(), Source: status.Attempt.LogPath,
				})
			} else if excerpt != "" {
				status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{
					Kind: "log", Title: "Recent process logs", Body: excerpt, Source: status.Attempt.LogPath,
				})
			}
		}
	}
	return status, observeErr
}

func reconcileNativeStatus(ctx context.Context, role lifecycle.Role, root string, status lifecycle.Status, newController nativeControllerFactory) (lifecycle.Status, error) {
	controller, err := newController(role.String(), root)
	if err != nil {
		return status, err
	}
	spec, err := controller.ReadSpec()
	if errors.Is(err, os.ErrNotExist) {
		status.Configured, status.Startup = false, nil
		return status, nil
	}
	var native servicelaunch.NativeStatus
	if err == nil {
		status.Startup = new(spec.Startup)
		var launch servicelaunch.LaunchSpec
		launch, err = servicelaunch.LaunchSpecFromService(spec)
		if err == nil {
			native, err = controller.Manager.Inspect(ctx, launch)
		}
	}
	status.Configured = err == nil && native.Installed && native.DefinitionMatches
	if err != nil {
		status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{Kind: "error", Title: "Native configuration", Body: err.Error()})
		return status, err
	}
	if !native.Installed {
		status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{Kind: "native", Title: "Native configuration", Body: "The native definition is missing. Run enable to repair the saved configuration."})
	} else if !native.DefinitionMatches {
		status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{Kind: "native", Title: "Native configuration", Body: "The native definition differs from the saved configuration. Run enable to repair it."})
	}
	// A live application owns its readiness and shutdown state. Native evidence
	// describes startup only while no application holds the runtime lock.
	if status.Process.State == lifecycle.ProcessStopped || status.Process.State == lifecycle.ProcessFailed {
		if native.Running {
			status.Process.State = lifecycle.ProcessStarting
			status.Readiness = new(lifecycle.ReadinessStarting)
			if native.PID > 0 {
				status.Process.PID = new(native.PID)
			}
		} else if native.Failure != "" {
			status.Process.State = lifecycle.ProcessFailed
			status.Readiness = new(lifecycle.ReadinessFailed)
			status.Diagnostics = append(status.Diagnostics, lifecycle.Diagnostic{Kind: "native", Title: "Native startup failure", Body: native.Failure})
		}
	}
	return status, nil
}

// errStoppedExit and errUnresponsiveExit carry the status exit codes
// through runCLI's error path.
var (
	errStoppedExit      = fmt.Errorf("\x00stopped")
	errUnresponsiveExit = fmt.Errorf("\x00unresponsive")
)

// executeModeStatusWithController reports process state and relevant diagnostics.
// Exit code 3 reports a stopped instance. A registered, disconnected worker
// remains running and returns exit code 0.
func executeModeStatusWithController(mode string, args []string, newController nativeControllerFactory) error {
	configuration, options, err := loadLifecycleCommand(mode, "status", args)
	if handleVersionQuery(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	status, observeErr := observeLifecycleModeWithController(ctx, configuration, newController)
	if options.json {
		if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
			return err
		}
	} else {
		printStatusReport(mode, status, "")
	}
	if observeErr != nil || status.Process.State == lifecycle.ProcessFailed {
		return errUnresponsiveExit
	}
	if status.Process.State == lifecycle.ProcessStopped {
		return errStoppedExit
	}
	return nil
}
