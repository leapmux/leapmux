package reasonix

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// reasonixAvailableModels is the static catalog of Reasonix's built-in provider
// entries (reasonix/internal/config/config.go). The id is the provider-entry
// name Reasonix's `--model` flag accepts (cfg.ResolveModel resolves it to the
// concrete model). deepseek-flash is Reasonix's own default_model.
var reasonixAvailableModels = []*agent.ModelInfo{
	{Id: "deepseek-flash", DisplayName: "DeepSeek Flash", Description: "Fast, economical DeepSeek model", IsDefault: true, ContextWindow: 1_000_000},
	{Id: "deepseek-pro", DisplayName: "DeepSeek Pro", Description: "Most capable DeepSeek model for complex work", ContextWindow: 1_000_000},
	{Id: "mimo-pro", DisplayName: "MiMo Pro", Description: "Xiaomi MiMo, most capable (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
	{Id: "mimo-flash", DisplayName: "MiMo Flash", Description: "Xiaomi MiMo, fast and economical (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
}

// reasonixLocator finds the Reasonix CLI on the user's PATH.
var reasonixLocator = launch.Binaries("reasonix")

// Registration states everything the worker knows about Reasonix before
// any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		Plugin:        reasonixProvider{},
		Start:         Start,
		Locator:       reasonixLocator,
		DefaultModels: reasonixAvailableModels,
		// The session supplies available modes and config options.
		OptionGroups:        nil,
		AdditionalOptionIDs: []string{agent.OptionIDPermissionMode, agent.OptionIDEffort, contracts.ReasonixConfigToolApproval},
		PermissionDefaults:  agent.PermissionDefaults{Fallback: contracts.ReasonixModeNormal},
		EnvModelKey:         "LEAPMUX_REASONIX_DEFAULT_MODEL",
	}
}
