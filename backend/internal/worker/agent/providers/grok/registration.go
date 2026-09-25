package grok

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// grokStaticOptionGroups holds Grok's static session-mode group and the
// approval-mode group. The registration and the start both read this one
// value, so the static fallback and a running agent's fallback modes cannot
// drift. The session reports no mode list, so this list is also the live one.
var grokStaticOptionGroups = append(
	acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, grokSessionModes()),
	approvalModeGroup(""),
)

// grokLocator finds the Grok CLI on the user's PATH.
var grokLocator = launch.Binaries("grok")

// Registration states everything the worker knows about Grok Build before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		Plugin:   grokProvider{},
		Start:    Start,
		Locator:  grokLocator,
		// Grok lists its models in config.toml and in its remote catalog, so the
		// session is the only source of the catalog.
		DefaultModels: nil,
		OptionGroups:  grokStaticOptionGroups,
		// The reasoning effort is a server-driven config option whose menu belongs
		// to the model, so it is known before the session reports it.
		AdditionalOptionIDs: []string{contracts.GrokConfigReasoningEffort},
		// The approval mode starts at the safe default for every agent. A stored
		// value that matches no mode falls back to the same default at the start.
		ProviderOptionDefaults: map[string]string{contracts.GrokOptionApprovalMode: grokApprovalModes[0].id},
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.GrokModeDefault},
			Fallback:   contracts.GrokModeDefault,
		},
		EnvModelKey:  "LEAPMUX_GROK_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_GROK_DEFAULT_EFFORT",
	}
}
