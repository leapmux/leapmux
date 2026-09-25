package cline

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// clineLocator finds the `cline` program on the user's PATH.
var clineLocator = launch.Binaries("cline")

// Registration states everything the worker knows about Cline before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE,
		Plugin:   clineProvider{},
		Start:    Start,
		Locator:  clineLocator,
		// The user's Cline settings decide the provider, so the static catalog
		// is the one entry that runs their selection. A running agent reports
		// the models of its provider (catalog.go).
		DefaultModels:       defaultModels,
		OptionGroups:        staticOptionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// Each model states its own reasoning efforts, and the static catalog
		// holds none, because it holds no concrete model.
		ManagesEffort: true,
		// A new session, and a session that stored no mode, run Act: Cline's own
		// default, which asks before each tool that changes something.
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: defaultPermissionMode},
			Fallback:   defaultPermissionMode,
		},
		// LeapMux states the three modes completely, so the worker refuses an
		// unknown value at launch.
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_CLINE_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_CLINE_DEFAULT_EFFORT",
		// The worker sweeps the directories that ended workers left, and ends
		// the daemon that each one records (see agentDirSpec).
		AgentDir: ptrconv.Ptr(agentDirSpec()),
	}
}
