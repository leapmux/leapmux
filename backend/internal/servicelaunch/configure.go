package servicelaunch

import (
	"context"
	"fmt"
	"sort"

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
	if err := ValidateLaunchSpec(mustLaunchSpec(desired)); err != nil {
		return ConfigurationChange{}, err
	}
	elevation := DescribeElevation(desired.Startup, c.Platform)

	changes := []string{fmt.Sprintf("Startup: %s", desired.Startup)}
	changes = append(changes,
		fmt.Sprintf("Account: %s", desired.AccountName),
		fmt.Sprintf("Mechanism: %s", c.NativeMechanism),
		fmt.Sprintf("Executable: %s", desired.Executable),
		fmt.Sprintf("Configuration: %s", desired.ConfigFile),
		fmt.Sprintf("Data directory: %s", desired.DataDirectory),
	)

	current, err := c.ReadSpec()
	if err == nil {
		// Repair and drift: state what differs from the saved
		// specification and the native definition.
		native, inspectErr := c.Manager.Inspect(ctx, mustLaunchSpec(current))
		if inspectErr == nil && native.Installed && !native.DefinitionMatches {
			changes = append(changes, "Repair: the installed definition differs from the saved specification and will be rewritten")
		}
		if current.Startup != desired.Startup || current.Executable != desired.Executable ||
			current.ConfigFile != desired.ConfigFile || current.AccountID != desired.AccountID {
			changes = append(changes, "Changing the startup mode, account, executable, or runtime configuration requires a stopped instance")
		}
	}
	return ConfigurationChange{
		Spec:              desired,
		NativeSummary:     c.NativeMechanism,
		RequiresElevation: elevation.Required,
		ElevationReason:   elevation.Reason,
		Changes:           changes,
	}, nil
}

// Apply performs a consented change: durably save the specification,
// then install the native definition from it. A failure between the two
// leaves a saved specification the next Apply repairs -- the
// specification is the source the native definition is generated from.
func (c *Controller) Apply(ctx context.Context, desired lifecycle.ServiceSpec) error {
	if err := c.SaveSpec(desired); err != nil {
		return fmt.Errorf("servicelaunch: save the service specification: %w", err)
	}
	launch, err := LaunchSpecFromService(desired)
	if err != nil {
		return err
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
	spec, err := c.ReadSpec()
	if err != nil {
		return err
	}
	launch, err := LaunchSpecFromService(spec)
	if err != nil {
		return err
	}
	if err := c.Manager.Remove(ctx, launch); err != nil {
		return fmt.Errorf("servicelaunch: remove the %s: %w", c.NativeMechanism, err)
	}
	return c.RemoveSpec()
}

// mustLaunchSpec validates and converts; the caller already validated
// the fields this reads.
func mustLaunchSpec(spec lifecycle.ServiceSpec) LaunchSpec {
	launch, err := LaunchSpecFromService(spec)
	if err != nil {
		// The controller's inputs come from Preview's validation or the
		// saved specification's writer; a failure here is a programming
		// error, and the panic names it.
		panic(fmt.Sprintf("servicelaunch: invalid specification: %v", err))
	}
	return launch
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
