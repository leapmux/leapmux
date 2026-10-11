package kimi

import (
	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// kimiLocator finds the `kimi` program on the user's PATH.
var kimiLocator = launch.Binaries(kimiBinaryName)

// Registration states everything the worker knows about Kimi Code before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE,
		Plugin:   kimiProvider{},
		Start:    Start,
		Locator:  kimiLocator,
		// The user's configuration and Kimi account decide which models exist, so
		// the catalog is read from the running server (catalog.go).
		DefaultModels:       nil,
		OptionGroups:        kimiStaticOptionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			// A session with no stored mode runs Always Ask, which is Kimi's own
			// default and the mode that asks before it runs a command.
			Fallback: contracts.KimiDefaultMode,
		},
		// Each model states its own thinking levels, and the catalog is read from
		// the running server. Without this a model switch would keep the previous
		// model's levels.
		ManagesEffort: true,
		// The permission modes are Kimi's three plus plan mode, and LeapMux states
		// all four itself.
		FixedPermissionModes: true,
		ProviderOptionDefaults: map[string]string{
			kimiOptionSwarmMode: kimiSwarmOff,
		},
		EnvModelKey:  "LEAPMUX_KIMI_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_KIMI_DEFAULT_EFFORT",
	}
}
