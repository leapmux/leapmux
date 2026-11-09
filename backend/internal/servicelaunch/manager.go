package servicelaunch

import (
	"context"

	"github.com/leapmux/leapmux/lifecycle"
)

// NativeStatus is what a platform's native manager states about one
// definition.
type NativeStatus struct {
	// Installed reports whether the native definition exists.
	Installed bool
	// Running includes execution and queued, starting, or stopping transitions.
	Running bool
	// State is the native state string, for diagnostics.
	State string
	// DefinitionMatches reports whether the INSTALLED definition
	// matches the saved specification. A changed or missing definition
	// with a saved specification is drift, not a complete
	// configuration.
	DefinitionMatches bool
	// PID is the running instance's process identifier, or zero.
	PID int
	// Failure is the native startup failure when the application did not
	// reach its own control interface. Empty means no failure was reported.
	Failure string
}

// Manager is the native mechanism one platform supplies: inspect,
// install, start, stop, remove, and diagnose ONE definition.
//
// Install CONFIGURES without starting: `enable` writes and enables the
// definition; `start` loads or starts it. Stop ends the running
// instance while PRESERVING the definition for the next login or boot.
// All arguments run through native APIs or direct commands; none is a
// shell string.
type Manager interface {
	// Inspect states what the native mechanism holds for the instance.
	Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error)
	// Install writes and enables the definition without starting it.
	Install(ctx context.Context, spec LaunchSpec) error
	// Start loads or starts the installed definition.
	Start(ctx context.Context, spec LaunchSpec) error
	// Stop ends the running instance, preserving the definition. It returns
	// nil only after that instance has finished its shutdown and released
	// the runtime lock of its data directory, so a later Start can acquire
	// it.
	Stop(ctx context.Context, spec LaunchSpec) error
	// Remove uninstalls the definition.
	Remove(ctx context.Context, spec LaunchSpec) error
	// Diagnose explains a definition that never started: native
	// manager errors, missing prerequisites, authorization refusals.
	Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error)
}

// StartAuthorizer completes interactive native authorization before the caller
// starts its runtime wait budget. Read-only inspection never uses it.
type StartAuthorizer interface {
	AuthorizeStart(context.Context, LaunchSpec) error
}

// ConfigurationChange is the preview a controller shows before it
// touches anything: what will be configured, and what authorization it
// needs.
type ConfigurationChange struct {
	// Spec is the specification the change would save.
	Spec lifecycle.ServiceSpec
	// NativeSummary states what the platform adapter will do, in the
	// platform's own terms ("launchd LaunchAgent", "systemd system service").
	NativeSummary string
	// RequiresElevation states whether applying needs administrator
	// authorization. ElevationReason gives the exact reason when it does.
	RequiresElevation bool
	ElevationReason   string
	// Changes lists the human-readable differences from the current
	// state, one entry each.
	Changes []string
}

// Elevation describes one authorization a platform needs. The native
// caller performs the elevation itself -- sudo, UAC, or another broker
// -- after the controller has explained this reason to the user.
type Elevation struct {
	// Required states whether this change needs administrator rights.
	Required bool
	// Reason states WHY, in the terms the platform's own tools report.
	Reason string
	// Mechanism identifies the authorization mechanism ("sudo", "UAC").
	Mechanism string
}

// DescribeElevation states the authorization a startup kind needs on a
// platform, before any native tool runs.
func DescribeElevation(startup lifecycle.StartupKind, platform string) Elevation {
	if platform == "windows" {
		if startup == lifecycle.StartupBoot {
			return Elevation{Required: true, Mechanism: "UAC", Reason: "Windows requests administrator authorization to create or repair this service and grant the selected account the service logon right if needed."}
		}
		return Elevation{Mechanism: "UAC", Reason: "The noninteractive login task requires the account password and the batch logon right. Windows may request administrator authorization to grant that right or repair task permissions."}
	}
	switch startup {
	case lifecycle.StartupBoot:
		return Elevation{
			Required:  true,
			Reason:    "Computer startup requires a system-level definition. sudo authorizes that configuration; the application still runs as the selected user account.",
			Mechanism: elevationMechanismOf(platform),
		}
	default:
		return Elevation{Required: false, Reason: "login startup runs as this account and needs no administrator rights"}
	}
}

// elevationMechanismOf identifies the platform's authorization tool.
func elevationMechanismOf(platform string) string {
	switch platform {
	case "darwin":
		return "sudo"
	case "windows":
		return "UAC"
	case "linux":
		return "sudo"
	default:
		return "administrator authorization"
	}
}

// NewPlatformManager builds this platform's native adapter. The
// platform files implement it; a platform without an adapter gets the
// UnsupportedManager.
//
// The indirection keeps cmd/leapmux free of platform build tags: the
// adapter selection compiles once, here.
func NewPlatformManager() Manager {
	return newPlatformManager()
}
