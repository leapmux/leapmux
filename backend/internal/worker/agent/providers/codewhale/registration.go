package codewhale

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Registration states everything the worker knows about Codewhale before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE,
		Plugin:   codewhaleProvider{},
		Start:    Start,
		Locator:  codewhaleLocator,
		// The catalog belongs to the provider the user configured Codewhale for,
		// so no static entry could be right for everyone. A running agent reads
		// it from the runtime.
		DefaultModels: nil,
		OptionGroups:  codewhaleStaticOptionGroups,
		// The effort axis. It depends on the model, and the catalog that states
		// each model's levels is read at runtime.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		ManagesEffort:       true,
		// LeapMux states the whole posture enum; the runtime refuses any other.
		FixedPermissionModes: true,
		// A fresh agent starts in agent mode. The thread would default to the
		// user's configured mode otherwise, which a plan-mode default would make a
		// session that refuses every edit with nothing on screen to say why.
		ProviderOptionDefaults: map[string]string{contracts.CodewhaleOptionMode: contracts.CodewhaleDefaultMode},
		// Ask already asks before a restricted command or edit, so a new session needs
		// no safe default of its own beyond the fallback.
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: contracts.CodewhalePostureAsk,
		},
		EnvModelKey:  "LEAPMUX_CODEWHALE_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_CODEWHALE_DEFAULT_EFFORT",
	}
}
