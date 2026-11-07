package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// Controller is the shared front half of every native configuration:
// it derives the preview from the saved specification and the live
// native state, applies a consented change under the directory's
// operation lock, and inspects for drift. The platform's Manager does
// the native half.
type Controller struct {
	// Manager is the platform adapter.
	Manager Manager
	// Platform names the platform, for the summaries and the elevation
	// description.
	Platform string
	// NativeMechanism is the platform's own name for what gets
	// configured ("launchd user agent", "system service", "scheduled
	// task").
	NativeMechanism string
	// SaveSpec durably publishes the service specification.
	SaveSpec func(spec lifecycle.ServiceSpec) error
	// ReadSpec loads the saved specification; os.ErrNotExist when none
	// exists.
	ReadSpec func() (lifecycle.ServiceSpec, error)
	// RemoveSpec removes the saved specification.
	RemoveSpec func() error
}

// Preview derives the change a configuration would make. It touches
// nothing: the caller shows Changes and the elevation, and applies only
// after consent.
func (c *Controller) Preview(ctx context.Context, desired lifecycle.ServiceSpec) (ConfigurationChange, error) {
	if _, err := LaunchSpecFromService(desired); err != nil {
		return ConfigurationChange{}, err
	}
	if err := ctx.Err(); err != nil {
		return ConfigurationChange{}, err
	}
	elevation := DescribeElevation(desired.Startup, c.Platform)
	mechanism := c.mechanism(desired.Startup)

	changes := []string{fmt.Sprintf("Startup: %s", desired.Startup)}
	changes = append(changes,
		fmt.Sprintf("Account: %s (%s)", desired.AccountName, desired.AccountID),
		fmt.Sprintf("Mechanism: %s", mechanism),
		fmt.Sprintf("Definition: %s", c.definitionName(desired)),
		fmt.Sprintf("Executable: %s", desired.Executable),
		fmt.Sprintf("Working directory: %s", desired.WorkingDirectory),
		fmt.Sprintf("Configuration: %s", desired.ConfigFile),
		fmt.Sprintf("Data directory: %s", desired.DataDirectory),
	)
	if desired.EnvironmentFile != nil {
		changes = append(changes, "Environment file: "+*desired.EnvironmentFile)
	}
	if len(desired.RuntimeEnvironment) > 0 {
		changes = append(changes, "Private environment overrides: "+strings.Join(SortedEnvironmentNames(desired.RuntimeEnvironment), ", "))
	}
	if c.Platform == "windows" {
		changes = append(changes, "Windows stores the account password securely for noninteractive startup. LeapMux requests protected input and does not save the password.", elevation.Reason)
	}

	current, err := c.ReadSpec()
	if err == nil {
		// Repair and drift: state what differs from the saved
		// specification and the native definition.
		currentLaunch, validationErr := LaunchSpecFromService(current)
		if validationErr != nil {
			changes = append(changes, fmt.Sprintf("Repair: the saved specification is invalid: %v", validationErr))
		} else {
			native, inspectErr := c.Manager.Inspect(ctx, currentLaunch)
			if inspectErr != nil {
				return ConfigurationChange{}, inspectErr
			}
			if native.Installed && !native.DefinitionMatches {
				changes = append(changes, "Repair: the installed definition differs from the saved specification and will be rewritten")
			}
		}
		if current.Startup != desired.Startup || current.Executable != desired.Executable ||
			current.ConfigFile != desired.ConfigFile || current.AccountID != desired.AccountID {
			changes = append(changes, "Changing the startup mode, account, executable, or runtime configuration requires a stopped instance")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ConfigurationChange{}, fmt.Errorf("read the saved specification: %w", err)
	}
	return ConfigurationChange{
		Spec:              desired,
		NativeSummary:     mechanism,
		RequiresElevation: elevation.Required,
		ElevationReason:   elevation.Reason,
		Changes:           changes,
	}, nil
}

func (c *Controller) mechanism(startup lifecycle.StartupKind) string {
	switch c.Platform {
	case "darwin":
		if startup == lifecycle.StartupBoot {
			return "launchd LaunchDaemon"
		}
		return "launchd LaunchAgent"
	case "linux":
		if startup == lifecycle.StartupBoot {
			return "systemd system service"
		}
		return "systemd user service"
	case "windows":
		if startup == lifecycle.StartupBoot {
			return "Windows service"
		}
		return "Windows noninteractive scheduled task"
	default:
		return c.NativeMechanism
	}
}

func (c *Controller) definitionName(spec lifecycle.ServiceSpec) string {
	if c.Platform == "darwin" {
		return "com.leapmux." + spec.Mode.String() + "." + spec.InstanceID
	}
	name := WindowsServiceName(spec.Mode, spec.InstanceID)
	if c.Platform == "linux" {
		return name + ".service"
	}
	return name
}

// Apply performs a consented change: durably save the specification,
// then install the native definition from it. A failure between the two
// leaves a saved specification the next Apply repairs -- the
// specification is the source the native definition is generated from.
func (c *Controller) Apply(ctx context.Context, desired lifecycle.ServiceSpec) error {
	launch, err := LaunchSpecFromService(desired)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, readErr := c.ReadSpec()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("servicelaunch: read the previous specification: %w", readErr)
	}
	if readErr == nil && !current.Equal(desired) {
		previous, err := launchSpecForRemoval(current)
		if err != nil {
			// Runtime fields may need repair. The valid desired launch can
			// inspect the same native identity under the previous startup mode.
			if current.InstanceID != desired.InstanceID || current.Mode != desired.Mode || current.AccountID != desired.AccountID || current.DataDirectory != desired.DataDirectory {
				return fmt.Errorf("servicelaunch: cannot replace a different saved native identity: %w", err)
			}
			previous = launch
			previous.Startup = current.Startup
			if _, err := lifecycle.ParseStartupKind(string(previous.Startup)); err != nil {
				return fmt.Errorf("servicelaunch: the previous startup mode must be repaired before its mechanism can be replaced: %w", err)
			}
		}
		native, err := c.Manager.Inspect(ctx, previous)
		if err != nil {
			return err
		}
		if native.Running || native.State == "activating" || native.State == "start-pending" || native.State == "stop-pending" {
			return fmt.Errorf("servicelaunch: stop the instance before changing its startup configuration")
		}
		// Keep the old specification until its mechanism is removed. If
		// publication then fails, the old specification can repair it.
		if current.Startup != desired.Startup {
			if err := c.Manager.Remove(ctx, previous); err != nil {
				return fmt.Errorf("servicelaunch: remove the previous startup mechanism: %w", err)
			}
		}
	}
	if err := c.SaveSpec(desired); err != nil {
		return fmt.Errorf("servicelaunch: save the service specification: %w", err)
	}
	if err := c.Manager.Install(ctx, launch); err != nil {
		return fmt.Errorf("servicelaunch: install the %s: %w", c.NativeMechanism, err)
	}
	return nil
}

// Inspect reports the combined state: the saved specification and the
// native definition's view of it. A saved specification whose native
// definition is missing or changed is DRIFT, reported as such rather
// than assumed complete.
func (c *Controller) Inspect(ctx context.Context) (lifecycle.ServiceSpec, NativeStatus, error) {
	spec, err := c.ReadSpec()
	if err != nil {
		return lifecycle.ServiceSpec{}, NativeStatus{}, err
	}
	launch, err := LaunchSpecFromService(spec)
	if err != nil {
		return spec, NativeStatus{}, err
	}
	native, err := c.Manager.Inspect(ctx, launch)
	if err != nil {
		return spec, NativeStatus{}, err
	}
	// The MANAGER owns the drift verdict: only it can read the installed
	// native definition and compare it with the launch spec the
	// controller derived.
	return spec, native, nil
}

// Remove uninstalls the native definition and the saved specification.
// The caller has already refused while an instance runs or starts.
func (c *Controller) Remove(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	spec, err := c.ReadSpec()
	if err != nil {
		return err
	}
	launch, err := launchSpecForRemoval(spec)
	if err != nil {
		return err
	}
	native, err := c.Manager.Inspect(ctx, launch)
	if err != nil {
		return err
	}
	if native.Running {
		return fmt.Errorf("the native process is running or starting; stop it before disabling startup")
	}
	if err := c.Manager.Remove(ctx, launch); err != nil {
		return fmt.Errorf("servicelaunch: remove the %s: %w", c.mechanism(spec.Startup), err)
	}
	return c.RemoveSpec()
}

// SortedEnvironmentNames lists a spec's environment names, sorted.
func SortedEnvironmentNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
