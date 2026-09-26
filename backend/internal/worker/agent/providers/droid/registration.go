package droid

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// droidLocator finds the `droid` program on the user's PATH. The npm package is
// a shim over a platform binary, so the launcher must resolve `droid` itself.
var droidLocator = launch.Binaries(droidBinaryName)

// PermissionModeLabel is the label of LeapMux's permission-mode axis for
// Droid. Droid states the autonomy axis itself (`--auto`), and the worker maps
// a permission-mode choice onto that axis at launch.
const PermissionModeLabel = "Permissions"

// permissionModeGroup is the static option group of the permission mode.
//
// Droid's autonomy axis is its own (`normal`, `auto-low`, `auto-medium`,
// `auto-high`, `spec`), and LeapMux folds the permission decision onto the
// same axis every provider uses, because the plan toggle and the plan approval
// read one axis for every provider.
var permissionModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDPermissionMode,
	Label:        PermissionModeLabel,
	DefaultValue: contracts.DroidModeDefault,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.DroidModeDefault, Name: "Default", Description: "Read-only: ask before any tool that changes something"},
		{Id: contracts.DroidModeAutoLow, Name: "Auto (Low)", Description: "Auto-approve low-risk tools"},
		{Id: contracts.DroidModeAutoMedium, Name: "Auto (Medium)", Description: "Auto-approve medium-risk tools"},
		{Id: contracts.DroidModeAutoHigh, Name: "Auto (High)", Description: "Auto-approve every tool"},
	},
}

// effortGroup is the static option group of the reasoning effort. Droid takes
// `none`, `low`, `medium` and `high`; a custom (BYOK) model states `none`.
var effortGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDEffort,
	Label:        "Effort",
	DefaultValue: contracts.DroidEffortMedium,
	Mutable:      true,
	Order:        agent.OptionOrderEffort,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.DroidEffortNone, Name: "None", Description: "No reasoning effort"},
		{Id: contracts.DroidEffortLow, Name: "Low", Description: "Light reasoning"},
		{Id: contracts.DroidEffortMedium, Name: "Medium", Description: "Balanced reasoning"},
		{Id: contracts.DroidEffortHigh, Name: "High", Description: "Deep reasoning"},
	},
}

// staticOptionGroups holds the option groups that do not depend on a running
// agent. The model catalog is discovered at startup (catalog.go).
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{effortGroup, permissionModeGroup}

// Registration states everything the worker knows about Factory Droid before
// any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID,
		Plugin:   droidProvider{},
		Start:    Start,
		Locator:  droidLocator,
		// The running agent reports the models of its BYOK configuration
		// (catalog.go). The static catalog is empty because a model list would
		// be Factory's account catalog, which a mock-backed run never reaches.
		DefaultModels:       defaultModels,
		OptionGroups:        staticOptionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// A new session, and a session that stored no mode, run Default: Droid's
		// own read-only autonomy, which asks before a tool that changes something.
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.DroidModeDefault},
			Fallback:   contracts.DroidModeDefault,
		},
		// LeapMux states the four modes completely, so the worker refuses an
		// unknown value at launch.
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_DROID_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_DROID_DEFAULT_EFFORT",
	}
}
